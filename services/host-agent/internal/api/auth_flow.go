// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"net/url"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
)

// LoginHandler initiates the OIDC login flow.
func (m *authModule) LoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isDirectAgentRequest(r, m.selfPort) {
			http.Error(w, "Login isn't available on this port. Use the app's Traefik URL (e.g. http://localhost:8080) instead.", http.StatusBadRequest)
			return
		}

		cfg := m.getAuthConfig()
		if cfg == nil || cfg.OIDCConfig == nil {
			m.logger.Error("auth not configured")
			http.Error(w, "Authentication not configured", http.StatusServiceUnavailable)
			return
		}

		state, err := generateState()
		if err != nil {
			m.logger.Error("failed to generate state", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     stateCookieName,
			Value:    state,
			Path:     "/",
			MaxAge:   stateCookieMaxAge,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		baseURL := m.oauthBaseURL(r)
		if baseURL == "" {
			m.logger.Error("cannot build OAuth redirect URI: no host set or SSO base URL configured")
			http.Error(w, "Authentication not configured", http.StatusServiceUnavailable)
			return
		}
		// Redirect URIs are registered for every configured host up front
		// (initAuthHelper → EnsureBloudOAuthApp). Nothing is registered from a
		// request: an unauthenticated caller must not be able to widen the OAuth
		// client's redirect-URI allowlist, which is what the previous lazy
		// AddRedirectURI call allowed via a spoofed Host/X-Forwarded-Host.
		redirectURI := baseURL + "/auth/callback"

		authURL, err := url.Parse(baseURL + cfg.OIDCConfig.AuthURL)
		if err != nil {
			m.logger.Error("failed to parse auth URL", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		q := authURL.Query()
		q.Set("client_id", cfg.OIDCConfig.ClientID)
		q.Set("redirect_uri", redirectURI)
		q.Set("response_type", "code")
		q.Set("scope", "openid profile email")
		q.Set("state", state)
		authURL.RawQuery = q.Encode()

		http.Redirect(w, r, authURL.String(), http.StatusFound)
	}
}

// ---- Callback ----

func (m *authModule) CallbackHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := m.getAuthConfig()
		if cfg == nil || cfg.OIDCConfig == nil {
			m.logger.Error("auth not configured")
			http.Error(w, "Authentication not configured", http.StatusServiceUnavailable)
			return
		}

		if !m.verifyCallbackState(w, r) {
			return
		}

		if m.reportProviderError(w, r) {
			return
		}

		userInfo, ok := m.exchangeCallbackCode(w, r, cfg)
		if !ok {
			return
		}

		username := userInfo.PreferredUsername
		if err := m.prefsStore.EnsureUser(username); err != nil {
			m.logger.Error("failed to ensure local user", "error", err)
			http.Error(w, "Failed to create user", http.StatusInternalServerError)
			return
		}

		session, ok := m.createCallbackSession(w, username, userInfo.Groups)
		if !ok {
			return
		}

		m.issueSessionCookie(w, session)
		m.logger.Info("user logged in", "username", username)

		// Redirect to home
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// verifyCallbackState checks the state query param against the state cookie and
// clears the cookie either way. The state is the CSRF guard: it must have come
// from the login this browser started, so a callback minted elsewhere fails here.
func (m *authModule) verifyCallbackState(w http.ResponseWriter, r *http.Request) bool {
	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil {
		m.logger.Warn("missing state cookie")
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return false
	}

	state := r.URL.Query().Get("state")
	if state == "" || state != stateCookie.Value {
		m.logger.Warn("state mismatch", "expected", stateCookie.Value, "got", state)
		http.Error(w, "Invalid state", http.StatusBadRequest)
		return false
	}

	// Clear state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	return true
}

// reportProviderError answers the callback with the provider's own error, if it
// sent one, and reports whether it did.
func (m *authModule) reportProviderError(w http.ResponseWriter, r *http.Request) bool {
	errParam := r.URL.Query().Get("error")
	if errParam == "" {
		return false
	}
	errDesc := r.URL.Query().Get("error_description")
	m.logger.Warn("OAuth error", "error", errParam, "description", errDesc)
	http.Error(w, "Authentication failed: "+errDesc, http.StatusUnauthorized)
	return true
}

// exchangeCallbackCode trades the authorization code for the identity behind
// it. The redirect URI must be byte-identical to the one the authorization
// request carried, so it is rebuilt from the same oauthBaseURL the login used.
func (m *authModule) exchangeCallbackCode(w http.ResponseWriter, r *http.Request, cfg *AuthConfig) (*authentik.UserInfo, bool) {
	code := r.URL.Query().Get("code")
	if code == "" {
		m.logger.Warn("missing authorization code")
		http.Error(w, "Missing authorization code", http.StatusBadRequest)
		return nil, false
	}

	baseURL := m.oauthBaseURL(r)
	if baseURL == "" {
		m.logger.Error("cannot build OAuth redirect URI: no host set or SSO base URL configured")
		http.Error(w, "Authentication not configured", http.StatusServiceUnavailable)
		return nil, false
	}

	tokenResp, err := m.authentikClient.ExchangeCode(
		r.Context(),
		code,
		baseURL+"/auth/callback",
		cfg.OIDCConfig.ClientID,
		cfg.OIDCConfig.ClientSecret,
	)
	if err != nil {
		m.logger.Error("failed to exchange code", "error", err)
		http.Error(w, "Failed to authenticate", http.StatusInternalServerError)
		return nil, false
	}

	userInfo, err := m.authentikClient.GetUserInfo(r.Context(), tokenResp.AccessToken)
	if err != nil {
		m.logger.Error("failed to get user info", "error", err)
		http.Error(w, "Failed to get user info", http.StatusInternalServerError)
		return nil, false
	}
	return userInfo, true
}

// createCallbackSession opens a session for the signed-in identity, with the
// admin role if Authentik says they are in the admins group.
func (m *authModule) createCallbackSession(w http.ResponseWriter, username string, groups []string) (*store.Session, bool) {
	role := store.RoleMember
	for _, group := range groups {
		if group == "authentik Admins" {
			role = store.RoleAdmin
			break
		}
	}

	session, err := m.sessionStore.Create(username, username, role)
	if err != nil {
		m.logger.Error("failed to create session", "error", err)
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return nil, false
	}
	return session, true
}

// issueSessionCookie sets the session cookie the rest of the API reads.
func (m *authModule) issueSessionCookie(w http.ResponseWriter, session *store.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.ID,
		Path:     "/",
		Expires:  session.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ---- Logout ----
