// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package sharing

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	container "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
)

// ProxyOutpostImage is the Authentik proxy outpost container image.
// Must match the Authentik server version to avoid API skew.
const ProxyOutpostImage = "ghcr.io/goauthentik/proxy:2025.10.3"

// ProxyOutpostPort is the host port for the standalone proxy outpost.
// The outpost listens on port 9000 inside the container; mapped to 9002 on the host
// (9001 is already used by the Authentik server).
const ProxyOutpostPort = 9002

const proxyOutpostContainerName = "apps-authentik-proxy-outpost"

// ProxyOutpostManagerInterface manages the standalone proxy outpost container.
// The proxy outpost handles forward-auth for tailnet access, running separately
// from the embedded outpost so AUTHENTIK_HOST_BROWSER can point to the tailnet URL.
type ProxyOutpostManagerInterface interface {
	EnsureRunning(ctx context.Context, token, tailnetDomain string) error
	Stop(ctx context.Context) error
}

// ProxyOutpostManager manages the standalone Authentik proxy outpost container.
type ProxyOutpostManager struct {
	containers container.Runtime
	logger     *slog.Logger
}

// NewProxyOutpostManager creates a ProxyOutpostManager.
func NewProxyOutpostManager(containers container.Runtime, logger *slog.Logger) *ProxyOutpostManager {
	return &ProxyOutpostManager{
		containers: containers,
		logger:     logger,
	}
}

// EnsureRunning starts the standalone proxy outpost container (idempotent).
// The outpost connects to Authentik via the apps-net Docker network and serves
// forward-auth on port 9002 (mapped from container port 9000).
func (m *ProxyOutpostManager) EnsureRunning(ctx context.Context, token, tailnetDomain string) error {
	if token == "" {
		return fmt.Errorf("proxy outpost token is empty")
	}
	if tailnetDomain == "" {
		return fmt.Errorf("tailnet domain is empty")
	}

	spec := container.Spec{
		Name:     proxyOutpostContainerName,
		Image:    ProxyOutpostImage,
		Networks: []string{"apps-net"},
		Environment: map[string]string{
			"AUTHENTIK_HOST":         "http://apps-authentik-server:9000",
			"AUTHENTIK_HOST_BROWSER": "https://bloud." + tailnetDomain,
			"AUTHENTIK_TOKEN":        token,
			"AUTHENTIK_INSECURE":     "true",
		},
		Ports: []container.Port{
			{Host: ProxyOutpostPort, Container: 9000},
		},
		Labels: map[string]string{
			"io.bloud.app":           "authentik",
			"io.bloud.proxy-outpost": "true",
		},
		RestartPolicy: "always",
	}

	if _, err := m.containers.Ensure(ctx, spec); err != nil {
		return fmt.Errorf("ensure proxy outpost container: %w", err)
	}

	return nil
}

// Stop removes the proxy outpost container. Ignoring "not found" errors makes
// this safe to call even if the container was already removed.
func (m *ProxyOutpostManager) Stop(ctx context.Context) error {
	if err := m.containers.Remove(ctx, proxyOutpostContainerName); err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no such") {
			return nil
		}
		return fmt.Errorf("remove proxy outpost: %w", err)
	}
	return nil
}
