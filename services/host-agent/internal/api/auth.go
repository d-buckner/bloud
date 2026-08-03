package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"

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

// isLocalRequest returns true if the request originates from localhost.
// Requests from localhost have implicit CLI-level trust.
func isLocalRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
