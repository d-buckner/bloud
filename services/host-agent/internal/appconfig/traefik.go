package appconfig

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// TraefikConfigurator manages the Traefik reverse proxy lifecycle.
type TraefikConfigurator struct {
	runtime       containerruntime.Runtime
	traefikPort   int
	hostAgentPort int
	authentikPort int
	dataDir       string
}

// NewTraefikConfigurator creates a new Traefik configurator.
func NewTraefikConfigurator(
	runtime containerruntime.Runtime,
	traefikPort int,
	hostAgentPort int,
	authentikPort int,
	dataDir string,
) *TraefikConfigurator {
	return &TraefikConfigurator{
		runtime:       runtime,
		traefikPort:   traefikPort,
		hostAgentPort: hostAgentPort,
		authentikPort: authentikPort,
		dataDir:       dataDir,
	}
}

func (c *TraefikConfigurator) Name() string { return "traefik" }

func (c *TraefikConfigurator) PreStart(_ context.Context, _ *configurator.AppState) error {
	traefikDir := filepath.Join(c.dataDir, "traefik")
	dynamicDir := filepath.Join(traefikDir, "dynamic")
	staticConfigPath := filepath.Join(traefikDir, "traefik.yml")

	for _, dir := range []string{traefikDir, dynamicDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	if err := writeFileAtomic(staticConfigPath, []byte(c.staticConfig())); err != nil {
		return fmt.Errorf("write traefik static config: %w", err)
	}

	if err := writeFileAtomic(filepath.Join(dynamicDir, "base.yml"), []byte(c.baseDynamicConfig())); err != nil {
		return fmt.Errorf("write traefik base config: %w", err)
	}

	if err := writeFileAtomic(filepath.Join(dynamicDir, "authentik-routes.yml"), []byte(c.authentikRoutes())); err != nil {
		return fmt.Errorf("write traefik authentik routes: %w", err)
	}

	return nil
}

func (c *TraefikConfigurator) PostStart(_ context.Context, _ *configurator.AppState) error {
	return nil
}

func (c *TraefikConfigurator) Remove(ctx context.Context, _ *configurator.AppState, _ bool) error {
	return c.runtime.Remove(ctx, "apps-traefik")
}

func (c *TraefikConfigurator) staticConfig() string {
	return `entryPoints:
  web:
    address: ":` + strconv.Itoa(c.traefikPort) + `"
    forwardedHeaders:
      insecure: true
providers:
  file:
    directory: "/dynamic"
    watch: true
api:
  dashboard: true
ping:
  entryPoint: web
log:
  level: INFO
`
}

func (c *TraefikConfigurator) baseDynamicConfig() string {
	agentURL := "http://localhost:" + strconv.Itoa(c.hostAgentPort)
	return `http:
  routers:
    # Traefik dashboard (access via /dashboard/)
    traefik-dashboard:
      rule: "PathPrefix(` + "`" + `/dashboard` + "`" + `)"
      service: api@internal
      priority: 95

    # Host agent API
    host-api:
      rule: "PathPrefix(` + "`" + `/api` + "`" + `)"
      service: host-agent
      priority: 90

    # Host agent auth routes (OAuth login/callback/logout)
    host-auth:
      rule: "PathPrefix(` + "`" + `/auth` + "`" + `)"
      service: host-agent
      priority: 89

    # Bloud UI (catch-all)
    bloud-ui:
      rule: "PathPrefix(` + "`" + `/` + "`" + `)"
      service: host-agent
      priority: 1

  services:
    host-agent:
      loadBalancer:
        servers:
          - url: "` + agentURL + `"
`
}

func (c *TraefikConfigurator) authentikRoutes() string {
	authentikURL := "http://localhost:" + strconv.Itoa(c.authentikPort)
	return `http:
  routers:
    # Authentik embedded outpost for forward auth
    authentik-outpost:
      rule: "PathPrefix(` + "`" + `/outpost.goauthentik.io` + "`" + `)"
      service: authentik
      priority: 96

    # Authentik API v3 endpoints (higher priority than Bloud /api routes)
    authentik-api:
      rule: "PathPrefix(` + "`" + `/api/v3` + "`" + `)"
      service: authentik
      priority: 95

    # Authentik OAuth/OIDC endpoints
    authentik-application:
      rule: "PathPrefix(` + "`" + `/application` + "`" + `)"
      service: authentik
      priority: 85

    # Authentik flows (login, logout, etc.)
    authentik-flows:
      rule: "PathPrefix(` + "`" + `/flows` + "`" + `)"
      service: authentik
      priority: 85

    # Authentik Identity Frontend UI
    authentik-if:
      rule: "PathPrefix(` + "`" + `/if` + "`" + `)"
      service: authentik
      priority: 85

    # Authentik internal endpoints
    authentik-internal:
      rule: "PathPrefix(` + "`" + `/-` + "`" + `)"
      service: authentik
      priority: 85

    # Authentik static assets
    authentik-static:
      rule: "PathPrefix(` + "`" + `/static` + "`" + `)"
      service: authentik
      priority: 85

    # Authentik WebSocket (admin UI live updates)
    authentik-ws:
      rule: "PathPrefix(` + "`" + `/ws` + "`" + `)"
      service: authentik
      priority: 85

  services:
    authentik:
      loadBalancer:
        servers:
          - url: "` + authentikURL + `"
`
}

func writeFileAtomic(path string, data []byte) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
