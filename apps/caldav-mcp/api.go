// SPDX-License-Identifier: AGPL-3.0-only

package caldavmcp

import (
	"context"
	"net/http"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// mcpAPI probes the supergateway health endpoint. The gateway answers "ok" on
// its configured health path while it is up and serving, which is the one fact
// that makes the wrapper real: a process that cannot serve MCP is not doing its
// job.
type mcpAPI struct {
	cl *appclient.Client
}

// newAPI builds the probe client against the gateway's own port.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *mcpAPI {
	return &mcpAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// waitReady polls the health endpoint until the gateway answers 200. First boot
// is a single Node process with no database, so the budget is short.
func (a *mcpAPI) waitReady(ctx context.Context) error {
	return a.cl.GET(healthEndpoint).
		Within(60 * time.Second).
		Ready(appclient.StatusIs(http.StatusOK)).
		Wait(ctx)
}
