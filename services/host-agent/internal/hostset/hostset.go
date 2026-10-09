// SPDX-License-Identifier: AGPL-3.0-only

// Package hostset models the address a Bloud instance is reachable at and
// derives every URL that depends on it:
//
//   - base URLs: the origins registered as OAuth redirect URIs in Authentik
//     for the dashboard and for each app;
//   - the OIDC issuer base URL: baked into app configs and used for
//     discovery by browsers and app containers alike;
//   - the extraHosts entry app containers need so the issuer hostname
//     resolves to the machine running Traefik.
//
// There is exactly one configured address, the public URL, typed as a normal
// origin: https://bloud.example.com:8443. The two built-in names (localhost
// and bloud.local) are always reachable as well, because Traefik routes are
// domain-agnostic, but they are not configurable and carry a fixed plain-http
// mapping. They show up in the derived redirect-URI list so local access keeps
// working next to a public domain; they are not a second knob.
package hostset

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
)

// BuiltinHosts are always reachable and cannot be configured away. They are
// plain http: no CA issues a certificate for a bare .local name or for
// localhost, and both are reached directly rather than through whatever proxy
// the public URL describes.
var BuiltinHosts = []string{"localhost", "bloud.local"}

// DefaultPublicURL is the address an unconfigured instance is reachable at.
// The 8080 is the dev/e2e convention rather than a guess: the dev VMs forward
// host 8080 to Traefik's canonical :80, and the native backend binds 8080
// directly because it runs unprivileged.
const DefaultPublicURL = "http://localhost:8080"

// builtinPorts pins each built-in name to the port its browser-facing URL
// uses. localhost carries the 8080 dev convention; bloud.local is served on
// the plain http default.
var builtinPorts = map[string]int{"localhost": 8080}

// Scheme is the protocol an address is served under. It lives in the model because
// a hard-coded "http" here would propagate into the OIDC issuer, every OAuth
// redirect URI, every launch URL, and the outpost's browser URL the moment a TLS
// terminator sits in front of Bloud.
type Scheme string

const (
	SchemeHTTP  Scheme = "http"
	SchemeHTTPS Scheme = "https"
)

// NormalizeScheme lowercases and validates a scheme, returning "" when it is
// neither http nor https so callers can reject rather than guess.
func NormalizeScheme(s string) Scheme {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "http":
		return SchemeHTTP
	case "https":
		return SchemeHTTPS
	default:
		return ""
	}
}

// DefaultPort returns the port a scheme means when the URL leaves it off.
func (s Scheme) DefaultPort() int {
	if s == SchemeHTTPS {
		return 443
	}
	return 80
}

// BuiltinSet returns the built-in hostnames as a set.
func BuiltinSet() map[string]bool {
	m := make(map[string]bool, len(BuiltinHosts))
	for _, b := range BuiltinHosts {
		m[b] = true
	}
	return m
}

// Normalize lowercases and trims a hostname, returning "" when invalid.
func Normalize(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if !ValidHostname(h) {
		return ""
	}
	return h
}

var hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// ValidHostname reports whether h is a usable hostname: lowercase RFC 1123
// labels (single labels like "localhost" are allowed), max 253 chars.
func ValidHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	return hostnameRe.MatchString(h)
}

// IsAddress reports whether host is an IP literal rather than a hostname.
// ValidHostname accepts an IPv4 address, because a dotted quad is a sequence
// of valid RFC 1123 labels. It is still not a name: it has no certificate
// story, so it is never a valid https origin.
func IsAddress(host string) bool {
	return net.ParseIP(Normalize(host)) != nil
}

// PublicURL is the one address an operator says this Bloud is reachable at.
// It is a bare origin: scheme, host, and an optional port. Port 0 means "the
// scheme default", so Origin() leaves it off.
//
// The port belongs to the proxy, not to anything inside Bloud. It is the port
// the public entrypoint is dialed on from outside, which behind a NAT or a
// non-standard terminator is not necessarily the port Traefik bound.
type PublicURL struct {
	Scheme Scheme
	Host   string
	Port   int
}

