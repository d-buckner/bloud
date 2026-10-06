// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestName(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	if got := c.Name(); got != "apps-hermes" {
		t.Fatalf("Name() = %q, want apps-hermes", got)
	}
}

func TestDefaultPort(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	if c.port != 9119 {
		t.Fatalf("default port = %d, want 9119", c.port)
	}
}

func TestAppExternalURL(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
	})
	if got := c.appExternalURL(); got != "http://hermes.localhost:8080" {
		t.Fatalf("appExternalURL() = %q, want http://hermes.localhost:8080", got)
	}
}

func TestAppExternalURLEmptyFallsBack(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	if got := c.appExternalURL(); got != "http://hermes.localhost:8080" {
		t.Fatalf("appExternalURL() with no base = %q, want fallback", got)
	}
}

// TestPreStartWritesOIDCConfig: SSO on merges the self-hosted provider keys
// + public_url into a fresh config.yaml and reports changed.
func TestPreStartWritesOIDCConfig(t *testing.T) {
	dir := t.TempDir()
	c := NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
	})
	state := &configurator.AppState{DataPath: dir, SSOEnabled: true, OIDC: &configurator.OIDCOutput{
		ClientID:  "hermes-client",
		IssuerURL: "http://sso.localhost:8080/application/o/hermes/",
	}}

	changed, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if !changed.RestartNeeded {
		t.Fatal("expected changed=true when writing SSO config to a fresh file")
	}

	raw, err := os.ReadFile(filepath.Join(dir, "data", "config.yaml"))
	if err != nil {
		t.Fatalf("reading written config: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("written config is not valid yaml: %v", err)
	}
	sh := nested(t, doc, "dashboard", "oauth", "self_hosted")
	if sh["issuer"] != state.OIDC.IssuerURL {
		t.Errorf("issuer = %v, want %v", sh["issuer"], state.OIDC.IssuerURL)
	}
	if sh["client_id"] != "hermes-client" {
		t.Errorf("client_id = %v, want hermes-client", sh["client_id"])
	}
	if sh["scopes"] != managedScopes {
		t.Errorf("scopes = %v, want %q", sh["scopes"], managedScopes)
	}
	dash := doc["dashboard"].(map[string]any)
	if dash["oauth"].(map[string]any)["provider"] != "self-hosted" {
		t.Errorf("provider = %v, want self-hosted", dash["oauth"].(map[string]any)["provider"])
	}
	if dash["public_url"] != "http://hermes.localhost:8080" {
		t.Errorf("public_url = %v, want http://hermes.localhost:8080", dash["public_url"])
	}
}

// TestPreStartIdempotent: a second pass over the file just written must find
// the SSO values already correct and report changed=false (no churn).
func TestPreStartIdempotent(t *testing.T) {
	dir := t.TempDir()
	c := NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
	})
	state := &configurator.AppState{DataPath: dir, SSOEnabled: true, OIDC: &configurator.OIDCOutput{
		ClientID:  "hermes-client",
		IssuerURL: "http://sso.localhost:8080/application/o/hermes/",
	}}
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("first PreStart: %v", err)
	}
	changed, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("second PreStart: %v", err)
	}
	if changed.RestartNeeded {
		t.Fatal("expected changed=false when SSO config already matches")
	}
}

// TestPreStartPreservesOtherKeys: the merge must leave Hermes' own settings
// untouched while adding the SSO keys.
func TestPreStartPreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	userCfg := "model:\n  default: gpt-test\nagent:\n  max_iterations: 42\n"
	cfgPath := filepath.Join(dir, "data", "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(userCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	state := &configurator.AppState{DataPath: dir, SSOEnabled: true, OIDC: &configurator.OIDCOutput{
		ClientID:  "hermes-client",
		IssuerURL: "http://sso.local/application/o/hermes/",
	}}
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	raw, _ := os.ReadFile(cfgPath)
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	if fmt.Sprint(nested(t, doc, "model")["default"]) != "gpt-test" {
		t.Errorf("user model setting lost: %v", doc["model"])
	}
	if fmt.Sprint(nested(t, doc, "agent")["max_iterations"]) != "42" {
		t.Errorf("user agent setting lost/changed: %v", doc["agent"])
	}
	if nested(t, doc, "dashboard", "oauth", "self_hosted")["client_id"] != "hermes-client" {
		t.Error("SSO keys not merged into an existing file")
	}
}

