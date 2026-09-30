// SPDX-License-Identifier: AGPL-3.0-only

// Package hostset models the collection of hostnames a Bloud instance is
// reachable under and derives every URL that depends on them:
//
//   - SSO base URLs (one per host): drive the OAuth redirect URIs
//     registered in Authentik for the dashboard and each app;
//   - the OIDC issuer base URL: baked into app configs and used for
//     discovery by browsers and app containers alike;
//   - the extraHosts entry app containers need so the issuer hostname
//     resolves to the machine running Traefik.
//
// Bloud ships with built-in hosts (localhost, bloud.local). Admins can add
// custom domains (e.g. example.com); one host is always marked primary and
// drives the issuer and launch URLs.
package hostset

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
)

// BuiltinHosts are always present and cannot be removed from the UI.
var BuiltinHosts = []string{"localhost", "bloud.local"}

// DefaultPrimary is the primary host until an admin picks another one.
const DefaultPrimary = "localhost"

// Scheme is the protocol a host is served under. It lives in the model because
// BaseURLFor used to hardcode the literal "http", which silently propagated into
// the OIDC issuer, every OAuth redirect URI, every launch URL, and the outpost's
// browser URL the moment a TLS terminator sat in front of Bloud.
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

// MaxHosts caps how many hosts an admin may configure.
const MaxHosts = 8

var hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// ValidHostname reports whether h is a usable hostname: lowercase RFC 1123
// labels (single labels like "localhost" are allowed), max 253 chars.
func ValidHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	return hostnameRe.MatchString(h)
}

// StoredHost is one row from the hosts store (admin-configured hosts).
type StoredHost struct {
	Hostname string
	Primary  bool
	// Scheme is the scheme this host is served under: "http", "https", or
	// "" for no stored statement. It exists because a host behind a
	// TLS-terminating proxy is reached at https:// while Bloud's own socket
	// speaks plain http, so nothing Bloud observes answers the question; it
	// has to be recorded. It is per host rather than global because redirect
	// URIs are registered per host and every one of them has to be exact.
	Scheme string
}

// Input is everything needed to resolve the effective host set at startup.
type Input struct {
	// Stored hosts from the database (custom hosts, possibly with a
	// primary flag). When non-empty, they take precedence over env.
	Stored []StoredHost
	// BaseDomain is BLOUD_BASE_DOMAIN (legacy single-domain knob).
	BaseDomain string
	// SSOBaseURL is BLOUD_SSO_BASE_URL (legacy full-URL knob). When set,
	// its host becomes primary and the URL is used verbatim for it.
	SSOBaseURL string
	// PublicScheme is BLOUD_PUBLIC_SCHEME: the scheme every host in the set
	// is served under, for a deployment behind a TLS terminator. Empty means
	// http, which is every topology Bloud shipped before this existed.
	//
	// This is the path that works for stored hosts. The legacy SSOBaseURL
	// override is only consulted when there are no stored hosts, so once an
	// admin adds a custom domain through the UI that knob is dead, and
	// without this field there would be no way at all to express https.
	// A stored per-host scheme overrides it for that host, so the env knob
	// sets the deployment-wide default and Settings -> Hosts can still pin
	// one host differently.
	PublicScheme string
}

// HostSet is an immutable, ordered set of hosts: index 0 is the primary.
type HostSet struct {
	hosts   []string
	primary string
	// urlOverrides pins a host's base URL (legacy BLOUD_SSO_BASE_URL).
	urlOverrides map[string]string
	// schemes pins a host's protocol. An absent host is SchemeHTTP, which is
	// what every host means in every deployment that predates this field, so
	// the zero value keeps derived URLs byte-identical.
	schemes map[string]Scheme
}

