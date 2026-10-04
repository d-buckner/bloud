// SPDX-License-Identifier: AGPL-3.0-only

// Package backrest wires the Backrest restic web UI into Bloud.
//
// Backrest is the orchestrator for restic: repositories, plans, schedules,
// retention, and restore. Bloud's job here is narrow and declarative. It gives
// the container read-only access to every app's private data, seeds one local
// repository and one plan that covers that tree, and then leaves the file to
// Backrest, which owns it from the first boot on.
package backrest

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

// appName is the catalog id, the label on the container, and the scope of the
// app's generated secrets.
const appName = "backrest"

// nodeName is this app's graph node and container name; see NodeLifecycle.Name.
const nodeName = "apps-backrest"

// defaultPort is the app's own web port, the value the constructor uses when
// registration passes 0. It matches metadata.yaml's `port`.
const defaultPort = 9898

const (
	// configFileName is the Backrest config, mounted at containerConfigPath.
	configFileName = "config.json"

	// containerAppDataDir is where metadata.yaml mounts $DATA_DIR/apps. Every
	// app's private tree is beneath it, so one plan covers them all.
	containerAppDataDir = "/bloud/apps"

	// containerRepoDir is where metadata.yaml mounts the default backup
	// destination, <dataDir>/backups.
	containerRepoDir = "/repos"

	// repoDirName is the restic repository inside containerRepoDir.
	repoDirName = "bloud"

	// backupsDirName is the shared backup destination under the Bloud data
	// directory. It sits beside, not inside, apps/, so the read-only app-tree
	// mount cannot see the repository it writes.
	backupsDirName = "backups"

	// resticPasswordKey is the private app secret holding the default
	// repository's encryption password. It is not declared under `provides:`
	// because no other app consumes it; it stays scoped to this app. Losing it
	// means losing access to every snapshot the default repository holds, so it
	// is generated once and reused, never rotated on a reconcile.
	resticPasswordKey = "resticPassword"
)

// Configurator implements configurator.NodeLifecycle for the Backrest node.
type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
	api     *backrestAPI
}

// NewConfigurator builds the configurator from the host Deps. It tolerates a
// zero Deps (no logger, no secrets, no runtime): CLI and test contexts supply
// none, and the contract is that PreStart still runs without panicking.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:    port,
		secrets: deps.Secrets,
		logger:  logger.With("app", appName),
	}
	c.api = newAPI(deps.HTTP, func() string { return fmt.Sprintf("http://localhost:%d", c.port) })
	return c
}

// Name reports the graph node this configurator manages.
func (c *Configurator) Name() string { return nodeName }

// PreStart creates the directories the container mounts and seeds the config
// on first boot. Both are idempotent: directories are MkdirAll, and the config
// is written only when absent.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	if state == nil {
		return configurator.NoRestart(), nil
	}
	if err := ensureDirs(state); err != nil {
		return configurator.NoRestart(), err
	}
	changed, err := c.ensureConfig(state)
	if err != nil {
		return configurator.NoRestart(), err
	}
	return configurator.RestartIf(changed, "seeded the Backrest repository and app-folders plan"), nil
}

// PostStart reads the running config back and reports what Backrest loaded. It
// is deliberately best-effort: the health check already proved the process is
// serving, and two legitimate states make the read fail without being faults.
// A transient read is one; the other is the operator switching Backrest's own
// login on in its UI, which answers this credential-less call with 401. Neither
// should move the node to ERROR, so both are logged and the pass succeeds.
func (c *Configurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	cfg, err := c.api.getConfig(ctx)
	if err != nil {
		if appclient.StatusOf(err) == 401 {
			c.logger.Info("Backrest's own login is enabled; skipping the config read")
			return nil
		}
		c.logger.Warn("could not read the Backrest config", "error", err)
		return nil
	}
	c.logger.Info("Backrest is serving",
		"instance", cfg.Instance, "repos", len(cfg.Repos), "plans", len(cfg.Plans))
	return nil
}

// ensureDirs creates every bind-mount source. Podman fails a create when a
// mount source is missing, so the app's own directories and the two shared
// ones it mounts have to exist before the container is built.
func ensureDirs(state *configurator.AppState) error {
	for _, dir := range []string{"data", "config", "cache", "userdata"} {
		if err := os.MkdirAll(filepath.Join(state.DataPath, dir), 0o755); err != nil {
			return fmt.Errorf("creating Backrest's %s directory: %w", dir, err)
		}
	}
	if state.BloudDataPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(state.BloudDataPath, backupsDirName), 0o755); err != nil {
		return fmt.Errorf("creating the backup destination: %w", err)
	}
	return nil
}

// ensureConfig writes the seed config when it is absent and never again.
//
// Backrest owns config.json once it exists: it rewrites the file to add its
// multihost identity on the first load and to record every repository, plan,
// and schedule the user edits in its UI. Rewriting it here on each reconcile
// would silently discard all of that, so the seed is one-shot by design. A
// wiped data directory (a clear-data uninstall, or a fresh install) is the only
// path back to a seed.
func (c *Configurator) ensureConfig(state *configurator.AppState) (bool, error) {
	path := filepath.Join(state.DataPath, "config", configFileName)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("checking %s: %w", path, err)
	}

	if c.secrets == nil {
		// CLI and test contexts have no store, so there is no stable password
		// to encrypt the default repository with. Leave the file to Backrest,
		// which creates its own default config on first boot; the next real
		// pass cannot seed it (the file now exists), so this is a limitation of
		// those contexts rather than of an install. Host-agent always supplies
		// the store.
		c.logger.Warn("no secret store available; leaving the Backrest config to the app")
		return false, nil
	}

	password, err := c.ensureResticPassword()
	if err != nil {
		return false, err
	}
	content, err := renderConfig(password)
	if err != nil {
		return false, err
	}
	changed, err := managedfile.Write(path, content, managedfile.ModeHostOnly)
	if err != nil {
		return false, fmt.Errorf("writing the Backrest config: %w", err)
	}
	if changed {
		c.logger.Info("seeded the Backrest repository and app-folders plan", "path", path)
	}
	return changed, nil
}

// ensureResticPassword returns the default repository's encryption password,
// generating it on first use. It is stored in the host secret store so a
// container recreate, a reinstall that keeps data, or a restore of the data
// directory all keep a working repository.
func (c *Configurator) ensureResticPassword() (string, error) {
	if existing := c.secrets.GetAppSecret(appName, resticPasswordKey); existing != "" {
		return existing, nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating the restic repository password: %w", err)
	}
	password := base64.RawURLEncoding.EncodeToString(buf)
	if err := c.secrets.SetAppSecret(appName, resticPasswordKey, password); err != nil {
		return "", fmt.Errorf("storing the restic repository password: %w", err)
	}
	c.logger.Info("generated the default restic repository password")
	return password, nil
}
