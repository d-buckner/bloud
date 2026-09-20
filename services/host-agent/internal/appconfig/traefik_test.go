// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The static config decides whether Traefik trusts client-supplied
// X-Forwarded-* headers. `forwardedHeaders.insecure: true` made Traefik skip
// its own DeleteXForwardedHeaders, so a client could assert any source address
// it liked — which is how the host-agent's loopback-admin rule became
// remotely forgeable (PR 4 removed the rule; this pins the other half).
func TestTraefikStaticConfig_DoesNotTrustClientForwardedHeaders(t *testing.T) {
	c := NewTraefikConfigurator(nil, 8080, 3000, 9001, t.TempDir())

	var cfg struct {
		EntryPoints map[string]struct {
			Address          string         `yaml:"address"`
			ForwardedHeaders map[string]any `yaml:"forwardedHeaders"`
		} `yaml:"entryPoints"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(c.staticConfig()), &cfg),
		"the generated static config must be valid YAML")

	web, ok := cfg.EntryPoints["web"]
	require.True(t, ok, "the web entrypoint must be defined")
	require.Equal(t, ":8080", web.Address)
	require.NotContains(t, web.ForwardedHeaders, "insecure",
		"insecure: true trusts a client's X-Forwarded-*/X-Real-Ip verbatim")
}

// The proxy must still learn the real client through the values it sets itself.
func TestTraefikStaticConfig_ServesTheExpectedEntrypoint(t *testing.T) {
	c := NewTraefikConfigurator(nil, 9090, 3000, 9001, t.TempDir())

	var cfg struct {
		EntryPoints map[string]struct {
			Address string `yaml:"address"`
		} `yaml:"entryPoints"`
		Ping map[string]string `yaml:"ping"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(c.staticConfig()), &cfg))
	require.Equal(t, ":9090", cfg.EntryPoints["web"].Address, "the configured Traefik port must be honoured")
	require.Equal(t, "web", cfg.Ping["entryPoint"])
}