// New builds a HostSet with the given primary. Unknown/invalid hosts are
// dropped; if the primary is invalid or missing it falls back to
// DefaultPrimary. Order: primary first, then the remaining hosts as given.
func New(hosts []string, primary string) HostSet {
	seen := map[string]bool{}
	var ordered []string
	for _, h := range hosts {
		if h = Normalize(h); h == "" || seen[h] {
			continue
		}
		seen[h] = true
		ordered = append(ordered, h)
	}
	if !ValidHostname(primary) || !seen[primary] {
		primary = DefaultPrimary
		if !seen[primary] {
			ordered = append([]string{primary}, ordered...)
			seen[primary] = true
		}
	}
	var rest []string
	for _, h := range ordered {
		if h != primary {
			rest = append(rest, h)
		}
	}
	return HostSet{
		hosts:        append([]string{primary}, rest...),
		primary:      primary,
		urlOverrides: map[string]string{},
		schemes:      map[string]Scheme{},
	}
}

// Hosts returns the hosts in order (primary first).
func (h HostSet) Hosts() []string {
	out := make([]string, len(h.hosts))
	copy(out, h.hosts)
	return out
}

// Primary returns the primary host.
func (h HostSet) Primary() string { return h.primary }

// Contains reports whether host (case-insensitive) is in the set.
func (h HostSet) Contains(host string) bool {
	for _, x := range h.hosts {
		if x == strings.ToLower(host) {
			return true
		}
	}
	return false
}

// IsBuiltin reports whether host is one of the built-in hosts.
func (h HostSet) IsBuiltin(host string) bool {
	host = strings.ToLower(host)
	for _, b := range BuiltinHosts {
		if b == host {
			return true
		}
	}
	return false
}

// BaseURLFor returns the base URL for one host, under that host's scheme.
// Plain-HTTP localhost keeps the http://localhost:8080 convention (the dev VMs
// expose Traefik's canonical :80 there, so browser and e2e URLs stay on 8080);
// that convention is about a dev VM port forward, not about TLS, so it is not
// applied to an https localhost. Every other host uses the bare host on the
// scheme's default port, which Traefik serves directly.
func (h HostSet) BaseURLFor(host string) string {
	if u, ok := h.urlOverrides[host]; ok {
		return u
	}
	scheme := h.SchemeFor(host)
	if host == "localhost" && scheme == SchemeHTTP {
		return "http://localhost:8080"
	}
	return string(scheme) + "://" + host
}

// SchemeFor returns the scheme one host is served under. An explicit base-URL
// override carries its own scheme and wins, since it is the more specific
// statement. A host with neither is SchemeHTTP, matching every deployment that
// predates the field.
func (h HostSet) SchemeFor(host string) Scheme {
	if raw, ok := h.urlOverrides[host]; ok {
		if parsed, err := url.Parse(raw); err == nil {
			if s := NormalizeScheme(parsed.Scheme); s != "" {
				return s
			}
		}
	}
	if s, ok := h.schemes[host]; ok && s != "" {
		return s
	}
	return SchemeHTTP
}

// PublicScheme is the scheme of the primary host, which is the scheme the
// deployment is reachable under from outside.
func (h HostSet) PublicScheme() Scheme { return h.SchemeFor(h.primary) }

// WithScheme returns a copy with one host's scheme pinned. An unrecognized
// scheme leaves the set unchanged, so a bad value cannot silently produce a
// URL with a nonsense protocol.
func (h HostSet) WithScheme(host string, scheme Scheme) HostSet {
	scheme = NormalizeScheme(string(scheme))
	if scheme == "" {
		return h
	}
	schemes := h.cloneSchemes()
	schemes[host] = scheme
	return h.withSchemes(schemes)
}

// WithPublicScheme returns a copy with every proxied host pinned to scheme.
//
// The built-in hosts are deliberately skipped. localhost and bloud.local are
// reached directly, not through whatever proxy the public scheme describes, so
// forcing them to https would break the LAN and dev paths that are the only
// reason those names exist. The public scheme answers "how do I reach this
// deployment from outside", and the built-ins are not how.
//
// Hosts that already carry an explicit base-URL override are also left alone:
// that override is the more specific statement of intent, and letting a broad
// scheme silently contradict a pinned URL is how a redirect ends up on a
// protocol the operator never asked for.
func (h HostSet) WithPublicScheme(scheme Scheme) HostSet {
	scheme = NormalizeScheme(string(scheme))
	if scheme == "" {
		return h
	}
	schemes := h.cloneSchemes()
	for _, host := range h.hosts {
		if h.IsBuiltin(host) {
			continue
		}
		if _, pinned := h.urlOverrides[host]; pinned {
			continue
		}
		schemes[host] = scheme
	}
	return h.withSchemes(schemes)
}

