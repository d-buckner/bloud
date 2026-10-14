// SPDX-License-Identifier: AGPL-3.0-only

// Package penpot configures Penpot: the shared environment file every Penpot
// container boots from, the check that the running app picked it up, and the
// publication of Penpot's own MCP endpoint to the apps that consume it.
package penpot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const appName = "penpot"

// The graph nodes of the install. Penpot is a multi-container app in Bloud's
// sense: every one of these is its own node, its own health check, and its own
// entry in the dependency graph (invariant 4).
const (
	backendNode      = "apps-penpot-backend"
	frontendNode     = "apps-penpot-frontend"
	exporterNode     = "apps-penpot-exporter"
	mcpNode          = "apps-penpot-mcp"
	adminConsoleNode = "apps-penpot-admin-console"
	postgresNode     = "apps-penpot-postgres"
	valkeyNode       = "apps-penpot-valkey"
)

// defaultPort is the app's own web port: the frontend's published port, and the
// address the frontend configurator reaches the app on from the host. It matches
// metadata.yaml's `port`, and TestMetadataAgreesWithCode pins the two together.
const defaultPort = 9002

// envWriter owns the one generated file the whole install reads. It is bound to
// the backend node because the backend is the first container that needs the
// file and the graph orders it before every other reader.
type envWriter struct {
	ssoBaseURL func() string
	logger     *slog.Logger
}

// newEnvWriter builds the backend node's configurator.
func newEnvWriter(deps configurator.Deps) *envWriter {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &envWriter{ssoBaseURL: deps.PrimaryBaseURL, logger: logger.With("app", appName)}
}

func (c *envWriter) Name() string { return backendNode }

// PreStart writes the shared env file.
func (c *envWriter) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	return writeEnvFile(state, c.appExternalURL(), c.logger)
}

// PostStart has nothing to verify: the node's health check is the JVM opening
// its listener, which Penpot only does after migrations and startup tasks have
// finished, and every value this install configures is read by the frontend
// node's PostStart, where the app actually serves it.
func (c *envWriter) PostStart(context.Context, *configurator.AppState) error { return nil }

// appExternalURL returns the public URL the browser uses to reach Penpot, e.g.
// "http://penpot.localhost:8080". Penpot puts it in every redirect it builds and
// in the URL the browser is sent to after the OIDC round trip, so it has to be
// the same origin Bloud registered the redirect URI on.
func (c *envWriter) appExternalURL() string {
	return configurator.AppExternalURL(c.ssoBaseURL, appName)
}

// writeEnvFile renders and writes the install's two dotenv files under
// <appDataDir>/config, reporting a recreate when either file's bytes changed.
//
// Both the backend and the frontend configurators call this. It is not a
// duplication to be hoisted away: the files are a property of the install, and
// each node that creates a container from one has to be able to guarantee the
// file exists before it does, because a declared envFile that cannot be read is
// a container-create failure. The renderer is deterministic and the signing key
// is read back out of the previous file, so the second writer converges on the
// same bytes and reports changed=false.
//
// The admin console gets its own file rather than a share of the first one;
// adminEnvFileName carries the reason.
func writeEnvFile(state *configurator.AppState, publicURL string, logger *slog.Logger) (configurator.PreStartResult, error) {
	dir := filepath.Join(state.DataPath, "config")
	path := filepath.Join(dir, envFileName)

	existing, _ := os.ReadFile(path)
	key, err := secretKey(existing)
	if err != nil {
		return configurator.NoRestart(), err
	}

	settings := publicSettings{publicURL: publicURL, secretKey: key, oidc: state.OIDC}
	changed, err := writeRendered(path, settings, renderEnv)
	if err != nil {
		return configurator.NoRestart(), err
	}

	adminChanged, err := writeRendered(filepath.Join(dir, adminEnvFileName), settings, renderAdminEnv)
	if err != nil {
		return configurator.NoRestart(), err
	}

	if !changed && !adminChanged {
		return configurator.NoRestart(), nil
	}
	logger.Info("wrote Penpot env files", "path", path, "sso", state.OIDC != nil)
	return configurator.MustRestart("Penpot env file rewritten"), nil
}

// writeRendered writes one generated dotenv file, reporting whether the bytes
// changed. Mode 0600: these files hold the OIDC client secret and the signing
// key. The reader is the host runtime, which merges the file into the
// container's environment when it creates the container; no container ever
// opens the file itself, so the app's uid is not a consideration here.
func writeRendered(path string, s publicSettings, render func(publicSettings) (string, error)) (bool, error) {
	content, err := render(s)
	if err != nil {
		return false, err
	}
	changed, err := managedfile.Write(path, []byte(content), managedfile.ModeHostOnly)
	if err != nil {
		return false, fmt.Errorf("writing penpot env file: %w", err)
	}
	return changed, nil
}
