// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ccSecret is the credential value these tests store. Named so a test can
// assert it never appears where it should not.
const ccSecret = "s3cr3t-client-password-DO-NOT-LEAK"

// fakeSecrets is an in-memory stand-in for the host secret store.
type fakeSecrets struct {
	values map[string]string
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{values: map[string]string{}}
}

func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) { return "", nil }
func (f *fakeSecrets) GetAppSecret(app, key string) string {
	return f.values[app+"/"+key]
}
func (f *fakeSecrets) SetAppSecret(app, key, value string) error {
	f.values[app+"/"+key] = value
	return nil
}
func (f *fakeSecrets) SetAppContractValue(string, string, string, string) error { return nil }
func (f *fakeSecrets) GetAppContractValue(string, string, string) string        { return "" }
func (f *fakeSecrets) DeleteAppSecrets(app string) error {
	for k := range f.values {
		if strings.HasPrefix(k, app+"/") {
			delete(f.values, k)
		}
	}
	return nil
}

// fakeCatalog serves one app with a fixed set of offers.
type fakeCatalog struct {
	app *catalog.App
}

func (f *fakeCatalog) Get(name string) (*catalog.App, error) {
	if f.app == nil || f.app.CatalogID != name {
		return nil, nil
	}
	return f.app, nil
}

// fakeSettings is an in-memory settings KV.
type fakeSettings struct {
	values map[string]string
}

func newFakeSettings() *fakeSettings { return &fakeSettings{values: map[string]string{}} }

func (f *fakeSettings) Get(key string) (string, error) { return f.values[key], nil }
func (f *fakeSettings) Set(key, value string) error    { f.values[key] = value; return nil }

// clientAccessApp builds a catalog app that offers clientPassword with the
// given access policy.
func clientAccessApp(access *catalog.ClientAccess) *catalog.App {
	return &catalog.App{
		CatalogID:   "hermes-webui",
		DisplayName: "Hermes Web UI",
		Description: "test",
		Category:    "productivity",
		Port:        8787,
		Provides: catalog.Provides{"clientPassword": catalog.ContractProvides{
			Secrets:      []string{"password"},
			ClientAccess: access,
		}},
	}
}

func newTestCCModule(access *catalog.ClientAccess, secrets *fakeSecrets) *clientCredentialsModule {
	return NewClientCredentialsModule(
		&fakeCatalog{app: clientAccessApp(access)},
		secrets,
		newFakeSettings(),
		nil,
		&fakeOrch{},
		nil,
	)
}

// fakeOrch records the intents the module submits, so a test can assert the
// rotate path asks the orchestrator to converge rather than touching a
// container itself.
type fakeOrch struct {
	intents []string
}

func (f *fakeOrch) Submit(intent orchestrator.Intent) {
	f.intents = append(f.intents, fmt.Sprintf("%T", intent))
}

func withURLParams(r *http.Request, params map[string]string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// revealRequest builds the POST the UI would send.
func revealRequest(app, secret string) *http.Request {
	return withURLParams(httptest.NewRequest(http.MethodPost, "/api/apps/"+app+"/client-credentials/"+secret+"/reveal", nil),
		map[string]string{"name": app, "secret": secret})
}

func listRequest(app string) *http.Request {
	return withURLParams(httptest.NewRequest(http.MethodGet, "/api/apps/"+app+"/client-credentials", nil),
		map[string]string{"name": app})
}

func rotateRequest(app, secret string) *http.Request {
	return withURLParams(httptest.NewRequest(http.MethodPost,
		"/api/apps/"+app+"/client-credentials/"+secret+"/rotate", nil),
		map[string]string{"name": app, "secret": secret})
}

func revokeRequest(app, secret string) *http.Request {
	return withURLParams(httptest.NewRequest(http.MethodPost,
		"/api/apps/"+app+"/client-credentials/"+secret+"/revoke", nil),
		map[string]string{"name": app, "secret": secret})
}

// TestRevokeSubmitsTheIntentAndSaysWhatItDoes pins the revoke contract.
//
// The response is 202 rather than 200 because the request is not the effect:
// the revocation lands on the next convergence pass. A 200 would tell the UI
// the devices are already logged out when they are not.
func TestRevokeSubmitsTheIntentAndSaysWhatItDoes(t *testing.T) {
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal:  catalog.ClientRevealOnce,
		Rotate:  catalog.ClientRotateBloud,
		Reaches: "the app",
	}, newFakeSecrets())
	orch := mod.orch.(*fakeOrch)

	rec := httptest.NewRecorder()
	mod.revokeHandler()(rec, revokeRequest("hermes-webui", "password"))
	require.Equal(t, http.StatusAccepted, rec.Code)

	require.Len(t, orch.intents, 1)
	assert.Equal(t, fmt.Sprintf("%T", orchestrator.RevokeClientSessionsIntent{}), orch.intents[0])

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, true, payload["endsAllSessions"],
		"the response must say plainly that every live session ends")
}

