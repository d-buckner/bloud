package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/config"
	containerruntime "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/container"
)

const traefikImage = "docker.io/traefik:v3.4"

// bootstrapTraefik ensures the Traefik reverse proxy is running with proper
// static and dynamic configuration. It runs Traefik on the host network so
// it can reach the host-agent process at localhost:3000.
func bootstrapTraefik(cfg *config.Config, runtime containerruntime.Runtime, logger *slog.Logger) error {
	ctx := context.Background()

	traefikDir := filepath.Join(cfg.DataDir, "traefik")
	dynamicDir := filepath.Join(traefikDir, "dynamic")
	staticConfigPath := filepath.Join(traefikDir, "traefik.yml")

	// Create config directories.
	for _, dir := range []string{traefikDir, dynamicDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	// Write Traefik static config.
	staticConfig := traefikStaticConfig(cfg.TraefikPort)
	if err := writeFileAtomic(staticConfigPath, []byte(staticConfig)); err != nil {
		return fmt.Errorf("write traefik static config: %w", err)
	}

	// Write base dynamic config (host-agent routes).
	baseConfig := traefikBaseDynamicConfig(cfg.Port)
	if err := writeFileAtomic(filepath.Join(dynamicDir, "base.yml"), []byte(baseConfig)); err != nil {
		return fmt.Errorf("write traefik base config: %w", err)
	}

	// Write Authentik routes dynamic config.
	authentikConfig := traefikAuthentikRoutes(cfg.AuthentikPort)
	if err := writeFileAtomic(filepath.Join(dynamicDir, "authentik-routes.yml"), []byte(authentikConfig)); err != nil {
		return fmt.Errorf("write traefik authentik routes: %w", err)
	}

	// Start Traefik container on host network.
	logger.Info("bootstrapping traefik")
	if _, err := runtime.Ensure(ctx, containerruntime.Spec{
		Name:          "apps-traefik",
		Image:         traefikImage,
		Network:       "host",
		RestartPolicy: "always",
		Mounts: []containerruntime.Mount{
			{Source: staticConfigPath, Destination: "/etc/traefik/traefik.yml", Options: []string{"ro"}},
			{Source: dynamicDir, Destination: "/dynamic", Options: []string{"ro"}},
		},
		Labels: map[string]string{"io.bloud.app": "traefik"},
	}); err != nil {
		return fmt.Errorf("ensure traefik container: %w", err)
	}

	logger.Info("traefik started", "port", cfg.TraefikPort)
	return nil
}

// traefikStaticConfig generates the Traefik static configuration YAML.
func traefikStaticConfig(port int) string {
	return `entryPoints:
  web:
    address: ":` + strconv.Itoa(port) + `"
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

// traefikBaseDynamicConfig generates the base dynamic config with host-agent
// routes. Base routes use PathPrefix only (no Host constraint) so they work
// from any origin. App subdomain routes have higher priority (200+) and win
// when a subdomain prefix matches.
func traefikBaseDynamicConfig(hostAgentPort int) string {
	agentURL := "http://localhost:" + strconv.Itoa(hostAgentPort)
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

// traefikAuthentikRoutes generates Traefik dynamic config for routing
// Authentik OAuth/OIDC/API paths to the Authentik server.
// Routes use PathPrefix only (no Host constraint) so they work from any origin.
func traefikAuthentikRoutes(authentikPort int) string {
	authentikURL := "http://localhost:" + strconv.Itoa(authentikPort)
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

// writeFileAtomic writes data to a file atomically by writing to a temp file
// and renaming it into place.
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
