// SPDX-License-Identifier: AGPL-3.0-only

package hermeswebui

import (
	"context"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// healthPath is the app's own liveness endpoint. It is one of the app's
// PUBLIC_PATHS, so it answers without the app's login and without a session,
// which is what makes it usable both as the container health check and here.
const healthPath = "/health"

// profilesPath is the endpoint that reports which Hermes profile the running
// server resolved, including the provider and model it read out of
// config.yaml. It is the only place the app states, in its own words, that
// the shared agent config actually took effect.
const profilesPath = "/api/profiles"

// mcpServersPath reports the MCP servers the running agent has in its
// config.yaml, with live connection state. It is the read that shows the
// shared $HERMES_HOME working: whatever Bloud wrote for Hermes, this front
// end sees, because it is the same file.
const mcpServersPath = "/api/mcp/servers"

// healthResponse is the subset of /health this app is checked on. The
// endpoint also carries stream and run counters that mean nothing to a
// reconciliation pass, so they are not modeled.
type healthResponse struct {
	Status string `json:"status"`
}

// profilesResponse is the shape of /api/profiles.
type profilesResponse struct {
	Profiles []profileEntry `json:"profiles"`
	Active   string         `json:"active"`
}

// profileEntry is one Hermes profile as the app reports it.
type profileEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsDefault bool   `json:"is_default"`
	IsActive  bool   `json:"is_active"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
}

// active returns the profile the server marked active, falling back to the
// default profile, and then to the first entry. The app sets both flags on
// the same entry today; the fallbacks keep the read meaningful if that ever
// changes rather than reporting nothing.
func (r profilesResponse) active() (profileEntry, bool) {
	for _, p := range r.Profiles {
		if p.IsActive {
			return p, true
		}
	}
	for _, p := range r.Profiles {
		if p.IsDefault {
			return p, true
		}
	}
	if len(r.Profiles) > 0 {
		return r.Profiles[0], true
	}
	return profileEntry{}, false
}

// webuiAPI is the typed surface over the running container's HTTP
// interface. Transport, retry, and timeouts live in appclient.
type webuiAPI struct {
	cl *appclient.Client
}

func newAPI(f configurator.ClientFactory, baseURLFn func() string) *webuiAPI {
	return &webuiAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// health fetches the liveness endpoint.
func (a *webuiAPI) health(ctx context.Context) (healthResponse, error) {
	var out healthResponse
	err := a.cl.GET(healthPath).OK(http.StatusOK).DoInto(ctx, &out)
	return out, err
}

// profiles fetches the profile list.
func (a *webuiAPI) profiles(ctx context.Context) (profilesResponse, error) {
	var out profilesResponse
	err := a.cl.GET(profilesPath).OK(http.StatusOK).DoInto(ctx, &out)
	return out, err
}

// mcpServersResponse is the shape of /api/mcp/servers. The app masks
// credentials in the entries it returns, so nothing sensitive is modeled
// here even by accident.
type mcpServersResponse struct {
	Servers []mcpServerEntry `json:"servers"`
}

// mcpServerEntry is one MCP server as the app reports it. `Status` is the
// app's own verdict: active, configured, disabled, or invalid_config.
type mcpServerEntry struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Enabled   bool   `json:"enabled"`
	Active    bool   `json:"active"`
	Status    string `json:"status"`
}

// mcpServers fetches the agent's MCP server inventory.
func (a *webuiAPI) mcpServers(ctx context.Context) (mcpServersResponse, error) {
	var out mcpServersResponse
	err := a.cl.GET(mcpServersPath).OK(http.StatusOK).DoInto(ctx, &out)
	return out, err
}