// TestRevokeRejectsAnUndeclaredCredential stops a revoke aimed at something the
// app never declared client-accessible.
func TestRevokeRejectsAnUndeclaredCredential(t *testing.T) {
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal:  catalog.ClientRevealOnce,
		Rotate:  catalog.ClientRotateBloud,
		Reaches: "the app",
	}, newFakeSecrets())

	rec := httptest.NewRecorder()
	mod.revokeHandler()(rec, revokeRequest("hermes-webui", "someOtherSecret"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, mod.orch.(*fakeOrch).intents)
}

// TestRevokeWithoutAnOrchestratorIsUnavailable proves the missing-dependency
// path. A revoke cannot be performed without the orchestrator, and pretending
// otherwise would tell the operator their users were logged out when nobody
// was.
func TestRevokeWithoutAnOrchestratorIsUnavailable(t *testing.T) {
	mod := NewClientCredentialsModule(
		&fakeCatalog{app: clientAccessApp(&catalog.ClientAccess{
			Reveal:  catalog.ClientRevealOnce,
			Rotate:  catalog.ClientRotateBloud,
			Reaches: "the app",
		})},
		newFakeSecrets(), newFakeSettings(), nil, nil, nil,
	)

	rec := httptest.NewRecorder()
	mod.revokeHandler()(rec, revokeRequest("hermes-webui", "password"))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestRotateReplacesTheValueAndAsksForConvergence pins the rotate contract.
func TestRotateReplacesTheValueAndAsksForConvergence(t *testing.T) {
	secrets := newFakeSecrets()
	require.NoError(t, secrets.SetAppSecret("hermes-webui", "password", ccSecret))
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal:  catalog.ClientRevealOnce,
		Rotate:  catalog.ClientRotateBloud,
		Reaches: "the app",
	}, secrets)
	orch := mod.orch.(*fakeOrch)

	rec := httptest.NewRecorder()
	mod.rotateHandler()(rec, rotateRequest("hermes-webui", "password"))
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	newValue, _ := payload["value"].(string)
	require.NotEmpty(t, newValue)
	assert.NotEqual(t, ccSecret, newValue, "rotate must produce a new value")
	assert.Equal(t, newValue, secrets.GetAppSecret("hermes-webui", "password"),
		"the store must hold the rotated value")
	assert.Contains(t, payload["snippet"], newValue,
		"the snippet must carry the new value, not the old one")
	assert.NotContains(t, rec.Body.String(), ccSecret)

	require.Len(t, orch.intents, 2, "rotate must submit a revoke intent and a reconcile intent")
	assert.Equal(t, fmt.Sprintf("%T", orchestrator.RevokeClientSessionsIntent{}), orch.intents[0],
		"rotate must revoke sessions first, before the reconcile")
	assert.Equal(t, fmt.Sprintf("%T", orchestrator.ReconcileIntent{}), orch.intents[1],
		"rotate must ask for a reconcile so the resync recreates the container")
}

