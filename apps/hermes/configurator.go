// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const (
	// appName is the catalog ID (and secrets/registry key) for Hermes.
	appName = "hermes"
	// defaultPort is the Hermes dashboard port (upstream default).
	defaultPort = 9119
	// configFileName is Hermes' own config file, at $HERMES_HOME/config.yaml.
	// $HERMES_HOME is /opt/data, mounted from {{appDataDir}}/data, so on the
	// host this is <DataPath>/data/config.yaml.
	configFileName = "config.yaml"
	// managedScopes is the OIDC scope set written into the self-hosted
	// provider block (matches the Hermes default; stated explicitly so the
	// managed block is unambiguous).
	managedScopes = "openid profile email"
)

// Configurator handles the Hermes node lifecycle. Hermes owns most of
// $HERMES_HOME (it seeds config, memory, skills, the session store on first
// boot), so the configurator does not manage the whole file: it merges the
// Bloud-owned SSO keys into Hermes' config.yaml before the container starts
// (PreStart) and verifies the dashboard boots with the self-hosted OIDC
// provider active afterwards (PostStart).
//
// The OIDC issuer/client_id are per-install values, and the container spec
// cannot render them (only {{dataDir}}/{{appDataDir}}/static TemplateVars
// are available there), so they are delivered through the config file rather
// than container env.
type Configurator struct {
	port       int
	ssoBaseURL func() string // current Bloud base URL (host-set aware; read each PreStart)
	logger     *slog.Logger
	api        *hermesAPI

	// baseURL is a test seam: when set, the API client resolves to it
	// instead of localhost:port.
	baseURL string
}

// NewConfigurator creates a new Hermes configurator from the host Deps.
// deps.PrimaryBaseURL supplies the current Bloud base URL (e.g.
// "http://localhost:8080"); the dashboard's public URL is derived from it
// the same way routes and OIDC redirect URIs are (hermes.<host>). It is a
// function so host changes made in the UI take effect on the next pass.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:       port,
		ssoBaseURL: deps.PrimaryBaseURL,
		logger:     logger.With("app", "hermes"),
	}
	c.api = newAPI(deps.HTTP, func() string {
		if c.baseURL != "" {
			return c.baseURL
		}
		return fmt.Sprintf("http://localhost:%d", c.port)
	})
	return c
}

// Name returns the node name this configurator manages.
// nodeName is the graph node and container name the host-agent reconciles
// this configurator under. Registration and Name() both read it, so the two
// cannot drift apart.
const nodeName = "apps-hermes"

func (c *Configurator) Name() string {
	return nodeName
}

// appExternalURL returns the public URL the browser uses to reach Hermes,
// e.g. "http://hermes.localhost:8080". The dashboard derives its OIDC
// callback (<public_url>/auth/callback) from this, so it must match the
// redirect URI the host-agent registered with Authentik.
func (c *Configurator) appExternalURL() string {
	baseURL := ""
	if c.ssoBaseURL != nil {
		baseURL = c.ssoBaseURL()
	}
	if baseURL == "" {
		return "http://hermes.localhost:8080"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" {
		return "http://hermes.localhost:8080"
	}
	parsed.Host = appName + "." + parsed.Host
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return strings.TrimSuffix(parsed.String(), "/")
}

// PreStart merges Bloud's SSO keys into Hermes' config.yaml so the
// dashboard boots with the self-hosted OIDC provider configured. Returns
// changed=true only when the managed keys differ from what is on disk, which
// makes the orchestrator (re)create the container so Hermes re-reads it.
//
// The merge is whole-file and semantically compared: Hermes' other settings
// are preserved untouched, and a file that already carries the right SSO
// values produces no write (no churn across reconciliation cycles). When
// SSO is disabled the managed keys are stripped instead, so a leftover
// provider never points at a dead issuer.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	cfgPath := filepath.Join(state.DataPath, "data", configFileName)

	existing, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return configurator.NoRestart(), fmt.Errorf("reading %s: %w", cfgPath, err)
	}

	// The managed edit is measured against the parsed document, not the raw
	// bytes: a re-parse-and-re-marshal of an unchanged file yields the same
	// document, so only a real SSO change reports changed=true.
	doc, err := parseConfig(existing)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("parsing %s: %w", cfgPath, err)
	}
	base, err := yaml.Marshal(doc)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("serializing %s: %w", cfgPath, err)
	}

	ssoActive := state != nil && state.SSOEnabled && state.OIDC != nil
	if ssoActive {
		applyOIDC(doc, state.OIDC, c.appExternalURL())
	} else {
		stripOIDC(doc)
	}

	binding, hasInference := inferenceBinding(state)
	if hasInference {
		applyInference(doc, binding)
	} else {
		stripInference(doc)
	}

	want, err := yaml.Marshal(doc)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("serializing %s: %w", cfgPath, err)
	}
	if bytes.Equal(base, want) {
		return configurator.NoRestart(), nil
	}

	changed, err := managedfile.Write(cfgPath, want, managedfile.ModeSharedConfig)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing %s: %w", cfgPath, err)
	}
	if changed {
		c.logger.Info("updated Hermes config", "path", cfgPath, "sso", ssoActive, "inference", hasInference)
	}
	return configurator.RestartIf(changed, "Hermes config rewritten"), nil
}

