// SPDX-License-Identifier: AGPL-3.0-only

package apps

import (
	"os"
	"path/filepath"
	"testing"

	"codeberg.org/d-buckner/bloud/apps/configtest"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/require"
)

// appSpec is one row of the conformance table: what the harness needs to
// exercise one user app.
type appSpec struct {
	// Dir is the app's directory under apps/.
	Dir string
	// Node is the graph node its configurator registers under.
	Node string
	// DefaultPort is the constructor's fallback port, checked against the
	// port the catalog publishes.
	DefaultPort int
	// WithSSO says whether to hand the configurator SSO enabled with an OIDC
	// output. Apps whose PreStart requires OIDC need this to reach the code
	// path that writes their config.
	WithSSO bool
	// WithLDAP says to hand the configurator a populated LDAP provider output.
	// Apps whose config is LDAP-backed (Radicale) need it to reach the path
	// that renders their real config rather than the locked-down no-provider
	// fallback the same code produces.
	WithLDAP bool
	// WithAgent says to hand the configurator a wired agentApi provider. Apps
	// that are front ends for an agent have no useful config to write without
	// one, so the harness has to supply the binding their PreStart requires.
	WithAgent bool
	// Preseed runs against a fresh data dir before each pass.
	Preseed func(dataDir string) error
}

// conformanceTable covers every user app the catalog registers. Adding an app
// to NodeNames() without adding it here fails TestConformanceTableIsComplete,
// so coverage cannot silently shrink.
var conformanceTable = []appSpec{
	{Dir: "affine", Node: "apps-affine", DefaultPort: 3010, WithSSO: true},
	{Dir: "affine-mcp", Node: "apps-affine-mcp", DefaultPort: 9222},
	{Dir: "arr-mcp", Node: "apps-arr-mcp", DefaultPort: 6060},
	{Dir: "calino", Node: "apps-calino", DefaultPort: 8180},
	{Dir: "dav-mcp", Node: "apps-dav-mcp", DefaultPort: 9333},
	{Dir: "hermes", Node: "apps-hermes", DefaultPort: 9119, WithSSO: true},
	{Dir: "hermes-webui", Node: "apps-hermes-webui", DefaultPort: 8787, WithSSO: true, WithAgent: true},
	{Dir: "homeassistant", Node: "apps-homeassistant", DefaultPort: 8123, WithSSO: true, Preseed: preseedHAComponent},
	{Dir: "immich", Node: "apps-immich-server", DefaultPort: 2283, WithSSO: true},
	{Dir: "jellyfin", Node: "apps-jellyfin", DefaultPort: 8096},
	{Dir: "navidrome", Node: "apps-navidrome", DefaultPort: 4533},
	{Dir: "paperless-ngx", Node: "apps-paperless-ngx", DefaultPort: 8000, WithSSO: true},
	{Dir: "prowlarr", Node: "apps-prowlarr", DefaultPort: 9696},
	{Dir: "qbittorrent", Node: "apps-qbittorrent", DefaultPort: 8081},
	{Dir: "radarr", Node: "apps-radarr", DefaultPort: 7878},
	{Dir: "radicale", Node: "apps-radicale", DefaultPort: 5232, WithSSO: true, WithLDAP: true},
	// The feed-sync sidecar shares the radicale directory and the app's port
	// belongs to the server, so it declares no default port of its own.
	{Dir: "radicale", Node: "apps-radicale-pimsync"},
	{Dir: "seerr", Node: "apps-seerr", DefaultPort: 5055},
	{Dir: "sonarr", Node: "apps-sonarr", DefaultPort: 8989},
	{Dir: "vaultwarden", Node: "apps-vaultwarden", DefaultPort: 8222, WithSSO: true},
}

// TestConformanceTableIsComplete pins the table against the registry, so a new
// app that registers a configurator but is not added here fails rather than
// going untested.
func TestConformanceTableIsComplete(t *testing.T) {
	RegisterAll()

	covered := map[string]bool{}
	for _, spec := range conformanceTable {
		require.False(t, covered[spec.Node], "duplicate node %q in the conformance table", spec.Node)
		covered[spec.Node] = true
	}

	for _, node := range NodeNames() {
		if !covered[node] {
			t.Errorf("node %q is registered but not in the conformance table", node)
		}
	}
	for node := range covered {
		if !contains(NodeNames(), node) {
			t.Errorf("conformance table lists %q which is not a registered node", node)
		}
	}
}