// TestRotateRefusedWhenPolicySaysNo proves the rotate policy is enforced.
func TestRotateRefusedWhenPolicySaysNo(t *testing.T) {
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal: catalog.ClientRevealAlways,
		Rotate: catalog.ClientRotateNone,
	}, newFakeSecrets())

	rec := httptest.NewRecorder()
	mod.rotateHandler()(rec, rotateRequest("hermes-webui", "password"))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestRotateIsHonestAboutSessions pins the disclosure the UI shows.
//
// The response carries sessionsSurvive so the frontend cannot quietly render a
// rotate as a lockout. It is a v1 truth that rotation leaves live sessions
// running, and the API says so rather than leaving the UI to infer it.
func TestRotateIsHonestAboutSessions(t *testing.T) {
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal:  catalog.ClientRevealOnce,
		Rotate:  catalog.ClientRotateBloud,
		Reaches: "the app",
	}, newFakeSecrets())

	rec := httptest.NewRecorder()
	mod.rotateHandler()(rec, rotateRequest("hermes-webui", "password"))
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, true, payload["endsAllSessions"],
		"rotation ends every live session, and the API must say so")
	assert.NotContains(t, rec.Body.String(), "sessionsSurvive",
		"the old field must not linger: it said the opposite of what rotate now does")
}

// TestRotateRotatedValueIsRevealableOnce proves the once counter follows the
// value across a rotate.
func TestRotateRotatedValueIsRevealableOnce(t *testing.T) {
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal:  catalog.ClientRevealOnce,
		Rotate:  catalog.ClientRotateBloud,
		Reaches: "the app",
	}, newFakeSecrets())

	rot := httptest.NewRecorder()
	mod.rotateHandler()(rot, rotateRequest("hermes-webui", "password"))
	require.Equal(t, http.StatusOK, rot.Code)

	first := httptest.NewRecorder()
	mod.revealHandler()(first, revealRequest("hermes-webui", "password"))
	require.Equal(t, http.StatusOK, first.Code)

	second := httptest.NewRecorder()
	mod.revealHandler()(second, revealRequest("hermes-webui", "password"))
	assert.Equal(t, http.StatusConflict, second.Code)
}

