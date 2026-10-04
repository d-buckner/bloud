// SPDX-License-Identifier: AGPL-3.0-only

package immich

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/require"
)

// newTestAPI points the typed Immich client at an httptest server so the
// already-done matcher is exercised through the real appclient retry and
// classification path.
func newTestAPI(t *testing.T, handler http.HandlerFunc) *immichAPI {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return newAPI(configurator.ClientFactory{}, func() string { return srv.URL })
}

// TestCreateAdmin_AdminSetupNotAvailableIsAlreadyDone pins issue #183: current
// Immich answers admin-sign-up for an existing admin with 400 "Admin setup is
// not available", which must read as already-converged rather than an error.
func TestCreateAdmin_AdminSetupNotAvailableIsAlreadyDone(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/auth/admin-sign-up", r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Admin setup is not available"}`))
	})

	require.NoError(t, api.createAdmin(context.Background(), bootstrapAdminName, bootstrapAdminEmail, "pw"))
}

// TestCreateAdmin_AlreadyHasAdminIsAlreadyDone keeps the wording older Immich
// builds used: the fix must widen the matcher, not replace it.
func TestCreateAdmin_AlreadyHasAdminIsAlreadyDone(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`Server already has an admin`))
	})

	require.NoError(t, api.createAdmin(context.Background(), bootstrapAdminName, bootstrapAdminEmail, "pw"))
}

// TestCreateAdmin_OtherBadRequestIsAnError makes sure the widened matcher is
// still narrow: a 400 that does not mean "an admin exists" must surface.
func TestCreateAdmin_OtherBadRequestIsAnError(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Invalid email"}`))
	})

	require.Error(t, api.createAdmin(context.Background(), bootstrapAdminName, bootstrapAdminEmail, "pw"))
}

// TestReplaceCompanionKey_ClearsSameNamedThenMints pins the replacement path:
// Immich never reveals a secret twice, so the only recovery is to delete every
// key carrying the companion's name and mint a fresh one.
func TestReplaceCompanionKey_ClearsSameNamedThenMints(t *testing.T) {
	var deleted []string
	var mintedBody string
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/api-keys":
			require.Equal(t, "Bearer session-1", r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"other","name":"someone-elses"},{"id":"old","name":"bloud-immich-mcp"}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/api-keys/old":
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/api-keys":
			body, _ := io.ReadAll(r.Body)
			mintedBody = string(body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"fresh","name":"bloud-immich-mcp","secret":"new-secret"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	secret, err := api.replaceCompanionKey(context.Background(), "session-1", companionKeyName)
	require.NoError(t, err)
	require.Equal(t, "new-secret", secret)
	require.Equal(t, []string{"/api/api-keys/old"}, deleted,
		"only the key carrying the companion's name is removed")
	require.Contains(t, mintedBody, `"all"`, "the key carries the admin's full permission set")
}

// TestValidateAPIKey_RejectedIsError is the signal that a published token went
// stale: Immich answers 401 for a key deleted or revoked in its UI, and the
// configurator uses that to mint a replacement.
func TestValidateAPIKey_RejectedIsError(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "stale-key", r.Header.Get("x-api-key"))
		w.WriteHeader(http.StatusUnauthorized)
	})

	require.Error(t, api.validateAPIKey(context.Background(), "stale-key"))
}

// TestValidateAPIKey_LiveIsAccepted is the steady state: the stored key still
// authenticates, so the configurator republishes nothing.
func TestValidateAPIKey_LiveIsAccepted(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"k","name":"bloud-immich-mcp"}`))
	})

	require.NoError(t, api.validateAPIKey(context.Background(), "live-key"))
}
