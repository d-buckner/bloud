// SPDX-License-Identifier: AGPL-3.0-only

package hindsight

import (
	"context"
	"time"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/mcp"
)

// mcpPublicPath is the path the `mcp` contract publishes, and the only path on
// the gate that reaches the memory API.
const mcpPublicPath = "/mcp"

// mcpAPI probes the app's MCP endpoint the way a consumer calls it.
type mcpAPI struct {
	cl *appclient.Client
}

// newAPI builds the probe client against the address the app publishes.
func newAPI(f configurator.HTTPFactory, baseURLFn func() string) *mcpAPI {
	return &mcpAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// waitServing polls the published MCP path until the server completes the
// handshake.
//
// The budget is short on purpose: both containers have passed their own health
// checks by the time this runs. It also has to fit inside the gate's own 10s
// re-resolution window, because a server container that moved while the gate
// was starting is re-dialled on that timer.
func (a *mcpAPI) waitServing(ctx context.Context, bearer string) error {
	return mcp.Probe(ctx, a.cl, mcpPublicPath, bearer, 45*time.Second)
}