// TestPreStartSSOOffStripsOIDC: turning SSO off removes the managed keys so
// a stale provider can't gate the dashboard on a dead issuer.
func TestPreStartSSOOffStripsOIDC(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	seeded := "dashboard:\n  oauth:\n    provider: self-hosted\n    self_hosted:\n      issuer: http://dead/iss/\n      client_id: hermes-client\n  public_url: http://hermes.localhost:8080\nmodel:\n  default: keepme\n"
	cfgPath := filepath.Join(dir, "data", "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(seeded), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	state := &configurator.AppState{DataPath: dir, SSOEnabled: false}
	changed, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if !changed.RestartNeeded {
		t.Fatal("expected changed=true when stripping a present OIDC block")
	}
	raw, _ := os.ReadFile(cfgPath)
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	if dash, ok := doc["dashboard"].(map[string]any); ok {
		if _, ok := dash["public_url"]; ok {
			t.Error("public_url should have been stripped")
		}
		if _, ok := dash["oauth"]; ok {
			t.Errorf("oauth should have been stripped: %v", dash["oauth"])
		}
	}
	if fmt.Sprint(nested(t, doc, "model")["default"]) != "keepme" {
		t.Errorf("unrelated key not preserved: %v", doc["model"])
	}
}

// TestPreStartSSOOffNoWriteWhenNothingToDo: SSO off with no managed keys and
// no existing file must not create a config.yaml (which would block Hermes'
// own first-boot seeding) and must report changed=false.
func TestPreStartSSOOffNoWriteWhenNothingToDo(t *testing.T) {
	dir := t.TempDir()
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	changed, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: dir, SSOEnabled: false})
	if err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if changed.RestartNeeded {
		t.Fatal("expected changed=false with SSO off and no file")
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("config.yaml should not have been created (err=%v)", err)
	}
}

// TestPreStartRejectsCorruptConfig: a malformed existing file surfaces an
// error rather than silently replacing it.
func TestPreStartRejectsCorruptConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "data", "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("dashboard: [unclosed\n  bad: : :\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	_, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: dir, SSOEnabled: true,
		OIDC: &configurator.OIDCOutput{ClientID: "x", IssuerURL: "http://i"}})
	if err == nil {
		t.Fatal("expected a parse error for a corrupt config")
	}
}

// TestPostStartSucceedsWhenServing: health + a status advertising the
// gate-on self-hosted provider satisfies PostStart.
func TestPostStartSucceedsWhenServing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			w.WriteHeader(http.StatusOK)
		case "/api/status":
			_, _ = w.Write([]byte(`{"auth_required":true,"auth_providers":["self-hosted"]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	c.baseURL = srv.URL
	err := c.PostStart(context.Background(), &configurator.AppState{SSOEnabled: true,
		OIDC: &configurator.OIDCOutput{ClientID: "c", IssuerURL: "http://i"}})
	if err != nil {
		t.Fatalf("PostStart: %v", err)
	}
}

// TestPostStartFailsWhenProviderNotSelfHosted: a dashboard that came up on
// the password provider (or otherwise lacks the self-hosted provider) must
// not pass: Bloud's SSO config did not take effect.
func TestPostStartFailsWhenProviderNotSelfHosted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			w.WriteHeader(http.StatusOK)
		case "/api/status":
			_, _ = w.Write([]byte(`{"auth_required":true,"auth_providers":["basic"]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	c.baseURL = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.PostStart(ctx, &configurator.AppState{SSOEnabled: true,
		OIDC: &configurator.OIDCOutput{ClientID: "c", IssuerURL: "http://i"}}); err == nil {
		t.Fatal("expected PostStart to fail when the self-hosted provider is absent")
	}
}

// TestPostStartSkipsSSOCheckWhenSSOOff: with SSO disabled PostStart only
// needs health; it must not require the provider.
func TestPostStartSkipsSSOCheckWhenSSOOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Any status request would be unexpected for an SSO-off app; return
		// gate-off so a wrong code path is still harmless.
		_, _ = w.Write([]byte(`{"auth_required":false,"auth_providers":[]}`))
	}))
	defer srv.Close()

	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	c.baseURL = srv.URL
	if err := c.PostStart(context.Background(), &configurator.AppState{SSOEnabled: false}); err != nil {
		t.Fatalf("PostStart (SSO off): %v", err)
	}
}

// TestHasSelfHostedProvider covers the two spellings the plugin exposes.
func TestHasSelfHostedProvider(t *testing.T) {
	if !hasSelfHostedProvider([]string{"nous", "self-hosted"}) {
		t.Error("expected self-hosted detected")
	}
	if !hasSelfHostedProvider([]string{"self_hosted"}) {
		t.Error("expected self_hosted normalized and detected")
	}
	if hasSelfHostedProvider([]string{"basic", "nous"}) {
		t.Error("did not expect a match on non-self-hosted providers")
	}
}

