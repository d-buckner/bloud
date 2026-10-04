// SPDX-License-Identifier: AGPL-3.0-only

package affine

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// inferenceConfigurator wires a configurator to a fake AFFiNE and a recording
// store, the same shape mcpConfigurator uses.
func inferenceConfigurator(t *testing.T, fake *fakeAffine, secrets *storeSecrets) *Configurator {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	c.baseURL = server.URL
	return c
}

// inferenceState builds an AppState carrying one resolved inference binding.
func inferenceState(dir string, b configurator.InferenceBinding) *configurator.AppState {
	st := &configurator.AppState{DataPath: dir}
	st.Integrations.Inference = []configurator.InferenceBinding{b}
	return st
}

func bindingFor(endpoint, model, key string) configurator.InferenceBinding {
	return configurator.InferenceBinding{Endpoint: endpoint, DefaultModel: model, APIKey: key}
}

// The first pass registers Bloud's endpoint as a workspace BYOK profile: the
// endpoint, dialect, model, and gateway credential all reach AFFiNE, and the
// profile id is recorded so the next pass can reconcile without re-reading a
// credential AFFiNE never returns.
func TestEnsureInferenceProvider_FirstPassRegistersEndpoint(t *testing.T) {
	fake := newFakeAffine("ws-1")
	secrets := newStoreSecrets("owner-pass")
	c := inferenceConfigurator(t, fake, secrets)
	state := inferenceState(t.TempDir(), bindingFor("http://apps-litellm:4000/v1", "gpt-4o-mini", "gw-key"))

	c.ensureInferenceProvider(context.Background(), state)

	creates, replaces, rotates, _ := fake.byokStats()
	assert.Equal(t, 1, creates)
	assert.Equal(t, 0, replaces)
	assert.Equal(t, 0, rotates)
	assert.Equal(t, 1, fake.byokProfileCount())

	endpoint, model, credential := fake.byokLastWrite()
	assert.Equal(t, "http://apps-litellm:4000/v1", endpoint)
	assert.Equal(t, "gpt-4o-mini", model)
	assert.Equal(t, "gw-key", credential, "the gateway credential reaches AFFiNE, not a provider key")

	stored := c.readByokState(state)
	assert.Equal(t, fake.byokProfileID(), stored.Profiles["ws-1"].ProfileID, "the profile id is recorded for the next pass")
	assert.NotEmpty(t, stored.Profiles["ws-1"].Fingerprint)
}

// A steady-state pass must touch nothing. Replacing the profile every
// reconciliation would bump its revision on a ~60s timer and re-write a
// credential nothing changed.
func TestEnsureInferenceProvider_SteadyStateDoesNothing(t *testing.T) {
	fake := newFakeAffine("ws-1")
	secrets := newStoreSecrets("owner-pass")
	c := inferenceConfigurator(t, fake, secrets)
	state := inferenceState(t.TempDir(), bindingFor("http://apps-litellm:4000/v1", "gpt-4o-mini", "gw-key"))

	c.ensureInferenceProvider(context.Background(), state)
	for i := 0; i < 3; i++ {
		c.ensureInferenceProvider(context.Background(), state)
	}

	creates, replaces, rotates, deletes := fake.byokStats()
	assert.Equal(t, 1, creates)
	assert.Equal(t, 0, replaces, "an unchanged endpoint is never replaced")
	assert.Equal(t, 0, rotates, "an unchanged credential is never rotated")
	assert.Equal(t, 0, deletes)
	assert.Equal(t, 1, fake.byokProfileCount())
}

// When only the gateway key changes, the definition is untouched: the profile
// is rotated rather than replaced, so its revision does not churn.
func TestEnsureInferenceProvider_RotatesChangedCredential(t *testing.T) {
	fake := newFakeAffine("ws-1")
	secrets := newStoreSecrets("owner-pass")
	c := inferenceConfigurator(t, fake, secrets)
	dir := t.TempDir()

	c.ensureInferenceProvider(context.Background(), inferenceState(dir, bindingFor("http://gw:4000/v1", "m1", "old-key")))
	c.ensureInferenceProvider(context.Background(), inferenceState(dir, bindingFor("http://gw:4000/v1", "m1", "new-key")))

	creates, replaces, rotates, _ := fake.byokStats()
	assert.Equal(t, 1, creates)
	assert.Equal(t, 1, rotates, "a credential-only change rotates")
	assert.Equal(t, 0, replaces, "a credential-only change does not replace the definition")

	_, _, credential := fake.byokLastWrite()
	assert.Equal(t, "new-key", credential)
}