// PostStart waits for the dashboard to serve and then, when SSO is
// configured, verifies the dashboard came up with the self-hosted OIDC
// provider registered (not the bundled password provider, and not an
// accidental loopback bind with the gate off). A green pass means the Bloud
// OIDC integration is the thing standing in front of this dashboard.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if err := c.api.waitDashboard(ctx); err != nil {
		return fmt.Errorf("waiting for hermes dashboard: %w", err)
	}
	if state == nil || !state.SSOEnabled || state.OIDC == nil {
		return nil
	}
	if err := c.api.waitSelfHostedProvider(ctx); err != nil {
		return fmt.Errorf("verifying self-hosted OIDC provider: %w", err)
	}
	c.logger.Info("Hermes dashboard serving under Bloud SSO", "issuer", state.OIDC.IssuerURL)
	return nil
}

// parseConfig decodes a Hermes config.yaml into a generic map. A missing or
// empty file is an empty document (Hermes fills defaults at runtime).
func parseConfig(raw []byte) (map[string]any, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return doc, nil
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

// applyOIDC sets the Bloud-managed self-hosted OIDC keys, creating the
// intermediate maps as needed without disturbing any other keys.
func applyOIDC(doc map[string]any, oidc *configurator.OIDCOutput, publicURL string) {
	dash := mapAt(doc, "dashboard")
	oauth := mapAt(dash, "oauth")
	oauth["provider"] = "self-hosted"
	sh := mapAt(oauth, "self_hosted")
	sh["issuer"] = oidc.IssuerURL
	sh["client_id"] = oidc.ClientID
	sh["scopes"] = managedScopes
	dash["public_url"] = publicURL
}

// stripOIDC removes the Bloud-managed SSO keys when SSO is off. A leftover
// provider would otherwise make the gated dashboard depend on a dead issuer;
// with it gone the operator can re-enable SSO (which re-adds the block) or
// run a loopback-only dashboard.
func stripOIDC(doc map[string]any) {
	dash, ok := doc["dashboard"].(map[string]any)
	if !ok {
		return
	}
	delete(dash, "public_url")
	if oauth, ok := dash["oauth"].(map[string]any); ok {
		delete(oauth, "self_hosted")
		delete(oauth, "provider")
		if len(oauth) == 0 {
			delete(dash, "oauth")
		}
	}
	if len(dash) == 0 {
		delete(doc, "dashboard")
	}
}

// inferenceProviderKey is the named provider entry Bloud registers in Hermes'
// config.yaml. Hermes selects a named provider as custom:<key>, so the operator
// can see and switch away from it in the dashboard rather than inheriting an
// invisible default.
const inferenceProviderKey = "bloud"

// inferenceBinding pulls the resolved inference binding out of the app state.
// A consumer with no binding has nothing to point at, which is different from a
// binding with an empty endpoint: the latter is a misconfiguration the resolver
// would not have produced.
func inferenceBinding(state *configurator.AppState) (configurator.InferenceBinding, bool) {
	if state == nil || len(state.Integrations.Inference) == 0 {
		return configurator.InferenceBinding{}, false
	}
	b := state.Integrations.Inference[0]
	if b.Endpoint == "" {
		return configurator.InferenceBinding{}, false
	}
	return b, true
}

// applyInference registers the Bloud provider block and adopts the instance
// default model only where Hermes has not chosen one.
//
// The provider block is always written when a binding exists, even if the
// operator has pointed Hermes somewhere else: keeping it registered means the
// operator can switch back in the dashboard without re-entering an endpoint.
// The model selection is the part that respects an existing choice, because
// silently replacing a model an operator picked is the failure mode that makes
// a managed default unwelcome.
func applyInference(doc map[string]any, b configurator.InferenceBinding) {
	providers := mapAt(doc, "providers")
	p := mapAt(providers, inferenceProviderKey)
	p["name"] = "Bloud"
	p["api"] = "openai-completions"
	p["base_url"] = b.Endpoint
	if b.APIKey != "" {
		p["api_key"] = b.APIKey
	} else {
		delete(p, "api_key")
	}
	if b.DefaultModel != "" {
		p["default_model"] = b.DefaultModel
	}
	// Ask Hermes to keep the model list current from the endpoint rather than
	// trusting a snapshot, so a model added upstream is reachable without a
	// Bloud change.
	p["discover_models"] = true

	adoptDefaultModel(doc, b.DefaultModel)
}

// adoptDefaultModel sets Hermes' active model to the Bloud default only when
// Hermes has no model selection of its own. An existing model.provider is left
// exactly as the operator set it.
func adoptDefaultModel(doc map[string]any, defaultModel string) {
	if defaultModel == "" {
		return
	}
	model := mapAt(doc, "model")
	if existing, ok := model["provider"]; ok && existing != nil && existing != "" {
		return
	}
	model["provider"] = "custom"
	model["model"] = inferenceProviderKey + "/" + defaultModel
}

// stripInference removes the Bloud-managed provider block, and the model
// selection only when that selection is the one Bloud wrote. An operator-chosen
// model pointing somewhere else is left alone, so removing Bloud's inference
// cannot break a configuration the operator built by hand.
func stripInference(doc map[string]any) {
	providers, ok := doc["providers"].(map[string]any)
	if ok {
		delete(providers, inferenceProviderKey)
		if len(providers) == 0 {
			delete(doc, "providers")
		}
	}

	model, ok := doc["model"].(map[string]any)
	if !ok {
		return
	}
	active, _ := model["model"].(string)
	provider, _ := model["provider"].(string)
	if provider != "custom" || !strings.HasPrefix(active, inferenceProviderKey+"/") {
		return
	}
	delete(model, "model")
	delete(model, "provider")
	if len(model) == 0 {
		delete(doc, "model")
	}
}

// mapAt returns doc[key] as a mutable map[string]any, replacing any
// non-map value (or absent key) with a fresh map.
func mapAt(doc map[string]any, key string) map[string]any {
	if m, ok := doc[key].(map[string]any); ok {
		return m
	}
	m := map[string]any{}
	doc[key] = m
	return m
}
