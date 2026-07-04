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

// GatewayManagerInterface manages the lifecycle of the gateway Tailscale container.
// The gateway joins the tailnet and exposes a SOCKS5 proxy so Traefik can reach
// remote tailnet nodes via host-agent reverse proxies.
type GatewayManagerInterface interface {
	EnsureRunning(ctx context.Context) error
	Stop(ctx context.Context) error
	StopAndPurge(ctx context.Context) error
	IsRunning(ctx context.Context) bool
	GetTailnetDomain(ctx context.Context) (string, error)
}

// GatewayManager manages a Tailscale gateway container that runs in userspace mode
// on the host network, exposing a SOCKS5 proxy for reaching remote tailnet nodes.
type GatewayManager struct {
	containers  container.Runtime
	exec        ContainerExec // runs commands inside the gateway container
	authKeyFn   func() string // called at container-creation time for the current auth key
	socksPort   int           // SOCKS5 proxy port (default 1055)
	traefikPort int           // Traefik entrypoint port for TS_SERVE_CONFIG proxy target
	dataDir     string        // root data dir — state stored under {dataDir}/ts-gateway/state/
	logger      *slog.Logger
}

const gatewayContainerName = "ts-gateway"

// DefaultGatewaySOCKSPort is the SOCKS5 proxy port exposed by the gateway container.
// The RemoteProxyManager connects through this port to reach remote tailnet nodes.
const DefaultGatewaySOCKSPort = 1055

// DefaultRemoteProxyBasePort is the starting port for localhost reverse proxies
// that front remote apps. Ports are assigned sequentially from this base.
const DefaultRemoteProxyBasePort = 10100

// NewGatewayManager creates a GatewayManager.
//   - containers: runtime for creating/removing the gateway container.
//   - exec: runs commands inside running containers (for tailscale CLI calls).
//   - authKeyFn: returns the current Tailscale auth key (called at creation time).
//   - socksPort: port for the SOCKS5 proxy (typically 1055).
//   - traefikPort: Traefik entrypoint port for TS_SERVE_CONFIG proxy target.
//   - dataDir: root data directory for storing persistent Tailscale state.
func NewGatewayManager(containers container.Runtime, exec ContainerExec, authKeyFn func() string, socksPort, traefikPort int, dataDir string, logger *slog.Logger) *GatewayManager {
	return &GatewayManager{
		containers:  containers,
		exec:        exec,
		authKeyFn:   authKeyFn,
		socksPort:   socksPort,
		traefikPort: traefikPort,
		dataDir:     dataDir,
		logger:      logger,
	}
}

// EnsureRunning starts the gateway Tailscale container (idempotent).
// Returns an error if no auth key is configured.
func (m *GatewayManager) EnsureRunning(ctx context.Context) error {
	authKey := m.authKeyFn()
	if authKey == "" {
		return fmt.Errorf("no tailnet connection configured")
	}

	// Persist Tailscale state so the gateway keeps its node identity across restarts.
	stateDir := filepath.Join(m.dataDir, gatewayContainerName, "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("create gateway state dir: %w", err)
	}

	// Write Tailscale Serve config so the gateway serves HTTPS on port 443,
	// proxying to Traefik on localhost.
	serveConfigDir := filepath.Join(m.dataDir, gatewayContainerName, "ts-serve")
	if err := os.MkdirAll(serveConfigDir, 0755); err != nil {
		return fmt.Errorf("create gateway serve config dir: %w", err)
	}
	serveCfg := buildGatewayServeConfig(m.traefikPort)
	data, err := json.MarshalIndent(serveCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal gateway serve config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(serveConfigDir, "serve.json"), data, 0644); err != nil {
		return fmt.Errorf("write gateway serve config: %w", err)
	}

	spec := container.Spec{
		Name:    gatewayContainerName,
		Image:   TailscaleImage,
		Network: "host",
		Environment: map[string]string{
			"TS_AUTHKEY":       authKey,
			"TS_HOSTNAME":      "bloud",
			"TS_USERSPACE":     "true",
			"TS_SOCKS5_SERVER": fmt.Sprintf(":%d", m.socksPort),
			"TS_EXTRA_ARGS":    "--accept-routes",
			"TS_STATE_DIR":     "/var/lib/tailscale",
			"TS_AUTH_ONCE":     "true",
			"TS_SERVE_CONFIG":  "/etc/ts-serve/serve.json",
		},
		Mounts: []container.Mount{
			{
				Source:      stateDir,
				Destination: "/var/lib/tailscale",
			},
			{
				Source:      serveConfigDir,
				Destination: "/etc/ts-serve",
				Options:     []string{"ro"},
			},
		},
		Labels: map[string]string{
			"io.bloud.gateway": "true",
		},
		RestartPolicy: "always",
	}

	if _, err := m.containers.Ensure(ctx, spec); err != nil {
		return fmt.Errorf("ensure gateway container: %w", err)
	}

	return nil
}

// Stop removes the gateway container. Ignoring "not found" errors makes this
// safe to call even if the gateway was already removed.
func (m *GatewayManager) Stop(ctx context.Context) error {
	if err := m.containers.Remove(ctx, gatewayContainerName); err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no such") {
			return nil
		}
		return fmt.Errorf("remove gateway: %w", err)
	}
	return nil
}

// StopAndPurge stops the gateway container and removes its persisted Tailscale state.
// Call this when the tailnet connection is deleted so a future connection starts fresh.
func (m *GatewayManager) StopAndPurge(ctx context.Context) error {
	_ = m.Stop(ctx)
	stateDir := filepath.Join(m.dataDir, gatewayContainerName, "state")
	if err := os.RemoveAll(stateDir); err != nil {
		return fmt.Errorf("purge gateway state: %w", err)
	}
	return nil
}

// IsRunning returns true if the gateway container is currently running.
func (m *GatewayManager) IsRunning(ctx context.Context) bool {
	state, err := m.containers.Inspect(ctx, gatewayContainerName)
	if err != nil {
		return false
	}
	return state.Running
}

// GetTailnetDomain discovers the tailnet MagicDNS suffix from the running gateway.
// Returns the domain (e.g. "tail12756a.ts.net") or an error if the gateway is not
// running or Tailscale is not connected yet.
func (m *GatewayManager) GetTailnetDomain(ctx context.Context) (string, error) {
	if m.exec == nil {
		return "", fmt.Errorf("container exec not available")
	}

	out, err := m.exec.Exec(ctx, gatewayContainerName, []string{"tailscale", "status", "--json"})
	if err != nil {
		return "", fmt.Errorf("exec tailscale status in gateway: %w", err)
	}

	var status struct {
		MagicDNSSuffix string `json:"MagicDNSSuffix"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return "", fmt.Errorf("parse tailscale status: %w", err)
	}

	domain := strings.TrimSuffix(status.MagicDNSSuffix, ".")
	if domain == "" {
		return "", fmt.Errorf("tailscale MagicDNSSuffix is empty (not connected yet?)")
	}

	return domain, nil
}
