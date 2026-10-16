// SPDX-License-Identifier: AGPL-3.0-only

// Package penpot configures Penpot: the shared environment file every Penpot
// container boots from, the check that the running app picked it up, and the
// publication of Penpot's own MCP endpoint to the apps that consume it.
package penpot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
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

// envStampSuffix names the per-node record of the env bytes a container was
// created from, beside the files themselves. See writeEnvFile for why the
// recreate decision needs one per node rather than one per file.
const envStampSuffix = ".env-stamp"

// defaultPort is the app's own web port: the frontend's published port, and the
// address the frontend configurator reaches the app on from the host. It matches
// metadata.yaml's `port`, and TestMetadataAgreesWithCode pins the two together.
const defaultPort = 9002

// envWriter owns the one generated file the whole install reads. It is bound to
// the backend node because the backend is the first container that needs the
// file and the graph orders it before every other reader.
type envWriter struct {
	logger *slog.Logger
}

// newEnvWriter builds the backend node's configurator.
func newEnvWriter(deps configurator.Deps) *envWriter {
	return &envWriter{logger: configurator.AppLogger(deps.Logger, appName)}
}

func (c *envWriter) Name() string { return backendNode }

// PreStart writes the shared env file.
func (c *envWriter) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	return writeEnvFile(backendNode, state, state.AppExternalURL(appName), c.logger)
}

// PostStart has nothing to verify: the node's health check is the JVM opening
// its listener, which Penpot only does after migrations and startup tasks have
// finished, and every value this install configures is read by the frontend
// node's PostStart, where the app actually serves it.
func (c *envWriter) PostStart(context.Context, *configurator.AppState) error { return nil }

// writeEnvFile renders and writes the install's two dotenv files under
// <appDataDir>/config, reporting a recreate when this node's container was
// created from different bytes than the install is configured with now.
//
// Both the backend and the frontend configurators call this. It is not a
// duplication to be hoisted away: the files are a property of the install, and
// each node that creates a container from one has to be able to guarantee the
// file exists before it does, because a declared envFile that cannot be read is a
// container-create failure.
//
// The recreate decision is per node, and it deliberately is not the file's own
// changed flag. managedfile.Write reports changed=false to whoever writes second,
// so a flag change would restart the first writer and tell the second that nothing
// happened while its own container was still running the previous bytes. That is
// not hypothetical: the frontend kept serving the old config.js after the backend
// had moved on, and no later pass would ever correct it, because every later pass
// also saw an up to date file. So each node stamps the digest of the bytes it last
// created its container from, and restarts when that differs. A steady-state pass
// still writes nothing and restarts nothing, which is what invariant 2 asks of a
// resync.
//
// The admin console gets its own file rather than a share of the first one;
// adminEnvFileName carries the reason.
func writeEnvFile(node string, state *configurator.AppState, publicURL string, logger *slog.Logger) (configurator.PreStartResult, error) {
	dir := filepath.Join(state.DataPath, "config")
	path := filepath.Join(dir, envFileName)

	existing, _ := os.ReadFile(path)
	key, err := secretKey(existing)
	if err != nil {
		return configurator.NoRestart(), err
	}

	settings := publicSettings{publicURL: publicURL, secretKey: key, oidc: state.OIDC}
	env, err := renderEnv(settings)
	if err != nil {
		return configurator.NoRestart(), err
	}
	admin, err := renderAdminEnv(settings)
	if err != nil {
		return configurator.NoRestart(), err
	}

	if err := writeRendered(path, env); err != nil {
		return configurator.NoRestart(), err
	}
	if err := writeRendered(filepath.Join(dir, adminEnvFileName), admin); err != nil {
		return configurator.NoRestart(), err
	}

	stamp := filepath.Join(dir, node+envStampSuffix)
	sum := envDigest(env, admin)
	if previous, err := os.ReadFile(stamp); err == nil && string(previous) == sum {
		return configurator.NoRestart(), nil
	}
	if _, err := managedfile.Write(stamp, []byte(sum), managedfile.ModeHostOnly); err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing penpot env stamp: %w", err)
	}

	logger.Info("wrote Penpot env files", "path", path, "node", node, "sso", state.OIDC != nil)
	return configurator.MustRestart("Penpot env file rewritten"), nil
}

// envDigest fingerprints the bytes one node stamps when it creates its container
// from them. Both files are in it: a node's container reads both, so a change to
// either is a change to that container's inputs.
func envDigest(env, admin string) string {
	sum := sha256.Sum256(append([]byte(env), append([]byte{0}, admin...)...))
	return hex.EncodeToString(sum[:]) + "\n"
}

// writeRendered writes one generated dotenv file, reporting whether the bytes
// changed. Mode 0600: these files hold the OIDC client secret and the signing
// key. The reader is the host runtime, which merges the file into the
// container's environment when it creates the container; no container ever
// opens the file itself, so the app's uid is not a consideration here.
func writeRendered(path, content string) error {
	_, err := managedfile.Write(path, []byte(content), managedfile.ModeHostOnly)
	if err != nil {
		return fmt.Errorf("writing penpot env file: %w", err)
	}
	return nil
}
