// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package hostset models the collection of hostnames a Bloud instance is
// reachable under and derives every URL that depends on them:
//
//   - SSO base URLs (one per host) — drive the OAuth redirect URIs
//     registered in Authentik for the dashboard and each app;
//   - the OIDC issuer base URL — baked into app configs and used for
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
}

// HostSet is an immutable, ordered set of hosts: index 0 is the primary.
type HostSet struct {
	hosts   []string
	primary string
	// urlOverrides pins a host's base URL (legacy BLOUD_SSO_BASE_URL).
	urlOverrides map[string]string
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
	return HostSet{hosts: append([]string{primary}, rest...), primary: primary, urlOverrides: map[string]string{}}
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

// BaseURLFor returns the base URL for one host: localhost keeps the
// http://localhost:8080 convention (dev/e2e parity), every other host is
// served on port 80 (the front proxy forwards to Traefik).
func (h HostSet) BaseURLFor(host string) string {
	if u, ok := h.urlOverrides[host]; ok {
		return u
	}
	switch host {
	case "localhost":
		return "http://localhost:8080"
	default:
		return "http://" + host
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
	return HostSet{hosts: h.hosts, primary: h.primary, urlOverrides: overrides}
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
			return hs.WithURLOverride(host, strings.TrimSuffix(in.SSOBaseURL, "/")), nil
		}
		return New(hosts, primary), nil
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
	return New(hosts, primary), nil
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