// nested descends a map chain for assertions, failing the test on a break.
func nested(t *testing.T, doc map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := doc
	for _, k := range keys {
		m, ok := cur[k].(map[string]any)
		if !ok {
			t.Fatalf("key %q not a map (path %v); doc=%v", k, keys, doc)
		}
		cur = m
	}
	return cur
}

// inferenceState builds an AppState carrying one resolved inference binding,
// with SSO off so the tests read only the inference effect.
func inferenceState(dir string, b configurator.InferenceBinding) *configurator.AppState {
	st := &configurator.AppState{DataPath: dir}
	if b.Endpoint != "" {
		st.Integrations.Inference = []configurator.InferenceBinding{b}
	}
	return st
}

func TestPreStartWritesInferenceProvider(t *testing.T) {
	dir := t.TempDir()
	c := NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
	})
	state := inferenceState(dir, configurator.InferenceBinding{
		Endpoint:     "https://api.example.com/v1",
		APIKey:       "sk-test",
		DefaultModel: "gpt-4o-mini",
	})

	changed, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if !changed.RestartNeeded {
		t.Fatal("expected changed=true when writing the inference provider")
	}

	raw, err := os.ReadFile(filepath.Join(dir, "data", "config.yaml"))
	if err != nil {
		t.Fatalf("reading written config: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("written config is not valid yaml: %v", err)
	}

	p := nested(t, doc, "providers", inferenceProviderKey)
	if p["base_url"] != "https://api.example.com/v1" {
		t.Errorf("base_url = %v, want https://api.example.com/v1", p["base_url"])
	}
	if p["api_mode"] != inferenceAPIMode {
		t.Errorf("api_mode = %v, want %q", p["api_mode"], inferenceAPIMode)
	}
	if _, ok := p["api"]; ok {
		t.Error(`"api" must not be written: the named-custom resolver reads it as the endpoint URL, so "api: openai-completions" makes base_url come back as that literal string`)
	}
	if p["api_key"] != "sk-test" {
		t.Errorf("api_key = %v, want sk-test", p["api_key"])
	}
	if p["default_model"] != "gpt-4o-mini" {
		t.Errorf("default_model = %v, want gpt-4o-mini", p["default_model"])
	}
	if p["discover_models"] != true {
		t.Errorf("discover_models = %v, want true", p["discover_models"])
	}
	model := nested(t, doc, "model")
	if model["provider"] != inferenceProviderSlug || model["model"] != "gpt-4o-mini" {
		t.Errorf("model selection = %v/%v, want %v/gpt-4o-mini", model["provider"], model["model"], inferenceProviderSlug)
	}
}

// TestInferenceProviderSlugIsHowHermesSelectsANamedEntry pins the two facts the
// resolver was measured on: a named entry is addressed as custom:<key>, and the
// model slug carries no provider prefix. Bare `custom` reads OPENAI_BASE_URL /
// OPENAI_API_KEY from the environment and never consults `providers:`, so the
// wrong form resolves to Hermes' OpenRouter default with no key and the agent
// fails init with "No LLM provider configured".
func TestInferenceProviderSlugIsHowHermesSelectsANamedEntry(t *testing.T) {
	if inferenceProviderSlug != "custom:bloud" {
		t.Errorf("inferenceProviderSlug = %q, want custom:bloud", inferenceProviderSlug)
	}
	if inferenceProviderSlug == "custom" {
		t.Error("bare custom never resolves a named provider")
	}
}

// TestPreStartAdoptUnlessOverridden: an operator who has already chosen a
// model keeps it. Bloud registers the provider so it is switchable, but does
// not replace the selection.
func TestPreStartAdoptUnlessOverridden(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	operatorConfig := "model:\n  provider: openrouter\n  model: anthropic/claude-sonnet-4\n"
	if err := os.WriteFile(filepath.Join(dir, "data", "config.yaml"), []byte(operatorConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
	})
	state := inferenceState(dir, configurator.InferenceBinding{
		Endpoint:     "https://api.example.com/v1",
		DefaultModel: "gpt-4o-mini",
	})

	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "data", "config.yaml"))
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("reparse: %v", err)
	}
	model := nested(t, doc, "model")
	if model["provider"] != "openrouter" || model["model"] != "anthropic/claude-sonnet-4" {
		t.Errorf("operator model was overwritten: %v/%v", model["provider"], model["model"])
	}
	// The provider is still registered, so the operator can switch to it.
	if _, ok := nested(t, doc, "providers")[inferenceProviderKey]; !ok {
		t.Error("expected the bloud provider to stay registered")
	}
}

