// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// isLocalRequest reports whether the request's source address is a trusted
// position: loopback, or an address in trustedNets (e.g. a dev VM's slirp NAT
// gateway, where host-forwarded connections arrive from a non-loopback source).
//
// This is a *scope*, not an authorization decision. It says where the API token
// is accepted, never that the caller is trusted. The address is the real TCP
// peer address: host-agent deliberately does not run middleware.RealIP, because
// letting a header rewrite RemoteAddr made this check forgeable.
func isLocalRequest(r *http.Request, trustedNets []string) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, netStr := range trustedNets {
		if _, cidr, err := net.ParseCIDR(netStr); err == nil {
			if cidr.Contains(ip) {
				return true
			}
			continue
		}
		if net.ParseIP(netStr).Equal(ip) {
			return true
		}
	}
	return false
}

// getUserFromContext retrieves the user from the request context.
func getUserFromContext(ctx context.Context) *store.User {
	user, ok := ctx.Value(userContextKey).(*store.User)
	if !ok {
		return nil
	}
	return user
}

// requestHost returns the request's Host header.
//
// Deliberately not X-Forwarded-Host: that header is client-controlled (Traefik
// forwards it verbatim with forwardedHeaders.insecure), and this value becomes an
// OAuth redirect URI registered in the identity provider.
func requestHost(r *http.Request) string {
	return r.Host
}

// isDirectAgentRequest reports whether r bypassed Traefik and hit
// host-agent's own bind port directly, where OIDC login can't work.
func isDirectAgentRequest(r *http.Request, selfPort int) bool {
	if selfPort <= 0 {
		return false
	}
	_, portStr, err := net.SplitHostPort(requestHost(r))
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portStr)
	return err == nil && port == selfPort
}

// oauthBaseURL returns the base URL used for OAuth redirects: the base URL the
// browser used, but only when it is one of the URLs already registered with the
// identity provider: hostset.AllBaseURLs, i.e. the configured hosts plus the
// host's detected local IPs on the entrypoint port, which is exactly what
// initAuthHelper registers (EnsureBloudOAuthApp). Anything else falls back to
// the primary host, so neither the request nor a spoofed X-Forwarded-Host can
// introduce a redirect target.
//
// Matching the registered set rather than only the hostname list is what keeps
// IP access working: the box's IPs are published as base URLs but are not
// hostnames in the set, and bouncing an IP visitor to the primary host would
// break login, because the OAuth state cookie is host-scoped: the callback would
// arrive on a different host than the one that set it.
//
// Returns "" when no host set is configured; callers must then refuse rather than
// fall back to the request's Host header.
func (m *authModule) oauthBaseURL(r *http.Request) string {
	if m.hosts == nil {
		return ""
	}
	hs := m.hosts.Get()
	if len(hs.Hosts()) == 0 {
		return ""
	}

	if host := hostOnly(r.Host); host != "" {
		for _, base := range hs.AllBaseURLs() {
			u, err := url.Parse(base)
			if err != nil {
				continue
			}
			if strings.EqualFold(u.Hostname(), host) {
				return strings.TrimSuffix(base, "/")
			}
		}
	}
	return hs.PrimaryBaseURL()
}

// hostOnly returns the hostname of a Host header, dropping any port.
func hostOnly(hostHeader string) string {
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		return h
	}
	return hostHeader
}

// generateState creates a cryptographically secure random state parameter.
func generateState() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
