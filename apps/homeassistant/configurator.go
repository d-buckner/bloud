// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appasset"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const appName = "homeassistant"

const (
	// bootstrapUsername is the HA owner account created by headless onboarding.
	// Unlike Jellyfin's bootstrap admin, an HA owner cannot be deleted — this is
	// the documented break-glass account. Its password lives in secrets.json and
	// is never shared; end users authenticate via SSO.
	bootstrapUsername = "bloud-bootstrap-admin"
	bootstrapFullname = "Bloud Bootstrap Admin"

	// onboardingClientID is one of Home Assistant's built-in OAuth2 clients.
	// The onboarding users endpoint requires a registered client id; a built-in
	// avoids a chicken-and-egg client-registration step.
	onboardingClientID = "https://home-assistant.io/iOS"

	// componentDomain is the hass-oidc-auth integration domain (manifest.json).
	componentDomain = "auth_oidc"
)

// Pinned hass-oidc-auth release. INTEGRATION.md "Verified constants" records
// the sha256 provenance. Fetched at PreStart; never vendored.
const (
	oidcComponentVersion = "v1.2.1"
	oidcComponentURL     = "https://github.com/christiaangoossens/hass-oidc-auth/releases/download/v1.2.1/hass-oidc-auth.zip"
	oidcComponentSHA256  = "e5badaaacaa63cfd6fe733924a05e76d75058836190398598fb24de57cd47ccd"
)

// Marker comments delimiting Bloud's block inside the user-owned
// configuration.yaml.
const (
	managedBegin = "# BEGIN bloud managed auth_oidc"
	managedEnd   = "# END bloud managed auth_oidc"
)

// xffProbeAddr is a TEST-NET-3 address (RFC 5737, reserved for documentation
// and guaranteed unroutable). It sits outside Bloud's trusted_proxies CIDR, so
// as an X-Forwarded-For value it is unambiguous forwarded-client traffic for
// HA's middleware to act on.
const xffProbeAddr = "203.0.113.119"

// Configurator handles Home Assistant configuration: it provisions the
// hass-oidc-auth auth provider and the managed auth_oidc configuration block
// before the container starts (PreStart), then completes HA's first-run
// onboarding and verifies the OIDC provider is live (PostStart). All HTTP goes
// through the typed client (api.go); asset install goes through appasset; the
// managed config block goes through managedfile.
type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger

	// componentURL/componentSHA are the pinned release coordinates; they are
	// fields (defaulting to the consts) so tests can serve a fixture zip.
	componentURL string
	componentSHA string

	// baseURLOverride redirects API calls in tests (httptest servers).
	baseURLOverride string

	pollInterval time.Duration

	// api is the typed HTTP surface (transport + retry + redirect policy live
	// behind it). Built in the constructor from Deps.HTTP.
	api *haAPI

	// assets installs the pinned hass-oidc-auth release (fetch/verify/stage/
	// commit) with the shared content cache.
	assets appasset.Installer

	// restartContainer stops and starts a running container by name through
	// the host runtime, forcing its process to re-exec and re-read on-disk
	// config. Injected via Deps (the configurator holds no podman handle of
	// its own). Nil when no runtime is available (CLI/tests); callers of
	// restartContainer treat that as "cannot apply now".
	restartContainerFn func(ctx context.Context, name string) error
}

// restartContainer restarts this configurator's container through the
// injected host-runtime callback. It is the only way this process reloads the
// patched http trust; the callback is expected to be a real container
// stop+start, so the re-exec re-reads .storage/http.
func (c *Configurator) restartContainer(ctx context.Context) error {
	if c.restartContainerFn == nil {
		return fmt.Errorf("no container restart available (no host runtime wired)")
	}
	return c.restartContainerFn(ctx, c.Name())
}

// NewConfigurator creates a new Home Assistant configurator from the host Deps.
// The container-restart callback is taken from Deps.RestartContainer; the typed
// HTTP client from Deps.HTTP (shared transport/policy); the asset installer from
// Deps.Assets (shared content cache). A zero-value Deps is usable: the client
// falls back to process defaults and the restart path degrades to a clear error.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = 8123
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:               port,
		secrets:            deps.Secrets,
		logger:             logger.With("app", appName),
		componentURL:       oidcComponentURL,
		componentSHA:       oidcComponentSHA256,
		pollInterval:       2 * time.Second,
		assets:             deps.Assets,
		restartContainerFn: deps.RestartContainer,
	}
	c.api = newAPI(deps.HTTP, func() string {
		if c.baseURLOverride != "" {
			return c.baseURLOverride
		}
		return fmt.Sprintf("http://localhost:%d", c.port)
	})
	return c
}