func TestPreStartInferenceIdempotent(t *testing.T) {
	dir := t.TempDir()
	c := NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
	})
	state := inferenceState(dir, configurator.InferenceBinding{
		Endpoint:     "https://api.example.com/v1",
		APIKey:       "sk-test",
		DefaultModel: "gpt-4o-mini",
	})
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("first PreStart: %v", err)
	}
	changed, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("second PreStart: %v", err)
	}
	if changed.RestartNeeded {
		t.Fatal("expected changed=false when the inference config already matches")
	}
}

func TestPreStartStripsInferenceWhenNoBinding(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "providers:\n  bloud:\n    base_url: https://old.example.com/v1\nmodel:\n  provider: custom:bloud\n  model: old-model\n"
	if err := os.WriteFile(filepath.Join(dir, "data", "config.yaml"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
	})

	changed, err := c.PreStart(context.Background(), inferenceState(dir, configurator.InferenceBinding{}))
	if err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if !changed.RestartNeeded {
		t.Fatal("expected changed=true when stripping a stale provider")
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "data", "config.yaml"))
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if _, ok := doc["providers"]; ok {
		t.Errorf("expected providers removed, got %v", doc["providers"])
	}
	if _, ok := doc["model"]; ok {
		t.Errorf("expected the bloud model selection removed, got %v", doc["model"])
	}
}

// TestStripInferenceKeepsOperatorModel: stripping Bloud's provider must not
// touch a model selection that points somewhere else.
func TestStripInferenceKeepsOperatorModel(t *testing.T) {
	doc := map[string]any{
		"providers": map[string]any{inferenceProviderKey: map[string]any{"base_url": "https://x/v1"}},
		"model":     map[string]any{"provider": "openrouter", "model": "anthropic/claude-sonnet-4"},
	}
	stripInference(doc)

	if _, ok := doc["providers"]; ok {
		t.Error("expected the bloud provider removed")
	}
	model := doc["model"].(map[string]any)
	if model["provider"] != "openrouter" || model["model"] != "anthropic/claude-sonnet-4" {
		t.Errorf("operator model was disturbed: %v", model)
	}
}

// TestPreStartNoAPIKeyWritesNoCredential: a keyless endpoint (Ollama behind a
// gateway, say) must not leave an empty api_key that Hermes would send as a
// blank bearer token.
func TestPreStartNoAPIKeyWritesNoCredential(t *testing.T) {
	dir := t.TempDir()
	c := NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
	})
	state := inferenceState(dir, configurator.InferenceBinding{
		Endpoint:     "http://127.0.0.1:8899/v1",
		DefaultModel: "some-local-model",
	})
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "data", "config.yaml"))
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("reparse: %v", err)
	}
	p := nested(t, doc, "providers", inferenceProviderKey)
	if _, ok := p["api_key"]; ok {
		t.Errorf("expected no api_key for a keyless binding, got %v", p["api_key"])
	}
}

// ---- the data-directory permission contract (issue #136) ----