// A changed endpoint or model is a definition change: the profile is replaced,
// because rotate leaves the definition alone and could not carry it.
func TestEnsureInferenceProvider_ReplacesChangedEndpoint(t *testing.T) {
	fake := newFakeAffine("ws-1")
	secrets := newStoreSecrets("owner-pass")
	c := inferenceConfigurator(t, fake, secrets)
	dir := t.TempDir()

	c.ensureInferenceProvider(context.Background(), inferenceState(dir, bindingFor("http://old:4000/v1", "m1", "key")))
	c.ensureInferenceProvider(context.Background(), inferenceState(dir, bindingFor("http://new:4000/v1", "m1", "key")))

	creates, replaces, rotates, _ := fake.byokStats()
	assert.Equal(t, 1, creates)
	assert.Equal(t, 1, replaces, "a changed endpoint replaces the profile")
	assert.Equal(t, 0, rotates)

	endpoint, _, _ := fake.byokLastWrite()
	assert.Equal(t, "http://new:4000/v1", endpoint)
}

// Deleting the profile in the AFFiNE UI is not sticky: the next pass notices the
// absence and registers it again, so the AI surface heals itself.
func TestEnsureInferenceProvider_RecreatesDeletedProfile(t *testing.T) {
	fake := newFakeAffine("ws-1")
	secrets := newStoreSecrets("owner-pass")
	c := inferenceConfigurator(t, fake, secrets)
	state := inferenceState(t.TempDir(), bindingFor("http://gw:4000/v1", "m1", "key"))

	c.ensureInferenceProvider(context.Background(), state)
	fake.byokDeleteExternally()

	c.ensureInferenceProvider(context.Background(), state)

	creates, _, _, _ := fake.byokStats()
	assert.Equal(t, 2, creates, "a profile removed out of band is recreated")
	assert.Equal(t, 1, fake.byokProfileCount())
}

// Unbinding the contract (gateway uninstalled, setting cleared) removes the
// profile Bloud owns, so AFFiNE stops dialing an endpoint that is gone.
func TestEnsureInferenceProvider_RemovesProfileWhenUnbound(t *testing.T) {
	fake := newFakeAffine("ws-1")
	secrets := newStoreSecrets("owner-pass")
	c := inferenceConfigurator(t, fake, secrets)
	dir := t.TempDir()

	c.ensureInferenceProvider(context.Background(), inferenceState(dir, bindingFor("http://gw:4000/v1", "m1", "key")))
	require.Equal(t, 1, fake.byokProfileCount())

	// An AppState with no inference slice means the contract is unbound.
	c.ensureInferenceProvider(context.Background(), &configurator.AppState{DataPath: dir})

	_, _, _, deletes := fake.byokStats()
	assert.Equal(t, 1, deletes)
	assert.Equal(t, 0, fake.byokProfileCount())
	assert.Empty(t, c.readByokState(&configurator.AppState{DataPath: dir}).Profiles["ws-1"].ProfileID,
		"the recorded profile id is cleared once the profile is gone")
}

// The operator's account may own several workspaces, and Bloud signs in as that
// account, so it can see and wire every one of them. Each workspace gets its own
// profile and its own recorded state, and a steady-state pass touches none.
func TestEnsureInferenceProvider_WiresAllWorkspaces(t *testing.T) {
	fake := newFakeAffine("ws-1", "ws-2")
	secrets := newStoreSecrets("owner-pass")
	c := inferenceConfigurator(t, fake, secrets)
	state := inferenceState(t.TempDir(), bindingFor("http://gw:4000/v1", "m1", "key"))

	c.ensureInferenceProvider(context.Background(), state)

	creates, _, _, _ := fake.byokStats()
	assert.Equal(t, 2, creates, "each workspace the operator owns gets a profile")
	assert.Equal(t, 2, fake.byokProfileCount())

	stored := c.readByokState(state)
	require.Len(t, stored.Profiles, 2)
	assert.NotEmpty(t, stored.Profiles["ws-1"].ProfileID)
	assert.NotEmpty(t, stored.Profiles["ws-2"].ProfileID)

	c.ensureInferenceProvider(context.Background(), state)
	creates, _, _, _ = fake.byokStats()
	assert.Equal(t, 2, creates, "a steady-state pass registers nothing new")
}

// A server whose custom-endpoint policy did not take effect (config.json not
// loaded) must be left alone: the create would be rejected every pass, and an
// opaque per-pass failure is worse than a single warning.
func TestEnsureInferenceProvider_DisabledCustomEndpointsWiresNothing(t *testing.T) {
	fake := newFakeAffine("ws-1")
	fake.setByokPolicyMode("disabled")
	secrets := newStoreSecrets("owner-pass")
	c := inferenceConfigurator(t, fake, secrets)

	c.ensureInferenceProvider(context.Background(), inferenceState(t.TempDir(), bindingFor("http://gw:4000/v1", "m1", "key")))

	creates, replaces, rotates, deletes := fake.byokStats()
	assert.Equal(t, 0, creates)
	assert.Equal(t, 0, replaces)
	assert.Equal(t, 0, rotates)
	assert.Equal(t, 0, deletes)
}

