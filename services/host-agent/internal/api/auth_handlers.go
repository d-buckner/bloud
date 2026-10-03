// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"net/url"
)

// LogoutHandler clears the session and redirects to Authentik for SSO invalidation.
func (m *authModule) LogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get session from cookie and delete from store if available
		cookie, err := r.Cookie(sessionCookieName)
		if err == nil && cookie.Value != "" && m.sessionStore != nil {
			if err := m.sessionStore.Delete(cookie.Value); err != nil {
				m.logger.Warn("failed to delete session", "error", err)
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

		// Redirect to Authentik's native invalidation flow to end the SSO session.
		cfg := m.getAuthConfig()
		if cfg != nil && cfg.OIDCConfig != nil && m.oauthBaseURL(r) != "" {
			baseURL := m.oauthBaseURL(r)
			logoutURL := baseURL + "/if/flow/default-invalidation-flow/?redirect=" + url.QueryEscape(baseURL+"/")
			http.Redirect(w, r, logoutURL, http.StatusFound)
			return
		}

		// Fallback when auth is not configured
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// ---- Current User ----

// GetCurrentUserHandler returns the current authenticated user.
func (m *authModule) GetCurrentUserHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Not authenticated",
			})
			return
		}

		if m.sessionStore == nil {
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Not authenticated",
			})
			return
		}

		session, err := m.sessionStore.Get(cookie.Value)
		if err != nil || session == nil {
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Not authenticated",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"id":       session.UserID,
			"username": session.Username,
			"role":     session.Role,
		})
	}
}