// TestMetadata_DeclaresTheDataDirPermissionContract: Hermes secures
// $HERMES_HOME to 0700 for a bare-metal install, and the host agent cannot
// undo that from outside, because the directory belongs to the container's uid
// mapped into the rootless podman subuid range (chmod is EPERM). These two
// env vars are the only thing between a second reconciliation pass and a node
// parked in error forever, so they are asserted here rather than trusted to a
// comment beside them.
func TestMetadata_DeclaresTheDataDirPermissionContract(t *testing.T) {
	raw, err := os.ReadFile("metadata.yaml")
	if err != nil {
		t.Fatalf("reading metadata.yaml: %v", err)
	}
	var m struct {
		Containers []struct {
			Name        string            `yaml:"name"`
			Environment map[string]string `yaml:"environment"`
		} `yaml:"containers"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parsing metadata.yaml: %v", err)
	}
	if len(m.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(m.Containers))
	}
	env := m.Containers[0].Environment
	if got := env["HERMES_CONTAINER"]; got != "1" {
		t.Errorf("HERMES_CONTAINER = %q, want \"1\": Hermes' container probe matches docker/lxc/kubepods but not podman, so without it Hermes hardens $HERMES_HOME as if it were bare metal", got)
	}
	if got := env["HERMES_HOME_MODE"]; got != "0777" {
		t.Errorf("HERMES_HOME_MODE = %q, want \"0777\": the host agent is neither owner nor group member of the container's mapped subuid, so only the world-write bit lets managedfile.Write create the temp file it renames into place", got)
	}
}

// TestMetadata_ScopeSetAndLifetimeMatchTheConfigurator: the scope set the
// configurator writes into Hermes' config.yaml and the scope set metadata.yaml
// asks the host-agent to attach to the Authentik provider are two halves of one
// contract. A scope the app requests but the provider does not carry is dropped
// by the IdP in silence, and the refresh token never arrives; a scope the
// provider carries that the app never asks for is a declaration that does
// nothing. Both fail here rather than in someone's login loop.
func TestMetadata_ScopeSetAndLifetimeMatchTheConfigurator(t *testing.T) {
	raw, err := os.ReadFile("metadata.yaml")
	if err != nil {
		t.Fatalf("reading metadata.yaml: %v", err)
	}
	var m struct {
		SSO struct {
			Strategy           string   `yaml:"strategy"`
			Scopes             []string `yaml:"scopes"`
			AccessTokenMinutes int      `yaml:"accessTokenMinutes"`
		} `yaml:"sso"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parsing metadata.yaml: %v", err)
	}
	if m.SSO.Strategy != "native-oidc" {
		t.Fatalf("sso.strategy = %q, want native-oidc: scopes and accessTokenMinutes are only valid for that strategy", m.SSO.Strategy)
	}

	requested := strings.Fields(managedScopes)
	for _, scope := range m.SSO.Scopes {
		if !strings.Contains(strings.Join(requested, " "), scope) {
			t.Errorf("sso.scopes asks the provider for %q, which managedScopes never requests", scope)
		}
	}
	for _, scope := range requested {
		if scope == "openid" || scope == "profile" || scope == "email" {
			continue // carried by every native-oidc provider, no declaration needed
		}
		found := false
		for _, declared := range m.SSO.Scopes {
			if declared == scope {
				found = true
			}
		}
		if !found {
			t.Errorf("managedScopes requests %q but sso.scopes does not declare it: the provider would not carry it", scope)
		}
	}

	// The lifetime is the other half of the symptom. Bloud's native-oidc
	// default is 5 minutes, and the dashboard session tracks it, so anything
	// at or under that is the bug we are fixing.
	if m.SSO.AccessTokenMinutes <= 5 {
		t.Errorf("sso.accessTokenMinutes = %d, want well beyond the 5 minute Bloud default: the dashboard session lifetime follows the access token's exp", m.SSO.AccessTokenMinutes)
	}
	if want := 90 * 24 * 60; m.SSO.AccessTokenMinutes != want {
		t.Errorf("sso.accessTokenMinutes = %d, want %d (90 days)", m.SSO.AccessTokenMinutes, want)
	}
}

func TestPermissionHint_NamesTheContractOnEPERM(t *testing.T) {
	perm := &fs.PathError{Op: "open", Path: "/opt/data/config.yaml", Err: fs.ErrPermission}
	hint := permissionHint(fmt.Errorf("writing %s: %w", "/opt/data/config.yaml", perm))
	if !strings.Contains(hint, "HERMES_HOME_MODE") || !strings.Contains(hint, "HERMES_CONTAINER") {
		t.Errorf("hint = %q, want it to name the env contract where the fix lives", hint)
	}
	if got := permissionHint(errors.New("boom")); got != "" {
		t.Errorf("a non-permission error must carry no hint, got %q", got)
	}
}

// TestPreStart_UnwritableDataDirReportsTheContract reproduces the failure in
// the issue: the config file itself is still readable, but the directory no
// longer accepts a new file, so the temp file managedfile.Write renames in
// cannot be created. The error has to say what to change, not just "permission
// denied". Skipped as root, where a directory mode denies nothing.
func TestPreStart_UnwritableDataDirReportsTheContract(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory modes do not deny the write")
	}
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "config.yaml"), []byte("model:\n  provider: other\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Readable and traversable, but no new file can be created: the shape a
	// container-owned directory leaves the host agent in.
	if err := os.Chmod(dataDir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dataDir, 0o755) })

	c := NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
	})
	state := &configurator.AppState{DataPath: dir, SSOEnabled: true, OIDC: &configurator.OIDCOutput{
		ClientID:  "hermes-client",
		IssuerURL: "http://sso.localhost:8080/application/o/hermes/",
	}}

	if _, err := c.PreStart(context.Background(), state); err == nil {
		t.Fatal("expected the write to fail against a non-writable data directory")
	} else if !strings.Contains(err.Error(), "HERMES_HOME_MODE") {
		t.Errorf("error = %v, want it to name the fix", err)
	}
}

