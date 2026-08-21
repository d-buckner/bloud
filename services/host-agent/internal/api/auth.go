// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
)

const (
	sessionCookieName = "bloud_session"
	stateCookieName   = "bloud_oauth_state"
	stateCookieMaxAge = 10 * 60 // 10 minutes
)

type contextKey string

const userContextKey contextKey = "user"

// AuthConfig holds OIDC configuration for authentication.
// OIDCConfig contains path templates only (no host). Full URLs are derived
// from the incoming request's Host header so OAuth works via any hostname/IP.
type AuthConfig struct {
	OIDCConfig *authentik.OIDCConfig
}

// authConfigRef is a shared, thread-safe reference to the AuthConfig.
// It is shared between the auth and settings modules (and the Server) so a
// post-convergence re-initialization is visible to all consumers without
// mutating module internals. The atomic pointer guards against concurrent
// reads while InitAuth swaps in a fresh config.
type authConfigRef struct {
	p      atomic.Pointer[AuthConfig]
	ensure func() *AuthConfig // re-init factory; nil for static refs (tests)
}

// newAuthConfigRef creates a reference wrapping the given config.
func newAuthConfigRef(cfg *AuthConfig) *authConfigRef {
	ref := &authConfigRef{}
	ref.p.Store(cfg)
	return ref
}

// Get returns the current auth config, or nil if auth is not initialized.
func (r *authConfigRef) Get() *AuthConfig {
	if r == nil {
		return nil
	}
	return r.p.Load()
}

// Set swaps in a fresh auth config. Safe to call concurrently with Get.
func (r *authConfigRef) Set(cfg *AuthConfig) {
	r.p.Store(cfg)
}

// SetEnsure attaches the re-init factory used by Ensure. It captures the
// dependencies (Authentik client, session store, server config) in the scope
// where they are available, so the Server can re-initialize without holding
// them itself.
func (r *authConfigRef) SetEnsure(fn func() *AuthConfig) {
	r.ensure = fn
}

// Ensure re-runs the init factory and swaps in a fresh config if it succeeds.
// Safe to call after system convergence (EnsureBloudOAuthApp is idempotent).
func (r *authConfigRef) Ensure() {
	if r == nil || r.ensure == nil {
		return
	}
	if cfg := r.ensure(); cfg != nil {
		r.Set(cfg)
	}
}

// isLocalRequest returns true if the request originates from localhost.
// Requests from localhost have implicit CLI-level trust. trustedNets lists
// additional CIDRs/IPs treated as local (e.g. a dev VM's slirp NAT gateway,
// where host-forwarded connections arrive from a non-loopback source).
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

// requestBaseURL derives the base URL (scheme + host) from the incoming request.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}

	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   host,
	}
	return u.String()
}

// generateState creates a cryptographically secure random state parameter.
func generateState() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