// TestListNeverReturnsTheValue is SC3's core assertion.
//
// The list route is what the dashboard polls. If the credential value could
// come back on it, "reveal once" would be a policy about the modal rather than
// about the API, and the modal is not the security boundary. This asserts the
// serialized response contains no trace of the value at any reveal policy,
// including `always`.
// TestNonPollableListNeverReturnsTheValue is the SC3 assertion. It walks the
// list response under every reveal policy and fails if the value appears in
// any of them. The name matches the pilot plan's "How to verify" command.
func TestNonPollableListNeverReturnsTheValue(t *testing.T) {
	policies := []catalog.ClientReveal{
		catalog.ClientRevealNever,
		catalog.ClientRevealOnce,
		catalog.ClientRevealAlways,
	}

	for _, policy := range policies {
		t.Run(string(policy), func(t *testing.T) {
			secrets := newFakeSecrets()
			require.NoError(t, secrets.SetAppSecret("hermes-webui", "password", ccSecret))

			mod := newTestCCModule(&catalog.ClientAccess{
				Reveal:  policy,
				Rotate:  catalog.ClientRotateBloud,
				Reaches: "the app",
			}, secrets)

			rec := httptest.NewRecorder()
			mod.ListHandler()(rec, listRequest("hermes-webui"))

			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			assert.NotContains(t, body, ccSecret,
				"the list response must never carry the credential under policy %q", policy)

			var payload struct {
				Credentials []credentialDescriptor `json:"credentials"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &payload))
			require.Len(t, payload.Credentials, 1)
			assert.Equal(t, string(policy), payload.Credentials[0].Reveal)
			assert.True(t, payload.Credentials[0].Published)
		})
	}
}

// TestListReturnsEmptyForAnAppWithoutClientAccess is the SC6 backend half:
// an app that declares no clientAccess block returns an empty descriptor list,
// which is what removes the panel from the UI.
func TestListReturnsEmptyForAnAppWithoutClientAccess(t *testing.T) {
	app := clientAccessApp(&catalog.ClientAccess{Reveal: catalog.ClientRevealOnce})
	app.Provides = catalog.Provides{} // the app offers nothing client-accessible

	mod := NewClientCredentialsModule(
		&fakeCatalog{app: app}, newFakeSecrets(), newFakeSettings(), nil, &fakeOrch{}, nil)

	rec := httptest.NewRecorder()
	mod.ListHandler()(rec, listRequest("hermes-webui"))
	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Credentials []credentialDescriptor `json:"credentials"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Empty(t, payload.Credentials, "no clientAccess block means no credentials to show")
}

// TestRevealUnderOnceServesOnceThenRefuses pins the once-only contract.
func TestRevealUnderOnceServesOnceThenRefuses(t *testing.T) {
	secrets := newFakeSecrets()
	require.NoError(t, secrets.SetAppSecret("hermes-webui", "password", ccSecret))
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal:  catalog.ClientRevealOnce,
		Rotate:  catalog.ClientRotateBloud,
		Reaches: "the app",
	}, secrets)

	first := httptest.NewRecorder()
	mod.revealHandler()(first, revealRequest("hermes-webui", "password"))
	require.Equal(t, http.StatusOK, first.Code)
	assert.Contains(t, first.Body.String(), ccSecret)

	second := httptest.NewRecorder()
	mod.revealHandler()(second, revealRequest("hermes-webui", "password"))
	assert.Equal(t, http.StatusConflict, second.Code,
		"a once credential must not be revealable twice")
	assert.NotContains(t, second.Body.String(), ccSecret)
}

// TestRotateMakesTheNewValueRevealable proves the once counter is bound to the
// value, not to the slot.
//
// A rotate produces a different credential, so it has not been shown, and the
// operator has to be able to see it once. Keying the counter on the slot
// instead would mean a rotate could never be revealed, which would make the
// recovery path for a lost password unusable.
func TestRotateMakesTheNewValueRevealable(t *testing.T) {
	secrets := newFakeSecrets()
	require.NoError(t, secrets.SetAppSecret("hermes-webui", "password", ccSecret))
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal:  catalog.ClientRevealOnce,
		Rotate:  catalog.ClientRotateBloud,
		Reaches: "the app",
	}, secrets)

	first := httptest.NewRecorder()
	mod.revealHandler()(first, revealRequest("hermes-webui", "password"))
	require.Equal(t, http.StatusOK, first.Code)

	rotated := "brand-new-rotated-value"
	require.NoError(t, secrets.SetAppSecret("hermes-webui", "password", rotated))

	after := httptest.NewRecorder()
	mod.revealHandler()(after, revealRequest("hermes-webui", "password"))
	require.Equal(t, http.StatusOK, after.Code, "a rotated value has not been shown yet")
	assert.Contains(t, after.Body.String(), rotated)
	assert.NotContains(t, after.Body.String(), ccSecret)
}

// TestRevealRefusedUnderNever proves the policy is enforced at the API.
func TestRevealRefusedUnderNever(t *testing.T) {
	secrets := newFakeSecrets()
	require.NoError(t, secrets.SetAppSecret("hermes-webui", "password", ccSecret))
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal: catalog.ClientRevealNever,
		Rotate: catalog.ClientRotateBloud,
	}, secrets)

	rec := httptest.NewRecorder()
	mod.revealHandler()(rec, revealRequest("hermes-webui", "password"))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.NotContains(t, rec.Body.String(), ccSecret)
}

