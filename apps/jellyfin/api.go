// SPDX-License-Identifier: AGPL-3.0-only

package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// mediaBrowserIdentity is the unauthenticated MediaBrowser auth header Jellyfin
// 10.11+ requires on the login call (it parses Client/Device from the
// Authorization header, not the legacy X-Emby-Authorization).
const mediaBrowserIdentity = `MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0"`

// mediaBrowserAuth builds the MediaBrowser Authorization header for an
// authenticated call. Single source of the format so the string is not repeated
// at every call site (it was duplicated 7× before this client existed).
func mediaBrowserAuth(token string) string {
	return fmt.Sprintf(`%s, Token="%s"`, mediaBrowserIdentity, token)
}

// AuthResponse represents the /Users/AuthenticateByName response.
type AuthResponse struct {
	AccessToken string `json:"AccessToken"`
	User        struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	} `json:"User"`
}

// SystemInfo represents the /System/Info(/Public) response.
type SystemInfo struct {
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ID                     string `json:"Id"`
}

// User represents a Jellyfin user.
type User struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

// VirtualFolder represents a Jellyfin library.
type VirtualFolder struct {
	Name           string   `json:"Name"`
	Locations      []string `json:"Locations"`
	CollectionType string   `json:"CollectionType"`
	ItemId         string   `json:"ItemId"`
}

// jellyfinAPI is the typed surface over Jellyfin's HTTP API. Every method reads
// as declared intent; transport, retry, timeouts and the auth header live in
// appclient / the helpers here. Jellyfin's auth is not a managed global token
// (only some calls carry it), so the client attaches the MediaBrowser header
// per call rather than via a TokenSpec.
type jellyfinAPI struct {
	cl *appclient.Client
}

// newAPI builds the typed client against a base-URL resolver using the shared
// HTTP factory.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *jellyfinAPI {
	return &jellyfinAPI{cl: f.New(appclient.Spec{Name: "jellyfin", BaseURLFn: baseURLFn})}
}

// --- auth ---

// authenticate logs in and returns an access token. Sends the MediaBrowser
// identity (without a token); Jellyfin only parses Client/Device from here.
func (a *jellyfinAPI) authenticate(ctx context.Context, username, password string) (string, error) {
	var out AuthResponse
	err := a.cl.POST("/Users/AuthenticateByName").
		Header("Authorization", mediaBrowserIdentity).
		JSON(map[string]string{"Username": username, "Pw": password}).
		OK(http.StatusOK).
		DoInto(ctx, &out)
	if err != nil {
		return "", err
	}
	return out.AccessToken, nil
}

// --- system info + readiness waits ---

