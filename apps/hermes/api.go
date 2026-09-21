// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"context"
	"net/http"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// hermesAPI is the typed surface over the Hermes dashboard HTTP API.
// Transport, retry, and timeouts live in appclient.
type hermesAPI struct {
	cl *appclient.Client
}

// newAPI builds the typed client against a base-URL resolver using the
// shared HTTP factory.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *hermesAPI {
	return &hermesAPI{cl: f.New(appclient.Spec{Name: "hermes", BaseURLFn: baseURLFn})}
}

// waitDashboard polls /api/health until the dashboard answers 200. The path
// is public upstream (exempt from the dashboard auth gate), so it is the
// correct liveness probe. First boot seeds $HERMES_HOME before serving,
// which can take a while.
func (a *hermesAPI) waitDashboard(ctx context.Context) error {
	return a.cl.GET("/api/health").
		Interval(2 * time.Second).
		Timeout(5 * time.Minute).
		Ready(appclient.StatusIs(http.StatusOK)).
		Wait(ctx)
}

// dashboardStatus is the subset of GET /api/status Bloud checks. The fields
// are public (upstream lets unauthenticated probes read the gate state).
type dashboardStatus struct {
	AuthRequired  bool     `json:"auth_required"`
	AuthProviders []string `json:"auth_providers"`
}

// waitSelfHostedProvider polls /api/status until the dashboard reports the
// gate engaged (auth_required) with the self-hosted OIDC provider registered.
// That combination is the observable proof the Bloud config took effect: a
// loopback bind (gate off) or a not-yet-loaded provider never satisfies it.
func (a *hermesAPI) waitSelfHostedProvider(ctx context.Context) error {
	var st dashboardStatus
	return a.cl.GET("/api/status").
		Interval(2 * time.Second).
		Timeout(5 * time.Minute).
		Ready(appclient.DecodeInto(&st, func() bool {
			return st.AuthRequired && hasSelfHostedProvider(st.AuthProviders)
		})).
		Wait(ctx)
}

// hasSelfHostedProvider reports whether the providers list names the
// self-hosted OIDC provider, tolerating the two spellings the plugin exposes
// across releases ("self-hosted" display / "self_hosted" internal).
func hasSelfHostedProvider(providers []string) bool {
	for _, p := range providers {
		if strings.EqualFold(strings.ReplaceAll(p, "_", "-"), "self-hosted") {
			return true
		}
	}
	return false
}
