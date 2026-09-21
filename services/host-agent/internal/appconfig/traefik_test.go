// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type staticConfigShape struct {
	EntryPoints map[string]struct {
		Address          string         `yaml:"address"`
		ForwardedHeaders map[string]any `yaml:"forwardedHeaders"`
	} `yaml:"entryPoints"`
	Ping struct {
		ManualRouting bool   `yaml:"manualRouting"`
		EntryPoint    string `yaml:"entryPoint"`
	} `yaml:"ping"`
}

func parseStaticConfig(t *testing.T, c *TraefikConfigurator) staticConfigShape {
	t.Helper()
	var cfg staticConfigShape
	require.NoError(t, yaml.Unmarshal([]byte(c.staticConfig()), &cfg),
		"the generated static config must be valid YAML")
	return cfg
}

// The static config decides whether Traefik trusts client-supplied
// X-Forwarded-* headers. `forwardedHeaders.insecure: true` made Traefik skip
// its own DeleteXForwardedHeaders, so a client could assert any source address
// it liked, which is how the host-agent's loopback-admin rule became
// remotely forgeable (PR 4 removed the rule; this pins the other half).
func TestTraefikStaticConfig_DoesNotTrustClientForwardedHeaders(t *testing.T) {
	cfg := parseStaticConfig(t, NewTraefikConfigurator(nil, 80, 3000, 9001, t.TempDir()))

	require.NotEmpty(t, cfg.EntryPoints)
	for name, ep := range cfg.EntryPoints {
		require.NotContains(t, ep.ForwardedHeaders, "insecure",
			"insecure: true trusts a client's X-Forwarded-*/X-Real-Ip verbatim (entrypoint %s)", name)
	}
}

// The canonical entrypoint honours Config.TraefikPort; the compat entrypoint
// stays on 8080 so app containers keep reaching Traefik for OIDC discovery
// (`sso.localhost:8080`) even when the canonical port is 80.
func TestTraefikStaticConfig_Entrypoints(t *testing.T) {
	t.Run("canonical port adds the 8080 compat entrypoint", func(t *testing.T) {
		cfg := parseStaticConfig(t, NewTraefikConfigurator(nil, 80, 3000, 9001, t.TempDir()))
		require.Equal(t, ":80", cfg.EntryPoints["web"].Address)
		require.Equal(t, ":8080", cfg.EntryPoints["web-local"].Address,
			"the compat entrypoint keeps in-container OIDC discovery on 8080")
		require.True(t, cfg.Ping.ManualRouting,
			"base.yml owns /ping so it answers on every entrypoint")
	})

	t.Run("non-default canonical port is honoured", func(t *testing.T) {
		cfg := parseStaticConfig(t, NewTraefikConfigurator(nil, 9090, 3000, 9001, t.TempDir()))
		require.Equal(t, ":9090", cfg.EntryPoints["web"].Address,
			"the configured Traefik port must be honoured")
		require.Equal(t, ":8080", cfg.EntryPoints["web-local"].Address)
	})

	t.Run("8080 canonical collapses to a single entrypoint", func(t *testing.T) {
		cfg := parseStaticConfig(t, NewTraefikConfigurator(nil, 8080, 3000, 9001, t.TempDir()))
		require.Equal(t, ":8080", cfg.EntryPoints["web"].Address)
		require.NotContains(t, cfg.EntryPoints, "web-local",
			"a backend that cannot bind :80 (native, CI) must not emit a duplicate entrypoint")
	})
}