func (c *Configurator) Name() string {
	return "apps-homeassistant"
}

// baseURL returns the base URL for API calls.
func (c *Configurator) baseURL() string {
	if c.baseURLOverride != "" {
		return c.baseURLOverride
	}
	return fmt.Sprintf("http://localhost:%d", c.port)
}

// PreStart creates the data directories, fetches the pinned hass-oidc-auth
// release into custom_components/, and merges Bloud's auth_oidc block into
// configuration.yaml. Returns changed=true when anything was written, which
// signals the orchestrator to (re)start the container so HA loads the new
// configuration — HA never hot-reloads auth providers.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (bool, error) {
	configDir := filepath.Join(state.DataPath, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return false, fmt.Errorf("failed to create config dir: %w", err)
	}

	// Reverse-proxy trust is required whenever HA sits behind Traefik — every
	// proxied request needs it, independent of SSO (see ensureReverseProxy).
	rpChanged, err := c.ensureReverseProxy(configDir)
	if err != nil {
		return false, err
	}

	// Self-heal a stale running process: the trust patch is on disk but the
	// live HA still rejects forwarded headers (an in-place restart was issued
	// on an earlier pass and never actually reloaded — see CI evidence where
	// the forward-rejection persisted ~100s after the restart call).
	// Reporting a change makes the orchestrator recreate the container; the
	// cold boot loads the patched entry with no admin token required. A
	// refused probe means HA is simply not reachable yet (fresh install /
	// crash recovery), which the normal start path handles — do NOT force.
	staleForce := false
	if !rpChanged && storedProxyTrusted(configDir) {
		if live, reachable, status, _ := c.probeProxyTrust(ctx); reachable && !live {
			c.logger.Warn("stored proxy trust not live in running Home Assistant; forcing container recreate", "status", status)
			staleForce = true
		}
	}

	marker := managedfile.Marker{Begin: managedBegin, End: managedEnd}
	cfgPath := filepath.Join(configDir, "configuration.yaml")

	if !state.SSOEnabled {
		// SSO off (e.g. authentik removed): a leftover auth_oidc block points at
		// a dead provider and would break HA startup. Strip it.
		stripped, err := managedfile.RemoveBlock(cfgPath, marker)
		if err != nil {
			return false, err
		}
		return rpChanged || stripped || staleForce, nil
	}
	if state.OIDC == nil {
		return false, fmt.Errorf("OIDC output not available for native-oidc setup")
	}

	changed, err := c.ensureOIDCComponent(ctx, configDir)
	if err != nil {
		return false, fmt.Errorf("failed to provision hass-oidc-auth: %w", err)
	}
	block := managedBlock(state.OIDC)
	ok, err := managedfile.Block(cfgPath, marker, 0o600, func() string { return block })
	if err != nil {
		return false, err
	}
	return rpChanged || changed || ok || staleForce, nil
}

// Remove is a no-op for the Home Assistant configurator; container and data
// removal are handled at a higher level by the orchestrator.
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}

// PostStart waits for the HTTP API, completes first-run onboarding headlessly,
// and verifies the OIDC provider is registered.
//
// It runs under the framework's PostStartBudget: the orchestrator bounds the
// finalization wait and cancels it on shutdown, so the app uses the pass ctx
// directly rather than detaching with WithoutCancel + its own deadline. The
// retry loops survive the pass because the pass ctx is process-scoped (only
// Stop cancels it), not because the app detached.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	return c.postStart(ctx, state)
}

