// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// Seerr's Jellyfin coupling: the API key Seerr mints *into Jellyfin* while it
// onboards, and the two calls that let Bloud mint and re-push a fresh one when
// that key stops working.
//
// The paths and payloads are taken from the pinned images and verified live
// against them, so a version bump can re-check each claim:
//
//	seerr v3.4.1  server/routes/auth.ts            the login mints the key
//	              server/routes/settings/index.ts  POST /settings/jellyfin tests
//	                                               the key it is handed
//	jellyfin      Auth/AuthKeysController.cs       POST/GET/DELETE /Auth/Keys
//	              Users/UserController.cs          POST /Users/AuthenticateByName
const (
	// jellyfinKeyPath is the cheapest endpoint that proves an API key: it
	// requires one, and answers 401 for a key Jellyfin does not know. The
	// anonymous /System/Info/Public used as the liveness probe proves nothing
	// about the key.
	jellyfinKeyPath = "/System/Info"

	// jellyfinAuthPath authenticates a user and answers {AccessToken, User}. It
	// is how Bloud obtains the admin token the key-minting call needs.
	jellyfinAuthPath = "/Users/AuthenticateByName"

	// jellyfinKeysPath is Jellyfin's API-key collection: POST mints one (204, no
	// body) and GET lists them with their tokens readable; unlike Prowlarr,
	// Jellyfin does not mask them, so the freshly minted key can be picked up
	// by its app name.
	jellyfinKeysPath = "/Auth/Keys"

	// jellyfinKeyApp is the app name Seerr mints its key under
	// (jellyfinClient.createApiToken('Seerr') in the login route), which is
	// also how the key is found again after it is minted.
	jellyfinKeyApp = "Seerr"

	// jellyfinKeyHeader is how a Jellyfin API key authenticates a request
	// (the Authorization: MediaBrowser Token=… form is equivalent).
	jellyfinKeyHeader = "X-Emby-Token"

	// jellyfinDeviceAuth is the device identity Jellyfin requires on an
	// authenticated request, in the canonical quoted form its own clients send.
	jellyfinDeviceAuth = `MediaBrowser Client="Bloud", Device="Bloud host agent", DeviceId="bloud-host-agent", Version="1.0.0"`

	// settingsJellyfinPath is Seerr's media-server settings resource. A POST
	// merges the body into settings.jellyfin, but only after testing the
	// resulting connection, which is what makes pushing a key safe: Seerr
	// rejects a key Jellyfin does not accept instead of storing it.
	settingsJellyfinPath = apiRoot + "/settings/jellyfin"
)

// jellyfinKey is one record of GET /Auth/Keys.
type jellyfinKey struct {
	AppName     string `json:"AppName"`
	AccessToken string `json:"AccessToken"`
	DateCreated string `json:"DateCreated"`
}

// jellyfinKeyList is the collection GET /Auth/Keys answers.
type jellyfinKeyList struct {
	Items []jellyfinKey `json:"Items"`
}