// WithSchemes returns a copy carrying a per-host scheme map, which is the
// shape both the hosts store and the SetHosts intent produce. Keys and values
// are normalized, and an entry that normalizes to nothing is dropped rather
// than guessed at, so a corrupt row cannot mint a nonsense protocol.
//
// Built-in hosts are skipped for the same reason WithPublicScheme skips them:
// localhost is http://localhost:8080 by dev/e2e convention, no public CA
// issues for it or for bloud.local, and both are reached directly rather than
// through whatever proxy a stored scheme describes. A stored scheme on a
// built-in would be a promise the install cannot keep.
func (h HostSet) WithSchemes(schemes map[string]Scheme) HostSet {
	if len(schemes) == 0 {
		return h
	}
	next := h.cloneSchemes()
	for rawHost, rawScheme := range schemes {
		host := Normalize(rawHost)
		scheme := NormalizeScheme(string(rawScheme))
		if host == "" || scheme == "" || h.IsBuiltin(host) {
			continue
		}
		next[host] = scheme
	}
	return h.withSchemes(next)
}

func (h HostSet) cloneSchemes() map[string]Scheme {
	out := make(map[string]Scheme, len(h.schemes))
	for k, v := range h.schemes {
		out[k] = v
	}
	return out
}

func (h HostSet) withSchemes(schemes map[string]Scheme) HostSet {
	return HostSet{
		hosts:        h.hosts,
		primary:      h.primary,
		urlOverrides: h.urlOverrides,
		schemes:      schemes,
	}
}

// PrimaryBaseURL returns the base URL of the primary host.
func (h HostSet) PrimaryBaseURL() string {
	return h.BaseURLFor(h.primary)
}

// BaseURLs returns one base URL per host, primary first. These drive the
// OAuth redirect URIs registered in the identity provider.
func (h HostSet) BaseURLs() []string {
	urls := make([]string, 0, len(h.hosts))
	for _, host := range h.hosts {
		urls = append(urls, h.BaseURLFor(host))
	}
	return urls
}

// AllBaseURLs returns every base URL to register with the identity provider:
// one per host (primary first) followed by this machine's LAN IP URLs, so login
// also works when the server is reached by address. Deduplicated.
//
// servedPort is the port the public entrypoint (Traefik) listens on. It is a
// parameter rather than a field of the set because the LAN URLs are the only
// derived values that depend on the socket rather than on a name. They are
// plain http on that port: the primary host's URL port describes the public
// origin, which behind a TLS terminator is 443, while a client on the LAN
// reaching the box by address hits whatever port the entrypoint opened. Taking
// either the scheme or the port from the primary is wrong there, and both were
// taken from it before: an https primary made every LAN IP URL an unreachable
// https origin.
func (h HostSet) AllBaseURLs(servedPort int) []string {
	urls := h.BaseURLs()
	for _, u := range netutil.LANBaseURLs(servedPort) {
		if !containsStr(urls, u) {
			urls = append(urls, u)
		}
	}
	return urls
}

// IssuerBaseURL returns the OIDC issuer base URL shared by browsers and app
// containers. For the localhost primary this is http://sso.localhost:8080
// (app containers resolve sso.localhost via extraHosts to the host
// gateway); for every other primary it is the primary host's base URL.
func (h HostSet) IssuerBaseURL() string {
	if h.primary == "localhost" {
		return "http://sso.localhost:8080"
	}
	return h.BaseURLFor(h.primary)
}

