// SPDX-License-Identifier: AGPL-3.0-only

package affinemcp

import (
	"context"
	"net/http"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// readyPath is the image's readiness probe. Unlike /healthz (process liveness),
// it checks the configured AFFiNE GraphQL endpoint and returns 503 with the
// failing component, which is what makes it worth probing here: a wrapper whose
// target is unreachable is not serving MCP even though the process is alive.
const readyPath = "/readyz"

// mcpAPI is the typed surface over the wrapper's own HTTP server. Every method
// reads as declared intent; transport, retry, and timeouts live in appclient.
type mcpAPI struct {
	cl *appclient.Client
}

func newAPI(f configurator.ClientFactory, baseURLFn func() string) *mcpAPI {
	return &mcpAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// waitReady polls /readyz until the server reports AFFiNE reachable, or the wait
// budget runs out. First boot is fast (a Node process, no database), so the
// budget is short.
func (a *mcpAPI) waitReady(ctx context.Context) error {
	return a.cl.GET(readyPath).
		Within(60 * time.Second).
		Ready(appclient.StatusIs(http.StatusOK)).
		Wait(ctx)
}
