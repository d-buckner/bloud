// SPDX-License-Identifier: AGPL-3.0-only

package penpot

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// frontendConfigurator owns the routed node: the one the browser and every
// consumer reach, and therefore the one that can check what the install actually
// became.
type frontendConfigurator struct {
	ssoBaseURL func() string
	logger     *slog.Logger
	api        *penpotAPI
	mcp        *mcpPublisher

	// baseURL is a test seam: when set, the API client resolves to it instead
	// of localhost:defaultPort.
	baseURL string
}

func newFrontendConfigurator(deps configurator.Deps) *frontendConfigurator {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("app", appName)
	c := &frontendConfigurator{
		ssoBaseURL: deps.PrimaryBaseURL,
		logger:     logger,
	}
	c.api = newAPI(deps.HTTP, func() string {
		if c.baseURL != "" {
			return c.baseURL
		}
		return fmt.Sprintf("http://localhost:%d", defaultPort)
	})
	c.mcp = &mcpPublisher{
		secrets: deps.Secrets,
		exec:    deps.Exec,
		running: deps.ContainerRunning,
		email:   deps.OperatorEmail,
		logger:  logger,
	}
	return c
}

func (c *frontendConfigurator) Name() string { return frontendNode }

// PreStart guarantees the shared env file exists before this node's container is
// created. See writeEnvFile for why both writers are correct rather than one.
func (c *frontendConfigurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	return writeEnvFile(state, c.appExternalURL(), c.logger)
}

// PostStart waits for the frontend to serve, checks that the generated
// configuration is what the running app handed the browser, and syncs the MCP
// offer with what Penpot itself has.
func (c *frontendConfigurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	configJS, err := c.api.waitConfig(ctx)
	if err != nil {
		return fmt.Errorf("waiting for penpot frontend: %w", err)
	}

	flags := flagsIn(configJS)
	var missing []string
	for _, want := range requiredFlags(state) {
		if !flags[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("penpot is serving flags without %s; the generated env file did not reach the container (serving %q)",
			strings.Join(missing, ", "), servedFlags(flags))
	}

	c.mcp.sync(ctx)
	return nil
}

func (c *frontendConfigurator) appExternalURL() string {
	return configurator.AppExternalURL(c.ssoBaseURL, appName)
}

// requiredFlags are the flags whose absence means the install is not what
// metadata and the configurator intended.
//
// enable-mcp and enable-admin-console are not cosmetic: the frontend's
// entrypoint installs or deletes the nginx location blocks that reach those two
// containers by grepping this very string, so without them the containers run and
// nothing routes to them. disable-registration is the sign-in policy, and with
// SSO the two OIDC flags are what make a first sign-in possible at all.
func requiredFlags(state *configurator.AppState) []string {
	want := []string{"enable-mcp", "enable-admin-console", "disable-registration"}
	if state != nil && state.OIDC != nil {
		want = append(want, "enable-login-with-oidc", "enable-oidc-registration")
	}
	return want
}

// servedFlags renders the flags the app is serving, for the error message.
func servedFlags(flags map[string]bool) string {
	out := make([]string, 0, len(flags))
	for flag := range flags {
		out = append(out, flag)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}
