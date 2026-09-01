// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package jellyfin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/xmlutil"
)

const (
	// Managed admin credentials used for setup and subsequent reconciliation.
	// Keep this account until Jellyfin configuration has a separate durable credential.
	bootstrapUsername = "bloud-bootstrap-admin"
	bootstrapPassword = "bloud-bootstrap-password-change-me"

	// LDAP plugin GUID - this is the standard ID for the Jellyfin LDAP-Auth plugin
	// Note: Jellyfin uses GUIDs without dashes in the API
	ldapPluginID     = "958aad6637844d2ab89aa7b6fab6e25c"
	ldapPluginURL    = "https://repo.jellyfin.org/files/plugin/ldap-authentication/ldap-authentication_23.0.0.0.zip"
	ldapPluginSHA256 = "952e33fa8d3ac512ccb5c1e2e1c655cbb1957e41fa1f00bd6ccf3076e0467446"
)

// Configurator handles Jellyfin configuration
type Configurator struct {
	Port         int
	baseURL      string // Override for testing; if empty, uses localhost:Port
	pluginURL    string
	pluginSHA256 string
	logger       *slog.Logger
}

// NewConfigurator creates a new Jellyfin configurator
func NewConfigurator(port int, logger *slog.Logger) *Configurator {
	if port == 0 {
		port = 8096
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Configurator{
		Port:         port,
		pluginURL:    ldapPluginURL,
		pluginSHA256: ldapPluginSHA256,
		logger:       logger.With("app", "jellyfin"),
	}
}

// getBaseURL returns the base URL for API calls
func (c *Configurator) getBaseURL() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return fmt.Sprintf("http://localhost:%d", c.Port)
}

func (c *Configurator) Name() string {
	return "apps-jellyfin"
}

// PreStart ensures directories exist, installs the LDAP plugin, and
// configures network settings.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (bool, error) {
	c.logger.Info("PreStart: creating data directories")
	dirs := []string{
		filepath.Join(state.DataPath, "config"),
		filepath.Join(state.DataPath, "cache"),
		filepath.Join(state.BloudDataPath, "media", "movies"),
		filepath.Join(state.BloudDataPath, "media", "shows"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return false, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	c.logger.Info("PreStart: ensuring LDAP plugin")
	pluginInstalled, err := c.ensureLDAPPlugin(ctx, state.DataPath)
	if err != nil {
		return false, fmt.Errorf("failed to install LDAP plugin: %w", err)
	}

	c.logger.Info("PreStart: configuring network")
	networkChanged, err := c.configureNetwork(state.DataPath)
	if err != nil {
		return false, fmt.Errorf("failed to configure network: %w", err)
	}

	c.logger.Info("PreStart complete", "plugin_installed", pluginInstalled, "network_changed", networkChanged)
	return pluginInstalled || networkChanged, nil
}

func (c *Configurator) ensureLDAPPlugin(ctx context.Context, dataPath string) (bool, error) {
	pluginParent := filepath.Join(dataPath, "config", "plugins")
	pluginDir := filepath.Join(pluginParent, "LDAP-Auth")
	pluginDLL := filepath.Join(pluginDir, "LDAP-Auth.dll")
	if _, err := os.Stat(pluginDLL); err == nil {
		c.logger.Info("LDAP plugin already installed", "path", pluginDLL)
		return false, nil
	}
	c.logger.Info("downloading LDAP plugin", "url", c.pluginURL)
	if err := os.MkdirAll(pluginParent, 0755); err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.pluginURL, nil)
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

	archive, err := os.CreateTemp(pluginParent, ".ldap-plugin-*.zip")
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
	if c.pluginSHA256 != "" && fmt.Sprintf("%x", hash.Sum(nil)) != c.pluginSHA256 {
		return false, fmt.Errorf("download checksum mismatch")
	}

	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return false, err
	}
	defer reader.Close()

	stagingDir, err := os.MkdirTemp(pluginParent, ".LDAP-Auth-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(stagingDir)
	for _, file := range reader.File {
		destination := filepath.Join(stagingDir, file.Name)
		if !strings.HasPrefix(filepath.Clean(destination), filepath.Clean(stagingDir)+string(os.PathSeparator)) {
			return false, fmt.Errorf("plugin archive contains invalid path %q", file.Name)
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
		target, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			source.Close()
			return false, err
		}
		_, copyErr := io.Copy(target, source)
		closeErr := target.Close()
		source.Close()
		if copyErr != nil {
			return false, copyErr
		}
		if closeErr != nil {
			return false, closeErr
		}
	}
	if _, err := os.Stat(filepath.Join(stagingDir, "LDAP-Auth.dll")); err != nil {
		return false, fmt.Errorf("plugin archive did not contain LDAP-Auth.dll")
	}
	if err := os.RemoveAll(pluginDir); err != nil {
		return false, err
	}
	if err := os.Rename(stagingDir, pluginDir); err != nil {
		return false, err
	}
	c.logger.Info("LDAP plugin installed", "path", pluginDir)
	return true, nil
}