// getSystemInfo fetches the public system info in one shot.
func (a *jellyfinAPI) getSystemInfo(ctx context.Context) (*SystemInfo, error) {
	var info SystemInfo
	if err := a.cl.GET("/System/Info/Public").OK(http.StatusOK).DoInto(ctx, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// waitForSystemInfo polls /System/Info/Public until it answers 200 with JSON.
// The container health check passes on the first 200, but Jellyfin oscillates
// during first-run init (briefly 200 then 503 "Server is loading"). A single
// 503 here must not fail PostStart (the reconciler treats PostStart errors as a
// terminal node ERROR), so wait out the transient.
func (a *jellyfinAPI) waitForSystemInfo(ctx context.Context) (*SystemInfo, error) {
	var info SystemInfo
	err := a.cl.GET("/System/Info/Public").
		WithRetry(systemInfoWaitPolicy).
		Ready(func(status int, body []byte) bool {
			return status == http.StatusOK && json.Unmarshal(body, &info) == nil
		}).
		Wait(ctx)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// awaitWizardCompletion re-polls system info while the wizard still reads as
// pending. It is best-effort: persistent 503 "Server is loading" must never
// fail PostStart, so the terminal error is intentionally swallowed and the last
// good read is returned unchanged. completeStartupWizard gates on
// /Startup/Configuration itself, which absorbs an API that hasn't settled.
func (a *jellyfinAPI) awaitWizardCompletion(ctx context.Context, info *SystemInfo) *SystemInfo {
	if info.StartupWizardCompleted {
		return info
	}
	cur := info
	// The error is deliberately ignored: a slow cold start that outlives the
	// attempt cap falls through rather than bricking the install.
	_ = a.cl.GET("/System/Info/Public").
		WithRetry(wizardCheckPolicy).
		TolerateFailures().
		Ready(func(status int, body []byte) bool {
			var next SystemInfo
			if status != http.StatusOK || json.Unmarshal(body, &next) != nil {
				return false
			}
			cur = &next
			return next.StartupWizardCompleted
		}).
		Wait(ctx)
	if cur != nil && cur.StartupWizardCompleted {
		return cur
	}
	return info
}

// waitForStartupWizardReady waits for the wizard API itself to be reachable.
// Jellyfin may answer 503 with HTML during init even after /health is OK. In
// 10.11.9+ the endpoint moves behind auth and returns 401 when the wizard is
// already complete, declared via AlreadyDone so the wait converges on it.
func (a *jellyfinAPI) waitForStartupWizardReady(ctx context.Context) error {
	return a.cl.GET("/Startup/Configuration").
		WithRetry(wizardReadyPolicy).
		AlreadyDone(http.StatusUnauthorized).
		Ready(func(status int, body []byte) bool {
			return status == http.StatusOK && appclient.JSONValid(status, body)
		}).
		Wait(ctx)
}

// --- setup wizard steps ---

// setStartupConfiguration posts the initial culture/country configuration.
func (a *jellyfinAPI) setStartupConfiguration(ctx context.Context) error {
	return a.cl.POST("/Startup/Configuration").
		JSON(map[string]string{
			"UICulture":                 "en-US",
			"MetadataCountryCode":       "US",
			"PreferredMetadataLanguage": "en",
		}).
		OK(http.StatusOK, http.StatusNoContent).
		Exec(ctx)
}

// setStartupUser waits for Jellyfin's auto-created initial user to become
// available (GET /Startup/User returns 200), then updates it with the managed
// credentials.
func (a *jellyfinAPI) setStartupUser(ctx context.Context, username, password string) error {
	// Best-effort wait for the async initial user to appear. A slow cold start
	// must not fail PostStart (the node would land in terminal ERROR), and the
	// POST below is the authoritative, idempotent step, so the wait's terminal
	// error is deliberately ignored.
	_ = a.cl.GET("/Startup/User").
		WithRetry(startupUserPolicy).
		Ready(appclient.StatusIs(http.StatusOK)).
		Wait(ctx)
	return a.cl.POST("/Startup/User").
		JSON(map[string]string{"Name": username, "Password": password}).
		OK(http.StatusOK, http.StatusNoContent).
		Exec(ctx)
}

// setRemoteAccess configures remote access settings.
func (a *jellyfinAPI) setRemoteAccess(ctx context.Context) error {
	return a.cl.POST("/Startup/RemoteAccess").
		JSON(map[string]bool{"EnableRemoteAccess": true, "EnableAutomaticPortMapping": false}).
		OK(http.StatusOK, http.StatusNoContent).
		Exec(ctx)
}

// completeWizard marks the startup wizard as complete.
func (a *jellyfinAPI) completeWizard(ctx context.Context) error {
	return a.cl.POST("/Startup/Complete").
		OK(http.StatusOK, http.StatusNoContent).
		Exec(ctx)
}

// --- plugin configuration ---

// getPluginConfiguration fetches a plugin's raw JSON configuration.
func (a *jellyfinAPI) getPluginConfiguration(ctx context.Context, token, pluginID string) ([]byte, error) {
	return a.cl.GET(fmt.Sprintf("/Plugins/%s/Configuration", pluginID)).
		Header("Authorization", mediaBrowserAuth(token)).
		OK(http.StatusOK).
		Do(ctx)
}

// setPluginConfiguration updates a plugin's configuration.
func (a *jellyfinAPI) setPluginConfiguration(ctx context.Context, token, pluginID string, config []byte) error {
	return a.cl.POST(fmt.Sprintf("/Plugins/%s/Configuration", pluginID)).
		Header("Authorization", mediaBrowserAuth(token)).
		Body(config, "application/json").
		OK(http.StatusOK, http.StatusNoContent).
		Exec(ctx)
}

// --- users ---

// getUsers lists all users.
func (a *jellyfinAPI) getUsers(ctx context.Context, token string) ([]User, error) {
	var users []User
	if err := a.cl.GET("/Users").
		Header("Authorization", mediaBrowserAuth(token)).
		OK(http.StatusOK).
		DoInto(ctx, &users); err != nil {
		return nil, err
	}
	return users, nil
}

// deleteUser deletes a user by ID.
func (a *jellyfinAPI) deleteUser(ctx context.Context, token, userID string) error {
	return a.cl.DELETE("/Users/"+userID).
		Header("Authorization", mediaBrowserAuth(token)).
		OK(http.StatusOK, http.StatusNoContent).
		Exec(ctx)
}

// deleteBootstrapAdmin removes the bootstrap admin user if present.
func (a *jellyfinAPI) deleteBootstrapAdmin(ctx context.Context, token string) error {
	users, err := a.getUsers(ctx, token)
	if err != nil {
		return fmt.Errorf("getting users: %w", err)
	}
	for _, user := range users {
		if user.Name == bootstrapUsername {
			if err := a.deleteUser(ctx, token, user.ID); err != nil {
				return fmt.Errorf("deleting user: %w", err)
			}
			return nil
		}
	}
	return nil
}

// --- libraries ---

// getVirtualFolders returns all configured libraries.
func (a *jellyfinAPI) getVirtualFolders(ctx context.Context, token string) ([]VirtualFolder, error) {
	var folders []VirtualFolder
	if err := a.cl.GET("/Library/VirtualFolders").
		Header("Authorization", mediaBrowserAuth(token)).
		OK(http.StatusOK).
		DoInto(ctx, &folders); err != nil {
		return nil, err
	}
	return folders, nil
}

// addVirtualFolder creates a new library (the API takes folder metadata as
// query parameters).
func (a *jellyfinAPI) addVirtualFolder(ctx context.Context, token, name, collectionType, path string) error {
	return a.cl.POST("/Library/VirtualFolders").
		Header("Authorization", mediaBrowserAuth(token)).
		Query("name", name).
		Query("collectionType", collectionType).
		Query("paths", path).
		Query("refreshLibrary", "false").
		OK(http.StatusOK, http.StatusNoContent).
		Exec(ctx)
}
