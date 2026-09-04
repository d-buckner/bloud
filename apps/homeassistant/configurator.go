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
}

// NewConfigurator creates a new Home Assistant configurator.
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

	if !state.SSOEnabled {
		// SSO off (e.g. authentik removed): a leftover auth_oidc block points at
		// a dead provider and would break HA startup. Strip it.
		return c.writeConfig(configDir, "")
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
	return changed || ok, nil
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
	if err := c.waitForAPI(ctx); err != nil {
		return err
	}
	if err := c.ensureOnboarded(ctx); err != nil {
		return err
	}
	if state.SSOEnabled {
		return c.waitForOIDCReady(ctx)
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.componentURL, nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	archive, err := os.CreateTemp(customDir, ".hass-oidc-auth-*.zip")
	if err != nil {
		return false, err
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)

	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(archive, hash), resp.Body); err != nil {
		archive.Close()
		return false, err
	}
	if err := archive.Close(); err != nil {
		return false, err
	}
	if c.componentSHA != "" && fmt.Sprintf("%x", hash.Sum(nil)) != c.componentSHA {
		return false, fmt.Errorf("download checksum mismatch")
	}

	// The release zip is flat (files at the archive root) — extract into the
	// target dir, no top-level entry to strip.
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return false, err
	}
	defer reader.Close()

	stagingDir, err := os.MkdirTemp(customDir, ".auth_oidc-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(stagingDir)
	for _, file := range reader.File {
		destination := filepath.Join(stagingDir, file.Name)
		if !strings.HasPrefix(filepath.Clean(destination), filepath.Clean(stagingDir)+string(os.PathSeparator)) {
			return false, fmt.Errorf("archive contains invalid path %q", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0755); err != nil {
				return false, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return false, err
		}
		source, err := file.Open()
		if err != nil {
			return false, err
		}
		mode := file.Mode()
		if mode == 0 {
			mode = 0644
		}
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			source.Close()
			return false, err
		}
		_, copyErr := io.Copy(out, source)
		closeErr := out.Close()
		source.Close()
		if copyErr != nil {
			return false, copyErr
		}
		if closeErr != nil {
			return false, closeErr
		}
	}
	// Sanity-check the payload: the domain's manifest must be present and must
	// declare the auth provider domain (guards against a wrong asset).
	manifest, err := os.ReadFile(filepath.Join(stagingDir, "manifest.json"))
	if err != nil {
		return false, fmt.Errorf("archive did not contain manifest.json: %w", err)
	}
	var m struct {
		Domain  string `json:"domain"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return false, fmt.Errorf("archive manifest.json is not valid JSON: %w", err)
	}
	if m.Domain != componentDomain {
		return false, fmt.Errorf("archive manifest domain %q != %q", m.Domain, componentDomain)
	}

	if _, err := os.Stat(targetDir); err == nil {
		if err := os.RemoveAll(targetDir); err != nil {
			return false, err
		}
	}
	if err := os.Rename(stagingDir, targetDir); err != nil {
		return false, err
	}
	c.logger.Info("hass-oidc-auth installed", "path", targetDir, "version", m.Version)
	return true, nil
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

// managedBlock renders the desired auth_oidc block. Fixed key order and
// always double-quoted values keep the rendering deterministic across
// reconciliation cycles. discovery_url must be the full well-known URL — the
// integration fetches it verbatim (verified against v1.2.1 source).
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
			resp.Body.Close()
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

// ensureOnboarded completes Home Assistant's first-run onboarding headlessly
// by creating the break-glass owner. Without an owner every request is
// redirected into the onboarding flow. The iOS client is one of HA's built-in
// OAuth2 clients (see onboardingClientID).
func (c *Configurator) ensureOnboarded(ctx context.Context) error {
	resp, err := c.apiGet(ctx, "/api/onboarding")
	if err != nil {
		return fmt.Errorf("onboarding status request failed: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("onboarding status returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	var steps []struct {
		Step string `json:"step"`
		Done bool   `json:"done"`
	}
	if err := json.Unmarshal(body, &steps); err != nil {
		return fmt.Errorf("onboarding status malformed: %w", err)
	}
	// HA returns the first-run step flow (user, core_config, analytics,
	// integration) with done flags. Only the "user" step is required for a
	// headless install (it creates the owner); the remaining steps are
	// interactive extras that are simply never shown once an owner exists.
	userPending := false
	for _, s := range steps {
		if s.Step == "user" && !s.Done {
			userPending = true
			break
		}
	}
	if !userPending {
		return nil
	}

	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		return fmt.Errorf("failed to obtain bootstrap password: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"client_id": onboardingClientID,
		"username":  bootstrapUsername,
		"password":  password,
		"name":      bootstrapFullname,
		"language":  "en",
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/api/onboarding/users", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	ownerResp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("onboarding request failed: %w", err)
	}
	ownerBody, _ := io.ReadAll(ownerResp.Body)
	ownerResp.Body.Close()
	if ownerResp.StatusCode != http.StatusOK && ownerResp.StatusCode != http.StatusCreated {
		return fmt.Errorf("onboarding returned HTTP %d: %s", ownerResp.StatusCode, string(ownerBody))
	}
	c.logger.Info("first-run onboarding completed", "owner", bootstrapUsername)
	return nil
}

// waitForOIDCReady verifies the OIDC auth provider is live by probing
// /auth/oidc/welcome. hass-oidc-auth registers that view only when its
// async_setup succeeds, and fetching the provider's discovery document is
// part of that setup — so a 200 from this page proves both the component
// loaded and the provider discovery succeeded. The route 404s while HA is
// still booting, so it is retried until the deadline.
func (c *Configurator) waitForOIDCReady(ctx context.Context) error {
	for {
		resp, err := c.apiGet(ctx, "/auth/oidc/welcome")
		if err == nil {
			resp.Body.Close()
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