// jellyfinNetworkConfig returns the desired XML config for network.xml.
var jellyfinNetworkConfig = xmlutil.ConfigValues{
	"PublishedServerUri":                "http://jellyfin.bloud.local",
	"EnablePublishedServerUriByRequest": "true",
	"KnownProxies":                      []string{"127.0.0.1", "::1"},
	"EnableHttps":                       "false",
	"RequireHttps":                      "false",
	"InternalHttpPort":                  "8096",
	"InternalHttpsPort":                 "8920",
	"PublicHttpPort":                    "8096",
	"PublicHttpsPort":                   "8920",
	"EnableRemoteAccess":                "true",
	"EnableIPv4":                        "true",
	"EnableIPv6":                        "false",
	"EnableUPnP":                        "false",
	"AutoDiscovery":                     "true",
	"IgnoreVirtualInterfaces":           "true",
}

// applyNetworkConfig applies jellyfinNetworkConfig to cfg if not already set.
// Returns true if changes were made.
func applyNetworkConfig(cfg *xmlutil.ConfigFile) bool {
	if cfg.HasConfig(jellyfinNetworkConfig) {
		return false
	}
	cfg.ApplyConfig(jellyfinNetworkConfig)
	return true
}

// configureNetwork creates or updates network.xml with reverse proxy settings.
// Returns true if the file content changed.
func (c *Configurator) configureNetwork(dataPath string) (bool, error) {
	networkPath := filepath.Join(dataPath, "config", "network.xml")

	cfg, err := xmlutil.Open(networkPath, "NetworkConfiguration")
	if err != nil {
		return false, err
	}

	if !applyNetworkConfig(cfg) {
		c.logger.Info("network.xml already configured for reverse proxy")
		return false, nil
	}

	changed, err := cfg.Save()
	if changed {
		c.logger.Info("network.xml configured for reverse proxy support")
	}
	return changed, err
}

// Remove is a no-op for the Jellyfin configurator; container and data removal
// are handled at a higher level by the orchestrator.
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}

// PostStart completes the Jellyfin setup wizard and configures LDAP.
// It runs on a context detached from the convergence pass with its own
// 90 s deadline so the 503-retry loop and network steps survive the pass
// completing. See the inline comment for the context-detach rationale.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// The pass context is short-lived and is cancelled when the pass
	// completes, but PostStart can outlive the pass — the 503-retry loop
	// sleeps between attempts, and the wizard/library/LDAP steps make
	// network calls. If any of those are bound to the pass context, the
	// cancellation surfaces as "context canceled" mid-step, the node goes
	// to terminal ERROR, and the reconciler never retries it.
	//
	// context.Background() detaches completely from the pass so the retry
	// loop and subsequent steps survive the pass completing. A 90 s
	// deadline bounds the work; the outer e2e timeout is the real backstop.
	// (context.WithoutCancel was tried first but did not prevent the
	// cancellation on Go 1.25 linux/amd64 — the retry loop still broke
	// at the ctx.Err() check after the first getSystemInfo call.)
	runCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return c.postStart(runCtx, state)
}

