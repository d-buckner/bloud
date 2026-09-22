// SPDX-License-Identifier: AGPL-3.0-only

package appconfig

import (
	"bytes"
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
	runtime          containerruntime.Runtime
	traefikPort      int
	hostAgentPort    int
	authentikPort    int
	dataDir          string
	trustedProxyNets []string
}

// NewTraefikConfigurator creates a new Traefik configurator. trustedProxyNets
// is the set of addresses (IP or CIDR, as seen from Traefik) of the reverse
// proxy directly in front of Bloud; see TraefikConfigurator.staticConfig for
// what the list does and an empty list for the default behaviour.
func NewTraefikConfigurator(
	runtime containerruntime.Runtime,
	traefikPort int,
	hostAgentPort int,
	authentikPort int,
	dataDir string,
	trustedProxyNets []string,
) *TraefikConfigurator {
	return &TraefikConfigurator{
		runtime:          runtime,
		traefikPort:      traefikPort,
		hostAgentPort:    hostAgentPort,
		authentikPort:    authentikPort,
		dataDir:          dataDir,
		trustedProxyNets: trustedProxyNets,
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

// traefikCompatPort is always bound alongside Config.TraefikPort. The dev VMs
// expose Config.TraefikPort (80) on the host as 8080 and resolve
// `sso.localhost` to the guest, so app containers reach Traefik on 8080 for
// OIDC discovery. Binding it everywhere keeps that path working, and lets a
// deployment that cannot bind a privileged port (native, CI) run on 8080
// alone by setting BLOUD_TRAEFIK_PORT=8080.
const traefikCompatPort = 8080

func (c *TraefikConfigurator) staticConfig() string {
	// Traefik's default is to discard client-supplied X-Forwarded-* / X-Real-Ip
	// from untrusted peers and set them itself. That default stays unless the
	// operator names an upstream proxy in trustedProxyNets, which adds
	// forwardedHeaders.trustedIPs so the original scheme survives one more hop
	// (see entrypointYaml). `insecure: true` is never emitted: it disabled the
	// discard entirely, letting any client assert an arbitrary source address
	// (PR 4 removed the host-agent rule that made that exploitable).
	entrypoints := c.entrypointYaml("web", c.traefikPort)
	if c.traefikPort != traefikCompatPort {
		entrypoints += c.entrypointYaml("web-local", traefikCompatPort)
	}
	return `entryPoints:
` + entrypoints + `providers:
  file:
    directory: "/dynamic"
    watch: true
api:
  dashboard: true
# manualRouting keeps the ping@internal service but drops Traefik's default
# ping router, so base.yml can route /ping on every entrypoint (the container
# health check probes the compat port, which is not always the canonical one).
ping:
  manualRouting: true
log:
  level: INFO
`
}

// entrypointYaml renders one entrypoint block. With no trusted proxy nets it is
// just the address, which keeps the emitted config byte-identical to a build
// without the setting. With them it adds forwardedHeaders.trustedIPs, so
// Traefik accepts X-Forwarded-* from the proxy directly in front of it and
// passes the original scheme on: without that, a TLS terminator leaves the
// hop to Traefik in plain HTTP, Traefik rewrites X-Forwarded-Proto to "http",
// and Authentik reads an HTTPS request as HTTP and generates http:// URLs the
// browser then blocks as mixed content, stalling the login flow.
//
// Trust is scoped to the source address, not the header: a peer outside the
// list keeps the secure default, and its X-Forwarded-* is overwritten. Each
// entry is quoted so YAML reads it as a string whatever it contains.
func (c *TraefikConfigurator) entrypointYaml(name string, port int) string {
	block := "  " + name + ":\n    address: \":" + strconv.Itoa(port) + "\"\n"
	if len(c.trustedProxyNets) == 0 {
		return block
	}
	block += "    forwardedHeaders:\n      trustedIPs:\n"
	for _, proxy := range c.trustedProxyNets {
		block += "        - " + strconv.Quote(proxy) + "\n"
	}
	return block
}

func (c *TraefikConfigurator) baseDynamicConfig() string {
	agentURL := "http://localhost:" + strconv.Itoa(c.hostAgentPort)
	return `http:
  routers:
    # Liveness probe, served by Traefik itself on every entrypoint (the static
    # config enables ping.manualRouting so this router can own /ping).
    traefik-ping:
      rule: "PathPrefix(` + "`" + `/ping` + "`" + `)"
      service: ping@internal
      priority: 96

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
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