func (c *Configurator) postStart(ctx context.Context, state *configurator.AppState) error {
	configDir := filepath.Join(state.DataPath, "config")

	if err := c.waitForAPI(ctx); err != nil {
		return err
	}

	if err := c.ensureOwner(ctx, configDir); err != nil {
		return err
	}
	// Reverse-proxy trust: HA writes its own http config entry on first boot
	// and only reads it at startup. Patch that stored entry so HA trusts
	// Traefik's X-Forwarded-* headers, then restart the CONTAINER so the new
	// process re-reads it. Without this, the OIDC callback proxied through
	// Traefik fails with 400. No-op once already set.
	//
	// We restart the container (via the host runtime callback), not HA's own
	// `homeassistant.restart` service: under the official Container image that
	// soft restart (exit-code 100 re-exec by s6) is unreliable and can accept
	// without ever reloading (see INTEGRATION.md). A podman stop+start is a
	// real process re-exec and needs no admin token — so it works on a retried
	// install too, where onboarding is already done and no token is available.
	changed, err := c.ensureReverseProxy(configDir)
	if err != nil {
		return err
	}
	if changed {
		c.logger.Info("restarting Home Assistant container to apply reverse-proxy trust")
		if err := c.restartContainer(ctx); err != nil {
			// The patched entry is on disk; if we leave this pass running, HA
			// keeps rejecting Traefik's forwarded headers (OIDC callback 400s)
			// until some later restart. Fail the pass instead: the node lands in
			// ERROR, and the PreStart stale-check (or a retry install's reset)
			// recreates the container — which loads the patched entry.
			c.logger.Warn("container restart unavailable; failing so a recreate applies the patched trust", "error", err)
			return fmt.Errorf("reverse-proxy trust written but the container could not be restarted (%v); the next recreate applies it", err)
		}
	}
	// Verify the RUNNING process actually honours forwarded headers before the
	// node can be marked RUNNING — but only when there is trust on disk to
	// verify (just-patched or already-trusted). When HA has not written its
	// http entry yet (early onboarding) there is nothing to check and the wait
	// is skipped. The restart call returns long before HA has reloaded — and can
	// return without reloading at all (CI showed the forward-rejection persisting
	// ~100s after the restart while the API was already answering 200). The
	// on-disk patch proves nothing about the live process; the probe does.
	if changed || storedProxyTrusted(configDir) {
		if err := c.waitForProxyTrust(ctx); err != nil {
			return err
		}
	}

	if state.SSOEnabled {
		if err := c.waitForOIDCReady(ctx); err != nil {
			// The OIDC provider registers at HA startup and its /auth/oidc/welcome
			// view answers only once setup + discovery succeed. A fresh container can
			// still be mid-boot (or mid first-run onboarding) when this probe runs, so
			// a timeout is usually transient — but the orchestrator treats PostStart
			// errors as terminal ERROR (only an explicit install intent resets them),
			// so word this as a recoverable retry rather than a hard failure.
			return fmt.Errorf("OIDC provider not yet live (%v); retry the install to reconcile and re-check it", err)
		}
		return nil
	}
	return nil
}

// ensureOIDCComponent installs the pinned hass-oidc-auth release into
// <configDir>/custom_components/auth_oidc/ via the shared asset installer:
// fetch (retried) → sha256 verify → stage → atomic commit. The version-aware
// SkipIf makes it a no-op (no network) when the installed manifest already
// reports the pinned version; the Verify callback asserts the archive actually
// declares the expected provider domain before anything lands.
func (c *Configurator) ensureOIDCComponent(ctx context.Context, configDir string) (bool, error) {
	targetDir := filepath.Join(configDir, "custom_components", componentDomain)
	return c.assets.Install(ctx, appasset.Asset{
		Name:     "hass-oidc-auth",
		Dest:     targetDir,
		Source:   appasset.URL(c.componentURL),
		Kind:     appasset.Zip,
		SHA256:   c.componentSHA,
		SkipIf:   appasset.ManifestVersionEq("manifest.json", oidcComponentVersion),
		Verify:   appasset.ManifestFieldEq("manifest.json", "domain", componentDomain),
		Sentinel: "manifest.json",
	})
}

// managedBlock renders the desired auth_oidc block. Fixed key order and always
// double-quoted values keep the rendering deterministic across reconciliation
// cycles. discovery_url must be the full well-known URL — the integration
// fetches it verbatim (verified against v1.2.1 source).
func managedBlock(oidc *configurator.OIDCOutput) string {
	discovery := strings.TrimSuffix(oidc.IssuerURL, "/") + "/.well-known/openid-configuration"
	return strings.Join([]string{
		managedBegin + " (Bloud-managed — do not edit)",
		componentDomain + ":",
		"  client_id: " + yamlQuote(oidc.ClientID),
		"  client_secret: " + yamlQuote(oidc.ClientSecret),
		"  discovery_url: " + yamlQuote(discovery),
		`  display_name: "Bloud"`,
		"  roles:",
		`    admin: "authentik Admins"`,
		managedEnd,
	}, "\n")
}

// trustedProxies are the networks Home Assistant must trust for X-Forwarded-*
// headers. Traefik (host network) reaches HA over the published loopback port,
// and podman's rootless port-forward presents that connection from HA's
// container network, so HA sees a peer in the private range. The broad /8 is a
// deliberate, documented trade-off (see INTEGRATION.md); Home Assistant logs a
// warning about it that is expected and accepted here.
var trustedProxies = []string{"10.0.0.0/8"}