// postStart contains the PostStart body. It receives a detached context with
// its own deadline, so the 503-retry loop and the wizard/library/LDAP steps
// survive the convergence pass completing.
func (c *Configurator) postStart(ctx context.Context, state *configurator.AppState) error {
	c.logger.Info("DBG postStart: entered", "ctx_err", ctx.Err(), "base_url", c.getBaseURL())
	c.logger.Info("PostStart: checking setup wizard status")

	// 1. Check if setup wizard is complete.
	// The container health check (curl -sf /System/Info/Public) passes on the
	// first 200, but Jellyfin oscillates during first-run init — it briefly
	// returns 200 then drops back to 503 "Server is loading" before stabilising.
	// A single 503 here would fail PostStart, which the reconciler treats as a
	// terminal node ERROR it never retries, so wait out the transient instead.
	//
	// ctx is detached from the pass (see PostStart), so the 2 s sleep between
	// attempts cannot be cancelled mid-pass. The loop is bounded by the 90 s
	// deadline on ctx.
	var info *SystemInfo
	var err error
	for i := range 10 {
		c.logger.Info("DBG retry-loop: iteration", "i", i, "ctx_err", ctx.Err())
		if i > 0 {
			select {
			case <-time.After(2 * time.Second):
				c.logger.Info("DBG retry-loop: sleep completed (2s elapsed)")
			case <-ctx.Done():
				c.logger.Info("DBG retry-loop: ctx.Done() fired during sleep", "ctx_err", ctx.Err())
			}
		}
		info, err = c.getSystemInfo(ctx)
		c.logger.Info("DBG retry-loop: getSystemInfo returned", "err", err, "info_nil", info == nil)
		if err == nil {
			c.logger.Info("DBG retry-loop: success, breaking")
			break
		}
		// Break if the context was cancelled (e.g. orchestrator shutdown)
		// or the 90 s deadline expired. A 503 or network error is not a
		// context error — retry it.
		isCanceled := errors.Is(err, context.Canceled)
		isDeadline := errors.Is(err, context.DeadlineExceeded)
		c.logger.Info("DBG retry-loop: error checks", "is_canceled", isCanceled, "is_deadline", isDeadline, "ctx_err", ctx.Err())
		if isCanceled || isDeadline {
			c.logger.Info("DBG retry-loop: context error, breaking")
			break
		}
		c.logger.Info("waiting for Jellyfin API", "attempt", i+1, "error", err)
	}
	c.logger.Info("DBG retry-loop: exited", "final_err", err, "info_nil", info == nil)
	if err != nil {
		return fmt.Errorf("failed to get system info: %w", err)
	}

	if !info.StartupWizardCompleted {
		// Retry a few times — Jellyfin 10.11.9+ may report false during early
		// init, and the API oscillates between 200 and 503 "Server is loading"
		// before stabilising. Retry on 503/network errors (not context
		// errors); the 2 s sleep and 5-iteration cap bound the work.
		for i := range 5 {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
			}
			info, err = c.getSystemInfo(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					break
				}
				// 503 or network error — retry on the next iteration
				c.logger.Info("waiting for Jellyfin API (wizard check)", "attempt", i+1, "error", err)
				continue
			}
			if info.StartupWizardCompleted {
				break
			}
		}
	}
	if err != nil {
		// All 5 retries hit 503/network errors — the API never stabilised
		return fmt.Errorf("failed to get system info: %w", err)
	}

	if !info.StartupWizardCompleted {
		c.logger.Info("completing setup wizard")
		if err := c.completeStartupWizard(ctx); err != nil {
			return fmt.Errorf("failed to complete startup wizard: %w", err)
		}
		c.logger.Info("setup wizard completed")
	} else {
		c.logger.Info("setup wizard already complete")
	}

	// 2. Configure media libraries
	c.logger.Info("PostStart: configuring media libraries")
	if err := c.configureLibraries(ctx); err != nil {
		return fmt.Errorf("failed to configure libraries: %w", err)
	}

	// 3. Configure LDAP if SSO integration is enabled and LDAP output is available
	if state.SSOEnabled && state.LDAP != nil {
		c.logger.Info("PostStart: configuring LDAP", "ldap_host", state.LDAP.Host, "ldap_port", state.LDAP.Port)
		if err := c.configureLDAP(ctx, state); err != nil {
			return fmt.Errorf("failed to configure LDAP: %w", err)
		}
	} else {
		c.logger.Info("PostStart: skipping LDAP config", "sso_enabled", state.SSOEnabled, "ldap_configured", state.LDAP != nil)
	}

	c.logger.Info("PostStart complete")
	return nil
}