// ParsePublicURL parses an origin into a PublicURL. A missing scheme is read
// as http, which is what an operator means when they type a bare host, and a
// port is optional. Anything that is not a bare origin - a path, a query, a
// credential, a non-numeric or out-of-range port, an unknown scheme - is
// rejected, because a partially-understood URL would register a redirect URI
// that never matches what the browser sends.
func ParsePublicURL(raw string) (PublicURL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return PublicURL{}, fmt.Errorf("url is empty")
	}
	// A bare host:port has no scheme, so url.Parse would read the whole thing
	// as a path. Give it one and remember that we did, so an explicit scheme
	// stays distinguishable from an assumed one.
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return PublicURL{}, fmt.Errorf("not a valid URL: %w", err)
	}
	scheme := NormalizeScheme(u.Scheme)
	if scheme == "" {
		return PublicURL{}, fmt.Errorf("scheme %q is not http or https", u.Scheme)
	}
	if err := rejectNonOrigin(u); err != nil {
		return PublicURL{}, err
	}
	host := Normalize(u.Hostname())
	if host == "" {
		return PublicURL{}, fmt.Errorf("host %q is not a valid hostname", u.Hostname())
	}
	if scheme == SchemeHTTPS && IsAddress(host) {
		return PublicURL{}, fmt.Errorf("https://%s is not usable: no CA issues a certificate for an IP address", host)
	}
	port, err := parseExplicitPort(u.Port())
	if err != nil {
		return PublicURL{}, err
	}
	return PublicURL{Scheme: scheme, Host: host, Port: port}, nil
}

// rejectNonOrigin refuses the parts a bare origin must not carry: credentials, a
// path, a query, or a fragment. Any of them would register a redirect URI that
// never matches what the browser sends.
func rejectNonOrigin(u *url.URL) error {
	if u.User != nil {
		return fmt.Errorf("a public URL cannot carry credentials")
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("a public URL must be an origin without a path (got %q)", u.Path)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("a public URL must be an origin without a query or fragment")
	}
	return nil
}

// parseExplicitPort reads the port the operator typed. "" means they named no
// port, which is recorded as 0: the scheme's own default.
func parseExplicitPort(p string) (int, error) {
	if p == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("port %q is out of range", p)
	}
	return n, nil
}

// HostPort renders the dialable host: host[:port], with the port left off when
// it is the scheme default, because `https://host` already means 443 and
// repeating it makes one address look like two. This is what a display shows
// when it wants the address without the scheme.
func (u PublicURL) HostPort() string {
	if u.Port == 0 || u.Port == u.Scheme.DefaultPort() {
		return u.Host
	}
	return u.Host + ":" + strconv.Itoa(u.Port)
}

// Origin renders the URL as scheme://host[:port], leaving the port off when it
// is the scheme default so a stored value and a derived one look the same.
func (u PublicURL) Origin() string {
	return string(u.Scheme) + "://" + u.HostPort()
}

// Input is everything needed to resolve the effective address at startup.
// Every field is optional; they are consulted in decreasing order of
// specificity, so a stored value wins over the env knobs and the env knobs
// win over the default.
type Input struct {
	// StoredURL is the persisted public URL (Settings -> Address). "" means
	// the operator has never set one.
	StoredURL string
	// BaseDomain is BLOUD_BASE_DOMAIN (legacy single-domain knob). It states
	// a host but no scheme, so PublicScheme still applies to it.
	BaseDomain string
	// SSOBaseURL is BLOUD_SSO_BASE_URL (legacy full-URL knob). It states a
	// scheme, so PublicScheme does not override it.
	SSOBaseURL string
	// PublicScheme is BLOUD_PUBLIC_SCHEME: the scheme to use when nothing
	// more specific stated one, for a deployment behind a TLS terminator.
	PublicScheme string
	// ServedPort is BLOUD_TRAEFIK_PORT: the port the public entrypoint
	// listens on. It is what the detected LAN IP URLs render on, because a
	// LAN client reaches the socket Bloud opened rather than the public
	// origin the URL describes. Those are different numbers in every
	// deployment that does not serve 80.
	ServedPort int
}