// ensureReverseProxy makes an existing Home Assistant http config entry honour
// the X-Forwarded-* headers that Traefik adds in front of it. HA 2026.x
// moved the http integration to a stored config entry (config/.storage/http)
// and ignores the `http:` block in configuration.yaml entirely (it files a
// yaml_still_present_after_onboarding repair). Without use_x_forwarded_for +
// trusted_proxies on that stored entry, HA's forwarding middleware rejects
// every request arriving via Traefik and the OIDC callback (/auth/oidc/callback)
// fails with 400.
//
// The entry HA writes (schema version 2) holds its live settings under
// data.stable — that is where the two keys are merged; every other field HA
// set is preserved. It never creates the file: a hand-written config entry is
// rejected by HA's strict schema validation on first boot (which would take
// down the entire http integration, and with it auth/onboarding/everything
// else). When the entry is absent — or carries no data.stable block yet — this
// is a no-op; the caller (PostStart) calls it again once HA has written its
// own entry, then restarts Home Assistant to apply it.
// Returns changed=true only when the on-disk content actually changed.
func (c *Configurator) ensureReverseProxy(configDir string) (bool, error) {
	path := filepath.Join(configDir, ".storage", "http")

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // HA hasn't written its own entry yet; nothing to patch
		}
		return false, fmt.Errorf("failed to read stored http entry: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return false, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false, fmt.Errorf("stored http entry %s is not valid JSON: %w", path, err)
	}
	data, ok := doc["data"].(map[string]any)
	if !ok {
		return false, fmt.Errorf("stored http entry has no data object")
	}
	httpCfg, ok := data["stable"].(map[string]any)
	if !ok {
		// No live settings block yet (e.g. pending first-boot migration);
		// nothing to patch this cycle.
		return false, nil
	}

	changed := false
	if v, ok := httpCfg["use_x_forwarded_for"].(bool); !ok || !v {
		httpCfg["use_x_forwarded_for"] = true
		changed = true
	}
	if !equalStrings(toStringSlice(httpCfg["trusted_proxies"]), trustedProxies) {
		httpCfg["trusted_proxies"] = trustedProxies
		changed = true
	}
	if !changed {
		return false, nil
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, fmt.Errorf("failed to marshal http config entry: %w", err)
	}
	return managedfile.Write(path, append(out, '\n'), 0600)
}

// toStringSlice coerces a decoded JSON value to []string, returning nil for a
// non-array or any non-string element.
func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func yamlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// --- HTTP wait/probe wrappers over the typed client ---

// waitForAPI polls /api/ until the HTTP listener answers anything under 500.
func (c *Configurator) waitForAPI(ctx context.Context) error {
	if err := c.api.waitAPI(ctx, c.pollInterval); err != nil {
		return fmt.Errorf("timed out waiting for Home Assistant API: %w", err)
	}
	return nil
}

// probeProxyTrust reports whether the RUNNING Home Assistant process currently
// accepts forwarded (X-Forwarded-For-bearing) requests. Single-shot, three
// states (trusted / reachable-but-stale / unreachable) — the contract the
// PreStart stale-check consumes. See haAPI.probeProxyTrust for the mapping.
func (c *Configurator) probeProxyTrust(ctx context.Context) (trusted, reachable bool, status int, perr error) {
	return c.api.probeProxyTrust(ctx)
}

// waitForProxyTrust polls the forwarded-header probe until the running process
// accepts forwarded requests (the forward middleware stops answering 400).
// A 400 (stale process that has not reloaded) is retried; a refused connection
// (mid-restart) and a 5xx (still booting) are retried. Any other status means
// the forward middleware let the request through. Returning nil guarantees the
// node never goes RUNNING on a proxy-rejecting process.
func (c *Configurator) waitForProxyTrust(ctx context.Context) error {
	if err := c.api.waitProxyTrust(ctx, c.pollInterval); err != nil {
		return fmt.Errorf("timed out waiting for Home Assistant to reload reverse-proxy trust (process still answers 400 to forwarded requests, restart never took effect): %w", err)
	}
	return nil
}

// waitForOIDCReady verifies the OIDC auth provider is live by probing
// /auth/oidc/welcome until it answers 200.
func (c *Configurator) waitForOIDCReady(ctx context.Context) error {
	if err := c.api.waitOIDCReady(ctx, c.pollInterval); err != nil {
		return fmt.Errorf("OIDC provider never became live at %s/auth/oidc/welcome (component missing or discovery failed): %w", c.baseURL(), err)
	}
	return nil
}

