// SPDX-License-Identifier: AGPL-3.0-only

package affine

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// affineAPI is the typed surface over AFFiNE's HTTP API.
type affineAPI struct {
	cl *appclient.Client
}

// newAPI builds the typed client against a base-URL resolver.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *affineAPI {
	return &affineAPI{cl: f.New(appclient.Spec{Name: "affine", BaseURLFn: baseURLFn})}
}

// waitServer polls /info (public) until the server answers 200. The first
// boot runs prisma migrations before the HTTP listener opens.
func (a *affineAPI) waitServer(ctx context.Context) error {
	return a.cl.GET("/info").
		Interval(2 * time.Second).
		Within(5 * time.Minute).
		Ready(appclient.StatusIs(http.StatusOK)).
		Wait(ctx)
}

// ensureOwner creates the first-run owner account. AFFiNE only accepts the
// call before any user exists and answers 403 "First user already created"
// otherwise: the idempotency signal for later reconciliation passes.
func (a *affineAPI) ensureOwner(ctx context.Context, name, email, password string) (bool, error) {
	return a.cl.POST("/api/setup/create-admin-user").
		JSON(map[string]string{"name": name, "email": email, "password": password}).
		OK(http.StatusOK, http.StatusCreated).
		// AFFiNE has no dedicated code for this case; documented in INTEGRATION.md.
		AlreadyDoneFunc(func(s int, b []byte) bool {
			return s == http.StatusForbidden && bytes.Contains(b, []byte("First user already created"))
		}).
		Ensure(ctx)
}

// waitForOIDCPreflight polls the public OAuth preflight endpoint until it
// returns the authorization URL. The server validates the issuer
// asynchronously after boot (with backoff), so allow a generous window.
func (a *affineAPI) waitForOIDCPreflight(ctx context.Context) error {
	return a.cl.POST("/api/oauth/preflight").
		Anonymous().
		JSON(map[string]string{"provider": "OIDC", "client": "web", "client_nonce": "bloud-poststart-check"}).
		Interval(3 * time.Second).
		Within(3 * time.Minute).
		Ready(func(s int, b []byte) bool {
			return s == http.StatusOK && strings.Contains(string(b), "\"url\"")
		}).
		Wait(ctx)
}