// readJellyfinKey reads settings.jellyfin.apiKey from Seerr's own settings
// file: the key Seerr minted into Jellyfin at onboarding time.
func readJellyfinKey(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	var doc struct {
		Jellyfin struct {
			APIKey string `json:"apiKey"`
		} `json:"jellyfin"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}
	return doc.Jellyfin.APIKey, nil
}

// jellyfinKeyValid reports whether Jellyfin still accepts a key. A 401/403 is a
// definitive "no"; any other failure (Jellyfin unreachable, a 5xx) is returned
// as an error so the caller can leave the coupling alone rather than conclude
// that a reachable Jellyfin rejected the key.
func (c *Configurator) jellyfinKeyValid(ctx context.Context, key string) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	err := c.jellyfin.GET(jellyfinKeyPath).
		Header(jellyfinKeyHeader, key).
		OK(http.StatusOK).
		NoRetry().
		Exec(probeCtx)
	if err == nil {
		return true, nil
	}
	switch appclient.StatusOf(err) {
	case http.StatusUnauthorized, http.StatusForbidden:
		return false, nil
	default:
		return false, err
	}
}

// jellyfinAdminToken logs the Jellyfin bootstrap admin in and returns the
// session token the key-minting call authenticates with. It is the same
// credential Seerr's onboarding logs in with (bloud-bootstrap-admin, the
// account apps/jellyfin creates and never deletes).
func (c *Configurator) jellyfinAdminToken(ctx context.Context, password string) (string, error) {
	var login struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := c.jellyfin.POST(jellyfinAuthPath).
		Anonymous().
		Header("Authorization", jellyfinDeviceAuth).
		JSON(map[string]string{"Username": jellyfinAdminUsername, "Pw": password}).
		OK(http.StatusOK).
		DoInto(ctx, &login); err != nil {
		return "", fmt.Errorf("logging in to Jellyfin as %s: %w", jellyfinAdminUsername, err)
	}
	if login.AccessToken == "" {
		return "", fmt.Errorf("jellyfin answered the admin login without an access token")
	}
	return login.AccessToken, nil
}

// mintJellyfinKey creates an API key for Bloud in Jellyfin and returns it.
// POST /Auth/Keys answers 204 with no body, so the key is read back from the
// collection (Jellyfin returns tokens in clear there), newest first for the
// app name Seerr mints under, in case an earlier one survived.
func (c *Configurator) mintJellyfinKey(ctx context.Context, adminToken string) (string, error) {
	if err := c.jellyfin.POST(jellyfinKeysPath).
		Query("App", jellyfinKeyApp).
		Header(jellyfinKeyHeader, adminToken).
		OK(http.StatusNoContent).
		Exec(ctx); err != nil {
		return "", fmt.Errorf("minting a %s API key in Jellyfin: %w", jellyfinKeyApp, err)
	}

	var keys jellyfinKeyList
	if err := c.jellyfin.GET(jellyfinKeysPath).
		Header(jellyfinKeyHeader, adminToken).
		OK(http.StatusOK).
		DoInto(ctx, &keys); err != nil {
		return "", fmt.Errorf("listing Jellyfin API keys: %w", err)
	}
	newest := jellyfinKey{}
	for _, key := range keys.Items {
		if key.AppName == jellyfinKeyApp && key.AccessToken != "" && key.DateCreated >= newest.DateCreated {
			newest = key
		}
	}
	if newest.AccessToken == "" {
		return "", fmt.Errorf("jellyfin reports no %s API key after minting one", jellyfinKeyApp)
	}
	return newest.AccessToken, nil
}

// pushJellyfinKey stores a Jellyfin key in Seerr, through the same settings
// resource the app's own UI writes: it tests the key against Jellyfin before
// persisting it, so a key Jellyfin rejects is refused rather than saved.
func (c *Configurator) pushJellyfinKey(ctx context.Context, seerrKey, jellyfinKey string) error {
	return c.api.cl.POST(settingsJellyfinPath).
		Header(apiKeyHeader, seerrKey).
		JSON(map[string]string{"apiKey": jellyfinKey}).
		OK(http.StatusOK).
		Exec(ctx)
}

// reconcileJellyfinCoupling keeps Seerr's media-server connection working after
// Jellyfin is replaced. Seerr mints its Jellyfin API key *into Jellyfin* at
// onboarding and stores it in settings.jellyfin; purging Jellyfin's data (a
// normal Bloud operation) leaves Seerr holding a key the new Jellyfin never
// issued, and Seerr only mints a key while it onboards, so an initialized
// instance never repairs itself: the library list, the library scan, "Play on
// Jellyfin" and metadata refresh all fail with 401, media added later never
// appears, and nothing in the UI says why.
//
// The repair is the pair of calls each app already owns (mint a key in
// Jellyfin, push it into Seerr) and it is deliberately not fatal: Seerr itself
// keeps working (its own login, requests, and the PVR links are unaffected), so
// a failure here is logged and retried on the next reconciliation instead of
// parking the node in the terminal ERROR state.
func (c *Configurator) reconcileJellyfinCoupling(ctx context.Context, settingsPath string) {
	seerrKey, err := readAPIKey(settingsPath)
	if err != nil {
		c.logger.Warn("cannot read Seerr's API key; skipping the Jellyfin connection check", "error", err)
		return
	}
	storedKey, err := readJellyfinKey(settingsPath)
	if err != nil {
		c.logger.Warn("cannot read Seerr's Jellyfin API key; skipping the Jellyfin connection check", "error", err)
		return
	}
	if storedKey == "" {
		// Nothing to verify: an instance that reports itself initialized always
		// has one, but a hand-edited settings file may not, and the wizard is the
		// operator's path back.
		c.logger.Info("Seerr stores no Jellyfin API key; leaving the media-server connection to Seerr's own setup")
		return
	}

	valid, err := c.jellyfinKeyValid(ctx, storedKey)
	if err != nil {
		c.logger.Warn("cannot verify Seerr's Jellyfin API key", "error", err)
		return
	}
	if valid {
		return
	}

	c.logger.Info("Seerr's Jellyfin API key is no longer accepted (the media server was reinstalled or its data was purged); issuing a new one")

	password, err := c.jellyfinAdminPassword()
	if err != nil {
		c.logger.Warn("cannot repair the Jellyfin connection: no bootstrap admin password", "error", err)
		return
	}
	adminToken, err := c.jellyfinAdminToken(ctx, password)
	if err != nil {
		c.logger.Warn("cannot repair the Jellyfin connection", "error", err)
		return
	}
	freshKey, err := c.mintJellyfinKey(ctx, adminToken)
	if err != nil {
		c.logger.Warn("cannot repair the Jellyfin connection", "error", err)
		return
	}
	if err := c.pushJellyfinKey(ctx, seerrKey, freshKey); err != nil {
		c.logger.Warn("cannot store the new Jellyfin API key in Seerr", "error", err)
		return
	}
	c.logger.Info("Seerr's Jellyfin connection was re-established with a new API key")

	// The library list was fetched with the dead key, so it is stale until it is
	// fetched again; the sync is best-effort in the same way as on onboarding.
	c.syncJellyfinLibraries(ctx, seerrKey)
}