// ---- reading a config file the container took ownership of (issue #136) ----

// recordingExec is the Deps.Exec test double: it records the container and
// argv it was asked to run and replays a canned result.
type recordingExec struct {
	calls []string
	out   string
	err   error
}

func (r *recordingExec) fn(_ context.Context, container string, _ map[string]string, cmd []string) ([]byte, error) {
	r.calls = append(r.calls, container+" \x00 "+strings.Join(cmd, " "))
	if r.err != nil {
		return nil, r.err
	}
	return []byte(r.out), nil
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// unreadableByHost makes cfgPath unreadable by the running test user. The real
// install reaches the same fs.ErrPermission from the other direction: the file
// is 0640 owned by the container's mapped subuid, so the agent is neither
// owner nor group member. A non-root test cannot chown to a foreign uid, so
// the mode is tightened all the way instead. What the code under test sees is
// identical: a host read that returns EACCES.
func unreadableByHost(t *testing.T, cfgPath string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: no file mode denies the read")
	}
	if err := os.Chmod(cfgPath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgPath, 0o644) })
	if _, err := os.ReadFile(cfgPath); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("setup: expected the host read to be refused, got %v", err)
	}
}

// TestReadConfig_FallsBackToTheContainerWhenTheHostCannotRead is the core of
// the issue: Hermes' stage2-hook chowns config.yaml to its runtime user at
// 0640 on every boot, so the agent cannot read the file it wrote. The read
// has to come back through the container, addressed by the in-container path.
func TestReadConfig_FallsBackToTheContainerWhenTheHostCannotRead(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o777); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dataDir, configFileName)
	if err := os.WriteFile(cfgPath, []byte("operator: kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadableByHost(t, cfgPath)

	fx := &recordingExec{out: b64("operator: kept\nmodel:\n  default: from-container\n") + "\n"}
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger(), Exec: fx.fn})

	raw, err := c.readConfig(context.Background(), cfgPath)
	if err != nil {
		t.Fatalf("readConfig: %v", err)
	}
	if string(raw) != "operator: kept\nmodel:\n  default: from-container\n" {
		t.Errorf("read %q, want the decoded container bytes", raw)
	}
	// The argv must name the in-container path, not the host path: the host
	// path does not exist inside the container, so a drift here reads nothing.
	want := nodeName + " \x00 base64 " + containerHome + "/" + configFileName
	if len(fx.calls) != 1 || fx.calls[0] != want {
		t.Errorf("exec calls = %q, want exactly [%q]", fx.calls, want)
	}
}

// TestReadConfig_UsesTheHostReadWhenItIsPermitted: the fallback is a fallback,
// not the default. A readable file must not shell into the container, or every
// steady-state reconciliation cycle would pay an exec.
func TestReadConfig_UsesTheHostReadWhenItIsPermitted(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, configFileName)
	if err := os.WriteFile(cfgPath, []byte("host: readable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fx := &recordingExec{out: b64("from-container: wrong\n")}
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger(), Exec: fx.fn})

	raw, err := c.readConfig(context.Background(), cfgPath)
	if err != nil {
		t.Fatalf("readConfig: %v", err)
	}
	if string(raw) != "host: readable\n" {
		t.Errorf("read %q, want the host bytes", raw)
	}
	if len(fx.calls) != 0 {
		t.Errorf("expected no exec for a readable file, got %q", fx.calls)
	}
}

// TestReadConfig_MissingFileIsNotAFallback: ENOENT is a first-run state, not a
// permission problem. Falling back here would turn "no config yet" into a
// container exec, and a container that seeds its own file would then be
// overwritten by whatever the exec returned.
func TestReadConfig_MissingFileIsNotAFallback(t *testing.T) {
	fx := &recordingExec{out: b64("should: not\n")}
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger(), Exec: fx.fn})

	_, err := c.readConfig(context.Background(), filepath.Join(t.TempDir(), "absent.yaml"))
	if !os.IsNotExist(err) {
		t.Errorf("err = %v, want the not-exist error preserved", err)
	}
	if len(fx.calls) != 0 {
		t.Errorf("expected no exec for a missing file, got %q", fx.calls)
	}
}

