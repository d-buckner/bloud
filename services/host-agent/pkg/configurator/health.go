// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package configurator

import (
	"context"
	"fmt"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// WaitForSSOReady waits for an app's SSO integration to be ready: the
// Authentik OpenID Connect discovery endpoint for the app must return a valid
// configuration (one carrying an "issuer" field), proving the provider and
// application have been fully created — not merely that Authentik is answering.
//
// This is the one health helper with a live caller (the CLI configure path). Its
// former hand-rolled siblings (WaitForHTTP / WaitForHTTPWithAuth / WaitForTCP /
// WaitForOpenIDConfig / ShouldWaitForSSO) had zero call sites and were removed
// in favour of appclient's Call.Wait, which this now rides on.
//
// Authentik answering 502/503 while still booting reads as "not ready" to the
// readiness predicate (a non-JSON body has no "issuer"), so the wait retries
// until the discovery document appears or the timeout expires.
func WaitForSSOReady(ctx context.Context, appName string, authentikPort int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	discoveryPath := fmt.Sprintf("/application/o/%s/.well-known/openid-configuration", appName)
	client := appclient.New(appclient.Spec{
		Name:    "sso-ready",
		BaseURL: fmt.Sprintf("http://localhost:%d", authentikPort),
	})
	return client.GET(discoveryPath).
		Ready(appclient.JSONHas("issuer")).
		Interval(2 * time.Second).
		Wait(ctx)
}
