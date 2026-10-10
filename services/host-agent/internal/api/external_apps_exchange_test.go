// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-chi/chi/v5"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// The remote-app form is generated from the contract registry, which is the
// right schema only while the credential a contract declares is something the
// operator can read off the remote app's own screen. These tests exercise the
// generic plumbing that assumption needs: an app declares a sign-in exchange, the
// form asks for the sign-in instead of the key, and what gets stored is what the
// remote app handed back. The Seerr-specific trade is tested in apps/seerr;
// nothing here knows what a Jellyfin is.

// stubExchange stands in for an app's exchange. It records the request it was
// handed so a test can assert what crossed the boundary, and returns whatever the
// remote app is modeled as reporting.
type stubExchange struct {
	contract      string
	inputs        []configurator.ExchangeInput
	loginContract string
	result        configurator.ExchangeResult
	err           error
	got           configurator.ExchangeRequest
	calls         int
}

func (s *stubExchange) Contract() string { return s.contract }
func (s *stubExchange) Inputs() []configurator.ExchangeInput {
	if s.inputs != nil {
		return s.inputs
	}
	return []configurator.ExchangeInput{
		{Key: configurator.ExchangeInputUsername, Label: "Username"},
		{Key: configurator.ExchangeInputPassword, Label: "Password", Secret: true},
	}
}
func (s *stubExchange) LoginContract() string {
	if s.loginContract != "" {
		return s.loginContract
	}
	return "mediaServer"
}
func (s *stubExchange) Exchange(_ context.Context, req configurator.ExchangeRequest) (configurator.ExchangeResult, error) {
	s.calls++
	s.got = req
	if s.err != nil {
		return configurator.ExchangeResult{}, s.err
	}
	return s.result, nil
}

// seerrLikeApp is a provider whose only credential is a login: one contract, one
// declared secret, and a static defaultUser that describes the install Bloud
// booted rather than a remote copy.
func seerrLikeApp() *catalog.App {
	return &catalog.App{
		CatalogID:   "seerr",
		DisplayName: "Seerr",
		Description: "request manager",
		Category:    "media",
		Port:        5055,
		Provides: catalog.Provides{
			"requestManager": catalog.ContractProvides{
				Secrets: []string{"apiKey"},
				Values:  map[string]string{"defaultUser": "bloud-admin@localhost"},
			},
		},
	}
}

// jellyfinLikeApp is the installed provider whose published login a sign-in
// exchange may borrow.
func jellyfinLikeApp() *catalog.App {
	return &catalog.App{
		CatalogID:   "jellyfin",
		DisplayName: "Jellyfin",
		Description: "media server",
		Category:    "media",
		Port:        8096,
		Provides: catalog.Provides{
			"mediaServer": catalog.ContractProvides{
				Secrets: []string{"adminPassword"},
				Values:  map[string]string{"adminUsername": "bloud-bootstrap-admin"},
			},
		},
	}
}

// exchangeTestModule wires a module with the exchange registered, and installs
// the media server when asked so Bloud holds a login to offer.
func exchangeTestModule(t *testing.T, exchange configurator.CredentialExchange, withMediaServer bool) (
	*externalAppsModule, *recordingOrchestrator, *fakeSecrets,
) {
	t.Helper()
	configurator.RegisterCredentialExchange("seerr", exchange)
	t.Cleanup(func() { configurator.RegisterCredentialExchange("seerr", registeredNothing{}) })

	cache := NewFakeCatalogCache()
	cache.AddApp(seerrLikeApp())
	cache.AddApp(jellyfinLikeApp())
	appStore := NewFakeAppStore()
	secrets := newFakeSecrets()
	if withMediaServer {
		appStore.apps["jellyfin"] = &store.InstalledApp{CatalogID: "jellyfin"}
		require.NoError(t, secrets.SetAppSecret("jellyfin", "adminPassword", "bootstrap-pw"))
	}
	orch := &recordingOrchestrator{}
	return &externalAppsModule{
		appStore: appStore,
		catalog:  cache,
		secrets:  secrets,
		orch:     orch,
		logger:   newTestSlogger(),
	}, orch, secrets
}

// registeredNothing unregisters by replacement: the registry has no delete, and
// an app that provides nothing can never be selected as a provider, so a
// no-op exchange left behind is inert for every other test in the package.
type registeredNothing struct{}

