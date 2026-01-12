package jellyseerr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// Default Jellyfin credentials (must match Jellyfin configurator)
const (
	jellyfinAdminUser     = "admin"
	jellyfinAdminPassword = "admin123"
	jellyfinAdminEmail    = "admin@bloud.local"
	jellyfinPort          = 8096
)

// Configurator handles Jellyseerr configuration
type Configurator struct {
	Port       int
	baseURL    string // Override for testing; if empty, uses localhost:Port
	jellyfinURL string // Override for testing; if empty, uses localhost:jellyfinPort
}

// NewConfigurator creates a new Jellyseerr configurator
func NewConfigurator(port int) *Configurator {
	if port == 0 {
		port = 5055
	}
	return &Configurator{Port: port}
}

// getBaseURL returns the base URL for Jellyseerr API calls
func (c *Configurator) getBaseURL() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return fmt.Sprintf("http://localhost:%d", c.Port)
}

// getJellyfinURL returns the Jellyfin URL for authentication
func (c *Configurator) getJellyfinURL() string {
	if c.jellyfinURL != "" {
		return c.jellyfinURL
	}
	return fmt.Sprintf("http://localhost:%d", jellyfinPort)
}

func (c *Configurator) Name() string {
	return "jellyseerr"
}

// PreStart ensures directories exist.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	configDir := filepath.Join(state.DataPath, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", configDir, err)
	}
	return nil
}

// HealthCheck waits for Jellyseerr's API to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	url := c.getBaseURL() + "/api/v1/status"
	return configurator.WaitForHTTP(ctx, url, 60*time.Second)
}

// PostStart completes the Jellyseerr setup wizard if not already done.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// Check if setup wizard is already completed
	initialized, err := c.isInitialized(ctx)
	if err != nil {
		return fmt.Errorf("failed to check initialization status: %w", err)
	}

	if !initialized {
		if err := c.completeSetupWizard(ctx); err != nil {
			return fmt.Errorf("failed to complete setup wizard: %w", err)
		}
	}

	// TODO: Configure Radarr/Sonarr integrations if they are installed

	return nil
}

// isInitialized checks if Jellyseerr setup has been completed
func (c *Configurator) isInitialized(ctx context.Context) (bool, error) {
	url := c.getBaseURL() + "/api/v1/settings/public"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("API returned %d", resp.StatusCode)
	}

	var settings struct {
		Initialized bool `json:"initialized"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		return false, err
	}

	return settings.Initialized, nil
}

// completeSetupWizard runs through the Jellyseerr setup process
func (c *Configurator) completeSetupWizard(ctx context.Context) error {
	baseURL := c.getBaseURL()
	jellyfinURL := c.getJellyfinURL()

	// Step 1: Authenticate with Jellyfin
	// This creates the admin user in Jellyseerr and connects to Jellyfin
	authPayload := map[string]string{
		"username": jellyfinAdminUser,
		"password": jellyfinAdminPassword,
		"hostname": jellyfinURL,
		"email":    jellyfinAdminEmail,
	}
	cookies, err := c.postJSONWithCookies(ctx, baseURL+"/api/v1/auth/jellyfin", authPayload)
	if err != nil {
		return fmt.Errorf("failed to authenticate with Jellyfin: %w", err)
	}

	// Step 2: Sync and get libraries from Jellyfin
	libraries, err := c.getLibraries(ctx, baseURL, cookies)
	if err != nil {
		return fmt.Errorf("failed to sync libraries: %w", err)
	}

	// Step 3: Enable all libraries
	if len(libraries) > 0 {
		if err := c.enableLibraries(ctx, baseURL, cookies, libraries); err != nil {
			return fmt.Errorf("failed to enable libraries: %w", err)
		}
	}

	// Step 4: Complete initialization
	if err := c.postJSONWithAuth(ctx, baseURL+"/api/v1/settings/initialize", nil, cookies); err != nil {
		return fmt.Errorf("failed to complete initialization: %w", err)
	}

	return nil
}

// Library represents a Jellyfin library
type Library struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// getLibraries syncs and retrieves libraries from Jellyfin
func (c *Configurator) getLibraries(ctx context.Context, baseURL string, cookies []*http.Cookie) ([]Library, error) {
	url := baseURL + "/api/v1/settings/jellyfin/library?sync=true"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(body))
	}

	var libraries []Library
	if err := json.NewDecoder(resp.Body).Decode(&libraries); err != nil {
		return nil, err
	}

	return libraries, nil
}

// enableLibraries enables the specified libraries in Jellyseerr
func (c *Configurator) enableLibraries(ctx context.Context, baseURL string, cookies []*http.Cookie, libraries []Library) error {
	// Build comma-separated list of library IDs
	var ids string
	for i, lib := range libraries {
		if i > 0 {
			ids += ","
		}
		ids += lib.ID
	}

	url := fmt.Sprintf("%s/api/v1/settings/jellyfin/library?enable=%s", baseURL, ids)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// postJSONWithCookies sends a POST request and returns the response cookies
func (c *Configurator) postJSONWithCookies(ctx context.Context, url string, payload any) ([]*http.Cookie, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
	}

	return resp.Cookies(), nil
}

// postJSONWithAuth sends a POST request with authentication cookies
func (c *Configurator) postJSONWithAuth(ctx context.Context, url string, payload any, cookies []*http.Cookie) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