// HostSet is the resolved, immutable address model. One configured public URL
// plus the built-in aliases that are always reachable alongside it.
type HostSet struct {
	public PublicURL
	// servedPort is the entrypoint socket's port, used for the detected LAN
	// URLs. It is a property of the set rather than an argument at each call
	// site because AllBaseURLs needs it and is reached from the SSO
	// provisioning and blueprint paths, none of which carry a port of their
	// own. 0 means "not stated" and renders as the http default.
	servedPort int
}

// New builds the host set around one public URL.
func New(public PublicURL) HostSet {
	if public.Host == "" {
		public = PublicURL{Scheme: SchemeHTTP, Host: "localhost", Port: 8080}
	}
	if public.Scheme == "" {
		public.Scheme = SchemeHTTP
	}
	return HostSet{public: public}
}

// Default returns the host set for an unconfigured instance: the default
// public address with no entrypoint port stated. It is the zero configuration
// every install starts from, and the fallback every caller lands on when it
// cannot resolve anything better.
func Default() HostSet {
	hs, err := ParsePublicURL(DefaultPublicURL)
	if err != nil {
		return New(PublicURL{})
	}
	return New(hs)
}

func (h HostSet) Public() PublicURL { return h.public }

// Hosts returns every hostname this instance answers: the public host first,
// then the built-in aliases it does not already duplicate.
func (h HostSet) Hosts() []string {
	out := []string{h.public.Host}
	for _, b := range BuiltinHosts {
		if b != h.public.Host {
			out = append(out, b)
		}
	}
	return out
}

// Primary returns the public host. The name stays because the SSO layer asks
// for "the primary host" and that question still has an answer; there is just
// no longer a choice to make about it.
func (h HostSet) Primary() string { return h.public.Host }

// Contains reports whether host (case-insensitive) is reachable under this
// instance's address model.
func (h HostSet) Contains(host string) bool {
	for _, x := range h.Hosts() {
		if x == strings.ToLower(strings.TrimSpace(host)) {
			return true
		}
	}
	return false
}

// IsBuiltin reports whether host is one of the built-in aliases.
func (h HostSet) IsBuiltin(host string) bool {
	return BuiltinSet()[strings.ToLower(strings.TrimSpace(host))]
}

// BaseURLFor returns the base URL one reachable host is served under, or ""
// when the host is not part of this instance's address.
//
// The public host renders as the configured origin, port included: that port
// is the operator's own statement about where the proxy is dialed, so it is
// carried verbatim. The built-in aliases render on their fixed plain-http
// mapping, because they are reached directly and never through the public
// terminator.
func (h HostSet) BaseURLFor(host string) string {
	host = Normalize(host)
	if host == "" {
		return ""
	}
	if host == h.public.Host {
		return h.public.Origin()
	}
	if !BuiltinSet()[host] {
		return ""
	}
	return string(SchemeHTTP) + "://" + host + netutil.PortSuffix(builtinPorts[host])
}

// SchemeFor returns the scheme one host is served under. Built-in aliases are
// always plain http; the public host carries the configured scheme.
func (h HostSet) SchemeFor(host string) Scheme {
	host = Normalize(host)
	if host == h.public.Host {
		return h.public.Scheme
	}
	if BuiltinSet()[host] {
		return SchemeHTTP
	}
	return ""
}

// PublicScheme is the scheme the deployment is reachable under from outside.
func (h HostSet) PublicScheme() Scheme { return h.public.Scheme }

