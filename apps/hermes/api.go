// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package hermes

import (
	"context"
	"net/http"
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

// waitDashboard polls /api/health until the dashboard answers 200. The
// path is public upstream (exempt from the dashboard auth gate, which
// would otherwise answer 401), so it is the correct liveness probe.
// First boot seeds $HERMES_HOME before serving, which can take a while.
func (a *hermesAPI) waitDashboard(ctx context.Context) error {
	return a.cl.GET("/api/health").
		Interval(2 * time.Second).
		Timeout(5 * time.Minute).
		Ready(appclient.StatusIs(http.StatusOK)).
		Wait(ctx)
}
