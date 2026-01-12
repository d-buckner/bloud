package sonarr

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

// Configurator handles Sonarr configuration
type Configurator struct {
	Port    int
	baseURL string // Override for testing; if empty, uses localhost:Port
}

// NewConfigurator creates a new Sonarr configurator
func NewConfigurator(port int) *Configurator {
	if port == 0 {
		port = 8989
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
	return "sonarr"
}

// PreStart ensures directories exist.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	dirs := []string{
		filepath.Join(state.DataPath, "config"),
		filepath.Join(state.BloudDataPath, "downloads"),
		filepath.Join(state.BloudDataPath, "tv"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return nil
}

// HealthCheck waits for Sonarr's API to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	url := c.getBaseURL() + "/api/v3/system/status"
	return configurator.WaitForHTTP(ctx, url, 60*time.Second)
}

// PostStart configures Sonarr with download client and root folder
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// Get API key from config.xml
	apiKey, err := c.getAPIKey(state.DataPath)
	if err != nil {
		return fmt.Errorf("failed to get API key: %w", err)
	}

	// Ensure download client is configured
	if err := c.ensureDownloadClient(ctx, apiKey, state); err != nil {
		return fmt.Errorf("failed to configure download client: %w", err)
	}

	// Ensure root folder is configured
	if err := c.ensureRootFolder(ctx, apiKey); err != nil {
		return fmt.Errorf("failed to configure root folder: %w", err)
	}

	return nil
}

// getAPIKey extracts the API key from Sonarr's config.xml
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

// ensureDownloadClient ensures qBittorrent is configured as a download client
func (c *Configurator) ensureDownloadClient(ctx context.Context, apiKey string, state *configurator.AppState) error {
	// Check if qBittorrent is an integration
	sources := state.Integrations["downloadClient"]
	if len(sources) == 0 {
		return nil // No download client configured
	}

	// Check if we already have this download client
	clients, err := c.getDownloadClients(ctx, apiKey)
	if err != nil {
		return err
	}

	for _, client := range clients {
		if client.Name == "Bloud: qBittorrent" {
			return nil // Already configured
		}
	}

	// Create the download client
	client := map[string]any{
		"name":           "Bloud: qBittorrent",
		"implementation": "QBittorrent",
		"configContract": "QBittorrentSettings",
		"enable":         true,
		"priority":       1,
		"fields": []map[string]any{
			{"name": "host", "value": "localhost"},
			{"name": "port", "value": 8086},
			{"name": "useSsl", "value": false},
			{"name": "username", "value": "admin"},
			{"name": "password", "value": "adminadmin"},
			{"name": "tvCategory", "value": "sonarr"},
		},
	}

	return c.createDownloadClient(ctx, apiKey, client)
}

type downloadClient struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (c *Configurator) getDownloadClients(ctx context.Context, apiKey string) ([]downloadClient, error) {
	url := c.getBaseURL() + "/api/v3/downloadclient"

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

	var clients []downloadClient
	if err := json.NewDecoder(resp.Body).Decode(&clients); err != nil {
		return nil, err
	}

	return clients, nil
}

func (c *Configurator) createDownloadClient(ctx context.Context, apiKey string, client map[string]any) error {
	url := c.getBaseURL() + "/api/v3/downloadclient"

	body, err := json.Marshal(client)
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
		return fmt.Errorf("failed to create download client: %d - %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// ensureRootFolder ensures /tv is configured as a root folder
func (c *Configurator) ensureRootFolder(ctx context.Context, apiKey string) error {
	// Check if root folder already exists
	folders, err := c.getRootFolders(ctx, apiKey)
	if err != nil {
		return err
	}

	for _, folder := range folders {
		if folder.Path == "/tv" {
			return nil // Already configured
		}
	}

	// Create the root folder
	return c.createRootFolder(ctx, apiKey, "/tv")
}

type rootFolder struct {
	ID   int    `json:"id"`
	Path string `json:"path"`
}

func (c *Configurator) getRootFolders(ctx context.Context, apiKey string) ([]rootFolder, error) {
	url := c.getBaseURL() + "/api/v3/rootfolder"

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

	var folders []rootFolder
	if err := json.NewDecoder(resp.Body).Decode(&folders); err != nil {
		return nil, err
	}

	return folders, nil
}

func (c *Configurator) createRootFolder(ctx context.Context, apiKey string, path string) error {
	url := c.getBaseURL() + "/api/v3/rootfolder"

	body, err := json.Marshal(map[string]string{"path": path})
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
		return fmt.Errorf("failed to create root folder: %d - %s", resp.StatusCode, string(respBody))
	}

	return nil
}