// TestConformance runs the shared harness over every user app.
func TestConformance(t *testing.T) {
	RegisterAll()

	for _, spec := range conformanceTable {
		spec := spec
		t.Run(spec.Node, func(t *testing.T) {
			md := configtest.LoadMetadata(t, spec.Dir)

			// The harness builds the configurator from the global factory
			// registry, so Cfg is resolved inside it; the table only has to
			// say which node to build.
			tc := configtest.Case{
				Node:        spec.Node,
				Metadata:    md,
				DefaultPort: spec.DefaultPort,
				State:       stateForSpec(spec),
				Preseed:     spec.Preseed,
			}
			configtest.Run(t, tc)
		})
	}
}

// TestCredentialExchangesNameAContractTheAppProvides is the same shape as every
// other exemption table in this repo: a registration is data, it is reviewed, and
// a registration that guards nothing is itself a failure.
//
// An exchange replaces the fields a remote-app form asks for. If it names a
// contract the app stopped providing, the form offers a sign-in for a credential
// nothing reads, and the contract's real secret fields are suppressed for nothing.
// The apps module cannot import the catalog loader, so this checks the half it can
// see: that the contract is in the app's own `provides:`, and that the inputs are
// the two a sign-in is made of.
func TestCredentialExchangesNameAContractTheAppProvides(t *testing.T) {
	RegisterAll()

	names := configurator.CredentialExchangeNames()
	require.NotEmpty(t, names, "at least one app trades a sign-in for its credential")

	for _, name := range names {
		exchange, ok := configurator.LookupCredentialExchange(name)
		require.True(t, ok, "registry listed %q but LookupCredentialExchange refused it", name)

		md := configtest.LoadMetadata(t, name)
		_, offered := md.Provides[exchange.Contract()]
		require.True(t, offered,
			"%s registers a credential exchange for %q, which its metadata does not provide",
			name, exchange.Contract())

		keys := map[string]bool{}
		for _, input := range exchange.Inputs() {
			require.NotEmpty(t, input.Label, "%s: input %q needs a label a person can read", name, input.Key)
			keys[input.Key] = true
		}
		require.True(t, keys[configurator.ExchangeInputUsername],
			"%s: a sign-in exchange must take a username", name)
		require.True(t, keys[configurator.ExchangeInputPassword],
			"%s: a sign-in exchange must take a password", name)

		require.NotEmpty(t, exchange.LoginContract(),
			"%s: name the contract whose published login Bloud may borrow, or none can be offered", name)
	}
}

// stateFor builds the AppState the harness passes to PreStart. With SSO off
// the configurator sees no provider, which is the shape it handles when
// Authentik is not installed.
// stateForSpec builds the AppState the harness passes to PreStart. With SSO off
// the configurator sees no provider, which is the shape it handles when
// Authentik is not installed.
func stateForSpec(spec appSpec) func(dataDir, bloudDataDir string) *configurator.AppState {
	return func(dataDir, bloudDataDir string) *configurator.AppState {
		st := &configurator.AppState{
			DataPath:      dataDir,
			BloudDataPath: bloudDataDir,
		}
		if spec.WithSSO {
			st.SSOEnabled = true
			st.OIDC = &configurator.OIDCOutput{
				ClientID:     "conformance-client",
				ClientSecret: "conformance-secret",
				IssuerURL:    "http://sso.localhost:8080/application/o/app/",
				RedirectURI:  "http://app.localhost:8080/mCallback",
			}
		}
		if spec.WithLDAP {
			st.SSOEnabled = true
			st.LDAP = &configurator.LDAPOutput{
				Host:         "apps-authentik-ldap",
				Port:         3389,
				BaseDN:       "dc=ldap,dc=goauthentik,dc=io",
				BindUser:     "cn=ldap-service,ou=users,dc=ldap,dc=goauthentik,dc=io",
				BindPassword: "conformance-ldap-reader-secret",
			}
		}
		if spec.WithAgent {
			st.Integrations.AgentAPIs = []configurator.AgentAPIBinding{
				{
					Endpoint:  "http://hermes.localhost:8080/v1",
					APIKey:    "conformance-agent-key",
					ModelName: "hermes",
				},
			}
		}
		return st
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// preseedHAComponent drops the hass-oidc-auth manifest where the configurator
// looks for it, so its pinned-asset install short-circuits instead of
// reaching the network. The manifest carries the same version the configurator
// compares against; that comparison is itself under test here, and getting it
// wrong is what made Home Assistant re-download and re-request a recreate on
// every pass.
func preseedHAComponent(dataDir string) error {
	dir := filepath.Join(dataDir, "config", "custom_components", "auth_oidc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	manifest := `{"domain": "auth_oidc", "name": "OpenID Connect/SSO Authentication", "version": "1.2.1"}`
	return os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644)
}
