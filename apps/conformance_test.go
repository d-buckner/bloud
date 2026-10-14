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
	// OwnsTeardown says the configurator implements configurator.Remover
	// because it leaves something behind outside its own container and data
	// (Home Assistant's minted long-lived token, for example). The harness
	// fails a Remover that is not declared, so this is the declaration.
	OwnsTeardown bool
	// WithAppAPI hands the configurator a wired appApi provider: an installed
	// target publishing an account and a credential. A wrapper's config is that
	// credential plus the address it belongs to, so without a binding the
	// harness would only ever exercise the "not ready" shape.
	WithAppAPI bool
	// Preseed runs against a fresh data dir before each pass.
	Preseed func(dataDir string) error
}

// conformanceTable covers every user app the catalog registers. Adding an app
// to NodeNames() without adding it here fails TestConformanceTableIsComplete,
// so coverage cannot silently shrink.
var conformanceTable = []appSpec{
	{Dir: "affine", Node: "apps-affine", WithSSO: true},
	{Dir: "affine-mcp", Node: "apps-affine-mcp"},
	{Dir: "arr-mcp", Node: "apps-arr-mcp"},
	{Dir: "calino", Node: "apps-calino"},
	{Dir: "dav-mcp", Node: "apps-dav-mcp"},
	{Dir: "hermes", Node: "apps-hermes", WithSSO: true},
	{Dir: "hermes-webui", Node: "apps-hermes-webui", WithSSO: true, WithAgent: true},
	{Dir: "homeassistant", Node: "apps-homeassistant", WithSSO: true, OwnsTeardown: true, Preseed: preseedHAComponent},
	{Dir: "ha-mcp", Node: "apps-ha-mcp", WithAppAPI: true},
	{Dir: "ha-mcp", Node: "apps-ha-mcp-gate", WithAppAPI: true},
	{Dir: "immich", Node: "apps-immich-server", WithSSO: true},
	{Dir: "jellyfin", Node: "apps-jellyfin"},
	{Dir: "navidrome", Node: "apps-navidrome"},
	{Dir: "paperless-ngx", Node: "apps-paperless-ngx", WithSSO: true},
	// The env file is written by the backend node and read by the frontend node,
	// so both are held to the same idempotency rule. The frontend publishes the
	// app port, so only its configurator reports one.
	{Dir: "penpot", Node: "apps-penpot-backend", WithSSO: true},
	{Dir: "penpot", Node: "apps-penpot-frontend", WithSSO: true},
	{Dir: "podly", Node: "apps-podly"},
	{Dir: "prowlarr", Node: "apps-prowlarr"},
	{Dir: "qbittorrent", Node: "apps-qbittorrent"},
	{Dir: "radarr", Node: "apps-radarr"},
	{Dir: "radicale", Node: "apps-radicale", WithSSO: true, WithLDAP: true},
	// The feed-sync sidecar shares the radicale directory, and the server
	// publishes the app port, so only the server reports one.
	{Dir: "radicale", Node: "apps-radicale-pimsync"},
	{Dir: "seerr", Node: "apps-seerr"},
	{Dir: "sonarr", Node: "apps-sonarr"},
	{Dir: "vaultwarden", Node: "apps-vaultwarden", WithSSO: true},
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
				Node:         spec.Node,
				Metadata:     md,
				State:        stateForSpec(spec),
				Preseed:      spec.Preseed,
				AllowRemover: spec.OwnsTeardown,
			}
			configtest.Run(t, tc)
		})
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
		if spec.WithAppAPI {
			st.Integrations.AppAPIs = []configurator.AppAPIBinding{
				{
					ProviderRef: configurator.ProviderRef{
						App:       "homeassistant",
						Installed: true,
						BaseURL:   "http://apps-homeassistant:8123",
					},
					Username: "conformance-owner",
					Password: "conformance-long-lived-token",
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
