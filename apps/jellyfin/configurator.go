package jellyfin

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

// Default admin credentials for Jellyfin
const (
	defaultAdminUser     = "admin"
	defaultAdminPassword = "admin123"
)

// Configurator handles Jellyfin configuration
type Configurator struct {
	Port    int
	baseURL string // Override for testing; if empty, uses localhost:Port
}

// NewConfigurator creates a new Jellyfin configurator
func NewConfigurator(port int) *Configurator {
	if port == 0 {
		port = 8096
	}
	return &Configurator{Port: port}
}

// getBaseURL returns the base URL for API calls
func (c *Configurator) getBaseURL() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return fmt.Sprintf("http://localhost:%d", c.Port)
}

func (c *Configurator) Name() string {
	return "jellyfin"
}

// PreStart ensures directories exist.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	dirs := []string{
		filepath.Join(state.DataPath, "config"),
		filepath.Join(state.DataPath, "cache"),
		filepath.Join(state.BloudDataPath, "movies"),
		filepath.Join(state.BloudDataPath, "tv"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return nil
}

// HealthCheck waits for Jellyfin's API to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	url := c.getBaseURL() + "/health"
	return configurator.WaitForHTTP(ctx, url, 60*time.Second)
}

// PostStart completes the Jellyfin setup wizard if not already done,
// and configures media libraries.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// Check if setup wizard is already completed
	completed, err := c.isWizardCompleted(ctx)
	if err != nil {
		return fmt.Errorf("failed to check wizard status: %w", err)
	}

	if !completed {
		if err := c.completeSetupWizard(ctx); err != nil {
			return fmt.Errorf("failed to complete setup wizard: %w", err)
		}
	}

	// TODO: Configure media libraries for /movies and /tv
	// This requires authentication after wizard completion

	return nil
}

// isWizardCompleted checks if the startup wizard has been completed
func (c *Configurator) isWizardCompleted(ctx context.Context) (bool, error) {
	url := c.getBaseURL() + "/System/Info/Public"

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

	var info struct {
		StartupWizardCompleted bool `json:"StartupWizardCompleted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return false, err
	}

	return info.StartupWizardCompleted, nil
}

// completeSetupWizard runs through the Jellyfin startup wizard API
func (c *Configurator) completeSetupWizard(ctx context.Context) error {
	baseURL := c.getBaseURL()

	// Step 1: Set startup configuration (server name, locale)
	configPayload := map[string]string{
		"UICulture":                 "en-US",
		"MetadataCountryCode":       "US",
		"PreferredMetadataLanguage": "en",
	}
	if err := c.postJSON(ctx, baseURL+"/Startup/Configuration", configPayload); err != nil {
		return fmt.Errorf("failed to set configuration: %w", err)
	}

	// Step 2: Create admin user
	userPayload := map[string]string{
		"Name":     defaultAdminUser,
		"Password": defaultAdminPassword,
	}
	if err := c.postJSON(ctx, baseURL+"/Startup/User", userPayload); err != nil {
		return fmt.Errorf("failed to create admin user: %w", err)
	}

	// Step 3: Enable remote access
	remotePayload := map[string]bool{
		"EnableRemoteAccess":    true,
		"EnableAutomaticPortMap": false,
	}
	if err := c.postJSON(ctx, baseURL+"/Startup/RemoteAccess", remotePayload); err != nil {
		return fmt.Errorf("failed to enable remote access: %w", err)
	}

	// Step 4: Complete the wizard
	if err := c.postJSON(ctx, baseURL+"/Startup/Complete", nil); err != nil {
		return fmt.Errorf("failed to complete wizard: %w", err)
	}

	return nil
}

// postJSON sends a POST request with JSON body
func (c *Configurator) postJSON(ctx context.Context, url string, payload any) error {
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
