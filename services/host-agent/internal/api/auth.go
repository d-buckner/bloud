package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/authentik"
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

// handleLogin initiates the OIDC login flow
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Try lazy initialization if auth isn't configured yet
	// This handles the case where Authentik configurator runs after server start
	if s.authConfig == nil || s.authConfig.OIDCConfig == nil {
		s.tryInitAuth()
	}

	if s.authConfig == nil || s.authConfig.OIDCConfig == nil {
		s.logger.Error("auth not configured")
		http.Error(w, "Authentication not configured", http.StatusServiceUnavailable)
		return
	}

	// Generate state parameter for CSRF protection
	state, err := generateState()
	if err != nil {
		s.logger.Error("failed to generate state", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Store state in a cookie
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   stateCookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	// Build authorization URL using the request's Host header
	baseURL := requestBaseURL(r)
	redirectURI := baseURL + "/auth/callback"

	// Lazily register this redirect URI in Authentik if we haven't seen this host before.
	// This handles access via mDNS, Tailscale, custom DNS, or any unexpected hostname.
	if _, known := s.knownRedirectURIs.Load(redirectURI); !known {
		if s.authentikClient != nil && s.authConfig.OIDCConfig.ProviderID > 0 {
			if err := s.authentikClient.AddRedirectURI(s.authConfig.OIDCConfig.ProviderID, redirectURI); err != nil {
				s.logger.Warn("failed to register redirect URI lazily", "uri", redirectURI, "error", err)
				// Continue anyway — it may already be registered, or the flow may still work
			} else {
				s.logger.Info("lazily registered redirect URI", "uri", redirectURI)
			}
			s.knownRedirectURIs.Store(redirectURI, true)
		}
	}

	authURL, err := url.Parse(baseURL + s.authConfig.OIDCConfig.AuthURL)
	if err != nil {
		s.logger.Error("failed to parse auth URL", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	q := authURL.Query()
	q.Set("client_id", s.authConfig.OIDCConfig.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", "openid profile email")
	q.Set("state", state)
	authURL.RawQuery = q.Encode()

	http.Redirect(w, r, authURL.String(), http.StatusFound)
}

// handleCallback handles the OAuth2 callback
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if s.authConfig == nil || s.authConfig.OIDCConfig == nil {
		s.logger.Error("auth not configured")
		http.Error(w, "Authentication not configured", http.StatusServiceUnavailable)
		return
	}

	// Verify state parameter
	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil {
		s.logger.Warn("missing state cookie")
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	state := r.URL.Query().Get("state")
	if state == "" || state != stateCookie.Value {
		s.logger.Warn("state mismatch", "expected", stateCookie.Value, "got", state)
		http.Error(w, "Invalid state", http.StatusBadRequest)
		return
	}

	// Clear state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	// Check for error response
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		errDesc := r.URL.Query().Get("error_description")
		s.logger.Warn("OAuth error", "error", errParam, "description", errDesc)
		http.Error(w, "Authentication failed: "+errDesc, http.StatusUnauthorized)
		return
	}

	// Get authorization code
	code := r.URL.Query().Get("code")
	if code == "" {
		s.logger.Warn("missing authorization code")
		http.Error(w, "Missing authorization code", http.StatusBadRequest)
		return
	}

	// Build redirect URI from request Host (must match what was sent in /auth/login)
	redirectURI := requestBaseURL(r) + "/auth/callback"

	// Exchange code for tokens
	tokenResp, err := s.authentikClient.ExchangeCode(
		code,
		redirectURI,
		s.authConfig.OIDCConfig.ClientID,
		s.authConfig.OIDCConfig.ClientSecret,
	)
	if err != nil {
		s.logger.Error("failed to exchange code", "error", err)
		http.Error(w, "Failed to authenticate", http.StatusInternalServerError)
		return
	}

	// Get user info
	userInfo, err := s.authentikClient.GetUserInfo(tokenResp.AccessToken)
	if err != nil {
		s.logger.Error("failed to get user info", "error", err)
		http.Error(w, "Failed to get user info", http.StatusInternalServerError)
		return
	}

	// Get or create local user
	user, err := s.userStore.GetByUsername(userInfo.PreferredUsername)
	if err != nil {
		s.logger.Error("failed to get user", "error", err)
		http.Error(w, "Failed to get user", http.StatusInternalServerError)
		return
	}

	// If user doesn't exist locally, create them (on-demand sync from Authentik)
	if user == nil {
		if err := s.userStore.Create(userInfo.PreferredUsername); err != nil {
			s.logger.Error("failed to create local user", "error", err)
			http.Error(w, "Failed to create user", http.StatusInternalServerError)
			return
		}
		user, err = s.userStore.GetByUsername(userInfo.PreferredUsername)
		if err != nil {
			s.logger.Error("failed to get created user", "error", err)
			http.Error(w, "Failed to get user", http.StatusInternalServerError)
			return
		}
	}

	// Create session
	ctx := r.Context()
	session, err := s.sessionStore.Create(ctx, user.ID, user.Username)
	if err != nil {
		s.logger.Error("failed to create session", "error", err)
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	// Set session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.ID,
		Path:     "/",
		Expires:  session.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	s.logger.Info("user logged in", "username", user.Username)

	// Redirect to home
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleLogout clears the session and redirects to Authentik logout
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Get session from cookie and delete from store if available
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && cookie.Value != "" && s.sessionStore != nil {
		ctx := r.Context()
		if err := s.sessionStore.Delete(ctx, cookie.Value); err != nil {
			s.logger.Warn("failed to delete session", "error", err)
		}
	}

	// Clear session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	// Redirect to home (which will show login page)
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleGetCurrentUser returns the current authenticated user
func (s *Server) handleGetCurrentUser(w http.ResponseWriter, r *http.Request) {
	// This endpoint is public so we need to check session manually
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		respondJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Not authenticated",
		})
		return
	}

	// Check if session store is available
	if s.sessionStore == nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Not authenticated",
		})
		return
	}

	// Get session from store
	ctx := r.Context()
	session, err := s.sessionStore.Get(ctx, cookie.Value)
	if err != nil || session == nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Not authenticated",
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"id":       session.UserID,
		"username": session.Username,
	})
}