// LoopbackIssuerBaseURL returns the OIDC issuer base URL reached through the
// host's own loopback (http://localhost:8080). Apps whose OIDC client accepts
// http only on a literal loopback hostname (catalog sso.loopbackIssuer) use
// it: they run with the host network namespace, so localhost:<Traefik port>
// inside the container is Traefik.
func (h HostSet) LoopbackIssuerBaseURL() string {
	return h.BaseURLFor("localhost")
}

// IssuerHost returns the hostname app containers must resolve to reach the
// issuer (sso.localhost for the localhost primary, else the primary host).
func (h HostSet) IssuerHost() string {
	if h.primary == "localhost" {
		return "sso.localhost"
	}
	return h.primary
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
// the connection is refused; without it the name resolves to the
// terminator and discovery succeeds.
//
// So an https issuer gets no pin, and the container reaches it over real
// DNS. That makes a proxied https issuer deployable without TLS at Traefik,
// which is the gap Deployability used to report as uncloseable.
func (h HostSet) IssuerExtraHost() string {
	if h.PublicScheme() == SchemeHTTPS {
		return ""
	}
	return h.IssuerHost() + ":host-gateway"
}

// WithURLOverride returns a copy with host's base URL pinned to raw (used
// for the legacy BLOUD_SSO_BASE_URL env value).
func (h HostSet) WithURLOverride(host, raw string) HostSet {
	overrides := map[string]string{}
	for k, v := range h.urlOverrides {
		overrides[k] = v
	}
	overrides[host] = raw
	return HostSet{hosts: h.hosts, primary: h.primary, urlOverrides: overrides, schemes: h.cloneSchemes()}
}

// ProxyConsistency reports the ways the deployment's declared scheme and the
// settings that make that scheme real disagree with each other.
//
// Each issue names one layer of the proxy story, so a failed login points at one
// setting instead of three candidates. The layers are independent, and the
// original bug was that all three had to be right at once with nothing saying
// so:
//
//	layer 1, trust scope: Traefik accepts X-Forwarded-* only from addresses in
//	    trustedProxyNets. Without them it rewrites X-Forwarded-Proto to http,
//	    and Authentik reads an HTTPS request as HTTP and generates http://
//	    URLs the browser then blocks as mixed content.
//	layer 2, derivation: the scheme carried by this HostSet is what every
//	    derived URL inherits. Wrong here means wrong in the issuer, the
//	    registered redirect URIs, the launch URLs, and the outpost's browser
//	    URL, all at once.
//	layer 3, dial plan: the container's host-gateway hop must speak the same
//	    scheme as the issuer string, because OIDC requires that string to match
//	    on every hop. See Deployability.
//
// trustedProxyNets is config.TrustedProxyNets, and tlsAtTraefik is whether TLS
// terminates where the container's issuer pin actually lands.
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
					"https. Set BLOUD_PUBLIC_SCHEME=https.",
			})
		}
	}

	// Layer 3 is independent of the trust scope: it is about the container hop.
	issues = append(issues, h.Deployability(tlsAtTraefik)...)
	return issues
}

// Issue is one reason the URLs derived from a HostSet cannot actually be
// served. Codes are stable so tests and logs can key off them.
type Issue struct {
	Code    string
	Message string
}

// Deployability reports why the derived URLs cannot be reached by the party they
// are handed to, if that is true. Empty means every derived URL is reachable.
//
// The case this exists for is a https issuer behind a TLS-terminating proxy.
// Deployability reports why the derived URLs cannot be reached by the party they
// are handed to, if that is true. Empty means every derived URL is reachable.
//
// The case this was written for was a https issuer whose container hop could not
// match the issuer string. That gap is closed by the dial plan: under https the
// container gets no host-gateway pin (see IssuerExtraHost) and reaches the
// issuer by resolving the real public name, which lands on the TLS terminator
// that actually serves it. A proxied https deployment is therefore deployable
// without TLS at Traefik.
//
// What is still genuinely undeployable is a https issuer whose hostname no
// container can resolve to a TLS endpoint. A special-use name - .local, or a
// loopback-derived name - resolves inside a container to mDNS or to nothing, so
// the terminator is unreachable no matter what the scheme says. That is a
// misconfiguration worth naming rather than a stalled login later.
//
// tlsAtTraefik records whether the deployment terminates TLS at Traefik. When
// it does, the gateway hop is valid too, so nothing is reported.
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
					"does not point at a TLS endpoint from inside a container. Point the primary host at a name that "+
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

