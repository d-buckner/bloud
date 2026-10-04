// SPDX-License-Identifier: AGPL-3.0-only

package immich

import (
	"context"
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
