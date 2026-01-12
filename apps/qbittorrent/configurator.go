package qbittorrent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// Configurator handles qBittorrent configuration
type Configurator struct {
	// Port is the host port qBittorrent is exposed on (default 8086)
	Port int
}

// NewConfigurator creates a new qBittorrent configurator
func NewConfigurator(port int) *Configurator {
	if port == 0 {
		port = 8086
	}
	return &Configurator{Port: port}
}

func (c *Configurator) Name() string {
	return "qbittorrent"
}

// PreStart ensures the config directory and file exist with required settings.
// This runs before/after the container starts and must be idempotent.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	// Ensure directories exist
	configDir := filepath.Join(state.DataPath, "config", "qBittorrent")
	downloadDir := filepath.Join(state.BloudDataPath, "downloads") // Shared downloads folder

	for _, dir := range []string{configDir, downloadDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Ensure config file has required settings for Bloud integration
	configPath := filepath.Join(configDir, "qBittorrent.conf")
	ini, err := configurator.LoadINI(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Set required keys for iframe embedding and reverse proxy
	ini.EnsureKeys("Preferences", map[string]string{
		"WebUI\\HostHeaderValidation":      "false",
		"WebUI\\CSRFProtection":            "false",
		"WebUI\\ClickjackingProtection":    "false",
		"WebUI\\AuthSubnetWhitelistEnabled": "true",
		"WebUI\\AuthSubnetWhitelist":       "127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16",
		"Downloads\\SavePath":              "/downloads",
	})

	// Only set these on fresh installs (section doesn't exist or empty)
	prefs := ini.Section("Preferences")
	if _, hasLocale := prefs.Get("General\\Locale"); !hasLocale {
		prefs.Set("General\\Locale", "en")
		prefs.Set("Downloads\\PreAllocation", "true")
	}

	// Set BitTorrent defaults if section doesn't exist
	bt := ini.Section("BitTorrent")
	if _, hasPath := bt.Get("Session\\DefaultSavePath"); !hasPath {
		bt.Set("Session\\DefaultSavePath", "/downloads")
	}

	if err := ini.Save(configPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	return nil
}

// HealthCheck waits for qBittorrent's WebUI to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	url := fmt.Sprintf("http://localhost:%d/api/v2/app/version", c.Port)
	return configurator.WaitForHTTPWithAuth(ctx, url, 30*time.Second)
}

// PostStart is a no-op for qBittorrent.
// qBittorrent is a "source" app - other apps integrate with it,
// but it doesn't need to integrate with anything else.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	return nil
}
