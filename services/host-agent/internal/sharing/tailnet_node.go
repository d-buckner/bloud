// SPDX-License-Identifier: AGPL-3.0-only

package sharing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	container "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/dirs"
)

// TailscaleImage is the pinned Tailscale container image used for tailnet nodes
// and the gateway. Using the "stable" tag provides a defined stability contract
// (tracks the latest stable release) without the unpredictability of "latest".
const TailscaleImage = "docker.io/tailscale/tailscale:stable"

// ContainerExec runs commands inside containers (satisfied by podman.Client).
type ContainerExec interface {
	Exec(ctx context.Context, containerName string, cmd []string) ([]byte, error)
}

// TailnetNodeManagerInterface allows the orchestrator to optionally manage
// Tailscale tailnet node containers for app sharing.
type TailnetNodeManagerInterface interface {
	EnsureRunning(ctx context.Context, appName string) error
	GetAddr(ctx context.Context, appName string) (string, error)
	Stop(ctx context.Context, appName string) error
	StopAndPurge(ctx context.Context, appName string) error
}

// TailnetNodeManager manages Tailscale tailnet node containers for user apps.
// Each tailnet node joins the tailnet via TS_AUTHKEY and exposes the app via
// Tailscale Serve, configured declaratively through TS_SERVE_CONFIG.
// Tailnet nodes run on the host network and proxy to Traefik, which routes to the
// app based on the Host header.
type TailnetNodeManager struct {
	containers  container.Runtime
	exec        ContainerExec
	authKeyFn   func() string // called at creation time to get the current auth key
	traefikPort int
	dataDir     string // root data dir: serve configs go under {dataDir}/{appName}/ts-serve/
	logger      *slog.Logger
}

// NewTailnetNodeManager creates a TailnetNodeManager.
//   - containers: runtime for creating/removing tailnet node containers.
//   - exec: runs commands inside running containers (for tailscale CLI calls).
//   - authKeyFn: returns the current Tailscale auth key (called at creation time).
//   - traefikPort: Traefik entrypoint port that tailnet nodes proxy to.
//   - dataDir: root data directory for storing serve config files.
func NewTailnetNodeManager(containers container.Runtime, exec ContainerExec, authKeyFn func() string, traefikPort int, dataDir string, logger *slog.Logger) *TailnetNodeManager {
	return &TailnetNodeManager{
		containers:  containers,
		exec:        exec,
		authKeyFn:   authKeyFn,
		traefikPort: traefikPort,
		dataDir:     dataDir,
		logger:      logger,
	}
}

// TailnetNodeContainerName returns the container name for an app's tailnet node.
func TailnetNodeContainerName(appName string) string {
	return "ts-" + appName
}

// EnsureRunning starts the Tailscale tailnet node for the given app (idempotent).
// The tailnet node runs on the host network and proxies to Traefik, which routes
// to the app based on the Host header. TS_SERVE_CONFIG points to a
// pre-generated JSON file with the serve configuration.
func (m *TailnetNodeManager) EnsureRunning(ctx context.Context, appName string) error {
	authKey := m.authKeyFn()
	if authKey == "" {
		return fmt.Errorf("no tailnet connection configured")
	}

	configDir, stateDir, err := m.writeNodeFiles(appName)
	if err != nil {
		return err
	}

	if _, err := m.containers.Ensure(ctx, tailnetNodeSpec(appName, authKey, configDir, stateDir)); err != nil {
		return fmt.Errorf("ensure tailnet node container %s: %w", TailnetNodeContainerName(appName), err)
	}

	return nil
}

// writeNodeFiles lays down what the tailnet node mounts: the serve config that
// proxies to Traefik, and the state directory that lets the node keep its own
// identity across restarts.
func (m *TailnetNodeManager) writeNodeFiles(appName string) (configDir, stateDir string, err error) {
	appDir := dirs.AppDataDir(m.dataDir, appName)
	configDir = filepath.Join(appDir, "ts-serve")
	stateDir = filepath.Join(appDir, "ts-state")

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return "", "", fmt.Errorf("create serve config dir: %w", err)
	}
	data, err := json.MarshalIndent(buildGatewayServeConfig(m.traefikPort), "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("marshal serve config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "serve.json"), data, 0644); err != nil {
		return "", "", fmt.Errorf("write serve config: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return "", "", fmt.Errorf("create tailnet node state dir: %w", err)
	}
	return configDir, stateDir, nil
}

// tailnetNodeSpec builds the node container: host network so it can carry the
// tailnet, userspace so it needs no root, and the two directories above
// mounted where Tailscale expects them. TS_AUTH_ONCE makes a restart reuse the
// identity in the state dir rather than re-authenticating.
func tailnetNodeSpec(appName, authKey, configDir, stateDir string) container.Spec {
	return container.Spec{
		Name:     TailnetNodeContainerName(appName),
		Image:    TailscaleImage,
		Networks: []string{"host"},
		Environment: map[string]string{
			"TS_AUTHKEY":      authKey,
			"TS_HOSTNAME":     appName,
			"TS_USERSPACE":    "true",
			"TS_EXTRA_ARGS":   "--accept-routes",
			"TS_SERVE_CONFIG": "/etc/ts-serve/serve.json",
			"TS_STATE_DIR":    "/var/lib/tailscale",
			"TS_AUTH_ONCE":    "true",
		},
		Mounts: []container.Mount{
			{
				Source:      configDir,
				Destination: "/etc/ts-serve",
				Options:     []string{"ro"},
			},
			{
				Source:      stateDir,
				Destination: "/var/lib/tailscale",
			},
		},
		Labels: map[string]string{
			"io.bloud.app":          appName,
			"io.bloud.tailnet-node": "true",
		},
		RestartPolicy: "always",
	}
}

// GetAddr returns the Tailscale IPv4 address of the tailnet node container.
func (m *TailnetNodeManager) GetAddr(ctx context.Context, appName string) (string, error) {
	name := TailnetNodeContainerName(appName)
	out, err := m.exec.Exec(ctx, name, []string{"tailscale", "ip", "--4"})
	if err != nil {
		return "", fmt.Errorf("get tailscale addr for %s: %w", name, err)
	}
	addr := strings.TrimSpace(string(out))
	if addr == "" {
		return "", fmt.Errorf("tailnet node %s has no tailscale address", name)
	}
	return addr, nil
}

// Stop removes the tailnet node container for the given app. Ignoring
// "not found" errors makes this safe to call even if the tailnet node
// was already removed.
func (m *TailnetNodeManager) Stop(ctx context.Context, appName string) error {
	name := TailnetNodeContainerName(appName)
	if err := m.containers.Remove(ctx, name); err != nil {
		// Ignore "not found": container may already be gone.
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no such") {
			return nil
		}
		return fmt.Errorf("remove tailnet node %s: %w", name, err)
	}
	return nil
}

// StopAndPurge stops the tailnet node container and removes its persisted Tailscale state.
// Call this when the tailnet connection is deleted so a future connection starts fresh.
func (m *TailnetNodeManager) StopAndPurge(ctx context.Context, appName string) error {
	_ = m.Stop(ctx, appName)
	stateDir := filepath.Join(dirs.AppDataDir(m.dataDir, appName), "ts-state")
	if err := os.RemoveAll(stateDir); err != nil {
		return fmt.Errorf("purge tailnet node state for %s: %w", appName, err)
	}
	return nil
}