// WithServedPort returns a copy carrying the port the public entrypoint
// listens on. Every path that rebuilds the set has to carry it: a set that
// lost the port would render the detected LAN URLs on 80 while the socket
// serves something else, and the redirect URIs would be re-registered
// against a port nothing answers on.
func (h HostSet) WithServedPort(port int) HostSet {
	next := h
	next.servedPort = port
	return next
}

// ServedPort returns the port the public entrypoint listens on (0 = not
// stated, which renders as the http default).
func (h HostSet) ServedPort() int { return h.servedPort }

// PrimaryBaseURL returns the public base URL.
func (h HostSet) PrimaryBaseURL() string {
	return h.public.Origin()
}

// BaseURLs returns one base URL per reachable host, public first. These drive
// the OAuth redirect URIs registered in the identity provider.
func (h HostSet) BaseURLs() []string {
	urls := make([]string, 0, len(h.Hosts()))
	for _, host := range h.Hosts() {
		if u := h.BaseURLFor(host); u != "" {
			urls = append(urls, u)
		}
	}
	return urls
}

// AllBaseURLs returns every base URL to register with the identity provider:
// the public origin, the built-in aliases, then this machine's LAN IP URLs,
// so login also works when the server is reached by address. Deduplicated.
//
// The LAN entries are plain http on the entrypoint port, for the reasons on
// LANBaseURLs: an address has no scheme-default port and no certificate, so
// the socket is the only truth.
func (h HostSet) AllBaseURLs() []string {
	urls := h.BaseURLs()
	for _, u := range netutil.LANBaseURLs(h.servedPort) {
		if !containsStr(urls, u) {
			urls = append(urls, u)
		}
	}
	return urls
}

// IssuerBaseURL returns the OIDC issuer base URL shared by browsers and app
// containers. For a localhost public URL this is http://sso.localhost:8080
// (app containers resolve sso.localhost via extraHosts to the host gateway);
// for every other public host it is that host's base URL.
func (h HostSet) IssuerBaseURL() string {
	if h.public.Host == "localhost" {
		return "http://sso.localhost:8080"
	}
	return h.public.Origin()
}

// LoopbackIssuerBaseURL returns the OIDC issuer base URL reached through the
// host's own loopback for an app whose OIDC client accepts a plain-http
// issuer only on a literal loopback hostname (catalog sso.loopbackIssuer), or
// "" when that issuer must not be used.
//
// The loopback URL is a statement about the server's own network namespace:
// the app container shares it, so localhost:<compat port> inside the
// container is Traefik. But the issuer string is not only dialed by that
// container, it is also where the browser is redirected, and there localhost
// is the visitor's machine. The override is therefore only sound while the
// deployment is plain http, where no accepted-and-reachable alternative
// exists and the app at least signs in from a browser on this box.
//
// Under a https public URL the override is a bug rather than a shortcut: the
// provider accepts the public issuer (its rule is https anywhere, http only
// on loopback), and the shared issuer is reachable by browser and container
// alike. Handing out the loopback issuer then redirects every remote browser
// off the instance, to a port on the visitor's own machine.
func (h HostSet) LoopbackIssuerBaseURL() string {
	if h.public.Scheme == SchemeHTTPS {
		return ""
	}
	return h.BaseURLFor("localhost")
}

// IssuerHost returns the hostname app containers must resolve to reach the
// issuer (sso.localhost for a localhost public URL, else the public host).
func (h HostSet) IssuerHost() string {
	if h.public.Host == "localhost" {
		return "sso.localhost"
	}
	return h.public.Host
}