// TestRevealRejectsAnUndeclaredSecret stops a caller naming any secret the app
// happens to hold.
//
// Without this, POST .../reveal with secret=oidcClientSecret would read a
// credential the app never declared client-accessible. The policy is per
// declared credential, so the name has to be checked against the declaration
// rather than against the store.
func TestRevealRejectsAnUndeclaredSecret(t *testing.T) {
	secrets := newFakeSecrets()
	require.NoError(t, secrets.SetAppSecret("hermes-webui", "oidcClientSecret", ccSecret))
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal:  catalog.ClientRevealAlways,
		Rotate:  catalog.ClientRotateBloud,
		Reaches: "the app",
	}, secrets)

	rec := httptest.NewRecorder()
	mod.revealHandler()(rec, revealRequest("hermes-webui", "oidcClientSecret"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotContains(t, rec.Body.String(), ccSecret)
}

// TestRevealOfAnUnpublishedCredentialIsNotFound distinguishes "not yet
// installed" from "no such credential".
func TestRevealOfAnUnpublishedCredentialIsNotFound(t *testing.T) {
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal:  catalog.ClientRevealAlways,
		Rotate:  catalog.ClientRotateBloud,
		Reaches: "the app",
	}, newFakeSecrets())

	rec := httptest.NewRecorder()
	mod.revealHandler()(rec, revealRequest("hermes-webui", "password"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestUnknownAppIsNotFound covers the other lookup failure.
func TestUnknownAppIsNotFound(t *testing.T) {
	mod := newTestCCModule(&catalog.ClientAccess{Reveal: catalog.ClientRevealAlways}, newFakeSecrets())

	rec := httptest.NewRecorder()
	mod.ListHandler()(rec, listRequest("nope"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestSnippetRendersThePair checks the copy-ready form the mobile shape needs.
func TestSnippetRendersThePair(t *testing.T) {
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal:  catalog.ClientRevealAlways,
		Snippet: catalog.SnippetURLAndPassword,
		Reaches: "the app",
	}, newFakeSecrets())

	snippet := mod.renderSnippet(catalog.SnippetURLAndPassword, "hermes-webui", "hunter2")
	assert.Contains(t, snippet, "URL:")
	assert.Contains(t, snippet, "hunter2")
	// The URL is the app's own routed subdomain, not the instance root.
	assert.Contains(t, snippet, "hermes-webui.localhost:8080")

	assert.Equal(t, "hunter2", mod.renderSnippet("", "hermes-webui", "hunter2"),
		"no snippet shape means the bare value")
}

// TestRevealedFlagIsAccurateOnList proves the UI can tell "already shown" from
// "never shown" without holding the value.
func TestRevealedFlagIsAccurateOnList(t *testing.T) {
	secrets := newFakeSecrets()
	require.NoError(t, secrets.SetAppSecret("hermes-webui", "password", ccSecret))
	mod := newTestCCModule(&catalog.ClientAccess{
		Reveal:  catalog.ClientRevealOnce,
		Rotate:  catalog.ClientRotateBloud,
		Reaches: "the app",
	}, secrets)

	before := httptest.NewRecorder()
	mod.ListHandler()(before, listRequest("hermes-webui"))
	var beforePayload struct {
		Credentials []credentialDescriptor `json:"credentials"`
	}
	require.NoError(t, json.Unmarshal(before.Body.Bytes(), &beforePayload))
	require.Len(t, beforePayload.Credentials, 1)
	assert.False(t, beforePayload.Credentials[0].Revealed)

	require.Equal(t, http.StatusOK, func() int {
		rec := httptest.NewRecorder()
		mod.revealHandler()(rec, revealRequest("hermes-webui", "password"))
		return rec.Code
	}())

	after := httptest.NewRecorder()
	mod.ListHandler()(after, listRequest("hermes-webui"))
	var afterPayload struct {
		Credentials []credentialDescriptor `json:"credentials"`
	}
	require.NoError(t, json.Unmarshal(after.Body.Bytes(), &afterPayload))
	require.Len(t, afterPayload.Credentials, 1)
	assert.True(t, afterPayload.Credentials[0].Revealed)
	assert.False(t, strings.Contains(after.Body.String(), ccSecret))
}