// SystemInfo represents the /System/Info response
type SystemInfo struct {
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ID                     string `json:"Id"`
}

// getSystemInfo fetches the system info from Jellyfin
func (c *Configurator) getSystemInfo(ctx context.Context) (*SystemInfo, error) {
	url := c.getBaseURL() + "/System/Info/Public"
	c.logger.Info("DBG getSystemInfo: start", "url", url, "ctx_err", ctx.Err())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		c.logger.Error("DBG getSystemInfo: NewRequestWithContext failed", "error", err)
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.logger.Error("DBG getSystemInfo: Do failed", "error", err, "is_ctx_canceled", errors.Is(err, context.Canceled), "is_deadline", errors.Is(err, context.DeadlineExceeded))
		return nil, err
	}
	defer resp.Body.Close()

	c.logger.Info("DBG getSystemInfo: got response", "status", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		c.logger.Warn("DBG getSystemInfo: non-200 status", "status", resp.StatusCode, "body", string(body[:min(len(body), 200)]))
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var info SystemInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}

	return &info, nil
}

// waitForStartupWizardReady waits for Jellyfin's startup wizard API to be ready.
// Even after /health returns OK, Jellyfin may still return 503 with HTML during initialization.
// In Jellyfin 10.11.9+, /Startup/Configuration returns 401 when the wizard is already complete.
func (c *Configurator) waitForStartupWizardReady(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/Configuration"

	for i := 0; i < 60; i++ { // Wait up to 60 seconds
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}

		contentType := resp.Header.Get("Content-Type")
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK && (contentType == "application/json" || contentType == "application/json; charset=utf-8") {
			c.logger.Info("startup wizard API ready")
			return nil
		}

		// Jellyfin 10.11.9+ returns 401 when the wizard is already complete
		// (the endpoint moves behind auth). Treat this as "already done".
		if resp.StatusCode == http.StatusUnauthorized {
			c.logger.Info("startup wizard already complete (API returned 401)")
			return nil
		}

		c.logger.Info("waiting for startup wizard API", "status", resp.StatusCode, "content_type", contentType)
		time.Sleep(time.Second)
	}

	return fmt.Errorf("startup wizard API not ready after 60 seconds")
}

// completeStartupWizard completes the Jellyfin initial setup wizard
func (c *Configurator) completeStartupWizard(ctx context.Context) error {
	// Wait for the startup wizard API to be ready
	// Jellyfin returns 503 with HTML while initializing, even if /health returns OK
	if err := c.waitForStartupWizardReady(ctx); err != nil {
		return fmt.Errorf("waiting for startup wizard: %w", err)
	}

	// Step 1: Set initial configuration
	if err := c.setStartupConfiguration(ctx); err != nil {
		return fmt.Errorf("setting startup configuration: %w", err)
	}

	// Step 2: Create the bootstrap admin user
	if err := c.setStartupUser(ctx, bootstrapUsername, bootstrapPassword); err != nil {
		return fmt.Errorf("creating startup user: %w", err)
	}

	// Step 3: Configure remote access
	if err := c.setRemoteAccess(ctx); err != nil {
		return fmt.Errorf("setting remote access: %w", err)
	}

	// Step 4: Mark wizard as complete
	if err := c.completeWizard(ctx); err != nil {
		return fmt.Errorf("completing wizard: %w", err)
	}

	return nil
}