// IssuerExtraHost is the host:target pair for app container extraHosts, or
// "" when the container must resolve the issuer itself.
//
// The pin exists for a plain-HTTP issuer: it points the issuer hostname at
// the host gateway so the container reaches Traefik directly without
// hairpinning through a router. Under a https issuer that pin is wrong. The
// TLS terminator is not this box - it is whatever public host the name
// resolves to - and Bloud serves no certificate at the gateway, so pinning
// sends the container to a port nothing answers on TLS. Verified against a
// real proxied deployment: with the pin the container dials the gateway and
// the connection is refused; without it the name resolves to the terminator
// and discovery succeeds.
func (h HostSet) IssuerExtraHost() string {
	if h.PublicScheme() == SchemeHTTPS {
		return ""
	}
	return h.IssuerHost() + ":host-gateway"
}

// Issue is one reason the URLs derived from a HostSet cannot actually be
// served. Codes are stable so tests and logs can key off them.
type Issue struct {
	Code    string
	Message string
}

// ProxyConsistency reports the ways the declared scheme and the settings that
// make that scheme real disagree with each other.
//
// Each issue names one layer of the proxy story, so a failed login points at
// one setting instead of three candidates:
//
//	layer 1, trust scope: Traefik accepts X-Forwarded-* only from addresses
//	    in trustedProxyNets. Without them it rewrites X-Forwarded-Proto to
//	    http, and Authentik reads an HTTPS request as HTTP and generates
//	    http:// URLs the browser then blocks as mixed content.
//	layer 2, derivation: the scheme carried by this HostSet is what every
//	    derived URL inherits. Wrong here means wrong in the issuer, the
//	    registered redirect URIs, the launch URLs, and the outpost's browser
//	    URL, all at once.
//	layer 3, dial plan: the container's host-gateway hop must speak the same
//	    scheme as the issuer string, because OIDC requires that string to
//	    match on every hop. See Deployability.
//
// trustedProxyNets is config.TrustedProxyNets, and tlsAtTraefik is whether
// TLS terminates where the container's issuer pin actually lands.
func (h HostSet) ProxyConsistency(trustedProxyNets []string, tlsAtTraefik bool) []Issue {
	var issues []Issue
	hasProxy := len(trustedProxyNets) > 0

	switch h.PublicScheme() {
	case SchemeHTTPS:
		if !hasProxy {
			issues = append(issues, Issue{
				Code: "https_without_trusted_proxy_nets",
				Message: "public scheme is https but BLOUD_TRUSTED_PROXY_NETS is empty, so Traefik rewrites " +
					"X-Forwarded-Proto to http and Authentik reads an HTTPS request as HTTP. Name the proxy " +
					"in BLOUD_TRUSTED_PROXY_NETS.",
			})
		}
	case SchemeHTTP:
		if hasProxy {
			issues = append(issues, Issue{
				Code: "proxy_present_but_scheme_is_http",
				Message: "BLOUD_TRUSTED_PROXY_NETS names an upstream proxy but the public scheme is still http, " +
					"so every derived URL (issuer, redirect URIs, launch URLs) is http while the proxy serves " +
					"https. Set the public URL to https.",
			})
		}
	}

	issues = append(issues, h.Deployability(tlsAtTraefik)...)
	return issues
}

// Deployability reports why the derived URLs cannot be reached by the party
// they are handed to, if that is true. Empty means every derived URL is
// reachable.
//
// A https issuer whose hostname no container can resolve to a TLS endpoint is
// the case that stays genuinely undeployable. A special-use name - .local, or
// a loopback-derived name - resolves inside a container to mDNS or to
// nothing, so the terminator is unreachable no matter what the scheme says.
// That is a misconfiguration worth naming rather than a stalled login later.
//
// tlsAtTraefik records whether the deployment terminates TLS at Traefik.
// When it does, the gateway hop is valid too, so nothing is reported.
func (h HostSet) Deployability(tlsAtTraefik bool) []Issue {
	if h.PublicScheme() != SchemeHTTPS || tlsAtTraefik {
		return nil
	}
	issuer := h.IssuerHost()
	if !resolvableFromContainer(issuer) {
		return []Issue{{
			Code: "https_issuer_host_not_resolvable",
			Message: fmt.Sprintf(
				"issuer %s is https, but app containers must reach it by resolving %s, a special-use name that "+
					"does not point at a TLS endpoint from inside a container. Point the public URL at a name that "+
					"resolves to the TLS terminator.",
				h.IssuerBaseURL(), issuer),
		}}
	}
	return nil
}

