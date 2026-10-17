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
	Name        string            `yaml:"name"`
	Entrypoint  []string          `yaml:"entrypoint"`
	Command     []string          `yaml:"command"`
	Ports       []catalogPort     `yaml:"ports"`
	Volumes     []catalogVol      `yaml:"volumes"`
	Environment map[string]string `yaml:"environment"`
}

type catalogPort struct {
	Host int `yaml:"host"`
}

type catalogVol struct {
	Destination string `yaml:"destination"`
}

type catalogProvides struct {
	Memory struct {
		Secrets []string `yaml:"secrets"`
	} `yaml:"memory"`
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

// TestAPIRoutesClaimOnlyTheirOwnPaths checks the match rules the gate is built
// on. Both are anchored and complete: a plain prefix location on `/v1` would also
// claim `/v123` that no upstream serves, and `/version` is a single path with
// nothing below it.
func TestAPIRoutesClaimOnlyTheirOwnPaths(t *testing.T) {
	prefix := regexp.MustCompile("^" + apiPathPrefix + "(/|$)")
	for _, path := range []string{apiPathPrefix, apiPathPrefix + "/",
		apiPathPrefix + "/default/banks", apiPathPrefix + "/default/banks/main"} {
		require.Truef(t, prefix.MatchString(path), "%s must reach the memory API", path)
	}
	for _, path := range []string{"/v", "/v1x", "/v10", "/api/v1", "/", "/login"} {
		require.Falsef(t, prefix.MatchString(path), "%s belongs to the Control Plane", path)
	}

	conf := renderNginxConfig(defaultPort, "bearer", "access-key")
	require.Contains(t, conf, "location ~ ^"+apiPathPrefix+"(/|$) {")
	require.Contains(t, conf, "location = "+apiVersionPath+" {")
	require.NotContains(t, conf, "location = "+apiVersionPath+"/")
}

// TestAPIRoutesCoverThePathsAMemoryClientAsks pins the gate to the client it
// publishes for. A memory client composes its own paths from the origin it is
// given, so a gate that routed only the versioned root would leave a configured
// client unable to introduce itself to the server: the version probe is the first
// request it makes, and the only one outside the prefix.
//
// The two paths are the ones hindsight-client-python 0.10.3 requests, read out of
// the gate's access log against the running app. A client bump that moved either
// one would make this test the place the change is noticed.
func TestAPIRoutesCoverThePathsAMemoryClientAsks(t *testing.T) {
	require.Equal(t, "/v1", apiPathPrefix, "the versioned root every API route sits under")
	require.Equal(t, "/version", apiVersionPath, "the unversioned probe the client asks first")
	require.True(t, strings.HasPrefix(apiProbePath, apiPathPrefix),
		"the readiness probe must read a route the gate actually routes")

	conf := renderNginxConfig(defaultPort, "bearer", "access-key")
	require.Contains(t, conf, apiVersionPath)
	require.Contains(t, conf, apiPathPrefix)
}

// TestGateBearerIsTheSecretTheContractPublishes keeps the credential the gate
// enforces and the credential the metadata offers from drifting apart. A gate
// enforcing a name the offer does not publish would lock the API forever with no
// consumer able to present it.
func TestGateBearerIsTheSecretTheContractPublishes(t *testing.T) {
	meta := loadMetadata(t)
	require.Equal(t, []string{httpTokenKey}, meta.Provides.Memory.Secrets,
		"the gate enforces the one secret the memory contract carries")
	require.Contains(t, renderNginxConfig(defaultPort, "bearer", "access-key"), apiPathPrefix)
}

// TestRenderedConfigRoutesEachSurfaceToItsOwnPort is the split the gate exists to
// make. Both upstreams are the same container name; only the port separates the
// memory API from the Control Plane.
func TestRenderedConfigRoutesEachSurfaceToItsOwnPort(t *testing.T) {
	conf := renderNginxConfig(defaultPort, "bearer", "access-key")

	apiBlock := locationBlock(t, conf, "location ~ ^"+apiPathPrefix+"(/|$) {")
	uiBlock := locationBlock(t, conf, "location / {")
	require.Contains(t, apiBlock, "proxy_pass http://$hindsight_api:8888")
	require.Contains(t, uiBlock, "proxy_pass http://$hindsight_ui:9999")

	// The version path is a second location onto the same upstream, and it exists
	// because a memory client asks it outside the versioned root.
	versionBlock := locationBlock(t, conf, "location = "+apiVersionPath+" {")
	require.Contains(t, versionBlock, "proxy_pass http://$hindsight_api:8888")

	// Both upstreams are the server container, and neither is a literal address:
	// a pinned IP goes stale the moment the server is recreated.
	require.Contains(t, conf, "set $hindsight_api "+nodeName)
	require.Contains(t, conf, "set $hindsight_ui "+nodeName)
	require.NotContains(t, conf, "proxy_pass http://10.")

	// The API serves attachment bodies as whole files, and the Control Plane
	// reports ingestion progress as it happens. A buffering proxy turns the first
	// into a held response and the second into a page that fills in at the end.
	require.Equal(t, 2, strings.Count(conf, "proxy_buffering off"))

	// The health route is answered here, so a server mid-restart is not a dead gate.
	require.Contains(t, conf, "location = /healthz { return 200 \"ok\"; }")
	require.NotContains(t, apiBlock, "healthz")
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
		"a missing access key must not take the memory API down with it")

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
	require.Equal(t, 3, strings.Count(conf, nginxResolverPort),
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
	require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{}))
}

// TestGatePostStartWithNoBearerIsNotAnError covers the locked shape. The gate is
// up and correct; there is simply no credential to present yet, and holding the
// node below RUNNING for a condition the operator has not had the chance to act on
// would turn a first boot into an ERROR node.
func TestGatePostStartWithNoBearerIsNotAnError(t *testing.T) {
	c := NewGateConfigurator(0, configurator.Deps{HTTP: fakeHTTP{}})
	require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{}))
}

// TestGatePortFollowsTheCatalogPort checks the configurator does not hard-code
// the listen port into the config it renders.
func TestGatePortFollowsTheCatalogPort(t *testing.T) {
	c := NewGateConfigurator(7777, configurator.Deps{})
	require.Equal(t, 7777, c.Port())
	require.Contains(t, renderNginxConfig(c.Port(), "b", "a"), "listen 7777;")
}

// locationBlock returns one location block out of a rendered config, addressed by
// its opening line. The config is small and machine-generated, so a brace count is
// enough and beats parsing nginx.
func locationBlock(t *testing.T, conf, opener string) string {
	t.Helper()
	start := strings.Index(conf, opener)
	require.Greaterf(t, start, -1, "no location opening with %s", opener)
	depth := 0
	for i := start; i < len(conf); i++ {
		switch conf[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return conf[start : i+1]
			}
		}
	}
	t.Fatalf("unbalanced braces after location %s", opener)
	return ""
}
