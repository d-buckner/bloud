// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// remoteProviderApp is the catalog entry these tests point Bloud at: a
// non-system app that provides `appApi` exactly as AFFiNE does.
func remoteProviderApp() *catalog.App {
	return &catalog.App{
		CatalogID:   "affine",
		DisplayName: "AFFiNE",
		Description: "knowledge base",
		Category:    "productivity",
		Port:        3010,
		Provides: catalog.Provides{
			"appApi": catalog.ContractProvides{
				Secrets:       []string{"password"},
				RuntimeValues: []string{"username", "workspaceId"},
			},
		},
	}
}

func providerTestModule() (*externalAppsModule, *recordingOrchestrator, *fakeSecrets) {
	secrets := newFakeSecrets()
	cache := NewFakeCatalogCache()
	cache.AddApp(remoteProviderApp())
	cache.AddApp(&catalog.App{CatalogID: "traefik", DisplayName: "Traefik", IsSystem: true})
	cache.AddApp(&catalog.App{CatalogID: "gitea", DisplayName: "Gitea"})
	orch := &recordingOrchestrator{}
	mod := &externalAppsModule{
		appStore: NewFakeAppStore(),
		catalog:  cache,
		secrets:  secrets,
		orch:     orch,
		logger:   newTestSlogger(),
	}
	return mod, orch, secrets
}

func postExternalApp(mod *externalAppsModule, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/external-apps", strings.NewReader(body))
	w := httptest.NewRecorder()
	mod.AddHandler()(w, req)
	return w
}

const validProviderBody = `{
	"kind": "provider",
	"source": "app:affine",
	"name": "NAS AFFiNE",
	"url": "https://affine.example.com",
	"values": {"appApi": {"username": "op@example.com", "workspaceId": "ws-1"}},
	"secrets": {"appApi": "the-real-password"}
}`

func TestExternalAppsModule_AddProviderSubmitsFullSpec(t *testing.T) {
	mod, orch, _ := providerTestModule()

	w := postExternalApp(mod, validProviderBody)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, orch.intents, 1)

	add, ok := orch.intents[0].(orchestrator.AddExternalAppIntent)
	require.True(t, ok)
	assert.Equal(t, string(store.ExternalAppKindProvider), add.Spec.Kind)
	assert.Equal(t, "app:affine", add.Spec.Source)
	assert.Equal(t, "NAS AFFiNE", add.Spec.Name)
	assert.Equal(t, "https://affine.example.com", add.Spec.URL)
	assert.Equal(t, map[string]map[string]string{"appApi": {"username": "op@example.com", "workspaceId": "ws-1"}}, add.Spec.Values)
	assert.Equal(t, map[string]string{"appApi": "the-real-password"}, add.Spec.Secrets)
}

func TestExternalAppsModule_AddProviderRejectsUnknownApp(t *testing.T) {
	mod, orch, _ := providerTestModule()

	w := postExternalApp(mod, strings.Replace(validProviderBody, "app:affine", "app:nope", 1))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, orch.intents, "a rejected request must not submit an intent")
}

func TestExternalAppsModule_AddProviderRejectsSystemApp(t *testing.T) {
	mod, orch, _ := providerTestModule()

	w := postExternalApp(mod, strings.Replace(validProviderBody, "app:affine", "app:traefik", 1))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, orch.intents)
}

func TestExternalAppsModule_AddProviderRejectsAppThatProvidesNothing(t *testing.T) {
	mod, orch, _ := providerTestModule()

	w := postExternalApp(mod, strings.Replace(validProviderBody, "app:affine", "app:gitea", 1))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, orch.intents)
}

func TestExternalAppsModule_AddProviderRejectsMalformedSource(t *testing.T) {
	mod, orch, _ := providerTestModule()

	for _, source := range []string{"", "affine", "app:", "contract:modelSource", "bogus:affine"} {
		w := postExternalApp(mod, strings.Replace(validProviderBody, `"source": "app:affine"`, `"source": "`+source+`"`, 1))
		assert.Equal(t, http.StatusBadRequest, w.Code, "source %q must be rejected", source)
	}
	assert.Empty(t, orch.intents)
}

