// SPDX-License-Identifier: AGPL-3.0-only

package appconfig

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type staticEntrypoint struct {
	Address          string         `yaml:"address"`
	ForwardedHeaders map[string]any `yaml:"forwardedHeaders"`
}

// forwardedTrustedIPs returns the entrypoint's forwardedHeaders.trustedIPs as
// strings, or nil when the block is absent. It also pins that every entry
// decoded as a quoted string: an unquoted scalar could be misread as a number
// or a sexagesimal, which would silently change which traffic is trusted.
func (e staticEntrypoint) forwardedTrustedIPs(t *testing.T) []string {
	t.Helper()
	raw, ok := e.ForwardedHeaders["trustedIPs"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	require.True(t, ok, "trustedIPs must decode as a list, got %T", raw)
	ips := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		require.True(t, ok, "each trustedIPs entry must be a quoted string, got %T (%v)", item, item)
		ips = append(ips, s)
	}
	return ips
}

type staticConfigShape struct {
	EntryPoints map[string]staticEntrypoint `yaml:"entryPoints"`
	Ping        struct {
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
// Trust is only ever expressed as trustedIPs, including when proxies are set.
func TestTraefikStaticConfig_DoesNotTrustClientForwardedHeaders(t *testing.T) {
	for _, nets := range [][]string{
		nil,
		{},
		{"192.168.1.7"},
		{"10.0.0.0/8", "172.16.0.0/12"},
	} {
		cfg := parseStaticConfig(t, NewTraefikConfigurator(nil, 80, 3000, 9001, t.TempDir(), nets))
		require.NotEmpty(t, cfg.EntryPoints)
		for name, ep := range cfg.EntryPoints {
			require.NotContains(t, ep.ForwardedHeaders, "insecure",
				"insecure: true trusts a client's X-Forwarded-*/X-Real-Ip verbatim (entrypoint %s)", name)
		}
		require.NotContains(t, staticConfigOf(t, nets), "insecure",
			"the word must never appear in the generated static config")
	}
}

func staticConfigOf(t *testing.T, nets []string) string {
	t.Helper()
	return NewTraefikConfigurator(nil, 80, 3000, 9001, t.TempDir(), nets).staticConfig()
}

// With nothing configured, no forwardedHeaders key is emitted at all, so the
// generated file stays byte-identical to a build without the setting and every
// existing deployment keeps Traefik's secure default.
func TestTraefikStaticConfig_NoForwardedHeadersWithoutTrustedProxies(t *testing.T) {
	for _, nets := range [][]string{nil, {}} {
		cfg := parseStaticConfig(t, NewTraefikConfigurator(nil, 80, 3000, 9001, t.TempDir(), nets))
		require.Len(t, cfg.EntryPoints, 2)
		for name, ep := range cfg.EntryPoints {
			require.Nil(t, ep.ForwardedHeaders,
				"an empty trusted-proxy list must emit no forwardedHeaders block (entrypoint %s)", name)
		}
		require.NotContains(t, staticConfigOf(t, nets), "forwardedHeaders",
			"the key itself must be absent when nothing is trusted")
	}

	// A nil list and an empty list render the same bytes.
	require.Equal(t, staticConfigOf(t, nil), staticConfigOf(t, []string{}))
}

// When the operator names the proxy in front of Bloud, every entrypoint Traefik
// gets must carry the list: a single entrypoint left untrusted is enough to keep
// the login flow broken, so partial coverage is a bug rather than a partial fix.
func TestTraefikStaticConfig_TrustedProxyNetsOnEveryEntrypoint(t *testing.T) {
	nets := []string{"192.168.1.7", "10.0.0.0/8"}

	t.Run("canonical port plus compat entrypoint", func(t *testing.T) {
		cfg := parseStaticConfig(t, NewTraefikConfigurator(nil, 80, 3000, 9001, t.TempDir(), nets))
		require.Equal(t, []string{"web", "web-local"}, sortedEntrypointNames(cfg))
		for name, ep := range cfg.EntryPoints {
			require.Equal(t, nets, ep.forwardedTrustedIPs(t),
				"every entrypoint must trust the same upstream proxies (%s)", name)
		}
	})

	t.Run("collapsed single entrypoint", func(t *testing.T) {
		cfg := parseStaticConfig(t, NewTraefikConfigurator(nil, 8080, 3000, 9001, t.TempDir(), nets))
		require.Equal(t, []string{"web"}, sortedEntrypointNames(cfg))
		require.Equal(t, nets, cfg.EntryPoints["web"].forwardedTrustedIPs(t),
			"the native backend's single entrypoint must also trust the upstream proxy")
	})
}

func sortedEntrypointNames(cfg staticConfigShape) []string {
	names := make([]string, 0, len(cfg.EntryPoints))
	for name := range cfg.EntryPoints {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// The canonical entrypoint honours Config.TraefikPort; the compat entrypoint
// stays on 8080 so app containers keep reaching Traefik for OIDC discovery
// (`sso.localhost:8080`) even when the canonical port is 80.
func TestTraefikStaticConfig_Entrypoints(t *testing.T) {
	t.Run("canonical port adds the 8080 compat entrypoint", func(t *testing.T) {
		cfg := parseStaticConfig(t, NewTraefikConfigurator(nil, 80, 3000, 9001, t.TempDir(), nil))
		require.Equal(t, ":80", cfg.EntryPoints["web"].Address)
		require.Equal(t, ":8080", cfg.EntryPoints["web-local"].Address,
			"the compat entrypoint keeps in-container OIDC discovery on 8080")
		require.True(t, cfg.Ping.ManualRouting,
			"base.yml owns /ping so it answers on every entrypoint")
	})

	t.Run("non-default canonical port is honoured", func(t *testing.T) {
		cfg := parseStaticConfig(t, NewTraefikConfigurator(nil, 9090, 3000, 9001, t.TempDir(), nil))
		require.Equal(t, ":9090", cfg.EntryPoints["web"].Address,
			"the configured Traefik port must be honoured")
		require.Equal(t, ":8080", cfg.EntryPoints["web-local"].Address)
	})

	t.Run("8080 canonical collapses to a single entrypoint", func(t *testing.T) {
		cfg := parseStaticConfig(t, NewTraefikConfigurator(nil, 8080, 3000, 9001, t.TempDir(), nil))
		require.Equal(t, ":8080", cfg.EntryPoints["web"].Address)
		require.NotContains(t, cfg.EntryPoints, "web-local",
			"a backend that cannot bind :80 (native, CI) must not emit a duplicate entrypoint")
	})
}
