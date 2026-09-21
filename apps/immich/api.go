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
		Timeout(5 * time.Minute).
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
		AlreadyDoneFunc(func(s int, b []byte) bool {
			return s == http.StatusBadRequest && strings.Contains(strings.ToLower(string(b)), "already has an admin")
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
