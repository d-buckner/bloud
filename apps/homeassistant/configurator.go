// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package homeassistant

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
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

// Configurator handles Home Assistant configuration: it provisions the
// hass-oidc-auth auth provider and the managed auth_oidc configuration block
// before the container starts (PreStart), then completes HA's first-run
// onboarding and verifies the OIDC provider is live (PostStart).
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

	pollInterval     time.Duration
	postStartTimeout time.Duration

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

// NewConfigurator creates a new Home Assistant configurator. Wire the
// container-restart callback with SetRestartContainer (the host-agent factory
// does this from Deps.RestartContainer).
func NewConfigurator(port int, secrets configurator.AppSecretsProvider, logger *slog.Logger) *Configurator {
	if port == 0 {
		port = 8123
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Configurator{
		port:             port,
		secrets:          secrets,
		logger:           logger.With("app", appName),
		componentURL:     oidcComponentURL,
		componentSHA:     oidcComponentSHA256,
		pollInterval:     2 * time.Second,
		postStartTimeout: 150 * time.Second,
	}
}

// SetRestartContainer injects the host-runtime callback used to restart this
// app's container so its process re-execs and reloads on-disk config. Called
// by the host-agent factory from Deps.RestartContainer; left unset (CLI) or
// set from a fake (tests) the restart path degrades to a clear error.
func (c *Configurator) SetRestartContainer(fn func(ctx context.Context, name string) error) {
	c.restartContainerFn = fn
}

func (c *Configurator) Name() string {
	return "apps-homeassistant"
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

	if !state.SSOEnabled {
		// SSO off (e.g. authentik removed): a leftover auth_oidc block points at
		// a dead provider and would break HA startup. Strip it.
		stripped, err := c.writeConfig(configDir, "")
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
	ok, err := c.writeConfigBlock(configDir, state.OIDC)
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
// and verifies the OIDC provider is registered. It runs on a context detached
// from the convergence pass with its own deadline so the retry loops survive
// the pass completing (same rationale as the Jellyfin configurator).
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.postStartTimeout)
	defer cancel()
	return c.postStart(detached, state)
}

func (c *Configurator) postStart(ctx context.Context, state *configurator.AppState) error {
	configDir := filepath.Join(state.DataPath, "config")

	if err := c.waitForAPI(ctx); err != nil {
		return err
	}

	if _, err := c.ensureOnboarded(ctx, configDir); err != nil {
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
	// http entry yet (early onboarding) there is nothing to check and the
	// wait is skipped. The restart call returns long before HA has reloaded
	// — and can return without reloading at all (CI showed the forward-
	// rejection persisting ~100s after the restart while the API was already
	// answering 200). The on-disk patch proves nothing about the live
	// process; the probe does. If trust never goes live this pass fails into
	// ERROR and the next install recreate (PreStart's stale check) loads the
	// patched entry on a cold boot.
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
			// so word this as a recoverable retry rather than a hard failure. The
			// owner is already created by this point, so once the provider is live the
			// browser's OIDC landing page works; a re-install re-runs this check.
			return fmt.Errorf("OIDC provider not yet live (%v); retry the install to reconcile and re-check it", err)
		}
		return nil
	}
	return nil
}

// ensureOIDCComponent installs the pinned hass-oidc-auth release into
// <configDir>/custom_components/auth_oidc/. It is a no-op when the installed
// manifest already reports the pinned version. The download is hashed while
// streaming and aborts on mismatch; extraction lands in a staging dir that is
// renamed into place, so a failed fetch never leaves a half-installed tree.
func (c *Configurator) ensureOIDCComponent(ctx context.Context, configDir string) (bool, error) {
	customDir := filepath.Join(configDir, "custom_components")
	targetDir := filepath.Join(customDir, componentDomain)
	if installedComponentVersion(targetDir) == oidcComponentVersion {
		return false, nil
	}

	c.logger.Info("downloading hass-oidc-auth", "url", c.componentURL, "version", oidcComponentVersion)
	if err := os.MkdirAll(customDir, 0755); err != nil {
		return false, err
	}

	archivePath, err := c.downloadComponentZip(ctx, customDir)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(archivePath) }()

	stagingDir, err := extractZipToStaging(archivePath, customDir, ".auth_oidc-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()

	version, err := validateComponentManifest(stagingDir)
	if err != nil {
		return false, err
	}

	if _, err := os.Stat(targetDir); err == nil {
		if err := os.RemoveAll(targetDir); err != nil {
			return false, err
		}
	}
	if err := os.Rename(stagingDir, targetDir); err != nil {
		return false, err
	}
	c.logger.Info("hass-oidc-auth installed", "path", targetDir, "version", version)
	return true, nil
}

