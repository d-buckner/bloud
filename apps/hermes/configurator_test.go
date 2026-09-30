// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if model["provider"] != "custom" || model["model"] != "bloud/gpt-4o-mini" {
		t.Errorf("model selection = %v/%v, want custom/bloud/gpt-4o-mini", model["provider"], model["model"])
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
	seed := "providers:\n  bloud:\n    base_url: https://old.example.com/v1\nmodel:\n  provider: custom\n  model: bloud/old-model\n"
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
		Endpoint:     "http://ollama.localhost:8080/v1",
		DefaultModel: "llama3.1",
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

var _ configurator.NodeLifecycle = (*Configurator)(nil)
