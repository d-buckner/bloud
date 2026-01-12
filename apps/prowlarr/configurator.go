package prowlarr

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// Configurator handles Prowlarr configuration
type Configurator struct {
	Port    int
	baseURL string // Override for testing; if empty, uses localhost:Port
}

// NewConfigurator creates a new Prowlarr configurator
func NewConfigurator(port int) *Configurator {
	if port == 0 {
		port = 9696
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
	return "prowlarr"
}

// PreStart ensures directories exist.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	configDir := filepath.Join(state.DataPath, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", configDir, err)
	}
	return nil
}

// HealthCheck waits for Prowlarr's API to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	url := c.getBaseURL() + "/api/v1/health"
	return configurator.WaitForHTTP(ctx, url, 60*time.Second)
}

// PostStart configures Prowlarr with Radarr/Sonarr as applications
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// Get Prowlarr API key
	apiKey, err := c.getAPIKey(state.DataPath)
	if err != nil {
		return fmt.Errorf("failed to get API key: %w", err)
	}

	// Try to add Radarr as an application (if installed)
	if err := c.ensureApplication(ctx, apiKey, state, "radarr", "Radarr", 7878); err != nil {
		// Log but don't fail - Radarr might not be installed
		fmt.Printf("Note: Could not configure Radarr in Prowlarr: %v\n", err)
	}

	// Try to add Sonarr as an application (if installed)
	if err := c.ensureApplication(ctx, apiKey, state, "sonarr", "Sonarr", 8989); err != nil {
		// Log but don't fail - Sonarr might not be installed
		fmt.Printf("Note: Could not configure Sonarr in Prowlarr: %v\n", err)
	}

	return nil
}

// getAPIKey extracts the API key from Prowlarr's config.xml
func (c *Configurator) getAPIKey(dataPath string) (string, error) {
	configPath := filepath.Join(dataPath, "config", "config.xml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("failed to read config.xml: %w", err)
	}

	var config struct {
		APIKey string `xml:"ApiKey"`
	}
	if err := xml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("failed to parse config.xml: %w", err)
	}

	if config.APIKey == "" {
		return "", fmt.Errorf("API key not found in config.xml")
	}

	return config.APIKey, nil
}

// getArrAPIKey reads the API key from another *arr app's config.xml
func (c *Configurator) getArrAPIKey(bloudDataPath, appName string) (string, error) {
	configPath := filepath.Join(bloudDataPath, appName, "config", "config.xml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err // App not installed or not configured yet
	}

	var config struct {
		APIKey string `xml:"ApiKey"`
	}
	if err := xml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("failed to parse config.xml: %w", err)
	}

	return config.APIKey, nil
}

// ensureApplication adds an *arr app as an application in Prowlarr
func (c *Configurator) ensureApplication(ctx context.Context, prowlarrAPIKey string, state *configurator.AppState, appName, appDisplayName string, appPort int) error {
	// Get the app's API key
	appAPIKey, err := c.getArrAPIKey(state.BloudDataPath, appName)
	if err != nil {
		return fmt.Errorf("%s not installed or not ready: %w", appName, err)
	}

	// Check if already configured
	apps, err := c.getApplications(ctx, prowlarrAPIKey)
	if err != nil {
		return err
	}

	bloudAppName := fmt.Sprintf("Bloud: %s", appDisplayName)
	for _, app := range apps {
		if app.Name == bloudAppName {
			return nil // Already configured
		}
	}

	// Create the application
	application := map[string]any{
		"name":             bloudAppName,
		"implementation":   appDisplayName,
		"configContract":   fmt.Sprintf("%sSettings", appDisplayName),
		"syncLevel":        "fullSync",
		"syncCategories":   c.getSyncCategories(appName),
		"fields": []map[string]any{
			{"name": "prowlarrUrl", "value": fmt.Sprintf("http://localhost:%d", c.Port)},
			{"name": "baseUrl", "value": fmt.Sprintf("http://localhost:%d", appPort)},
			{"name": "apiKey", "value": appAPIKey},
		},
	}

	return c.createApplication(ctx, prowlarrAPIKey, application)
}

// getSyncCategories returns appropriate category IDs for the app type
func (c *Configurator) getSyncCategories(appName string) []int {
	switch appName {
	case "radarr":
		// Movie categories
		return []int{2000, 2010, 2020, 2030, 2040, 2045, 2050, 2060}
	case "sonarr":
		// TV categories
		return []int{5000, 5010, 5020, 5030, 5040, 5045, 5050, 5060}
	default:
		return []int{}
	}
}

type application struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (c *Configurator) getApplications(ctx context.Context, apiKey string) ([]application, error) {
	url := c.getBaseURL() + "/api/v1/applications"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(body))
	}

	var apps []application
	if err := json.NewDecoder(resp.Body).Decode(&apps); err != nil {
		return nil, err
	}

	return apps, nil
}

func (c *Configurator) createApplication(ctx context.Context, apiKey string, app map[string]any) error {
	url := c.getBaseURL() + "/api/v1/applications"

	body, err := json.Marshal(app)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create application: %d - %s", resp.StatusCode, string(respBody))
	}

	return nil
}