// TestExternalAppsModule_AddProviderRejectsLocalInstall is the v1 exclusivity
// rule: a catalog ID is either run here or pointed at externally, never both,
// so a consumer never faces two providers of the same name it cannot tell apart.
func TestExternalAppsModule_AddProviderRejectsLocalInstall(t *testing.T) {
	mod, orch, _ := providerTestModule()
	appStore := mod.appStore.(*FakeAppStore)
	appStore.apps["affine"] = &store.InstalledApp{CatalogID: "affine"}

	w := postExternalApp(mod, validProviderBody)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, orch.intents)
}

// TestExternalAppsModule_AddProviderRejectsEndpointWithPath pins that the
// endpoint is an origin. A path here would be concatenated with contract paths
// downstream, which is how `https://host/old` + `/api/x` becomes an address
// that connects somewhere the operator never named.
func TestExternalAppsModule_AddProviderRejectsEndpointWithPath(t *testing.T) {
	mod, orch, _ := providerTestModule()

	for _, bad := range []string{
		"https://affine.example.com/api",
		"https://affine.example.com?debug=1",
		"https://affine.example.com#frag",
		"http://affine.example.com:3010/some/path",
	} {
		w := postExternalApp(mod, strings.Replace(validProviderBody, "https://affine.example.com", bad, 1))
		assert.Equal(t, http.StatusBadRequest, w.Code, "endpoint %q must be rejected", bad)
	}
	assert.Empty(t, orch.intents)
}

func TestExternalAppsModule_AddProviderRejectsUndeclaredValue(t *testing.T) {
	mod, orch, _ := providerTestModule()

	w := postExternalApp(mod, strings.Replace(validProviderBody,
		`"username": "op@example.com", "workspaceId": "ws-1"`,
		`"username": "op@example.com", "workspaceId": "ws-1", "bogus": "x"`, 1))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, orch.intents)
}

func TestExternalAppsModule_AddProviderRejectsMissingRequiredValue(t *testing.T) {
	mod, orch, _ := providerTestModule()

	w := postExternalApp(mod, strings.Replace(validProviderBody,
		`"username": "op@example.com", "workspaceId": "ws-1"`,
		`"workspaceId": "ws-1"`, 1))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, orch.intents)
}

func TestExternalAppsModule_AddProviderRejectsMissingCredential(t *testing.T) {
	mod, orch, _ := providerTestModule()

	w := postExternalApp(mod, strings.Replace(validProviderBody,
		`"secrets": {"appApi": "the-real-password"}`, `"secrets": {}`, 1))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, orch.intents)
}

