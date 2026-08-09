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
	dataDir     string // root data dir — serve configs go under {dataDir}/{appName}/ts-serve/
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

	name := TailnetNodeContainerName(appName)

	// Write serve config file for Tailscale — proxy to Traefik.
	configDir := filepath.Join(m.dataDir, appName, "ts-serve")
	configFile := filepath.Join(configDir, "serve.json")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("create serve config dir: %w", err)
	}
	serveCfg := buildGatewayServeConfig(m.traefikPort)
	data, err := json.MarshalIndent(serveCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal serve config: %w", err)
	}
	if err := os.WriteFile(configFile, data, 0644); err != nil {
		return fmt.Errorf("write serve config: %w", err)
	}

	// Persist Tailscale state so the tailnet node keeps its node identity across restarts.
	stateDir := filepath.Join(m.dataDir, appName, "ts-state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("create tailnet node state dir: %w", err)
	}

	spec := container.Spec{
		Name:     name,
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

	if _, err := m.containers.Ensure(ctx, spec); err != nil {
		return fmt.Errorf("ensure tailnet node container %s: %w", name, err)
	}

	return nil
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
		// Ignore "not found" — container may already be gone.
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
	stateDir := filepath.Join(m.dataDir, appName, "ts-state")
	if err := os.RemoveAll(stateDir); err != nil {
		return fmt.Errorf("purge tailnet node state for %s: %w", appName, err)
	}
	return nil
}
