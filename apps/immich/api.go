// SPDX-License-Identifier: AGPL-3.0-only

package immich

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// immichAPI is the typed surface over Immich's HTTP API. Each method reads as
// declared intent; transport, retry, and timeouts live in appclient.
type immichAPI struct {
	cl *appclient.Client
}

// newAPI builds the typed client against a base-URL resolver using the shared
// HTTP factory.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *immichAPI {
	return &immichAPI{cl: f.New(appclient.Spec{Name: "immich", BaseURLFn: baseURLFn})}
}

// waitServer polls /api/server/ping until the server answers 200. The first
// boot runs database migrations, which can take a while.
func (a *immichAPI) waitServer(ctx context.Context) error {
	return a.cl.GET("/api/server/ping").
		Interval(2 * time.Second).
		Within(5 * time.Minute).
		Ready(appclient.StatusIs(http.StatusOK)).
		Wait(ctx)
}

// createAdmin registers the first admin. Only works while no admin exists;
// Immich rejects it with 400 otherwise (which is fine: an admin is present).
func (a *immichAPI) createAdmin(ctx context.Context, name, email, password string) error {
	_, err := a.cl.POST("/api/auth/admin-sign-up").
		JSON(map[string]string{"name": name, "email": email, "password": password}).
		OK(http.StatusCreated).
		// Immich has no dedicated code for "admin already exists"; it returns
		// 400 with a message. Declared here so the idempotency is explicit and
		// logged as already-converged rather than string-guessed at the call site.
		//
		// The message is not stable across releases: older builds answer "already
		// has an admin", newer ones "admin setup is not available". Both mean an
		// admin exists, which is exactly the state this call wants, so both are
		// already-done. Matching only the older wording is what left every
		// reconcile after the first install parking Immich in ERROR (#183).
		AlreadyDoneFunc(func(s int, b []byte) bool {
			if s != http.StatusBadRequest {
				return false
			}
			msg := strings.ToLower(string(b))
			return strings.Contains(msg, "already has an admin") ||
				strings.Contains(msg, "admin setup is not available")
		}).
		Ensure(ctx)
	return err
}

// login exchanges credentials for an access token.
func (a *immichAPI) login(ctx context.Context, email, password string) (string, error) {
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	err := a.cl.POST("/api/auth/login").
		Anonymous().
		JSON(map[string]string{"email": email, "password": password}).
		OK(http.StatusOK).
		DoInto(ctx, &out)
	if err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("empty token")
	}
	return out.AccessToken, nil
}

// apiKeyInfo is one API key as Immich lists it. Immich reveals a key's secret
// exactly once, at creation, so a listed key never carries its value; only the
// id and the name survive for anything but the creation response.
type apiKeyInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// listAPIKeys returns every API key the session's user owns.
func (a *immichAPI) listAPIKeys(ctx context.Context, session string) ([]apiKeyInfo, error) {
	var out []apiKeyInfo
	err := a.cl.GET("/api/api-keys").
		Header("Authorization", "Bearer "+session).
		OK(http.StatusOK).
		DoInto(ctx, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// deleteAPIKey removes one key by id.
func (a *immichAPI) deleteAPIKey(ctx context.Context, session, id string) error {
	return a.cl.DELETE("/api/api-keys/"+id).
		Header("Authorization", "Bearer "+session).
		OK(http.StatusNoContent, http.StatusOK).
		Exec(ctx)
}

// mintAPIKey creates a key with full permissions and returns its secret, the
// only time Immich ever reveals it.
//
// The permission set is `all` rather than a curated list because the principal
// is Bloud's own internal admin: an API key cannot exceed the account that owns
// it, so scoping below `all` would not reduce what the key can reach, only
// which of the account's own powers the companion may use. The wrapper exposes
// the full read-write tool surface and upstream adds tools between releases, so
// a curated list would go stale as a silent 403. The operator revokes the key
// in Immich's own UI, which is what a provider-minted, provider-revocable
// credential buys over a password.
func (a *immichAPI) mintAPIKey(ctx context.Context, session, name string) (string, error) {
	var out struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	err := a.cl.POST("/api/api-keys").
		Header("Authorization", "Bearer "+session).
		JSON(map[string]any{"name": name, "permissions": []string{"all"}}).
		OK(http.StatusCreated).
		DoInto(ctx, &out)
	if err != nil {
		return "", err
	}
	if out.Secret == "" {
		return "", fmt.Errorf("immich returned a key with no secret")
	}
	return out.Secret, nil
}

// validateAPIKey reports whether a stored key still authenticates. Immich
// answers 200 for a live key and 401 for one that was deleted or revoked in its
// UI, which is the only signal that a published token has gone stale.
func (a *immichAPI) validateAPIKey(ctx context.Context, key string) error {
	return a.cl.GET("/api/api-keys/me").
		Header("x-api-key", key).
		NoRetry().
		OK(http.StatusOK).
		Exec(ctx)
}

// replaceCompanionKey mints a fresh key under name, removing any existing keys
// that already carry it first so repeated replacement leaves exactly one behind.
//
// An orphan is possible on the replacement path: the key Bloud holds may have
// been deleted in Immich's UI while an older same-named key still exists, and
// Immich cannot hand back a secret it has already shown, so the only way to a
// working credential is a new key. Clearing by name before minting keeps the
// companion's footprint to one row in Immich's API-keys screen.
func (a *immichAPI) replaceCompanionKey(ctx context.Context, session, name string) (string, error) {
	existing, err := a.listAPIKeys(ctx, session)
	if err != nil {
		return "", err
	}
	for _, key := range existing {
		if key.Name != name {
			continue
		}
		if err := a.deleteAPIKey(ctx, session, key.ID); err != nil {
			return "", err
		}
	}
	return a.mintAPIKey(ctx, session, name)
}