// TestExternalAppsModule_ProvidersDerivesFormFromRegistry checks the operator
// form is generated from the contract registry rather than hand-written: the
// required/optional split and the secret-vs-value split all come from there.
func TestExternalAppsModule_ProvidersDerivesFormFromRegistry(t *testing.T) {
	mod, _, _ := providerTestModule()

	req := httptest.NewRequest(http.MethodGet, "/api/external-apps/providers", nil)
	w := httptest.NewRecorder()
	mod.ProvidersHandler()(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var out []externalProviderOption
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Len(t, out, 1, "only non-system apps that provide a contract are selectable")
	assert.Equal(t, "affine", out[0].App)

	require.Len(t, out[0].Contracts, 1)
	fields := out[0].Contracts[0].Fields
	byKey := map[string]externalProviderField{}
	for _, f := range fields {
		byKey[f.Key] = f
	}
	require.Contains(t, byKey, "username")
	assert.Equal(t, "value", byKey["username"].Kind)
	assert.True(t, byKey["username"].Required)
	require.Contains(t, byKey, "workspaceId")
	assert.False(t, byKey["workspaceId"].Required, "workspaceId is Optional in the registry")
	require.Contains(t, byKey, "password")
	assert.Equal(t, "secret", byKey["password"].Kind)
}

// TestExternalAppsModule_ResponseNeverCarriesSecretValues pins the one thing a
// list endpoint must never do: read a credential back onto the wire.
func TestExternalAppsModule_ResponseNeverCarriesSecretValues(t *testing.T) {
	mod, _, _ := providerTestModule()
	require.NoError(t, mod.secrets.SetAppSecret(store.ExternalSecretScope("ext-1"), "appApi", "the-real-password"))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/external-apps", nil)
	mod.ListHandler()(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "the-real-password")
}

// A `source: contract:<name>` record is a bare off-host provider: it fills one
// named role with no catalog app behind it, and the contract registry is the
// whole schema for what the form has to ask for.
func TestExternalAppsModule_AddContractProviderSubmitsContractSource(t *testing.T) {
	mod, orch, _ := providerTestModule()

	w := postExternalApp(mod, `{
		"kind": "provider",
		"source": "contract:pvr",
		"name": "Off-host Radarr",
		"url": "https://radarr.example.com",
		"secrets": {"pvr": "off-host-key"}
	}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, orch.intents, 1)

	add, ok := orch.intents[0].(orchestrator.AddExternalAppIntent)
	require.True(t, ok)
	assert.Equal(t, "contract:pvr", add.Spec.Source)
	assert.Equal(t, "https://radarr.example.com", add.Spec.URL)
	assert.Equal(t, map[string]string{"pvr": "off-host-key"}, add.Spec.Secrets)
}

// The three contracts that carry the instance's own plumbing cannot be pointed
// somewhere else. Saying so at the boundary is what stops a form from accepting
// a record that would resolve into a consumer dialing the wrong thing.
func TestExternalAppsModule_AddContractProviderRejectsSystemContracts(t *testing.T) {
	for _, contractName := range []string{"proxy", "database", "sso"} {
		t.Run(contractName, func(t *testing.T) {
			mod, orch, _ := providerTestModule()

			w := postExternalApp(mod, `{
				"kind": "provider",
				"source": "contract:`+contractName+`",
				"name": "Off-host thing",
				"url": "https://example.com",
				"secrets": {"`+contractName+`": "k"}
			}`)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "cannot be filled externally")
			assert.Empty(t, orch.intents, "a rejected record never reaches the queue")
		})
	}
}

func TestExternalAppsModule_AddContractProviderRejectsUnknownContract(t *testing.T) {
	mod, orch, _ := providerTestModule()

	w := postExternalApp(mod, `{
		"kind": "provider",
		"source": "contract:nope",
		"name": "Off-host thing",
		"url": "https://example.com"
	}`)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "unknown contract")
	assert.Empty(t, orch.intents)
}

func TestExternalAppsModule_AddContractProviderRequiresCredential(t *testing.T) {
	mod, orch, _ := providerTestModule()

	w := postExternalApp(mod, `{
		"kind": "provider",
		"source": "contract:pvr",
		"name": "Off-host Radarr",
		"url": "https://radarr.example.com"
	}`)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "requires a credential")
	assert.Empty(t, orch.intents)
}

// A contract that declares no values rejects one the operator invented, so a
// typo cannot be stored and then silently never reach a consumer.
func TestExternalAppsModule_AddContractProviderRejectsUndeclaredValue(t *testing.T) {
	mod, orch, _ := providerTestModule()

	w := postExternalApp(mod, `{
		"kind": "provider",
		"source": "contract:pvr",
		"name": "Off-host Radarr",
		"url": "https://radarr.example.com",
		"secrets": {"pvr": "off-host-key"},
		"values": {"pvr": {"baseUrl": "https://wrong.example.com"}}
	}`)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "does not declare a value")
	assert.Empty(t, orch.intents)
}

// A value the provider's catalog entry declares statically is a fact about the
// app, not about this instance, so a remote copy inherits it and the operator
// is not asked to retype it. One endpoint and one key is the whole form for a
// remote Radarr.
func TestValidateProviderValues_StaticCatalogValuesAreInherited(t *testing.T) {
	radarr := &catalog.App{
		CatalogID: "radarr",
		Provides: catalog.Provides{
			"pvr": {Secrets: []string{"apiKey"}},
			"icsFeed": {Secrets: []string{"apiKey"}, Values: map[string]string{
				"path":         "/feed/v3/calendar/Radarr.ics",
				"calendarName": "Movies",
			}},
		},
	}

	values, err := validateProviderValues(radarr, map[string]map[string]string{})
	require.NoError(t, err, "nothing supplied must still validate: the catalog declares every value")
	assert.Equal(t, "/feed/v3/calendar/Radarr.ics", values["icsFeed"]["path"])
	assert.Equal(t, "Movies", values["icsFeed"]["calendarName"])

	// The operator's own value overrides the static one.
	values, err = validateProviderValues(radarr, map[string]map[string]string{
		"icsFeed": {"calendarName": "Films"},
	})
	require.NoError(t, err)
	assert.Equal(t, "Films", values["icsFeed"]["calendarName"])
	assert.Equal(t, "/feed/v3/calendar/Radarr.ics", values["icsFeed"]["path"])
}

// A required value with no static default is still demanded.
func TestValidateProviderValues_DemandsWhatTheCatalogCannotSupply(t *testing.T) {
	affine := &catalog.App{
		CatalogID: "affine",
		Provides: catalog.Provides{
			"appApi": {Secrets: []string{"password"}, Values: map[string]string{}},
		},
	}
	_, err := validateProviderValues(affine, map[string]map[string]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "username")
}

// One credential fills every contract of one app that publishes the same secret
// name. Radarr's `pvr` and `icsFeed` offers are the same key, and making the
// operator paste it twice only lets the two copies disagree.
func TestValidateProviderSecrets_OneKeyFillsEveryContractNamingIt(t *testing.T) {
	radarr := &catalog.App{
		CatalogID: "radarr",
		Provides: catalog.Provides{
			"pvr":     {Secrets: []string{"apiKey"}},
			"icsFeed": {Secrets: []string{"apiKey"}},
		},
	}
	secrets, err := validateProviderSecrets(radarr, map[string]string{"pvr": "one-key"}, false)
	require.NoError(t, err)
	assert.Equal(t, "one-key", secrets["pvr"])
	assert.Equal(t, "one-key", secrets["icsFeed"])
}

// The sharing matches on the declared secret name, so a credential cannot leak
// between contracts that carry different ones.
func TestValidateProviderSecrets_DoesNotShareAcrossDifferentSecretNames(t *testing.T) {
	app := &catalog.App{
		CatalogID: "mixed",
		Provides: catalog.Provides{
			"mediaServer": {Secrets: []string{"adminPassword"}},
			"pvr":         {Secrets: []string{"apiKey"}},
		},
	}
	_, err := validateProviderSecrets(app, map[string]string{"mediaServer": "a-password"}, false)
	require.Error(t, err, "an apiKey slot must not be filled from an adminPassword")
	assert.Contains(t, err.Error(), "pvr")
}

// realProviderCatalogDir points at the checked-in catalog. The test below reads
// it rather than a fixture because the property it pins lives in the app
// metadata, not in this package.
func realProviderCatalogDir(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"../../../../apps", "../../apps", "apps"} {
		if _, err := os.Stat(filepath.Join(p, "sonarr", "metadata.yaml")); err == nil {
			return p
		}
	}
	t.Skip("real catalog not reachable from the test working directory")
	return ""
}

// TestRealCatalogServarrNeedsOnlyEndpointAndKey pins what an operator is
// actually asked for when they point Bloud at someone else's Sonarr or Radarr:
// the endpoint and one API key.
//
// It reads the real catalog on purpose. The property is a fact about the two
// apps' `provides:` blocks, so only the metadata can say when it stops being
// true: a required non-static value added to either icsFeed offer, or a second
// credential name on either, turns the two-field form back into one that
// recites the contract registry at a person who has nothing to add.
func TestRealCatalogServarrNeedsOnlyEndpointAndKey(t *testing.T) {
	apps, err := catalog.NewLoader(realProviderCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	for _, name := range []string{"sonarr", "radarr"} {
		t.Run(name, func(t *testing.T) {
			app, ok := apps[name]
			require.True(t, ok, "%s must be in the catalog", name)

			var required []externalProviderField
			for _, contract := range providerContractFields(app) {
				for _, field := range contract.Fields {
					if field.Required {
						required = append(required, field)
					}
				}
			}

			// Two fields arrive, one input renders: the same apiKey on both the
			// pvr and the icsFeed offer is one credential, and the form folds
			// them together the way the resolver shares it.
			require.Len(t, required, 2)
			distinct := map[string]bool{}
			for _, field := range required {
				assert.Equal(t, "secret", field.Kind,
					"a required value for %s would put a fact on the form the operator does not own", name)
				assert.Equal(t, "apiKey", field.Key)
				distinct[field.Kind+":"+field.Key] = true
			}
			assert.Len(t, distinct, 1, "one credential input, not one per contract")
		})
	}
}
