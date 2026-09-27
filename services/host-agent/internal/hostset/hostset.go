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
// one per host (primary first) followed by the detected local-IP URLs, so
// login also works when the server is reached by IP. Deduplicated.
func (h HostSet) AllBaseURLs() []string {
	urls := h.BaseURLs()
	for _, u := range netutil.BuildBaseURLs(h.PrimaryBaseURL())[1:] {
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

// IssuerExtraHost is the host:target pair for app container extraHosts so
// the issuer hostname resolves to the machine running Traefik.
func (h HostSet) IssuerExtraHost() string {
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
// OIDC requires the issuer string to be byte-identical on every hop, so the
// browser and the app container must both be able to reach the same URL. The
// container reaches the issuer hostname through the IssuerExtraHost pin, which
// lands on the host gateway so it never has to hairpin through a router or rely
// on split-horizon DNS. Bloud serves plain HTTP there, on the Traefik port and
// the compat port, and ships no certificate resolver. A https issuer therefore
// asks the container to dial TLS at an address where Bloud serves none, and
// discovery fails with nothing in the login flow that names the cause.
//
// tlsAtTraefik records whether the deployment terminates TLS where the
// container actually lands. It is false for every topology Bloud ships today,
// which is the honest answer: a https public scheme is not deployable until TLS
// at Traefik lands. Reporting that at startup beats a stalled login later.
func (h HostSet) Deployability(tlsAtTraefik bool) []Issue {
	if h.PublicScheme() != SchemeHTTPS || tlsAtTraefik {
		return nil
	}
	return []Issue{{
		Code: "https_issuer_no_tls_at_gateway",
		Message: fmt.Sprintf(
			"issuer %s is https, but app containers reach %s through %s, where Bloud serves plain HTTP. "+
				"OIDC requires the issuer to match on both hops, so container discovery will fail until TLS terminates at Traefik.",
			h.IssuerBaseURL(), h.IssuerHost(), h.IssuerExtraHost()),
	}}
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
	}
	return finalizeResolve(New(hosts, primary), in)
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