// Resolve computes the effective host set at startup from stored (admin)
// hosts and legacy env knobs. Stored hosts win over env entirely.
func Resolve(in Input) (HostSet, error) {
	var hosts []string
	primary := DefaultPrimary

	if len(in.Stored) == 0 {
		hosts = append(hosts, BuiltinHosts...)
		if in.BaseDomain != "" {
			primary = Normalize(in.BaseDomain)
			if !ValidHostname(primary) {
				return HostSet{}, fmt.Errorf("BLOUD_BASE_DOMAIN %q is not a valid hostname", in.BaseDomain)
			}
			if !containsStr(hosts, primary) {
				hosts = append(hosts, primary)
			}
		}
		if in.SSOBaseURL != "" {
			u, err := url.Parse(in.SSOBaseURL)
			if err != nil || u.Host == "" {
				return HostSet{}, fmt.Errorf("BLOUD_SSO_BASE_URL %q is not a valid URL", in.SSOBaseURL)
			}
			host := Normalize(u.Hostname())
			if host == "" {
				return HostSet{}, fmt.Errorf("BLOUD_SSO_BASE_URL host %q is not a valid hostname", u.Hostname())
			}
			if !containsStr(hosts, host) {
				hosts = append(hosts, host)
			}
			primary = host
			hs := New(hosts, primary)
			return finalizeResolve(hs.WithURLOverride(host, strings.TrimSuffix(in.SSOBaseURL, "/")), in)
		}
		return finalizeResolve(New(hosts, primary), in)
	}

	// Stored hosts: built-ins are always present; customs come from the DB.
	hosts = append(hosts, BuiltinHosts...)
	storedSchemes := map[string]Scheme{}
	for _, s := range in.Stored {
		h := Normalize(s.Hostname)
		if h == "" {
			continue // skip corrupt rows
		}
		if !containsStr(hosts, h) {
			hosts = append(hosts, h)
		}
		if s.Primary {
			primary = h
		}
		// "" is the absence of a stored statement, not a statement of
		// http, so it is left out and the host falls to the wider defaults.
		if scheme := NormalizeScheme(s.Scheme); scheme != "" {
			storedSchemes[h] = scheme
		}
	}
	// The deployment-wide scheme from the env lands first and the stored
	// per-host scheme lands on top of it. The stored value is the more
	// specific statement of intent: an admin set it for one named host,
	// while the env value describes the deployment as a whole. Registering
	// redirect URIs per host means the value the UI shows for a host has to
	// be the value its derived URL uses, so the specific one wins.
	hs, err := finalizeResolve(New(hosts, primary), in)
	if err != nil {
		return HostSet{}, err
	}
	return hs.WithSchemes(storedSchemes), nil
}

// finalizeResolve applies the deployment-wide public scheme, if one was given.
// It is the single place PublicScheme reaches the set, so the stored-hosts and
// env paths cannot drift apart on it the way the legacy SSOBaseURL override
// already has (that one is only reachable when there are no stored hosts).
func finalizeResolve(hs HostSet, in Input) (HostSet, error) {
	if strings.TrimSpace(in.PublicScheme) == "" {
		return hs, nil
	}
	scheme := NormalizeScheme(in.PublicScheme)
	if scheme == "" {
		return HostSet{}, fmt.Errorf("BLOUD_PUBLIC_SCHEME %q is not http or https", in.PublicScheme)
	}
	return hs.WithPublicScheme(scheme), nil
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// State is a thread-safe holder for the live HostSet. The orchestrator,
// SSO provisioning, and the API all read through Get; only the orchestrator
// writes via Set (through the SetHosts intent).
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
