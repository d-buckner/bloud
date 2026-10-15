// SPDX-License-Identifier: AGPL-3.0-only

package hindsight

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// catalogMetadata is the slice of metadata.yaml this package has to agree with.
// It is deliberately narrow: the loader owns the full schema, and re-declaring it
// here would build a second authority that can drift on its own.
type catalogMetadata struct {
	Port       int                `yaml:"port"`
	Containers []catalogContainer `yaml:"containers"`
	Provides   catalogProvides    `yaml:"provides"`
}

// catalogContainer is the half of a container declaration this package is bound
// to: how it starts, what it publishes, and what it mounts.
type catalogContainer struct {
	Name       string        `yaml:"name"`
	Entrypoint []string      `yaml:"entrypoint"`
	Command    []string      `yaml:"command"`
	Ports      []catalogPort `yaml:"ports"`
	Volumes    []catalogVol  `yaml:"volumes"`
}

type catalogPort struct {
	Host int `yaml:"host"`
}

type catalogVol struct {
	Destination string `yaml:"destination"`
}

type catalogProvides struct {
	MCP struct {
		Values struct {
			Path string `yaml:"path"`
		} `yaml:"values"`
	} `yaml:"mcp"`
}

func loadMetadata(t *testing.T) catalogMetadata {
	t.Helper()
	raw, err := os.ReadFile("metadata.yaml")
	require.NoError(t, err)
	var meta catalogMetadata
	require.NoError(t, yaml.Unmarshal(raw, &meta))
	return meta
}

func container(t *testing.T, meta catalogMetadata, name string) catalogContainer {
	t.Helper()
	for _, c := range meta.Containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("metadata.yaml declares no container %q", name)
	return catalogContainer{}
}

// TestGateCommandMatchesMetadata binds the rendered config to the container that
// finishes it. The config carries a placeholder only the container can resolve,
// so the two halves are one mechanism split across two files: a change to either
// one alone leaves a gate that starts nginx with a literal `@RESOLVER@` in it.
func TestGateCommandMatchesMetadata(t *testing.T) {
	meta := loadMetadata(t)
	gate := container(t, meta, gateNodeName)

	require.Equal(t, []string{"/bin/sh", "-c"}, gate.Entrypoint,
		"the gate must replace the image's entrypoint to finish its own config")
	require.Len(t, gate.Command, 1)
	require.Equal(t, nginxResolverScript(), strings.TrimRight(gate.Command[0], "\n"),
		"gate.go and metadata.yaml must render the same script")

	require.Len(t, gate.Volumes, 1)
	require.Equal(t, nginxMountPath, gate.Volumes[0].Destination,
		"the file the configurator writes must be the file the container reads")
}

// TestOnlyTheGatePublishesTheAppPort is the property the gate exists to hold. If
// the server container published a port too, Traefik would have two ways into the
// app and the gate's locked shape would be a route an attacker walks around.
func TestOnlyTheGatePublishesTheAppPort(t *testing.T) {
	meta := loadMetadata(t)
	require.Equal(t, defaultPort, meta.Port)

	require.Empty(t, container(t, meta, nodeName).Ports,
		"the server publishes no port: the gate is the only way in")
	require.Empty(t, container(t, meta, "apps-hindsight-postgres").Ports,
		"a database that answers the LAN is a second, unauthenticated front door")

	gate := container(t, meta, gateNodeName)
	require.Len(t, gate.Ports, 1)
	require.Equal(t, defaultPort, gate.Ports[0].Host,
		"the gate publishes exactly the app port, which is also the port it listens on")
	require.Equal(t, defaultPort, NewGateConfigurator(0, configurator.Deps{}).Port())
}

// TestMCPRouteClaimsOnlyTheMCPPath checks the match rule the gate is built on.
// The endpoint is both `/mcp` and `/mcp/<bank>/`, so a plain prefix location would
// also claim `/mcpsomething`, and a request for a path no upstream serves would
// be answered by the wrong one.
func TestMCPRouteClaimsOnlyTheMCPPath(t *testing.T) {
	match := regexp.MustCompile(`^/mcp(/|$)`)
	for _, path := range []string{"/mcp", "/mcp/", "/mcp/default/", "/mcp/default"} {
		require.Truef(t, match.MatchString(path), "%s must reach the memory API", path)
	}
	for _, path := range []string{"/mcpsomething", "/api/mcp", "/mcpx", "/", "/login"} {
		require.Falsef(t, match.MatchString(path), "%s belongs to the Control Plane", path)
	}

	// The rendered config is what nginx reads, so the rule has to survive into it.
	require.Contains(t, renderNginxConfig(defaultPort, "bearer", "access-key"),
		"location ~ ^/mcp(/|$) {")
}

// TestMCPRouteMatchesThePublishedContractValue keeps the gate's route and the
// contract's advertised path from drifting: a consumer calls the value in
// metadata.yaml, and the gate serves exactly one prefix.
func TestMCPRouteMatchesThePublishedContractValue(t *testing.T) {
	meta := loadMetadata(t)
	require.Equal(t, mcpPublicPath, meta.Provides.MCP.Values.Path)
	require.Contains(t, renderNginxConfig(defaultPort, "bearer", "access-key"), mcpPublicPath)
}

