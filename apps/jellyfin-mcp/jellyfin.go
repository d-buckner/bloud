// SPDX-License-Identifier: AGPL-3.0-only

package jellyfinmcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// mediaBrowserIdentity is the unauthenticated MediaBrowser Authorization
// header Jellyfin 10.11 requires on the login call: it parses Client/Device
// from the Authorization header, not the legacy X-Emby-Authorization.
const mediaBrowserIdentity = `MediaBrowser Client="jellyfin-mcp", Device="Bloud Host", DeviceId="bloud-jellyfin-mcp", Version="1.0.0"`

// mediaBrowserAuth builds the MediaBrowser Authorization header for an
// authenticated call.
func mediaBrowserAuth(token string) string {
	return fmt.Sprintf(`%s, Token="%s"`, mediaBrowserIdentity, token)
}

// authResponse is the /Users/AuthenticateByName response.
type authResponse struct {
	AccessToken string `json:"AccessToken"`
}

// apiKeyList is the /auth/keys response: the server's whole API key table.
type apiKeyList struct {
	Items []struct {
		AccessToken string `json:"AccessToken"`
		AppName     string `json:"AppName"`
	} `json:"Items"`
}

// jellyfinClient is the typed surface over the calls this app makes against
// Jellyfin to provision its own credential. It reaches the server from the
// host, at the binding's LocalURL, which is what the caller hands in.
type jellyfinClient struct {
	cl *appclient.Client
}

func newJellyfinClient(cl *appclient.Client) *jellyfinClient {
	return &jellyfinClient{cl: cl}
}

// ensureAPIKey returns the value of the Jellyfin API key named keyName,
// creating it when the server has no key under that name.
//
// Lookup before create, because create is not idempotent: Jellyfin appends a
// new key every time, so a configurator that minted without looking would
// leave a stack of identical keys behind on every Bloud database reset that
// re-ran this pass. Adopting the existing key is also what makes the second
// install of this app reuse the credential the first one made.
func (j *jellyfinClient) ensureAPIKey(ctx context.Context, username, password, keyName string) (string, error) {
	session, err := j.authenticate(ctx, username, password)
	if err != nil {
		return "", err
	}
	if key, found, err := j.findAPIKey(ctx, session, keyName); err != nil {
		return "", err
	} else if found {
		return key, nil
	}
	if err := j.createAPIKey(ctx, session, keyName); err != nil {
		return "", err
	}
	key, found, err := j.findAPIKey(ctx, session, keyName)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("jellyfin created no API key named %q", keyName)
	}
	return key, nil
}

// authenticate logs in with the provider's admin credential and returns a session
// token. The credential is the one the `mediaServer` contract publishes; it is
// used here for exactly one call, to mint the key this app actually runs on. The
// username appears in the error text on purpose: the contract declares it a value
// rather than a secret, and "which account got refused" is the one thing an
// operator pointed at a remote Jellyfin needs to know.
func (j *jellyfinClient) authenticate(ctx context.Context, username, password string) (string, error) {
	var out authResponse
	err := j.cl.POST("/Users/AuthenticateByName").
		Header("Authorization", mediaBrowserIdentity).
		JSON(map[string]string{"Username": username, "Pw": password}).
		OK(http.StatusOK).
		DoInto(ctx, &out)
	if err != nil {
		return "", fmt.Errorf("authenticating as %q on the jellyfin media server: %w", username, err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("jellyfin returned an empty session token")
	}
	return out.AccessToken, nil
}

// findAPIKey looks the key named keyName up in the server's key table.
func (j *jellyfinClient) findAPIKey(ctx context.Context, session, keyName string) (string, bool, error) {
	var list apiKeyList
	err := j.cl.GET("/auth/keys").
		Header("Authorization", mediaBrowserAuth(session)).
		OK(http.StatusOK).
		DoInto(ctx, &list)
	if err != nil {
		return "", false, fmt.Errorf("reading the jellyfin API key table: %w", err)
	}
	for _, item := range list.Items {
		if item.AppName == keyName && item.AccessToken != "" {
			return item.AccessToken, true, nil
		}
	}
	return "", false, nil
}

// createAPIKey asks Jellyfin for a key named keyName. The name is a query
// parameter, not a request body: the endpoint takes `?app=`, and a body is
// rejected with a validation error naming a field that is not in it. Verified
// against Jellyfin 10.11.11.
func (j *jellyfinClient) createAPIKey(ctx context.Context, session, keyName string) error {
	if _, err := j.cl.POST("/auth/keys?app="+url.QueryEscape(keyName)).
		Header("Authorization", mediaBrowserAuth(session)).
		OK(http.StatusOK, http.StatusNoContent).
		Do(ctx); err != nil {
		return fmt.Errorf("creating the jellyfin API key %q: %w", keyName, err)
	}
	return nil
}