// ensureOwner makes the instance SSO-ready headlessly by creating the
// break-glass owner (HA's "user" onboarding step) when none exists. It
// closes NO onboarding steps: core_config (home name/location/units),
// analytics, and integration are all left for the human. The integration
// step's Finish button is the only thing in HA's onboarding SPA that
// redirects the browser into the app, so Bloud must leave it for the human
// to click — pre-closing it strands them on a spinner once analytics ends.
// With the owner created, the HA router no longer forces the create-account
// screen and routes the first visitor through the normal authorize flow
// (where "Login with Bloud" lives) into the welcome screens. Returns nil
// when no owner step is pending (owner already exists / human already
// finished — HA then deregisters /api/onboarding, the permanent-404 case).
func (c *Configurator) ensureOwner(ctx context.Context, configDir string) error {
	body, alreadyOnboarded, err := c.api.onboardingStatus(ctx, c.pollInterval, func() bool { return ownerOnDisk(configDir) })
	if err != nil {
		return err
	}
	if alreadyOnboarded {
		c.logger.Info("Home Assistant already fully onboarded; onboarding endpoint not registered")
		return nil
	}

	var steps []onboardingStep
	if err := json.Unmarshal(body, &steps); err != nil {
		return fmt.Errorf("onboarding status malformed: %w", err)
	}
	// "user" is the gate: until the owner exists every visitor is forced
	// into the create-account wizard. Once the owner exists (this step
	// closed), the HA onboarding router detects steps[0].done and routes
	// visitors through the normal authorize flow — where the OIDC provider
	// lives — then surfaces core_config/analytics/integration for the
	// human. So Bloud acts only while the user step is still pending, and
	// only to create the owner; every human step stays open.
	if !userStepPending(steps) {
		return nil
	}

	return c.createFirstRunOwner(ctx)
}

// onboardingStep is one entry of HA's /api/onboarding step list.
type onboardingStep struct {
	Step string `json:"step"`
	Done bool   `json:"done"`
}

// userStepPending reports whether the "user" onboarding step is still open.
func userStepPending(steps []onboardingStep) bool {
	for _, s := range steps {
		if s.Step == "user" && !s.Done {
			return true
		}
	}
	return false
}

// createFirstRunOwner runs HA's first-run "user" step headlessly: create
// the break-glass owner. Bloud does NOT exchange the returned authorization
// code and does NOT close any later step — the human completes core_config →
// analytics → integration (whose Finish redirects them into the app), and
// the post-trust reload is a tokenless container restart, so the owner's
// access token is never needed here.
func (c *Configurator) createFirstRunOwner(ctx context.Context) error {
	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		return fmt.Errorf("failed to obtain bootstrap password: %w", err)
	}
	if _, err := c.api.createOwner(ctx, map[string]string{
		"client_id": onboardingClientID,
		"username":  bootstrapUsername,
		"password":  password,
		"name":      bootstrapFullname,
		"language":  "en",
	}); err != nil {
		return fmt.Errorf("onboarding request failed: %w", err)
	}
	c.logger.Info("first-run owner created", "user", bootstrapUsername)
	return nil
}

// ownerOnDisk reports whether Home Assistant's auth store — read the same way
// ensureReverseProxy reads the http store — already holds an owner user. That
// distinguishes the permanent 404 of /api/onboarding (endpoint deregistered
// once every step is closed) from the transient 404 of a booting instance.
func ownerOnDisk(configDir string) bool {
	raw, err := os.ReadFile(filepath.Join(configDir, ".storage", "auth"))
	if err != nil {
		return false
	}
	var doc struct {
		Data struct {
			Users []struct {
				IsOwner         bool `json:"is_owner"`
				SystemGenerated bool `json:"system_generated"`
			} `json:"users"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false
	}
	for _, u := range doc.Data.Users {
		if u.IsOwner && !u.SystemGenerated {
			return true
		}
	}
	return false
}

// storedProxyTrusted reports whether the on-disk http entry already carries
// Bloud's proxy-trust settings. PreStart uses it to tell "HA wrote its own
// file and we have not patched it yet" (absent → nothing to force) from "we
// patched it but the live process disagrees" (present → check the probe).
// Read errors are treated as false; ensureReverseProxy is the authoritative
// merge path that surfaces real corruption.
func storedProxyTrusted(configDir string) bool {
	raw, err := os.ReadFile(filepath.Join(configDir, ".storage", "http"))
	if err != nil {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false
	}
	data, ok := doc["data"].(map[string]any)
	if !ok {
		return false
	}
	httpCfg, ok := data["stable"].(map[string]any)
	if !ok {
		return false
	}
	return httpCfg["use_x_forwarded_for"] == true && equalStrings(toStringSlice(httpCfg["trusted_proxies"]), trustedProxies)
}