// TestReadConfig_ContaminatedStreamIsAnError: Deps.Exec merges stderr into the
// output, so a podman warning can arrive glued to the payload. Base64 makes
// that loud. Silently accepting it would parse as garbage and get written back
// over the operator's real config.
func TestReadConfig_ContaminatedStreamIsAnError(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, configFileName)
	if err := os.WriteFile(cfgPath, []byte("real: content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadableByHost(t, cfgPath)

	fx := &recordingExec{out: "time=... level=warning msg=podman complained\n" + b64("real: content\n")}
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger(), Exec: fx.fn})

	if _, err := c.readConfig(context.Background(), cfgPath); err == nil {
		t.Fatal("expected a decode error for a stream with non-base64 noise in it")
	}
}

// TestReadConfig_NoRuntimeNamesTheCause: with no Exec wired (CLI, tests) a
// denied host read has no remedy, and the message has to say why rather than
// leaving "permission denied" to be read as a Bloud misconfiguration.
func TestReadConfig_NoRuntimeNamesTheCause(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, configFileName)
	if err := os.WriteFile(cfgPath, []byte("x: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadableByHost(t, cfgPath)

	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	_, err := c.readConfig(context.Background(), cfgPath)
	if err == nil {
		t.Fatal("expected an error with no Exec available")
	}
	if !strings.Contains(err.Error(), "INTEGRATION.md") || !strings.Contains(err.Error(), "0640") {
		t.Errorf("err = %v, want it to name the ownership mechanism and where it is documented", err)
	}
}

// TestPreStart_MergesOverAConfigOnlyTheContainerCanRead is the issue scenario
// end to end: the container owns config.yaml at a mode the host cannot read,
// the operator's own keys live in it, and Bloud still has to merge its SSO
// keys without losing them. Before the fallback this failed the pass and parked
// the node in error permanently.
func TestPreStart_MergesOverAConfigOnlyTheContainerCanRead(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o777); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dataDir, configFileName)
	operatorCfg := "model:\n  provider: openrouter\n  model: anthropic/claude-sonnet-4\nagent:\n  max_iterations: 42\n"
	if err := os.WriteFile(cfgPath, []byte(operatorCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadableByHost(t, cfgPath)

	fx := &recordingExec{out: b64(operatorCfg) + "\n"}
	c := NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
		Exec:           fx.fn,
	})
	state := &configurator.AppState{DataPath: dir, SSOEnabled: true, OIDC: &configurator.OIDCOutput{
		ClientID:  "hermes-client",
		IssuerURL: "http://sso.localhost:8080/application/o/hermes/",
	}}

	changed, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if !changed.RestartNeeded {
		t.Fatal("expected changed=true: the SSO block was missing from the operator's config")
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reading the rewritten config: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("rewritten config is not valid yaml: %v", err)
	}
	if nested(t, doc, "dashboard", "oauth", "self_hosted")["client_id"] != "hermes-client" {
		t.Error("SSO keys were not merged")
	}
	if fmt.Sprint(nested(t, doc, "model")["provider"]) != "openrouter" {
		t.Errorf("operator model lost: %v", doc["model"])
	}
	if fmt.Sprint(nested(t, doc, "agent")["max_iterations"]) != "42" {
		t.Errorf("operator agent setting lost: %v", doc["agent"])
	}
	// The write landed at the shared-config mode, so the container can read it
	// back on its next boot (its own hook re-tightens it to 0640 then, which
	// is what the fallback exists for).
	if info, err := os.Stat(cfgPath); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("config mode = %v (err %v), want 0644", info.Mode().Perm(), err)
	}
}

// TestPreStart_NoChurnWhenTheContainerCopyAlreadyMatches: the merge is
// computed against the bytes read through the container, so a steady state
// must still report changed=false. If the fallback content were ignored this
// would rewrite the file every cycle and restart the app forever.
func TestPreStart_NoChurnWhenTheContainerCopyAlreadyMatches(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o777); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dataDir, configFileName)
	state := &configurator.AppState{DataPath: dir, SSOEnabled: true, OIDC: &configurator.OIDCOutput{
		ClientID:  "hermes-client",
		IssuerURL: "http://sso.localhost:8080/application/o/hermes/",
	}}
	c := NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
	})
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("seed PreStart: %v", err)
	}
	current, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	unreadableByHost(t, cfgPath)

	fx := &recordingExec{out: b64(string(current)) + "\n"}
	c2 := NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
		Exec:           fx.fn,
	})
	changed, err := c2.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if changed.RestartNeeded {
		t.Fatal("expected changed=false when the container's copy already carries the SSO block")
	}
	if len(fx.calls) != 1 {
		t.Errorf("expected exactly one container read, got %d", len(fx.calls))
	}
}

// runningProbe is the Deps.ContainerRunning test double: a fixed answer for
// the container readConfig asks about.
type runningProbe struct{ running bool }

func (p runningProbe) fn(_ context.Context, _ string) (bool, error) { return p.running, nil }

