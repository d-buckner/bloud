// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appconfig

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// TraefikConfigurator manages the Traefik reverse proxy lifecycle.
type TraefikConfigurator struct {
	runtime        containerruntime.Runtime
	traefikPort    int
	traefikTLSPort int
	hostAgentPort  int
	authentikPort  int
	dataDir        string
}

// NewTraefikConfigurator creates a new Traefik configurator.
func NewTraefikConfigurator(
	runtime containerruntime.Runtime,
	traefikPort int,
	traefikTLSPort int,
	hostAgentPort int,
	authentikPort int,
	dataDir string,
) *TraefikConfigurator {
	return &TraefikConfigurator{
		runtime:        runtime,
		traefikPort:    traefikPort,
		traefikTLSPort: traefikTLSPort,
		hostAgentPort:  hostAgentPort,
		authentikPort:  authentikPort,
		dataDir:        dataDir,
	}
}

func (c *TraefikConfigurator) Name() string { return "apps-traefik" }

func (c *TraefikConfigurator) PreStart(_ context.Context, _ *configurator.AppState) (bool, error) {
	traefikDir := filepath.Join(c.dataDir, "traefik")
	dynamicDir := filepath.Join(traefikDir, "dynamic")
	staticConfigPath := filepath.Join(traefikDir, "traefik.yml")

	for _, dir := range []string{traefikDir, dynamicDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return false, fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	changed := false

	files := []struct {
		path    string
		content []byte
	}{
		{staticConfigPath, []byte(c.staticConfig())},
		{filepath.Join(dynamicDir, "base.yml"), []byte(c.baseDynamicConfig())},
		{filepath.Join(dynamicDir, "authentik-routes.yml"), []byte(c.authentikRoutes())},
		{filepath.Join(dynamicDir, "tls.yml"), []byte(c.tlsDynamicConfig())},
	}

	for _, f := range files {
		existing, err := os.ReadFile(f.path)
		if err != nil || !bytes.Equal(existing, f.content) {
			changed = true
		}
		if err := writeFileAtomic(f.path, f.content); err != nil {
			return false, fmt.Errorf("write %s: %w", f.path, err)
		}
	}

	return changed, nil
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
  websecure:
    address: ":` + strconv.Itoa(c.traefikTLSPort) + `"
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

// tlsDynamicConfig writes the certificate store for the websecure entrypoint.
// Traefik v3 moved the TLS store out of the static config: it is dynamic
// configuration, served from the same /dynamic file provider. The default
// certificate is the Bloud leaf (internal/tlsca), mounted at /certs.
func (c *TraefikConfigurator) tlsDynamicConfig() string {
	return `tls:
  stores:
    default:
      defaultCertificate:
        certFile: /certs/server.crt
        keyFile: /certs/server.key
`
}

// routerPair emits a router bound to the default entrypoint (web) and a
// "-tls" twin bound to websecure with TLS enabled. The websecure entrypoint's
// default certificate (the Bloud leaf, mounted at /certs) is used.
func (c *TraefikConfigurator) routerPair(name, rule, service string, priority int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("    %s:\n      rule: \"%s\"\n      service: %s\n      priority: %d\n\n", name, rule, service, priority))
	b.WriteString(fmt.Sprintf("    %s-tls:\n      rule: \"%s\"\n      entryPoints:\n        - websecure\n      tls: true\n      service: %s\n      priority: %d\n\n", name, rule, service, priority))
	return b.String()
}

func (c *TraefikConfigurator) baseDynamicConfig() string {
	agentURL := "http://localhost:" + strconv.Itoa(c.hostAgentPort)
	routers := []struct {
		comment, name, rule, service string
		priority                     int
	}{
		{"Traefik dashboard (access via /dashboard/)", "traefik-dashboard", "PathPrefix(`/dashboard`)", "api@internal", 95},
		{"Host agent API", "host-api", "PathPrefix(`/api`)", "host-agent", 90},
		{"Host agent auth routes (OAuth login/callback/logout)", "host-auth", "PathPrefix(`/auth`)", "host-agent", 89},
		{"Bloud UI (catch-all)", "bloud-ui", "PathPrefix(`/`)", "host-agent", 1},
	}
	var b strings.Builder
	b.WriteString("http:\n  routers:\n")
	for _, r := range routers {
		b.WriteString(fmt.Sprintf("    # %s\n", r.comment))
		b.WriteString(c.routerPair(r.name, r.rule, r.service, r.priority))
	}
	b.WriteString("  services:\n    host-agent:\n      loadBalancer:\n        servers:\n          - url: \"" + agentURL + "\"\n")
	return b.String()
}

func (c *TraefikConfigurator) authentikRoutes() string {
	authentikURL := "http://localhost:" + strconv.Itoa(c.authentikPort)
	routers := []struct {
		comment, name, rule string
		priority            int
	}{
		{"Authentik embedded outpost for forward auth", "authentik-outpost", "PathPrefix(`/outpost.goauthentik.io`)", 96},
		{"Authentik API v3 endpoints (higher priority than Bloud /api routes)", "authentik-api", "PathPrefix(`/api/v3`)", 95},
		{"Authentik OAuth/OIDC endpoints", "authentik-application", "PathPrefix(`/application`)", 85},
		{"Authentik flows (login, logout, etc.)", "authentik-flows", "PathPrefix(`/flows`)", 85},
		{"Authentik Identity Frontend UI", "authentik-if", "PathPrefix(`/if`)", 85},
		{"Authentik internal endpoints", "authentik-internal", "PathPrefix(`/-`)", 85},
		{"Authentik static assets", "authentik-static", "PathPrefix(`/static`)", 85},
		{"Authentik WebSocket (admin UI live updates)", "authentik-ws", "PathPrefix(`/ws`)", 85},
	}
	var b strings.Builder
	b.WriteString("http:\n  routers:\n")
	for _, r := range routers {
		b.WriteString(fmt.Sprintf("    # %s\n", r.comment))
		b.WriteString(c.routerPair(r.name, r.rule, "authentik", r.priority))
	}
	b.WriteString("  services:\n    authentik:\n      loadBalancer:\n        servers:\n          - url: \"" + authentikURL + "\"\n")
	return b.String()
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
