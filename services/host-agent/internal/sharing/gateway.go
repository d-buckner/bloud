package sharing

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	container "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/container"
)

// GatewayManagerInterface manages the lifecycle of the gateway Tailscale container.
// The gateway joins the tailnet and exposes a SOCKS5 proxy so Traefik can reach
// remote sidecars via host-agent reverse proxies.
type GatewayManagerInterface interface {
	EnsureRunning(ctx context.Context) error
	Stop(ctx context.Context) error
	IsRunning(ctx context.Context) bool
}

// GatewayManager manages a Tailscale gateway container that runs in userspace mode
// on the host network, exposing a SOCKS5 proxy for reaching remote sidecars.
type GatewayManager struct {
	containers container.Runtime
	authKeyFn  func() string // called at container-creation time for the current auth key
	socksPort  int           // SOCKS5 proxy port (default 1055)
	logger     *slog.Logger
}

const gatewayContainerName = "ts-gateway"

// NewGatewayManager creates a GatewayManager.
//   - containers: runtime for creating/removing the gateway container.
//   - authKeyFn: returns the current Tailscale auth key (called at creation time).
//   - socksPort: port for the SOCKS5 proxy (typically 1055).
func NewGatewayManager(containers container.Runtime, authKeyFn func() string, socksPort int, logger *slog.Logger) *GatewayManager {
	return &GatewayManager{
		containers: containers,
		authKeyFn:  authKeyFn,
		socksPort:  socksPort,
		logger:     logger,
	}
}

// EnsureRunning starts the gateway Tailscale container (idempotent).
// Returns an error if no auth key is configured.
func (m *GatewayManager) EnsureRunning(ctx context.Context) error {
	authKey := m.authKeyFn()
	if authKey == "" {
		return fmt.Errorf("no tailnet connection configured")
	}

	spec := container.Spec{
		Name:    gatewayContainerName,
		Image:   "docker.io/tailscale/tailscale:latest",
		Network: "host",
		Environment: map[string]string{
			"TS_AUTHKEY":       authKey,
			"TS_HOSTNAME":      gatewayContainerName,
			"TS_USERSPACE":     "true",
			"TS_SOCKS5_SERVER": fmt.Sprintf(":%d", m.socksPort),
			"TS_EXTRA_ARGS":    "--accept-routes",
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

// IsRunning returns true if the gateway container is currently running.
func (m *GatewayManager) IsRunning(ctx context.Context) bool {
	state, err := m.containers.Inspect(ctx, gatewayContainerName)
	if err != nil {
		return false
	}
	return state.Running
}