// setStartupConfiguration sets the initial configuration
func (c *Configurator) setStartupConfiguration(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/Configuration"

	config := map[string]interface{}{
		"UICulture":                 "en-US",
		"MetadataCountryCode":       "US",
		"PreferredMetadataLanguage": "en",
	}

	body, _ := json.Marshal(config)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// setStartupUser creates the first admin user
// Jellyfin auto-creates an initial user internally, but it may take a moment to be ready.
// We first wait for GET /Startup/User to succeed, then POST to update it.
func (c *Configurator) setStartupUser(ctx context.Context, username, password string) error {
	url := c.getBaseURL() + "/Startup/User"

	// Wait for the initial user to be available (Jellyfin creates it asynchronously)
	var lastErr error
	for i := 0; i < 10; i++ {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			// Initial user is ready, proceed with update
			break
		}

		lastErr = fmt.Errorf("GET /Startup/User returned %d", resp.StatusCode)
		time.Sleep(500 * time.Millisecond)
	}

	if lastErr != nil {
		c.logger.Warn("initial user not ready after retries", "error", lastErr)
	}

	// Now update the user with our credentials
	user := map[string]string{
		"Name":     username,
		"Password": password,
	}

	body, _ := json.Marshal(user)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// setRemoteAccess configures remote access settings
func (c *Configurator) setRemoteAccess(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/RemoteAccess"

	config := map[string]bool{
		"EnableRemoteAccess":         true,
		"EnableAutomaticPortMapping": false,
	}

	body, _ := json.Marshal(config)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// completeWizard marks the startup wizard as complete
func (c *Configurator) completeWizard(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/Complete"

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// VirtualFolder represents a Jellyfin library
type VirtualFolder struct {
	Name           string   `json:"Name"`
	Locations      []string `json:"Locations"`
	CollectionType string   `json:"CollectionType"`
	ItemId         string   `json:"ItemId"`
}

// configureLibraries sets up the default media libraries
func (c *Configurator) configureLibraries(ctx context.Context) error {
	// Authenticate first
	token, err := c.authenticate(ctx, bootstrapUsername, bootstrapPassword)
	if err != nil {
		return fmt.Errorf("authenticating: %w", err)
	}

	// Get existing libraries
	existingLibraries, err := c.getVirtualFolders(ctx, token)
	if err != nil {
		return fmt.Errorf("getting libraries: %w", err)
	}

	// Create a map of existing library names
	existingNames := make(map[string]bool)
	for _, lib := range existingLibraries {
		existingNames[lib.Name] = true
	}

	// Define libraries to create
	libraries := []struct {
		name           string
		collectionType string
		path           string
	}{
		{"Movies", "movies", "/movies"},
		{"Shows", "shows", "/shows"},
	}

	for _, lib := range libraries {
		if existingNames[lib.name] {
			c.logger.Info("media library already exists", "library", lib.name)
			continue
		}

		c.logger.Info("creating media library", "library", lib.name, "path", lib.path)
		if err := c.addVirtualFolder(ctx, token, lib.name, lib.collectionType, lib.path); err != nil {
			return fmt.Errorf("creating library %s: %w", lib.name, err)
		}
	}

	return nil
}

// getVirtualFolders returns all configured libraries
func (c *Configurator) getVirtualFolders(ctx context.Context, token string) ([]VirtualFolder, error) {
	url := c.getBaseURL() + "/Library/VirtualFolders"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var folders []VirtualFolder
	if err := json.NewDecoder(resp.Body).Decode(&folders); err != nil {
		return nil, err
	}

	return folders, nil
}

// addVirtualFolder creates a new library
func (c *Configurator) addVirtualFolder(ctx context.Context, token, name, collectionType, path string) error {
	// The API uses query parameters for the folder metadata
	reqURL := fmt.Sprintf("%s/Library/VirtualFolders?name=%s&collectionType=%s&paths=%s&refreshLibrary=false",
		c.getBaseURL(), url.QueryEscape(name), url.QueryEscape(collectionType), url.QueryEscape(path))

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// LDAPConfig represents the LDAP plugin configuration
type LDAPConfig struct {
	LdapServer                     string   `json:"LdapServer"`
	LdapPort                       int      `json:"LdapPort"`
	UseSsl                         bool     `json:"UseSsl"`
	UseStartTls                    bool     `json:"UseStartTls"`
	SkipSslVerify                  bool     `json:"SkipSslVerify"`
	LdapBindUser                   string   `json:"LdapBindUser"`
	LdapBindPassword               string   `json:"LdapBindPassword"`
	LdapBaseDn                     string   `json:"LdapBaseDn"`
	LdapSearchFilter               string   `json:"LdapSearchFilter"`
	LdapAdminBaseDn                string   `json:"LdapAdminBaseDn"`
	LdapAdminFilter                string   `json:"LdapAdminFilter"`
	EnableLdapAdminFilterMemberUid bool     `json:"EnableLdapAdminFilterMemberUid"`
	LdapSearchAttributes           string   `json:"LdapSearchAttributes"`
	LdapClientCertPath             string   `json:"LdapClientCertPath"`
	LdapClientKeyPath              string   `json:"LdapClientKeyPath"`
	LdapRootCaPath                 string   `json:"LdapRootCaPath"`
	CreateUsersFromLdap            bool     `json:"CreateUsersFromLdap"`
	AllowPassChange                bool     `json:"AllowPassChange"`
	LdapUidAttribute               string   `json:"LdapUidAttribute"`
	LdapUsernameAttribute          string   `json:"LdapUsernameAttribute"`
	LdapPasswordAttribute          string   `json:"LdapPasswordAttribute"`
	EnableLdapProfileImageSync     bool     `json:"EnableLdapProfileImageSync"`
	RemoveImagesNotInLdap          bool     `json:"RemoveImagesNotInLdap"`
	LdapProfileImageAttribute      string   `json:"LdapProfileImageAttribute"`
	EnableAllFolders               bool     `json:"EnableAllFolders"`
	EnabledFolders                 []string `json:"EnabledFolders"`
	PasswordResetUrl               string   `json:"PasswordResetUrl"`
}

// configureLDAP configures the LDAP plugin using the typed LDAP output from AppState.
func (c *Configurator) configureLDAP(ctx context.Context, state *configurator.AppState) error {
	ldap := state.LDAP
	desiredConfig := desiredLDAPConfig(ldap)

	// First, authenticate to get an access token
	token, err := c.authenticate(ctx, bootstrapUsername, bootstrapPassword)
	if err != nil {
		return fmt.Errorf("authenticating: %w", err)
	}

	// Get current LDAP config to check if already configured
	currentConfig, err := c.getPluginConfiguration(ctx, token, ldapPluginID)
	if err != nil {
		c.logger.Warn("could not get LDAP plugin config (plugin may not be installed)", "error", err)
		return nil // Plugin not installed, skip LDAP configuration
	}

	var config LDAPConfig
	if err := json.Unmarshal(currentConfig, &config); err != nil {
		return fmt.Errorf("parsing LDAP config: %w", err)
	}

	if ldapConfigMatchesDesired(config, desiredConfig) {
		c.logger.Info("LDAP already configured")
		return nil
	}

	c.logger.Info("applying LDAP configuration", "ldap_host", ldap.Host, "ldap_port", ldap.Port, "base_dn", ldap.BaseDN)
	configBytes, err := json.Marshal(desiredConfig)
	if err != nil {
		return fmt.Errorf("marshalling LDAP config: %w", err)
	}
	if err := c.setPluginConfiguration(ctx, token, ldapPluginID, configBytes); err != nil {
		return fmt.Errorf("setting LDAP config: %w", err)
	}

	c.logger.Info("LDAP configured successfully")
	return nil
}

func desiredLDAPConfig(ldap *configurator.LDAPOutput) LDAPConfig {
	return LDAPConfig{
		LdapServer:            ldap.Host,
		LdapPort:              ldap.Port,
		UseSsl:                false,
		UseStartTls:           false,
		SkipSslVerify:         true,
		LdapBindUser:          ldap.BindUser,
		LdapBindPassword:      ldap.BindPassword,
		LdapBaseDn:            ldap.BaseDN,
		LdapSearchFilter:      "(objectClass=user)",
		LdapAdminBaseDn:       "",
		LdapAdminFilter:       fmt.Sprintf("(memberOf=cn=authentik Admins,ou=groups,%s)", ldap.BaseDN),
		LdapSearchAttributes:  "uid, cn, mail, displayName, sAMAccountName",
		LdapUidAttribute:      "sAMAccountName",
		LdapUsernameAttribute: "cn",
		LdapPasswordAttribute: "userPassword",
		CreateUsersFromLdap:   true,
		AllowPassChange:       false,
		EnableAllFolders:      true,
		EnabledFolders:        []string{},
	}
}

func ldapConfigMatchesDesired(current, desired LDAPConfig) bool {
	return current.LdapServer == desired.LdapServer &&
		current.LdapPort == desired.LdapPort &&
		current.UseSsl == desired.UseSsl &&
		current.UseStartTls == desired.UseStartTls &&
		current.SkipSslVerify == desired.SkipSslVerify &&
		current.LdapBindUser == desired.LdapBindUser &&
		current.LdapBindPassword == desired.LdapBindPassword &&
		current.LdapBaseDn == desired.LdapBaseDn &&
		current.LdapSearchFilter == desired.LdapSearchFilter &&
		current.LdapAdminBaseDn == desired.LdapAdminBaseDn &&
		current.LdapAdminFilter == desired.LdapAdminFilter &&
		current.LdapSearchAttributes == desired.LdapSearchAttributes &&
		current.LdapUidAttribute == desired.LdapUidAttribute &&
		current.LdapUsernameAttribute == desired.LdapUsernameAttribute &&
		current.LdapPasswordAttribute == desired.LdapPasswordAttribute &&
		current.CreateUsersFromLdap == desired.CreateUsersFromLdap &&
		current.AllowPassChange == desired.AllowPassChange &&
		current.EnableAllFolders == desired.EnableAllFolders
}

// AuthResponse represents the authentication response
type AuthResponse struct {
	AccessToken string `json:"AccessToken"`
	User        struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	} `json:"User"`
}

// authenticate logs in to Jellyfin and returns an access token
func (c *Configurator) authenticate(ctx context.Context, username, password string) (string, error) {
	url := c.getBaseURL() + "/Users/AuthenticateByName"

	body := map[string]string{
		"Username": username,
		"Pw":       password,
	}

	bodyBytes, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// Jellyfin 10.11+ only parses Client/Device from the Authorization header,
	// not X-Emby-Authorization. Using the wrong header causes request.App=null crashes.
	req.Header.Set("Authorization", `MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0"`)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("authentication failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var authResp AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return "", err
	}

	return authResp.AccessToken, nil
}

// getPluginConfiguration fetches a plugin's configuration
func (c *Configurator) getPluginConfiguration(ctx context.Context, token, pluginID string) ([]byte, error) {
	url := fmt.Sprintf("%s/Plugins/%s/Configuration", c.getBaseURL(), pluginID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return io.ReadAll(resp.Body)
}

// setPluginConfiguration updates a plugin's configuration
func (c *Configurator) setPluginConfiguration(ctx context.Context, token, pluginID string, config []byte) error {
	url := fmt.Sprintf("%s/Plugins/%s/Configuration", c.getBaseURL(), pluginID)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(config))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// User represents a Jellyfin user
type User struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

// getUsers fetches all users from Jellyfin
func (c *Configurator) getUsers(ctx context.Context, token string) ([]User, error) {
	url := c.getBaseURL() + "/Users"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var users []User
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}

	return users, nil
}

// deleteUser deletes a user by ID
func (c *Configurator) deleteUser(ctx context.Context, token, userID string) error {
	url := fmt.Sprintf("%s/Users/%s", c.getBaseURL(), userID)

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// deleteBootstrapAdmin removes the bootstrap admin user
func (c *Configurator) deleteBootstrapAdmin(ctx context.Context, token string) error {
	users, err := c.getUsers(ctx, token)
	if err != nil {
		return fmt.Errorf("getting users: %w", err)
	}

	for _, user := range users {
		if user.Name == bootstrapUsername {
			c.logger.Info("deleting bootstrap admin user")
			if err := c.deleteUser(ctx, token, user.ID); err != nil {
				return fmt.Errorf("deleting user: %w", err)
			}
			c.logger.Info("bootstrap admin deleted")
			return nil
		}
	}

	c.logger.Info("bootstrap admin not found (may already be deleted)")
	return nil
}