// TestReadConfig_StoppedContainerIsSkippable pins issue #184: when the host
// read is refused and the container that owns the file is stopped, the read
// cannot be done this pass, but that is the cold-start state after a reboot
// rather than a fault. The sentinel lets PreStart skip the merge instead of
// parking the node in ERROR.
func TestReadConfig_StoppedContainerIsSkippable(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o777); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dataDir, configFileName)
	if err := os.WriteFile(cfgPath, []byte("operator: kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadableByHost(t, cfgPath)

	fx := &recordingExec{err: errors.New("can only create exec sessions on running containers: container state improper")}
	c := NewConfigurator(0, configurator.Deps{
		Logger:           quietLogger(),
		Exec:             fx.fn,
		ContainerRunning: runningProbe{running: false}.fn,
	})

	_, err := c.readConfig(context.Background(), cfgPath)
	if !errors.Is(err, errContainerNotRunning) {
		t.Fatalf("err = %v, want the skippable sentinel", err)
	}
}

// TestReadConfig_RunningContainerExecFailureIsHard: the skip is only for a
// stopped container. A running container whose read still fails is the fault
// the original error described, and it must still fail the pass.
func TestReadConfig_RunningContainerExecFailureIsHard(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o777); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dataDir, configFileName)
	if err := os.WriteFile(cfgPath, []byte("operator: kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadableByHost(t, cfgPath)

	fx := &recordingExec{err: errors.New("base64: not found")}
	c := NewConfigurator(0, configurator.Deps{
		Logger:           quietLogger(),
		Exec:             fx.fn,
		ContainerRunning: runningProbe{running: true}.fn,
	})

	_, err := c.readConfig(context.Background(), cfgPath)
	if err == nil {
		t.Fatal("expected a hard error when the container is running")
	}
	if errors.Is(err, errContainerNotRunning) {
		t.Fatalf("err = %v, must not be the skippable sentinel", err)
	}
}

// TestPreStart_SkipsMergeWhenContainerStopped is the cold-start scenario end
// to end: the file is left exactly as it is (no rewrite, no error), so Hermes
// boots on the config it already had instead of the node going to ERROR.
func TestPreStart_SkipsMergeWhenContainerStopped(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o777); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dataDir, configFileName)
	operatorCfg := "model:\n  provider: openrouter\n"
	if err := os.WriteFile(cfgPath, []byte(operatorCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadableByHost(t, cfgPath)

	fx := &recordingExec{err: errors.New("can only create exec sessions on running containers: container state improper")}
	c := NewConfigurator(0, configurator.Deps{
		Logger:           quietLogger(),
		PrimaryBaseURL:   func() string { return "http://localhost:8080" },
		Exec:             fx.fn,
		ContainerRunning: runningProbe{running: false}.fn,
	})
	state := &configurator.AppState{DataPath: dir, SSOEnabled: true, OIDC: &configurator.OIDCOutput{
		ClientID:  "hermes-client",
		IssuerURL: "http://sso.localhost:8080/application/o/hermes/",
	}}

	res, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if res.RestartNeeded {
		t.Fatal("expected no restart: nothing was written")
	}
	if err := os.Chmod(cfgPath, 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != operatorCfg {
		t.Errorf("config = %q, want it left alone", raw)
	}
}

// TestPreStart_WritesWhenTheConfigIsMissingEvenIfContainerStopped: a missing
// file is the first-run state, not a permission problem, so the cold-start
// skip must not apply. ENOENT never reaches the container probe.
func TestPreStart_WritesWhenTheConfigIsMissingEvenIfContainerStopped(t *testing.T) {
	dir := t.TempDir()
	fx := &recordingExec{out: b64("should: not be read\n")}
	c := NewConfigurator(0, configurator.Deps{
		Logger:           quietLogger(),
		PrimaryBaseURL:   func() string { return "http://localhost:8080" },
		Exec:             fx.fn,
		ContainerRunning: runningProbe{running: false}.fn,
	})
	state := &configurator.AppState{DataPath: dir, SSOEnabled: true, OIDC: &configurator.OIDCOutput{
		ClientID:  "hermes-client",
		IssuerURL: "http://sso.localhost:8080/application/o/hermes/",
	}}

	res, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if !res.RestartNeeded {
		t.Fatal("expected the fresh SSO config to be written and the container recreated")
	}
	if len(fx.calls) != 0 {
		t.Errorf("expected no container read for a missing file, got %q", fx.calls)
	}
}

var _ configurator.NodeLifecycle = (*Configurator)(nil)