// resolvableFromContainer reports whether a container can resolve host to a
// real network endpoint. .local is mDNS (not reachable from a container's
// resolver) and the localhost family names the container's own loopback, so
// neither reaches a remote TLS terminator.
func resolvableFromContainer(host string) bool {
	host = Normalize(host)
	if host == "" {
		return false
	}
	if strings.HasSuffix(host, ".local") {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	return true
}

// Resolve computes the effective address at startup from the stored public
// URL and the legacy env knobs, most specific first: the stored value, then
// BLOUD_SSO_BASE_URL, then BLOUD_BASE_DOMAIN, then the default.
//
// BLOUD_PUBLIC_SCHEME fills in a scheme only when the winning source did not
// state one. BLOUD_BASE_DOMAIN is exactly that case; a stored URL and
// BLOUD_SSO_BASE_URL are full origins and carry their own.
func Resolve(in Input) (HostSet, error) {
	var public PublicURL
	schemeStated := false

	switch {
	case strings.TrimSpace(in.StoredURL) != "":
		u, err := ParsePublicURL(in.StoredURL)
		if err != nil {
			return HostSet{}, fmt.Errorf("stored public URL %q: %w", in.StoredURL, err)
		}
		public, schemeStated = u, true
	case strings.TrimSpace(in.SSOBaseURL) != "":
		u, err := ParsePublicURL(in.SSOBaseURL)
		if err != nil {
			return HostSet{}, fmt.Errorf("BLOUD_SSO_BASE_URL %q: %w", in.SSOBaseURL, err)
		}
		public, schemeStated = u, true
	case strings.TrimSpace(in.BaseDomain) != "":
		host := Normalize(in.BaseDomain)
		if host == "" {
			return HostSet{}, fmt.Errorf("BLOUD_BASE_DOMAIN %q is not a valid hostname", in.BaseDomain)
		}
		public = PublicURL{Scheme: SchemeHTTP, Host: host}
	default:
		u, err := ParsePublicURL(DefaultPublicURL)
		if err != nil {
			return HostSet{}, err
		}
		public = u
	}

	if !schemeStated && strings.TrimSpace(in.PublicScheme) != "" {
		scheme := NormalizeScheme(in.PublicScheme)
		if scheme == "" {
			return HostSet{}, fmt.Errorf("BLOUD_PUBLIC_SCHEME %q is not http or https", in.PublicScheme)
		}
		// A scheme the operator never typed still has to be one the address can
		// honor: an https IP literal is not a usable origin, and turning a
		// bare address into an https promise is how a login ends up pointed at
		// a port nothing answers on.
		if scheme == SchemeHTTPS && IsAddress(public.Host) {
			return HostSet{}, fmt.Errorf(
				"BLOUD_PUBLIC_SCHEME=https cannot apply to %s: no CA issues a certificate for an IP address; "+
					"set the public URL to a hostname", public.Host)
		}
		public.Scheme = scheme
	}

	return New(public).WithServedPort(in.ServedPort), nil
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// State is a thread-safe holder for the live HostSet. The orchestrator, SSO
// provisioning, and the API all read through Get; only the orchestrator
// writes via Set (through the SetPublicURL intent).
type State struct {
	mu sync.RWMutex
	hs HostSet
}

// NewState creates a State from an initial host set.
func NewState(hs HostSet) *State {
	return &State{hs: hs}
}

// Get returns the current host set.
func (s *State) Get() HostSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hs
}

// Set swaps in a new host set.
func (s *State) Set(hs HostSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hs = hs
}