// TestRenderedConfigRoutesEachSurfaceToItsOwnPort is the split the gate exists to
// make. Both upstreams are the same container name; only the port separates the
// memory API from the Control Plane.
func TestRenderedConfigRoutesEachSurfaceToItsOwnPort(t *testing.T) {
	conf := renderNginxConfig(defaultPort, "bearer", "access-key")

	mcpBlock, uiBlock := splitLocations(t, conf, "/mcp", "/")
	require.Contains(t, mcpBlock, "proxy_pass http://$hindsight_api:8888")
	require.Contains(t, uiBlock, "proxy_pass http://$hindsight_ui:9999")

	// Both upstreams are the server container, and neither is a literal address:
	// a pinned IP goes stale the moment the server is recreated.
	require.Contains(t, conf, "set $hindsight_api "+nodeName)
	require.Contains(t, conf, "set $hindsight_ui "+nodeName)
	require.NotContains(t, conf, "proxy_pass http://10.")

	// A streamable-HTTP server holds an SSE response open, and the Control Plane
	// reports ingestion progress as it happens. A buffering proxy turns both into
	// a page that fills in when the request ends.
	require.Equal(t, 2, strings.Count(conf, "proxy_buffering off"))

	// The health route is answered here, so a server mid-restart is not a dead gate.
	require.Contains(t, conf, "location = /healthz { return 200 \"ok\"; }")
	require.NotContains(t, mcpBlock, "healthz")
}

// TestEachHalfLocksIndependently covers the "not configured yet" shape. Rendering
// the proxy with an empty bearer would be the one configuration worse than a
// refusal, because the API would then be reachable with no credential in front of
// it, and a rotation of one credential must never open the other surface.
func TestEachHalfLocksIndependently(t *testing.T) {
	both := renderNginxConfig(defaultPort, "bearer", "access-key")
	require.NotContains(t, both, "return 401")
	require.NotContains(t, both, "return 503")

	noBearer := renderNginxConfig(defaultPort, "", "access-key")
	require.Contains(t, noBearer, "return 401")
	require.NotContains(t, noBearer, "proxy_pass http://$hindsight_api")
	require.Contains(t, noBearer, "proxy_pass http://$hindsight_ui",
		"a missing bearer must not take the UI down with it")

	noAccessKey := renderNginxConfig(defaultPort, "bearer", "")
	require.Contains(t, noAccessKey, "return 503")
	require.NotContains(t, noAccessKey, "proxy_pass http://$hindsight_ui")
	require.Contains(t, noAccessKey, "proxy_pass http://$hindsight_api",
		"a missing access key must not take the MCP endpoint down with it")

	locked := renderNginxConfig(defaultPort, "", "")
	require.NotContains(t, locked, "proxy_pass",
		"with neither credential the gate serves nothing but its own health route")
	require.Contains(t, locked, "location = /healthz")
}

// TestRenderedConfigCarriesOnlyTheResolverPlaceholder is what the container-side
// substitution has to be able to finish: one token, and no other value the host
// could not have written itself.
func TestRenderedConfigCarriesOnlyTheResolverPlaceholder(t *testing.T) {
	conf := renderNginxConfig(defaultPort, "bearer", "access-key")
	require.Equal(t, 2, strings.Count(conf, nginxResolverPort),
		"one per proxied location, and the container substitutes every one")
	require.NotContains(t, conf, "$resolver")
}

// TestGatePreStartIsIdempotent is invariant 2 for the second node: nginx reads its
// config at start and never again, so the only correct signal is "unchanged".
func TestGatePreStartIsIdempotent(t *testing.T) {
	secrets := newFakeSecrets()
	c := NewGateConfigurator(0, configurator.Deps{Secrets: secrets})
	state := &configurator.AppState{DataPath: t.TempDir()}

	first, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded)
	path := filepath.Join(state.DataPath, configDir, nginxFileName)
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.False(t, second.RestartNeeded)

	rewritten, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(content), string(rewritten))
}

// TestGatePostStartWithoutAProbeClientIsNotAnError covers the CLI path, where no
// HTTP factory exists. A node with nothing to dial must reach RUNNING rather than
// fail a phase it cannot perform.
func TestGatePostStartWithoutAProbeClientIsNotAnError(t *testing.T) {
	c := NewGateConfigurator(0, configurator.Deps{})
	require.Nil(t, c.api)
	require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{}))
}

// TestGatePostStartWithNoBearerIsNotAnError covers the locked shape. The gate is
// up and correct; there is simply no credential to present yet, and holding the
// node below RUNNING for a condition the operator has not had the chance to act on
// would turn a first boot into an ERROR node.
func TestGatePostStartWithNoBearerIsNotAnError(t *testing.T) {
	c := NewGateConfigurator(0, configurator.Deps{HTTP: fakeHTTP{}})
	require.NotNil(t, c.api)
	require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{}))
}

// TestGatePortFollowsTheCatalogPort checks the configurator does not hard-code
// the listen port into the config it renders.
func TestGatePortFollowsTheCatalogPort(t *testing.T) {
	c := NewGateConfigurator(7777, configurator.Deps{})
	require.Equal(t, 7777, c.Port())
	require.Contains(t, renderNginxConfig(c.Port(), "b", "a"), "listen 7777;")
}

// splitLocations returns the two named location blocks out of a rendered config.
// The config is small and machine-generated, so a brace count is enough and beats
// parsing nginx.
func splitLocations(t *testing.T, conf, first, second string) (string, string) {
	t.Helper()
	blocks := map[string]string{}
	for _, loc := range []string{first, second} {
		start := strings.Index(conf, "location "+loc+" {")
		if loc == "/mcp" {
			start = strings.Index(conf, "location ~ ^/mcp(/|$) {")
		}
		require.Greaterf(t, start, -1, "no location for %s", loc)
		depth := 0
		for i := start; i < len(conf); i++ {
			switch conf[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					blocks[loc] = conf[start : i+1]
				}
			}
			if blocks[loc] != "" {
				break
			}
		}
		require.NotEmptyf(t, blocks[loc], "unbalanced braces after location %s", loc)
	}
	return blocks[first], blocks[second]
}
