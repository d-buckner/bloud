// SPDX-License-Identifier: AGPL-3.0-only

// Inference wiring for AFFiNE's BYOK endpoint: reconciliation of the
// Bloud-owned profile, the persisted state fingerprint, and the workspace
// scope that carries it. Split out of configurator.go so that file stays
// under the file-length ceiling.
package affine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
)

// --- Inference (BYOK endpoint) ---

// byokProfileName labels the profile Bloud owns in AFFiNE's BYOK list. It is
// the identity key for reconciliation: a profile with this name is Bloud's to
// replace or remove, and any other profile is left alone.
const byokProfileName = "Bloud"

// byokDescription is required to be present explicitly on create and replace
// (AFFiNE rejects an input that omits the key). It is what the BYOK list shows
// beside the name.
const byokDescription = "Bloud inference endpoint"

// byokStateFileName is Bloud's own record of what it last wrote into AFFiNE.
// AFFiNE never returns the stored credential, so this is the only way to tell
// a stale gateway key from a current one without rewriting the profile every
// pass. It lives at the app data root, not under config/, so it is not mounted
// into the container.
const byokStateFileName = "bloud-inference.json"

// byokState is the persisted reconciliation state, one record per workspace:
// the profile Bloud owns there and a fingerprint of the credential, endpoint,
// and model it last wrote. Keyed by workspace id because the profile is
// per-workspace and the operator may own several.
type byokState struct {
	Profiles map[string]byokWorkspaceState `json:"profiles"`
}

type byokWorkspaceState struct {
	ProfileID   string `json:"profileId"`
	Fingerprint string `json:"fingerprint"`
}

// inferenceBinding pulls the resolved inference binding out of the app state.
// The bool reports whether the contract is bound at all, which is what separates
// "remove Bloud's endpoint" (unbound) from "the binding is not ready yet"
// (bound but incomplete).
func inferenceBinding(state *configurator.AppState) (bindings.InferenceBinding, bool) {
	if state == nil || len(state.Integrations.Inference) == 0 {
		return bindings.InferenceBinding{}, false
	}
	return state.Integrations.Inference[0], true
}

// ensureInferenceProvider points AFFiNE's built-in AI at Bloud's resolved
// inference endpoint by registering it as a per-workspace BYOK profile.
//
// AFFiNE's AI is "bring your own key" in shape; Bloud makes it "bring your own
// endpoint" by holding the credential behind its gateway, so the operator never
// handles a real provider secret. The profile is workspace-scoped and stored in
// AFFiNE's database, which is why this runs in PostStart through AFFiNE's API
// rather than being written into config.json (config.json only opens the
// custom-endpoint policy; see renderConfigFile).
//
// Idempotent: when the profile already matches what Bloud last wrote, nothing
// is sent. A credential-only change rotates the credential; an endpoint or
// model change replaces the profile; a profile deleted in the AFFiNE UI is
// recreated. Failures are logged and swallowed, exactly like the MCP
// credential: a knowledge base that serves its users fine must not land in
// ERROR because AI wiring failed, and the next pass retries.
func (c *Configurator) ensureInferenceProvider(ctx context.Context, state *configurator.AppState) {
	binding, declared := inferenceBinding(state)
	if !declared {
		c.removeInferenceProvider(ctx, state)
		return
	}
	// A bound but incomplete binding is "not ready", not "unbind": an empty
	// endpoint or default model would be stored as a profile AFFiNE cannot use.
	// A missing credential is NOT "not ready": this instance's gateway may be
	// keyless, and the profile still has to be registered (see byokCredential).
	if binding.Endpoint == "" || binding.DefaultModel == "" {
		c.logger.Info("affine AI: inference binding is not ready, wiring nothing",
			"endpoint", binding.Endpoint, "model", binding.DefaultModel)
		return
	}
	if c.secrets == nil {
		c.logger.Warn("cannot wire affine AI: no secrets provider")
		return
	}
	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		c.logger.Warn("cannot wire affine AI: no owner password", "err", err)
		return
	}
	if err := c.api.signIn(ctx, c.adminEmail, password); err != nil {
		c.logger.Warn("affine AI: owner sign-in failed, wiring nothing", "err", err)
		return
	}
	ids, err := c.api.listWorkspaces(ctx)
	if err != nil {
		c.logger.Warn("affine AI: could not list workspaces", "err", err)
		return
	}
	if len(ids) == 0 {
		c.logger.Info("affine AI: no workspaces yet, wiring nothing")
		return
	}

	stored := c.readByokState(state)
	if stored.Profiles == nil {
		stored.Profiles = map[string]byokWorkspaceState{}
	}
	for _, workspaceID := range ids {
		c.ensureWorkspaceInference(ctx, workspaceID, binding, &stored)
	}
	// Drop records for workspaces that no longer exist so the state file does
	// not accumulate dead profile ids.
	keep := make(map[string]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	for id := range stored.Profiles {
		if !keep[id] {
			delete(stored.Profiles, id)
		}
	}
	c.writeByokState(state, stored)
}