func (registeredNothing) Contract() string                     { return "" }
func (registeredNothing) Inputs() []configurator.ExchangeInput { return nil }
func (registeredNothing) LoginContract() string                { return "" }
func (registeredNothing) Exchange(context.Context, configurator.ExchangeRequest) (configurator.ExchangeResult, error) {
	return configurator.ExchangeResult{}, nil
}

const exchangeBody = `{
	"kind": "provider",
	"source": "app:seerr",
	"name": "NAS Seerr",
	"url": "https://seerr.example.com",
	"exchange": {"username": "daniel", "password": "the-jellyfin-password"}
}`

func providerForm(t *testing.T, mod *externalAppsModule) []externalProviderOption {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/external-apps/providers", nil)
	w := httptest.NewRecorder()
	mod.ProvidersHandler()(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var out []externalProviderOption
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

func optionFor(t *testing.T, options []externalProviderOption, app string) externalProviderOption {
	t.Helper()
	for _, option := range options {
		if option.App == app {
			return option
		}
	}
	t.Fatalf("no provider option for %q", app)
	return externalProviderOption{}
}

// TestProviders_SwapTheKeyFieldForASignIn is the whole point of the mechanism:
// an app that can trade a login for its credential must not be shown a box for
// the key, because the operator does not have it and the login does the job.
func TestProviders_SwapTheKeyFieldForASignIn(t *testing.T) {
	mod, _, _ := exchangeTestModule(t, &stubExchange{contract: "requestManager"}, false)

	option := optionFor(t, providerForm(t, mod), "seerr")
	require.NotNil(t, option.Exchange)
	assert.Equal(t, "requestManager", option.Exchange.Contract)
	require.Len(t, option.Exchange.Inputs, 2)
	assert.Equal(t, "secret", option.Exchange.Inputs[1].Kind, "the password must render as a password box")
	assert.True(t, option.Exchange.Inputs[0].Required, "with no login on file the inputs are the only way in")
	assert.Empty(t, option.Exchange.StoredLogin)

	for _, contract := range option.Contracts {
		for _, field := range contract.Fields {
			assert.NotEqual(t, "apiKey", field.Key,
				"the exchanged contract's own secret must not be asked for alongside the sign-in")
		}
	}
}

func TestProviders_OfferTheLoginBloudAlreadyHolds(t *testing.T) {
	mod, _, _ := exchangeTestModule(t, &stubExchange{contract: "requestManager"}, true)

	option := optionFor(t, providerForm(t, mod), "seerr")
	require.NotNil(t, option.Exchange)
	assert.Contains(t, option.Exchange.StoredLogin, "Jellyfin")
	assert.Contains(t, option.Exchange.StoredLogin, "bloud-bootstrap-admin",
		"the label has to name the account, or the checkbox is a guess")
	for _, input := range option.Exchange.Inputs {
		assert.False(t, input.Required, "a stored login makes the typed inputs an alternative, not a requirement")
	}
}

// TestProviders_LeaveTheKeyFieldWhenNothingCanTradeForIt pins the fallback: an
// exchange that names a contract the app does not offer suppresses nothing, so a
// misregistration cannot produce a form that asks for nothing at all.
func TestProviders_LeaveTheKeyFieldWhenNothingCanTradeForIt(t *testing.T) {
	mod, _, _ := exchangeTestModule(t, &stubExchange{contract: "pvr"}, false)

	option := optionFor(t, providerForm(t, mod), "seerr")
	assert.Nil(t, option.Exchange, "an exchange for a contract this app does not offer is not offered")
	found := false
	for _, contract := range option.Contracts {
		for _, field := range contract.Fields {
			if field.Key == "apiKey" {
				found = true
			}
		}
	}
	assert.True(t, found, "with no way to trade for it, the key is the only way to fill the contract")
}

func TestAddProvider_StoresWhatTheRemoteHandedBack(t *testing.T) {
	stub := &stubExchange{contract: "requestManager", result: configurator.ExchangeResult{
		Secrets: map[string]string{"requestManager": "exchanged-key"},
		Values:  map[string]map[string]string{"requestManager": {"defaultUser": "daniel@example.com"}},
	}}
	mod, orch, _ := exchangeTestModule(t, stub, false)

	w := postExternalApp(mod, exchangeBody)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, orch.intents, 1)
	add := orch.intents[0].(orchestrator.AddExternalAppIntent)

	assert.Equal(t, map[string]string{"requestManager": "exchanged-key"}, add.Spec.Secrets)
	// The exchanged account wins over the catalog's static default, which is a
	// claim about the install Bloud booted, not about this one.
	assert.Equal(t, "daniel@example.com", add.Spec.Values["requestManager"]["defaultUser"])
	assert.Equal(t, 1, stub.calls)
	assert.Equal(t, "https://seerr.example.com", stub.got.Endpoint)
	assert.Equal(t, "daniel", stub.got.Inputs[configurator.ExchangeInputUsername])
	assert.Equal(t, "the-jellyfin-password", stub.got.Inputs[configurator.ExchangeInputPassword])
	assert.Nil(t, stub.got.Login)
}

func TestAddProvider_NeverStoresTheTypedPassword(t *testing.T) {
	stub := &stubExchange{contract: "requestManager", result: configurator.ExchangeResult{
		Secrets: map[string]string{"requestManager": "exchanged-key"},
	}}
	mod, orch, _ := exchangeTestModule(t, stub, false)

	w := postExternalApp(mod, exchangeBody)
	require.Equal(t, http.StatusAccepted, w.Code)
	require.Len(t, orch.intents, 1)
	blob, err := json.Marshal(orch.intents[0])
	require.NoError(t, err)
	assert.NotContains(t, string(blob), "the-jellyfin-password",
		"the sign-in is a means to the key; only the key belongs on the record")
	assert.NotContains(t, w.Body.String(), "the-jellyfin-password")
}

func TestAddProvider_UsesTheLoginBloudHoldsWhenAsked(t *testing.T) {
	stub := &stubExchange{contract: "requestManager", result: configurator.ExchangeResult{
		Secrets: map[string]string{"requestManager": "exchanged-key"},
	}}
	mod, orch, _ := exchangeTestModule(t, stub, true)

	w := postExternalApp(mod, `{
		"kind": "provider", "source": "app:seerr", "name": "Seerr",
		"url": "https://seerr.example.com", "exchangeLogin": true
	}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, orch.intents, 1)
	require.NotNil(t, stub.got.Login)
	// The username comes from the provider's static declaration when it published
	// no runtime value: a Bloud-booted Jellyfin's account name is metadata.
	assert.Equal(t, configurator.Login{Username: "bloud-bootstrap-admin", Password: "bootstrap-pw"}, *stub.got.Login)
}

func TestAddProvider_ReadsARuntimePublishedUsername(t *testing.T) {
	stub := &stubExchange{contract: "requestManager", result: configurator.ExchangeResult{
		Secrets: map[string]string{"requestManager": "exchanged-key"},
	}}
	mod, _, secrets := exchangeTestModule(t, stub, true)
	require.NoError(t, secrets.SetAppContractValue("jellyfin", "mediaServer", "adminUsername", "typed-in-account"))

	w := postExternalApp(mod, `{"kind":"provider","source":"app:seerr","name":"Seerr",
		"url":"https://seerr.example.com","exchangeLogin":true}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.NotNil(t, stub.got.Login)
	assert.Equal(t, "typed-in-account", stub.got.Login.Username,
		"a value the provider published at runtime is the current answer")
}

func TestAddProvider_RefusesTheStoredLoginWhenNothingProvidesIt(t *testing.T) {
	stub := &stubExchange{contract: "requestManager"}
	mod, orch, _ := exchangeTestModule(t, stub, false)

	w := postExternalApp(mod, `{"kind":"provider","source":"app:seerr","name":"Seerr",
		"url":"https://seerr.example.com","exchangeLogin":true}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "type the account instead")
	assert.Empty(t, orch.intents)
	assert.Equal(t, 0, stub.calls, "a save with nothing to sign in with must not reach the remote")
}

// TestAddProvider_ExplainsThatASignInWasMissing is the message that replaces
// "contract requires a credential", which would point at a field the form never
// showed the operator.
func TestAddProvider_ExplainsThatASignInWasMissing(t *testing.T) {
	stub := &stubExchange{contract: "requestManager"}
	mod, orch, _ := exchangeTestModule(t, stub, false)

	w := postExternalApp(mod, `{"kind":"provider","source":"app:seerr","name":"Seerr",
		"url":"https://seerr.example.com"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "needs a sign-in")
	assert.Empty(t, orch.intents)
	assert.Equal(t, 0, stub.calls)
}

func TestAddProvider_ReportsWhatTheRemoteRefused(t *testing.T) {
	stub := &stubExchange{contract: "requestManager", err: assertAnError{}}
	mod, orch, _ := exchangeTestModule(t, stub, false)

	w := postExternalApp(mod, exchangeBody)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "the remote said no")
	assert.Empty(t, orch.intents)
}

// TestAddProvider_RejectsAnExchangeThatTradedForNothing: a 200 that produced no
// credential must not become a record that claims a contract it cannot
// authenticate against.
func TestAddProvider_RejectsAnExchangeThatTradedForNothing(t *testing.T) {
	stub := &stubExchange{contract: "requestManager", result: configurator.ExchangeResult{}}
	mod, orch, _ := exchangeTestModule(t, stub, false)

	w := postExternalApp(mod, exchangeBody)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, orch.intents)
}

// TestUpdateProvider_WithoutASignInKeepsTheStoredCredential is the rename case:
// an update that names neither inputs nor the stored login leaves the credential
// alone, exactly as it leaves an omitted secret alone.
func TestUpdateProvider_WithoutASignInKeepsTheStoredCredential(t *testing.T) {
	stub := &stubExchange{contract: "requestManager"}
	mod, orch, _ := exchangeTestModule(t, stub, false)

	// No store is wired here, so the PATCH has to carry the fields the merge
	// would otherwise read off the record; what is under test is that it says
	// nothing about credentials. The router supplies the {id} the handler reads to
	// tell an update from an add.
	router := chi.NewRouter()
	router.Patch("/external-apps/{id}", mod.UpdateHandler())
	req := httptest.NewRequest(http.MethodPatch, "/external-apps/ext-1", strings.NewReader(`{
		"kind": "provider", "source": "app:seerr", "name": "Renamed",
		"url": "https://seerr.example.com"
	}`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	require.Len(t, orch.intents, 1)
	update, ok := orch.intents[0].(orchestrator.UpdateExternalAppIntent)
	require.True(t, ok)
	assert.Empty(t, update.Spec.Secrets, "an update that said nothing about credentials must not blank them")
	assert.Equal(t, 0, stub.calls, "a rename must not sign in to the remote again")
}

func TestApplyProviderExchange_RejectsInputsForAnAppWithNoExchange(t *testing.T) {
	mod, orch, _ := providerTestModule()

	w := postExternalApp(mod, `{
		"kind": "provider", "source": "app:affine", "name": "NAS AFFiNE",
		"url": "https://affine.example.com",
		"values": {"appApi": {"username": "op@example.com", "workspaceId": "ws-1"}},
		"secrets": {"appApi": "the-real-password"},
		"exchange": {"username": "op", "password": "pw"}
	}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "no sign-in exchange")
	assert.Empty(t, orch.intents)
}

// TestResolveLogin_OnlyAUsernameAndPasswordIsALogin pins the generic rule: the
// shortcut is available to a contract that *is* a credential pair, and not to one
// that merely happens to carry a secret.
func TestResolveLogin_OnlyAUsernameAndPasswordIsALogin(t *testing.T) {
	mod, _, _ := exchangeTestModule(t, &stubExchange{contract: "requestManager"}, true)

	if _, _, ok := mod.resolveLogin("pvr"); ok {
		t.Error("a contract with a secret and no username must not resolve to a login")
	}
	if _, _, ok := mod.resolveLogin(""); ok {
		t.Error("no contract is no login")
	}
	login, label, ok := mod.resolveLogin("mediaServer")
	require.True(t, ok)
	assert.Equal(t, "bloud-bootstrap-admin", login.Username)
	assert.Equal(t, "bootstrap-pw", login.Password)
	assert.Contains(t, label, "Jellyfin")
}

// assertAnError is a distinct error type so a test can tell its own refusal from
// one the plumbing produced on its own.
type assertAnError struct{}

func (assertAnError) Error() string { return "the remote said no" }