// A bound-but-incomplete binding is "not ready", not "unbind": an empty model
// would be stored as a profile AFFiNE cannot use, and the teardown path must not
// fire just because the gateway has not finished publishing. A missing
// credential is NOT incomplete: a keyless gateway has none, and the profile
// still has to be registered (with the placeholder credential).
func TestEnsureInferenceProvider_IncompleteBindingWiresNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		binding configurator.InferenceBinding
	}{
		{"no endpoint", bindingFor("", "m1", "key")},
		{"no model", bindingFor("http://gw:4000/v1", "", "key")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeAffine("ws-1")
			secrets := newStoreSecrets("owner-pass")
			c := inferenceConfigurator(t, fake, secrets)

			c.ensureInferenceProvider(context.Background(), inferenceState(t.TempDir(), tc.binding))

			creates, _, _, deletes := fake.byokStats()
			assert.Equal(t, 0, creates, "an incomplete binding registers nothing")
			assert.Equal(t, 0, deletes, "and does not tear anything down")
		})
	}
}

// A credential rotation must change the fingerprint, because AFFiNE never
// returns the stored credential and the hash is the only signal that a rotation
// is due. A keyless binding fingerprints as the placeholder, so an operator who
// later sets a real key sees the fingerprint move and the credential rotate.
func TestByokFingerprint_ChangesWithCredential(t *testing.T) {
	a := byokFingerprint("ws-1", bindingFor("http://gw:4000/v1", "m1", "old"))
	b := byokFingerprint("ws-1", bindingFor("http://gw:4000/v1", "m1", "new"))
	c := byokFingerprint("ws-1", bindingFor("http://gw:4000/v1", "m1", "old"))

	assert.NotEqual(t, a, b)
	assert.Equal(t, a, c, "the fingerprint is stable for identical inputs")

	keyless := byokFingerprint("ws-1", bindingFor("http://gw:4000/v1", "m1", ""))
	assert.NotEqual(t, a, keyless, "adding a real key to a keyless binding is a change")
	assert.Equal(t, keyless, byokFingerprint("ws-1", bindingFor("http://gw:4000/v1", "m1", "")))
}

// sameByokDefinition is what separates a credential rotation from a definition
// replace, so each field it reads has to be load-bearing.
func TestSameByokDefinition(t *testing.T) {
	desired := bindingFor("http://gw:4000/v1", "m1", "key")
	var matching byokProfile
	require.NoError(t, json.Unmarshal([]byte(`{
		"profileId": "p1",
		"provider": "openai",
		"name": "Bloud",
		"enabled": true,
		"revision": 1,
		"definition": {
			"endpoint": {"kind": "openai_compatible", "url": "http://gw:4000/v1", "dialect": "chat_completions"},
			"models": [{"modelId": "m1", "enabled": true}]
		}
	}`), &matching))

	assert.True(t, sameByokDefinition(&matching, desired))

	matching.Enabled = false
	assert.False(t, sameByokDefinition(&matching, desired), "a disabled profile is re-enabled, not rotated")

	matching.Enabled = true
	matching.Definition.Models[0].ModelID = "other"
	assert.False(t, sameByokDefinition(&matching, desired), "a different model is a definition change")
}

// A keyless gateway (no credential in the binding) still registers a profile:
// AFFiNE requires a credential string, so Bloud stores the inert placeholder and
// the keyless upstream ignores it. This is the real shape of Bloud's own
// instance gateway, so it is the case that must not be skipped.
func TestEnsureInferenceProvider_KeylessGatewayRegisters(t *testing.T) {
	fake := newFakeAffine("ws-1")
	secrets := newStoreSecrets("owner-pass")
	c := inferenceConfigurator(t, fake, secrets)
	state := inferenceState(t.TempDir(), bindingFor("http://gw:4000/v1", "m1", ""))

	c.ensureInferenceProvider(context.Background(), state)

	creates, _, _, _ := fake.byokStats()
	assert.Equal(t, 1, creates, "a keyless binding still registers a profile")
	_, _, credential := fake.byokLastWrite()
	assert.Equal(t, byokKeylessCredential, credential, "the placeholder stands in for the missing key")

	// Steady state is a no-op: the placeholder is stable, so the fingerprint is
	// stable and the profile is not rewritten every pass.
	c.ensureInferenceProvider(context.Background(), state)
	creates, _, rotates, _ := fake.byokStats()
	assert.Equal(t, 1, creates)
	assert.Equal(t, 0, rotates)
}

// The config.json half: without the byok policy the server refuses a custom
// endpoint, so the flags are part of the contract, not decoration.
func TestRenderConfigFile_OpensCustomEndpointPolicy(t *testing.T) {
	content, err := renderConfigFile("http://affine.localhost:8080", nil, nil)
	require.NoError(t, err)

	var cfg map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &cfg))
	copilot, ok := cfg["copilot"].(map[string]any)
	require.True(t, ok)
	byok, ok := copilot["byok"].(map[string]any)
	require.True(t, ok, "the byok policy block must be present for the inference wiring")
	assert.Equal(t, true, byok["enabled"])
	assert.Equal(t, true, byok["allowCustomEndpoint"])
	assert.Equal(t, true, byok["allowPrivateEndpoint"], "Bloud's gateway is a private-network address")
}