// ensureWorkspaceInference makes one workspace's BYOK settings point at Bloud's
// endpoint. It is the single-workspace half of ensureInferenceProvider: the
// caller decides which workspaces to visit (now: every workspace the operator's
// account belongs to), this function makes each one match. The recorded state is
// stored under the workspace id in the shared map so a profile id is never
// mistaken for another workspace's.
func (c *Configurator) ensureWorkspaceInference(ctx context.Context, workspaceID string, binding bindings.InferenceBinding, stored *byokState) {
	settings, err := c.api.byokSettings(ctx, workspaceID)
	if err != nil {
		c.logger.Warn("affine AI: could not read BYOK settings", "workspace", workspaceID, "err", err)
		return
	}
	// The mode is driven by config.json, which PreStart writes and which forces
	// a container recreate before PostStart, so a disabled mode here means the
	// config did not take effect rather than a code defect.
	if settings.Policy.CustomEndpointMode != "enabled" {
		c.logger.Warn("affine AI: custom endpoints are disabled on the server",
			"workspace", workspaceID, "mode", settings.Policy.CustomEndpointMode)
		return
	}

	existing := findBloudProfile(settings.Profiles)
	fingerprint := byokFingerprint(workspaceID, binding)
	ws := stored.Profiles[workspaceID]
	if existing != nil && ws.ProfileID == existing.ProfileID && ws.Fingerprint == fingerprint {
		return
	}

	if existing == nil {
		profileID, err := c.api.createByokProfile(ctx, workspaceID, byokCredential(binding), byokProfileName, byokDescription, binding)
		if err != nil {
			c.logger.Warn("affine AI: could not register the endpoint", "workspace", workspaceID, "err", err)
			return
		}
		stored.Profiles[workspaceID] = byokWorkspaceState{ProfileID: profileID, Fingerprint: fingerprint}
		c.logger.Info("registered affine AI endpoint", "workspace", workspaceID, "profile", profileID)
		return
	}

	// The definition is unchanged, so only the credential can differ. Rotating
	// leaves the definition and its revision untouched; a replace would needlessly
	// bump the revision. A missing state record also lands here, which converges
	// in one pass because the record is written below.
	if sameByokDefinition(existing, binding) {
		if err := c.api.rotateByokCredential(ctx, workspaceID, existing.ProfileID, byokCredential(binding), existing.Revision); err != nil {
			c.logger.Warn("affine AI: could not rotate the credential", "workspace", workspaceID, "err", err)
			return
		}
		stored.Profiles[workspaceID] = byokWorkspaceState{ProfileID: existing.ProfileID, Fingerprint: fingerprint}
		c.logger.Info("rotated the affine AI credential", "workspace", workspaceID, "profile", existing.ProfileID)
		return
	}

	if err := c.api.replaceByokProfile(ctx, workspaceID, existing.ProfileID, byokCredential(binding), byokProfileName, byokDescription, existing.Revision, binding); err != nil {
		c.logger.Warn("affine AI: could not update the endpoint", "workspace", workspaceID, "err", err)
		return
	}
	stored.Profiles[workspaceID] = byokWorkspaceState{ProfileID: existing.ProfileID, Fingerprint: fingerprint}
	c.logger.Info("updated affine AI endpoint", "workspace", workspaceID, "profile", existing.ProfileID)
}

