// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// DeriveAPIKey logs into Seerr with the Jellyfin credentials its admin account
// was created from and returns the API key Seerr stores for itself, which is
// the credential a consumer presents as X-Api-Key.
//
// Seerr's only non-interactive login is the same Jellyfin login its onboarding
// wizard uses: POST /auth/jellyfin with the Jellyfin username and password and
// no hostname, so a configured instance logs the account in rather than
// re-running setup (server/routes/auth.ts). That call answers with a session
// cookie, and the session can then read main.apiKey out of GET /settings/main
// (server/routes/settings/main.ts). Seerr has no token-minting endpoint and no
// independent local account for its admin, so this login-and-read is the only
// way to hand a consumer the key without asking an operator to copy it out of
// the UI.
func DeriveAPIKey(ctx context.Context, f configurator.ClientFactory, baseURLFn func() string, username, password string) (string, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", fmt.Errorf("creating the Seerr session cookie jar: %w", err)
	}
	cl := f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn, Jar: jar})

	if err := cl.POST(apiRoot + "/auth/jellyfin").
		Anonymous().
		JSON(map[string]string{"username": username, "password": password}).
		OK(http.StatusOK).
		Exec(ctx); err != nil {
		return "", fmt.Errorf("logging in to Seerr as %s: %w", username, err)
	}

	var settings struct {
		APIKey string `json:"apiKey"`
	}
	if err := cl.GET(apiRoot+"/settings/main").
		OK(http.StatusOK).
		DoInto(ctx, &settings); err != nil {
		return "", fmt.Errorf("reading Seerr's main settings: %w", err)
	}
	if settings.APIKey == "" {
		return "", fmt.Errorf("seerr's main settings carried no apiKey")
	}
	return settings.APIKey, nil
}
