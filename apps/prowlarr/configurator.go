package prowlarr

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
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/xmlutil"
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

// PreStart ensures directories exist and configures external authentication.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	configDir := filepath.Join(state.DataPath, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", configDir, err)
	}

	// Configure external authentication for forward-auth
	// This must be done before Prowlarr starts to avoid double-auth prompts
	if err := c.configureExternalAuth(state.DataPath); err != nil {
		return fmt.Errorf("failed to configure external auth: %w", err)
	}

	return nil
}

// configureExternalAuth sets AuthenticationMethod to External in config.xml
// This allows Prowlarr to trust forward-auth from Traefik/Authentik
func (c *Configurator) configureExternalAuth(dataPath string) error {
	configPath := filepath.Join(dataPath, "config", "config.xml")

	cfg, err := xmlutil.Open(configPath, "Config")
	if err != nil {
		return err
	}

	// Check if already configured
	if cfg.GetElement("AuthenticationMethod") == "External" {
		return nil
	}

	cfg.SetElement("AuthenticationMethod", "External")
	cfg.SetElement("AuthenticationRequired", "Enabled")

	return cfg.Save()
}

// HealthCheck waits for Prowlarr's API to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	// Use /ping endpoint which doesn't require authentication
	url := c.getBaseURL() + "/ping"
	return configurator.WaitForHTTP(ctx, url, 60*time.Second)
}

// PostStart configures Prowlarr with Radarr/Sonarr as applications
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// Get Prowlarr API key
	apiKey, err := c.getAPIKey(state.DataPath)
	if err != nil {
		return fmt.Errorf("failed to get API key: %w", err)
	}

	// Setup Internet Archive indexer for public domain content
	if err := c.ensureInternetArchiveIndexer(ctx, apiKey); err != nil {
		fmt.Printf("Note: Could not configure Internet Archive indexer: %v\n", err)
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

	cfg, err := xmlutil.OpenExisting(configPath)
	if err != nil {
		return "", err
	}

	apiKey := cfg.GetElement("ApiKey")
	if apiKey == "" {
		return "", fmt.Errorf("API key not found in config.xml")
	}

	return apiKey, nil
}

// getArrAPIKey reads the API key from another *arr app's config.xml
func (c *Configurator) getArrAPIKey(bloudDataPath, appName string) (string, error) {
	configPath := filepath.Join(bloudDataPath, appName, "config", "config.xml")

	cfg, err := xmlutil.OpenExisting(configPath)
	if err != nil {
		return "", err // App not installed or not configured yet
	}

	return cfg.GetElement("ApiKey"), nil
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

	// Build URLs for container-to-container communication
	// Prowlarr needs to reach the apps via container DNS names, not localhost
	// Container names in Bloud are just the app name (e.g., "radarr", "sonarr")
	prowlarrURL := c.getProwlarrURL()
	appURL := fmt.Sprintf("http://%s:%d", appName, appPort)

	// Create the application
	application := map[string]any{
		"name":           bloudAppName,
		"implementation": appDisplayName,
		"configContract": fmt.Sprintf("%sSettings", appDisplayName),
		"syncLevel":      "fullSync",
		"syncCategories": c.getSyncCategories(appName),
		"tags":           []int{},
		"fields": []map[string]any{
			{"name": "prowlarrUrl", "value": prowlarrURL},
			{"name": "baseUrl", "value": appURL},
			{"name": "apiKey", "value": appAPIKey},
		},
	}

	return c.createApplication(ctx, prowlarrAPIKey, application)
}

// getProwlarrURL returns the URL that other apps should use to reach Prowlarr
func (c *Configurator) getProwlarrURL() string {
	if c.baseURL != "" {
		return c.baseURL // For testing
	}
	return fmt.Sprintf("http://prowlarr:%d", c.Port)
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

// ensureInternetArchiveIndexer adds the Internet Archive as an indexer for public domain content
func (c *Configurator) ensureInternetArchiveIndexer(ctx context.Context, apiKey string) error {
	// Check if already configured
	indexers, err := c.getIndexers(ctx, apiKey)
	if err != nil {
		return err
	}

	for _, idx := range indexers {
		if idx.Implementation == "InternetArchive" {
			return nil // Already configured
		}
	}

	// Get the schema for Internet Archive indexer
	schema, err := c.getIndexerSchema(ctx, apiKey, "InternetArchive")
	if err != nil {
		return fmt.Errorf("failed to get Internet Archive schema: %w", err)
	}

	// Configure the indexer fields
	// Enable "noMagnet" since archive.org often doesn't have magnet links
	c.setSchemaField(schema, "noMagnet", true)

	// Set categories for movies and TV
	schema["enable"] = true
	schema["appProfileId"] = 1 // Default profile

	return c.createIndexer(ctx, apiKey, schema)
}

type indexer struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
}

func (c *Configurator) getIndexers(ctx context.Context, apiKey string) ([]indexer, error) {
	url := c.getBaseURL() + "/api/v1/indexer"

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

	var indexers []indexer
	if err := json.NewDecoder(resp.Body).Decode(&indexers); err != nil {
		return nil, err
	}

	return indexers, nil
}

func (c *Configurator) getIndexerSchema(ctx context.Context, apiKey, implementation string) (map[string]any, error) {
	url := c.getBaseURL() + "/api/v1/indexer/schema"

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

	var schemas []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&schemas); err != nil {
		return nil, err
	}

	for _, schema := range schemas {
		if schema["implementation"] == implementation {
			return schema, nil
		}
	}

	return nil, fmt.Errorf("schema not found for %s", implementation)
}

func (c *Configurator) setSchemaField(schema map[string]any, fieldName string, value any) {
	fields, ok := schema["fields"].([]any)
	if !ok {
		return
	}

	for _, f := range fields {
		field, ok := f.(map[string]any)
		if !ok {
			continue
		}
		if field["name"] == fieldName {
			field["value"] = value
			return
		}
	}
}

func (c *Configurator) createIndexer(ctx context.Context, apiKey string, indexer map[string]any) error {
	url := c.getBaseURL() + "/api/v1/indexer"

	body, err := json.Marshal(indexer)
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
		return fmt.Errorf("failed to create indexer: %d - %s", resp.StatusCode, string(respBody))
	}

	return nil
}