// authMiddleware checks for a valid session and adds user to context
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check for session cookie
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Not authenticated",
			})
			return
		}

		// Get session from store
		ctx := r.Context()
		session, err := s.sessionStore.Get(ctx, cookie.Value)
		if err != nil {
			s.logger.Error("failed to get session", "error", err)
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Failed to validate session",
			})
			return
		}

		if session == nil {
			// Session not found or expired
			// Clear the invalid cookie
			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookieName,
				Value:    "",
				Path:     "/",
				MaxAge:   -1,
				HttpOnly: true,
			})
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Session expired",
			})
			return
		}

		// Check if session is expired (belt and suspenders - Redis TTL should handle this)
		if time.Now().After(session.ExpiresAt) {
			s.sessionStore.Delete(ctx, session.ID)
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Session expired",
			})
			return
		}

		// Create user object from session
		user := &store.User{
			ID:       session.UserID,
			Username: session.Username,
		}

		// Add user to context
		ctx = context.WithValue(ctx, userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// getUserFromContext retrieves the user from the request context
func getUserFromContext(ctx context.Context) *store.User {
	user, ok := ctx.Value(userContextKey).(*store.User)
	if !ok {
		return nil
	}
	return user
}

// requestBaseURL derives the base URL (scheme + host) from the incoming request.
// This allows OAuth redirects to work with any hostname/IP the user accesses.
// It respects X-Forwarded-Proto and X-Forwarded-Host headers set by Traefik.
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

// generateState creates a cryptographically secure random state parameter
func generateState() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