// downloadComponentZip fetches the component release into a temp zip inside
// dir and verifies its checksum (when configured). Returns the zip path.
func (c *Configurator) downloadComponentZip(ctx context.Context, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.componentURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	archive, err := os.CreateTemp(dir, ".hass-oidc-auth-*.zip")
	if err != nil {
		return "", err
	}
	archivePath := archive.Name()

	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(archive, hash), resp.Body); err != nil {
		_ = archive.Close()
		return "", err
	}
	if err := archive.Close(); err != nil {
		return "", err
	}
	if c.componentSHA != "" && fmt.Sprintf("%x", hash.Sum(nil)) != c.componentSHA {
		return "", fmt.Errorf("download checksum mismatch")
	}
	return archivePath, nil
}

// extractZipToStaging unpacks the zip into a fresh temp dir created in
// parent with the given glob pattern. Rejects archive entries that would
// escape the staging dir (zip-slip guard).
func extractZipToStaging(archivePath, parent, pattern string) (string, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()

	stagingDir, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", err
	}

	for _, file := range reader.File {
		destination := filepath.Join(stagingDir, file.Name)
		if !strings.HasPrefix(filepath.Clean(destination), filepath.Clean(stagingDir)+string(os.PathSeparator)) {
			return "", fmt.Errorf("archive contains invalid path %q", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0755); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return "", err
		}
		source, err := file.Open()
		if err != nil {
			return "", err
		}
		mode := file.Mode()
		if mode == 0 {
			mode = 0644
		}
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			_ = source.Close()
			return "", err
		}
		_, copyErr := io.Copy(out, source)
		closeErr := out.Close()
		_ = source.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return stagingDir, nil
}

