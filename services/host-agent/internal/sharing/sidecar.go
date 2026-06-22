package sharing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	container "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/container"
)

// SidecarManagerInterface allows the orchestrator to optionally manage
// Tailscale sidecar containers for app sharing.
type SidecarManagerInterface interface {
	EnsureRunning(ctx context.Context, appName string, appPort int) error
	Stop(ctx context.Context, appName string) error
}

// SidecarManager manages Tailscale sidecar containers alongside user apps.
// Each sidecar joins the tailnet via TS_AUTHKEY and exposes the app via
// Tailscale Serve, configured declaratively through TS_SERVE_CONFIG.
// The sidecar's systemd unit is bound to the app's unit via DependsOn,
// so systemd handles lifecycle coupling automatically.
type SidecarManager struct {
	containers container.Runtime
	authKey    string
	network    string
	dataDir    string // root data dir — serve configs go under {dataDir}/{appName}/ts-serve/
	logger     *slog.Logger
}

// NewSidecarManager creates a SidecarManager.
//   - authKey: Tailscale auth key for sidecar containers to join the tailnet.
//   - network: container network sidecars join (typically "apps-net").
//   - dataDir: root data directory for storing serve config files.
func NewSidecarManager(containers container.Runtime, authKey, network, dataDir string, logger *slog.Logger) *SidecarManager {
	return &SidecarManager{
		containers: containers,
		authKey:    authKey,
		network:    network,
		dataDir:    dataDir,
		logger:     logger,
	}
}

// SidecarContainerName returns the container name for an app's sidecar.
func SidecarContainerName(appName string) string {
	return "ts-" + appName
}

// EnsureRunning starts the Tailscale sidecar for the given app (idempotent).
// The sidecar is configured declaratively: TS_SERVE_CONFIG points to a
// pre-generated JSON file, and DependsOn binds the sidecar's systemd unit
// to the app's unit so they share lifecycle.
func (m *SidecarManager) EnsureRunning(ctx context.Context, appName string, appPort int) error {
	name := SidecarContainerName(appName)
	appService := fmt.Sprintf("apps-%s.service", appName)

	// Write serve config file for Tailscale.
	configDir := filepath.Join(m.dataDir, appName, "ts-serve")
	configFile := filepath.Join(configDir, "serve.json")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("create serve config dir: %w", err)
	}
	serveConfig := buildServeConfig(appName, appPort)
	data, err := json.MarshalIndent(serveConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal serve config: %w", err)
	}
	if err := os.WriteFile(configFile, data, 0644); err != nil {
		return fmt.Errorf("write serve config: %w", err)
	}

	spec := container.Spec{
		Name:    name,
		Image:   "docker.io/tailscale/tailscale:latest",
		Network: m.network,
		Environment: map[string]string{
			"TS_AUTHKEY":      m.authKey,
			"TS_HOSTNAME":     name,
			"TS_USERSPACE":    "true",
			"TS_EXTRA_ARGS":   "--accept-routes",
			"TS_SERVE_CONFIG": "/etc/ts-serve/serve.json",
		},
		Mounts: []container.Mount{
			{
				Source:      configDir,
				Destination: "/etc/ts-serve",
				Options:     []string{"ro"},
			},
		},
		Labels: map[string]string{
			"io.bloud.app":     appName,
			"io.bloud.sidecar": "true",
		},
		RestartPolicy: "always",
		DependsOn:     appService,
	}

	if _, err := m.containers.Ensure(ctx, spec); err != nil {
		return fmt.Errorf("ensure sidecar container %s: %w", name, err)
	}

	return nil
}

// Stop removes the sidecar container for the given app. Ignoring
// "not found" errors makes this safe to call even if the sidecar
// was already removed.
func (m *SidecarManager) Stop(ctx context.Context, appName string) error {
	name := SidecarContainerName(appName)
	if err := m.containers.Remove(ctx, name); err != nil {
		// Ignore "not found" — container may already be gone.
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no such") {
			return nil
		}
		return fmt.Errorf("remove sidecar %s: %w", name, err)
	}
	return nil
}

// serveConfig is the Tailscale Serve JSON configuration structure.
// See https://github.com/tailscale/tailscale/blob/main/ipn/serve.go
type serveConfig struct {
	TCP map[string]tcpConfig `json:"TCP"`
	Web map[string]webConfig `json:"Web"`
}

type tcpConfig struct {
	HTTPS bool `json:"HTTPS"`
}

type webConfig struct {
	Handlers map[string]handler `json:"Handlers"`
}

type handler struct {
	Proxy string `json:"Proxy"`
}

// buildServeConfig creates the Tailscale Serve config that proxies HTTPS
// traffic on port 443 to the app container on the shared network.
func buildServeConfig(appName string, appPort int) serveConfig {
	proxyTarget := fmt.Sprintf("http://apps-%s:%d", appName, appPort)
	return serveConfig{
		TCP: map[string]tcpConfig{
			"443": {HTTPS: true},
		},
		Web: map[string]webConfig{
			"${TS_CERT_DOMAIN}:443": {
				Handlers: map[string]handler{
					"/": {Proxy: proxyTarget},
				},
			},
		},
	}
}