// removeInferenceProvider deletes the profile Bloud owns when the app no longer
// declares a bound inference contract (the gateway was uninstalled or the
// setting cleared), so AFFiNE stops dialing an endpoint that is gone. It acts
// only on the profile id Bloud recorded, so a profile the operator registered by
// hand is never touched.
func (c *Configurator) removeInferenceProvider(ctx context.Context, state *configurator.AppState) {
	stored := c.readByokState(state)
	if len(stored.Profiles) == 0 || c.secrets == nil {
		return
	}
	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		c.logger.Warn("affine AI: cannot remove the endpoint: no owner password", "err", err)
		return
	}
	if err := c.api.signIn(ctx, c.adminEmail, password); err != nil {
		c.logger.Warn("affine AI: owner sign-in failed, removing nothing", "err", err)
		return
	}
	ids, err := c.api.listWorkspaces(ctx)
	if err != nil {
		return
	}
	for _, workspaceID := range ids {
		ws, ok := stored.Profiles[workspaceID]
		if !ok || ws.ProfileID == "" {
			continue
		}
		if err := c.api.deleteByokProfile(ctx, workspaceID, ws.ProfileID); err != nil {
			c.logger.Warn("affine AI: could not remove the endpoint", "workspace", workspaceID, "err", err)
			continue
		}
		c.logger.Info("removed affine AI endpoint", "workspace", workspaceID)
	}
	c.removeByokState(state)
}

// findBloudProfile returns the profile Bloud owns, or nil. Provider is checked
// alongside the name so an operator profile that happens to be named "Bloud"
// but points elsewhere is never adopted.
func findBloudProfile(profiles []byokProfile) *byokProfile {
	for i := range profiles {
		if profiles[i].Provider == byokProviderOpenAI && profiles[i].Name == byokProfileName {
			return &profiles[i]
		}
	}
	return nil
}

// sameByokDefinition reports whether AFFiNE's stored profile already has the
// endpoint, dialect, and model Bloud wants. A disabled profile counts as
// different so it is re-enabled.
func sameByokDefinition(p *byokProfile, b bindings.InferenceBinding) bool {
	if !p.Enabled || p.Definition.Endpoint.Kind != byokEndpointOpenAICompatible {
		return false
	}
	if p.Definition.Endpoint.URL == nil || *p.Definition.Endpoint.URL != b.Endpoint {
		return false
	}
	if p.Definition.Endpoint.Dialect == nil || *p.Definition.Endpoint.Dialect != byokDialectChatCompletions {
		return false
	}
	if len(p.Definition.Models) != 1 {
		return false
	}
	m := p.Definition.Models[0]
	return m.ModelID == b.DefaultModel && m.Enabled
}

// byokKeylessCredential fills the credential slot when the resolved upstream
// needs no key. AFFiNE's BYOK input requires a credential string and its
// OpenAI-compatible client sends it as a bearer, so an empty value risks a
// client-construction failure; a keyless gateway ignores the header, so the
// value is inert. An operator who sets a real key on the instance upstream gets
// that key instead (see byokCredential).
const byokKeylessCredential = "bloud"

// byokCredential is the credential Bloud stores in AFFiNE for this binding: the
// instance's real key when there is one, otherwise the keyless placeholder.
func byokCredential(b bindings.InferenceBinding) string {
	if b.APIKey.Published() {
		return b.APIKey.String()
	}
	return byokKeylessCredential
}

// byokFingerprint is a stable hash of everything Bloud controls about the
// profile except its identity. The credential is included because AFFiNE does
// not return it, so this hash is the only signal that a rotation is due; the
// full definition is included so a capability change (e.g. adding tool_calling)
// also re-writes the profile.
func byokFingerprint(workspaceID string, b bindings.InferenceBinding) string {
	def, _ := json.Marshal(byokDefinition(b))
	sum := sha256.Sum256([]byte(strings.Join([]string{
		workspaceID, byokProviderOpenAI, byokProfileName, b.Endpoint,
		byokDialectChatCompletions, b.DefaultModel, byokCredential(b), string(def),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (c *Configurator) byokStatePath(state *configurator.AppState) string {
	return filepath.Join(state.DataPath, byokStateFileName)
}

func (c *Configurator) readByokState(state *configurator.AppState) byokState {
	raw, err := os.ReadFile(c.byokStatePath(state))
	if err != nil {
		return byokState{}
	}
	var s byokState
	if err := json.Unmarshal(raw, &s); err != nil {
		return byokState{}
	}
	return s
}

func (c *Configurator) writeByokState(state *configurator.AppState, s byokState) {
	raw, err := json.Marshal(s)
	if err != nil {
		return
	}
	if _, err := managedfile.Write(c.byokStatePath(state), append(raw, '\n'), managedfile.ModeHostOnly); err != nil {
		c.logger.Warn("affine AI: could not persist reconciliation state", "err", err)
	}
}

func (c *Configurator) removeByokState(state *configurator.AppState) {
	if err := os.Remove(c.byokStatePath(state)); err != nil && !os.IsNotExist(err) {
		c.logger.Warn("affine AI: could not clear reconciliation state", "err", err)
	}
}
