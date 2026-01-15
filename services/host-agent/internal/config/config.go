package config

import (
	"os"
	"path/filepath"
	"strconv"
)

// Config holds the application configuration
type Config struct {
	Port         int
	DataDir      string
	AppsDir      string // Path to apps/ directory containing app definitions
	NixConfigDir string
	FlakePath    string // Path to flake.nix for nixos-rebuild
	FlakeTarget  string // Flake target for nixos-rebuild (e.g., "vm-dev", "vm-test")
	NixosPath    string // Path to nixos/ modules directory
	DatabaseURL  string // PostgreSQL connection string
	// SSO configuration
	SSOHostSecret  string // Master secret for deriving client secrets
	SSOBaseURL     string // Base URL for callbacks (e.g., "http://localhost:8080")
	AuthentikToken string // Authentik API token for SSO cleanup
	// Authentik bootstrap configuration
	AuthentikPort          int
	AuthentikAdminPassword string
	AuthentikAdminEmail    string
	// LDAP configuration
	LDAPBindPassword string
}

// Load reads configuration from environment variables with sensible defaults
func Load() *Config {
	dataDir := getEnv("BLOUD_DATA_DIR", getDefaultDataDir())
	appsDir := getEnv("BLOUD_APPS_DIR", "../../apps")

	// FlakePath and NixosPath default to being relative to apps dir
	// but can be overridden for dev environments where apps is synced separately
	defaultFlakePath := filepath.Clean(filepath.Join(appsDir, ".."))
	defaultNixosPath := filepath.Clean(filepath.Join(appsDir, "..", "nixos"))

	cfg := &Config{
		Port:                   getEnvAsInt("BLOUD_PORT", 3000),
		DataDir:                dataDir,
		AppsDir:                appsDir,
		NixConfigDir:           getEnv("BLOUD_NIX_CONFIG_DIR", filepath.Join(dataDir, "nix")),
		FlakePath:              getEnv("BLOUD_FLAKE_PATH", defaultFlakePath),
		FlakeTarget:            getEnv("BLOUD_FLAKE_TARGET", "vm-dev"),
		NixosPath:              getEnv("BLOUD_NIXOS_PATH", defaultNixosPath),
		// Default matches postgres module defaults - NixOS injects actual values via DATABASE_URL
		DatabaseURL:            getEnv("DATABASE_URL", "postgres://apps:testpass123@localhost:5432/bloud?sslmode=disable"),
		SSOHostSecret:          getEnv("BLOUD_SSO_HOST_SECRET", "dev-secret-change-in-production"),
		SSOBaseURL:             getEnv("BLOUD_SSO_BASE_URL", "http://localhost:8080"),
		AuthentikToken:         getEnv("BLOUD_AUTHENTIK_TOKEN", "test-bootstrap-token-change-in-production"),
		AuthentikPort:          getEnvAsInt("BLOUD_AUTHENTIK_PORT", 9001),
		AuthentikAdminPassword: getEnv("BLOUD_AUTHENTIK_ADMIN_PASSWORD", "password"),
		AuthentikAdminEmail:    getEnv("BLOUD_AUTHENTIK_ADMIN_EMAIL", "admin@localhost"),
		LDAPBindPassword:       getEnv("BLOUD_LDAP_BIND_PASSWORD", "ldap-bind-password-change-in-production"),
	}

	return cfg
}

// getDefaultDataDir returns the default data directory path
func getDefaultDataDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/bloud"
	}
	return filepath.Join(homeDir, ".local", "share", "bloud")
}

// getEnv reads an environment variable or returns a default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvAsInt reads an environment variable as an integer or returns a default value
func getEnvAsInt(key string, defaultValue int) int {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}

	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return defaultValue
	}

	return value
}
