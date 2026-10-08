// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