// validateComponentManifest sanity-checks the payload: the domain's manifest
// must be present and declare the expected auth provider domain (guards
// against a wrong asset). Returns the manifest's version string.
func validateComponentManifest(stagingDir string) (string, error) {
	manifest, err := os.ReadFile(filepath.Join(stagingDir, "manifest.json"))
	if err != nil {
		return "", fmt.Errorf("archive did not contain manifest.json: %w", err)
	}
	var m struct {
		Domain  string `json:"domain"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return "", fmt.Errorf("archive manifest.json is not valid JSON: %w", err)
	}
	if m.Domain != componentDomain {
		return "", fmt.Errorf("archive manifest domain %q != %q", m.Domain, componentDomain)
	}
	return m.Version, nil
}

// installedComponentVersion returns the version recorded in the installed
// component's manifest.json, or "" when absent/unreadable/corrupt.
func installedComponentVersion(targetDir string) string {
	data, err := os.ReadFile(filepath.Join(targetDir, "manifest.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	return m.Version
}

// writeConfigBlock merges Bloud's managed auth_oidc block into
// configuration.yaml. The file belongs to the user; only the marker-delimited
// block is touched. Rendering is deterministic so unchanged values never churn
// the file (changed=false).
func (c *Configurator) writeConfigBlock(configDir string, oidc *configurator.OIDCOutput) (bool, error) {
	return c.writeConfig(configDir, managedBlock(oidc))
}

// writeConfig replaces the marker-delimited block with block (empty block
// removes it), preserving everything else. Returns changed=true only when the
// file content actually changes.
func (c *Configurator) writeConfig(configDir string, block string) (bool, error) {
	path := filepath.Join(configDir, "configuration.yaml")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("failed to read configuration.yaml: %w", err)
	}

	var desired string
	if block == "" {
		desired, _ = removeManagedBlock(string(existing))
	} else {
		desired = mergeManagedBlock(string(existing), block)
	}
	if desired == string(existing) {
		return false, nil
	}
	if len(existing) == 0 && len(desired) == 0 {
		return false, nil
	}
	// The block contains client_secret.
	if err := os.WriteFile(path, []byte(desired), 0o600); err != nil {
		return false, fmt.Errorf("failed to write configuration.yaml: %w", err)
	}
	return true, nil
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
	if err := os.WriteFile(path, append(out, '\n'), 0600); err != nil {
		return false, fmt.Errorf("failed to write http config entry: %w", err)
	}
	return true, nil
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

// mergeManagedBlock returns existing with the marker-delimited region replaced
// by block (appended when no region exists). A lone BEGIN (no END) is treated
// as an unterminated region and replaced to EOF.
func mergeManagedBlock(existing, block string) string {
	blockLines := strings.Split(block, "\n")
	lines := []string{}
	if existing != "" {
		lines = strings.Split(existing, "\n")
	}
	begin, end := -1, -1
	for i, line := range lines {
		if strings.HasPrefix(line, managedBegin) {
			if begin == -1 {
				begin = i
			}
			if strings.HasPrefix(line, managedEnd) {
				end = i
			}
		} else if begin != -1 && strings.HasPrefix(line, managedEnd) {
			end = i
		}
	}
	switch {
	case begin == -1:
		if strings.TrimSpace(existing) == "" {
			return block + "\n"
		}
		return strings.TrimRight(existing, "\n") + "\n\n" + block + "\n"
	case end == -1 || end < begin:
		// Unterminated region: replace from BEGIN to EOF.
		out := append(append([]string{}, lines[:begin]...), blockLines...)
		return joinLines(out)
	default:
		out := append(append([]string{}, lines[:begin]...), blockLines...)
		out = append(out, lines[end+1:]...)
		return joinLines(out)
	}
}

// removeManagedBlock deletes the marker-delimited region (including an
// unterminated one, to EOF).
func removeManagedBlock(existing string) (string, bool) {
	if existing == "" {
		return "", false
	}
	lines := strings.Split(existing, "\n")
	begin, end := -1, len(lines)-1
	for i, line := range lines {
		if strings.HasPrefix(line, managedBegin) && begin == -1 {
			begin = i
		}
		if begin != -1 && strings.HasPrefix(line, managedEnd) {
			end = i
			break
		}
	}
	if begin == -1 {
		return existing, false
	}
	out := append(append([]string{}, lines[:begin]...), lines[end+1:]...)
	trimmed := strings.TrimRight(joinLines(out), "\n")
	if trimmed == "" {
		return "", true
	}
	return trimmed + "\n", true
}

func joinLines(lines []string) string {
	out := strings.Join(lines, "\n")
	if out == "" {
		return out
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

func yamlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// baseURL returns the base URL for API calls.
func (c *Configurator) baseURL() string {
	if c.baseURLOverride != "" {
		return c.baseURLOverride
	}
	return fmt.Sprintf("http://localhost:%d", c.port)
}

// apiClient performs single API requests without following redirects (the
// OIDC liveness probe inspects the redirect itself).
func (c *Configurator) apiGet(ctx context.Context, path string) (*http.Response, error) {
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+path, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

// waitForAPI polls /api/ (public, answers "API running" without auth) until
// the HTTP listener is up. Connection errors and 5xx mean HA is still booting.
func (c *Configurator) waitForAPI(ctx context.Context) error {
	for {
		resp, err := c.apiGet(ctx, "/api/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for Home Assistant API: %w", ctx.Err())
		case <-time.After(c.pollInterval):
		}
	}
}

// xffProbeAddr is a TEST-NET-3 address (RFC 5737, reserved for documentation
// and guaranteed unroutable). It sits outside Bloud's trusted_proxies CIDR, so
// as an X-Forwarded-For value it is unambiguous forwarded-client traffic for
// HA's middleware to act on.
const xffProbeAddr = "203.0.113.119"

// probeProxyTrust reports whether the RUNNING Home Assistant process currently
// accepts forwarded (X-Forwarded-For-bearing) requests. It GETs /api/ with a
// forged X-Forwarded-For header and reads the answer at the forwarding
// middleware — the same gate a proxied browser request hits:
//
//	trusted=true                  → the forward middleware passed the
//	                               forwarded request (any HTTP status except
//	                               400; an unauthenticated /api/ answers 401
//	                               Bearer once trust is loaded — the auth
//	                               layer is downstream of the forward check
//	                               and irrelevant to this property).
//	trusted=false, status=400     → the running process still has the
//	                               pre-patch settings and the forwarding
//	                               middleware rejects forwarded headers
//	                               ("not set-up for reverse proxies") — the
//	                               precise failure the popup observed.
//	reachable=false               → the probe never connected: HA is down or
//	                               mid-restart (connection refused), or
//	                               still booting (HTTP 5xx) — retried, NOT
//	                               a definitive rejection.
//
// The probe reaches HA over the same published port Traefik's traffic is
// port-forwarded through, so HA sees a peer inside trusted_proxies (see
// trustedProxies) and the use_x_forwarded_for flag alone governs the
// 400-vs-pass boundary — the same coupling the trust design already relies on.
func (c *Configurator) probeProxyTrust(ctx context.Context) (trusted bool, reachable bool, status int, perr error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+"/api/", nil)
	if err != nil {
		return false, false, 0, err
	}
	req.Header.Set("X-Forwarded-For", xffProbeAddr)
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, false, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	// 400 = forward middleware rejected (stale). 5xx = still booting.
	// Anything else that connected means the forward middleware passed.
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode >= 500 {
		return false, true, resp.StatusCode, nil
	}
	return true, true, resp.StatusCode, nil
}

// waitForProxyTrust polls the forwarded-header probe until the running process
// accepts forwarded requests (the forward middleware stops answering 400).
// A 400 (stale process that has not reloaded) and a refused connection
// (mid-restart) are both retried; an HTTP 5xx is treated as still-booting.
// Any other status — 200 or 401 or 302 — means the forward middleware let the
// request through, which is exactly the property we need: a browser's proxied
// request will no longer hit the forward-400. (Unauthenticated, HA 2026.9
// answers /api/ with 401 Bearer once trust is loaded; the 400-vs-not-400
// boundary at the forwarding middleware, not 200, is the live signal.)
// Returning nil guarantees the node never goes RUNNING on a proxy-rejecting
// process.
func (c *Configurator) waitForProxyTrust(ctx context.Context) error {
	// lastTrustLive remembers the most recent probe that actually connected
	// (regardless of auth outcome). The final poll is often cancelled mid-
	// flight by the deadline (Do returns a transport error), which would
	// otherwise misreport a live-401 process as "unreachable".
	lastReachable := false
	for {
		trusted, reachable, _, perr := c.probeProxyTrust(ctx)
		if trusted {
			return nil
		}
		if reachable {
			lastReachable = true
		}
		select {
		case <-ctx.Done():
			if lastReachable {
				return fmt.Errorf("timed out waiting for Home Assistant to reload reverse-proxy trust (process still answers 400 to forwarded requests, restart never took effect): %w", ctx.Err())
			}
			return fmt.Errorf("timed out waiting for Home Assistant reverse-proxy trust (process unreachable: %v): %w", perr, ctx.Err())
		case <-time.After(c.pollInterval):
		}
	}
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

// ensureOnboarded completes Home Assistant's first-run onboarding headlessly
// (creating the break-glass owner and finishing the remaining steps) so the
// instance is fully onboarded before any user browser can reach it: HA answers
// every unauthenticated request with the onboarding wizard until the last step
// closes, and the sign-in page — where the OIDC provider lives — is only
// reachable once HA considers itself onboarded. The iOS client is one of HA's
// built-in OAuth2 clients (see onboardingClientID). It returns the owner's
// access token (the only token usable for authenticated API calls after this
// point), or "" when HA was already fully onboarded.
func (c *Configurator) ensureOnboarded(ctx context.Context, configDir string) (string, error) {
	body, alreadyOnboarded, err := c.pollOnboardingStatus(ctx, configDir)
	if err != nil {
		return "", err
	}
	if alreadyOnboarded {
		return "", nil
	}

	var steps []onboardingStep
	if err := json.Unmarshal(body, &steps); err != nil {
		return "", fmt.Errorf("onboarding status malformed: %w", err)
	}
	// HA returns the first-run step flow (user, core_config, analytics,
	// integration) with done flags. "user" is the gate: until the owner exists
	// every visitor is redirected into the wizard; the remaining steps must be
	// closed too, or the wizard keeps intercepting the sign-in page (see
	// finishFirstRunSteps below).
	if !userStepPending(steps) {
		return "", nil
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

// pollOnboardingStatus retries GET /api/onboarding until it succeeds or the
// context deadline passes. /api/onboarding is served only once HA's
// onboarding integration has registered its routes — during boot the HTTP
// listener is already up (waitForAPI passes on any <500 response) while this
// route still 404s, so any non-2xx is retried until the deadline. A 404 is
// special: Home Assistant *deregisters* the endpoint once every step is
// closed, so a 404 alongside an owner in the auth store means "already
// onboarded" (alreadyOnboarded=true), not "still booting" — see ownerOnDisk.
func (c *Configurator) pollOnboardingStatus(ctx context.Context, configDir string) ([]byte, bool, error) {
	var lastErr error
	for {
		r, err := c.apiGet(ctx, "/api/onboarding")
		if err == nil {
			b, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if r.StatusCode == http.StatusOK {
				return b, false, nil
			}
			if r.StatusCode == http.StatusNotFound && ownerOnDisk(configDir) {
				// Permanent 404: Home Assistant deregisters this endpoint once
				// every onboarding step is closed — with an owner in the auth
				// store this is "already onboarded", not a boot race. Return
				// no token; postStart still runs ensureReverseProxy afterwards,
				// so a not-yet-applied trust patch (e.g. an owner created via
				// the CLI rather than here) is picked up on this or the next
				// pass, exactly as before.
				c.logger.Info("Home Assistant already fully onboarded; onboarding endpoint not registered")
				return nil, true, nil
			}
			lastErr = fmt.Errorf("HTTP %d: %s", r.StatusCode, string(b))
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return nil, false, fmt.Errorf("timed out waiting for the Home Assistant onboarding API (HTTP listener is up but /api/onboarding keeps failing: %v): %w", lastErr, ctx.Err())
		case <-time.After(c.pollInterval):
		}
	}
}

// createFirstRunOwner runs the first-run wizard: create the bootstrap owner,
// exchange its one-shot authorization code for an access token, and close
// the remaining onboarding steps. Returns the owner access token.
func (c *Configurator) createFirstRunOwner(ctx context.Context) (string, error) {
	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		return "", fmt.Errorf("failed to obtain bootstrap password: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"client_id": onboardingClientID,
		"username":  bootstrapUsername,
		"password":  password,
		"name":      bootstrapFullname,
		"language":  "en",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/api/onboarding/users", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	ownerResp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("onboarding request failed: %w", err)
	}
	ownerBody, _ := io.ReadAll(ownerResp.Body)
	_ = ownerResp.Body.Close()
	if ownerResp.StatusCode != http.StatusOK && ownerResp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("onboarding returned HTTP %d: %s", ownerResp.StatusCode, string(ownerBody))
	}
	c.logger.Info("first-run owner created", "user", bootstrapUsername)

	// HA's onboarding hands back a one-shot authorization code, not an access
	// token (verified against core 2026.9 onboarding/views.py — it returns
	// {"auth_code": …} and nothing else). Exchange it for the owner's real
	// access token; that token is what drives the authenticated restart below.
	var onboarding struct {
		AuthCode string `json:"auth_code"`
	}
	if err := json.Unmarshal(ownerBody, &onboarding); err != nil || onboarding.AuthCode == "" {
		return "", fmt.Errorf("onboarding response carried no authorization code: %s", string(ownerBody))
	}
	accessToken, err := c.exchangeAuthCode(ctx, onboarding.AuthCode)
	if err != nil {
		return "", fmt.Errorf("failed to exchange the onboarding authorization code for an access token: %w", err)
	}
	// Close the steps that follow "user" with the owner token. The integration
	// step exists to hand the browser an auth_code; we complete it with the
	// built-in my.home-assistant.io redirect that nobody redeems — the point is
	// that HA then reports the instance onboarded, so the user's first visit
	// lands on the sign-in page and goes straight through OIDC instead of the
	// half-raced wizard.
	if err := c.finishFirstRunSteps(ctx, accessToken); err != nil {
		return "", fmt.Errorf("failed to finish first-run steps: %w", err)
	}
	return accessToken, nil
}

// finishFirstRunSteps closes the onboarding steps that follow "user" using the
// owner token. Idempotent: HTTP 403 means a previous reconciliation already
// closed the step, which is treated as success.
func (c *Configurator) finishFirstRunSteps(ctx context.Context, token string) error {
	steps := []struct{ path, body string }{
		{"/api/onboarding/core_config", "{}"},
		{"/api/onboarding/analytics", "{}"},
		{"/api/onboarding/integration", `{"client_id":"https://my.home-assistant.io/redirect/oauth","redirect_uri":"https://my.home-assistant.io/redirect/oauth"}`},
	}
	for _, s := range steps {
		if err := c.postJSONBearer(ctx, s.path, []byte(s.body), token); err != nil {
			return fmt.Errorf("%s: %w", s.path, err)
		}
	}
	return nil
}

// postJSONBearer POSTs a JSON body with an owner bearer token. 403 (step
// already done, the idempotent replay case) is returned as success.
func (c *Configurator) postJSONBearer(ctx context.Context, path string, payload []byte, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusForbidden {
		return nil
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
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

// exchangeAuthCode trades a Home Assistant authorization code for an owner
// access token. HA's onboarding (and login flow) hand out a short-lived,
// single-use authorization code rather than a usable token; POST /auth/token
// with the authorization_code grant exchanges it for a Bearer access token plus
// a refresh token (core auth/__init__.py). The built-in iOS client id used to
// create the owner is reused here — HA validates only that the client id parses
// as an IndieAuth URL, so no redirect_uri or client secret is involved.
func (c *Configurator) exchangeAuthCode(ctx context.Context, authCode string) (string, error) {
	form := url.Values{
		"grant_type": {"authorization_code"},
		"client_id":  {onboardingClientID},
		"code":       {authCode},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/auth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("token response malformed: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("token exchange returned no access token: %s", string(body))
	}
	return tok.AccessToken, nil
}

// waitForOIDCReady verifies the OIDC auth provider is live by probing
// /auth/oidc/welcome. hass-oidc-auth registers that view only when its
// async_setup succeeds, and fetching the provider's discovery document is part
// of that setup — so a 200 from this page proves both the component loaded and
// provider discovery succeeded. The route 404s while HA is still booting (and
// while the onboarding integration registers its routes), so it is retried until
// the deadline.
func (c *Configurator) waitForOIDCReady(ctx context.Context) error {
	for {
		resp, err := c.apiGet(ctx, "/auth/oidc/welcome")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("OIDC provider never became live at %s/auth/oidc/welcome (component missing or discovery failed): %w", c.baseURL(), ctx.Err())
		case <-time.After(c.pollInterval):
		}
	}
}
