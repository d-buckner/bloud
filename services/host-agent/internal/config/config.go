package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/secrets"
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
	SSOHostSecret    string // Master secret for deriving client secrets
	SSOBaseURL       string // Base URL for callbacks (e.g., "http://localhost:8080")
	SSOAuthentikURL  string // Authentik external URL for discovery (e.g., "http://localhost:8080")
	AuthentikToken   string // Authentik API token for SSO cleanup
	// Authentik bootstrap configuration
	AuthentikPort          int
	AuthentikAdminPassword string
	AuthentikAdminEmail    string
	// LDAP configuration
	LDAPBindPassword string
	// Secrets manager for accessing generated secrets
	Secrets *secrets.Manager
}

// Load reads configuration from environment variables with sensible defaults.
// It also initializes the secrets manager and uses generated secrets for
// any values not explicitly set via environment variables.
func Load() *Config {
	return LoadWithLogger(slog.Default())
}

// LoadWithLogger is like Load but allows specifying a logger.
func LoadWithLogger(logger *slog.Logger) *Config {
	dataDir := getEnv("BLOUD_DATA_DIR", getDefaultDataDir())
	appsDir := getEnv("BLOUD_APPS_DIR", "../../apps")

	// FlakePath and NixosPath default to being relative to apps dir
	// but can be overridden for dev environments where apps is synced separately
	defaultFlakePath := filepath.Clean(filepath.Join(appsDir, ".."))
	defaultNixosPath := filepath.Clean(filepath.Join(appsDir, "..", "nixos"))

	// Initialize secrets manager
	secretsPath := filepath.Join(dataDir, "secrets.json")
	secretsMgr := secrets.NewManager(secretsPath)
	if err := secretsMgr.Load(); err != nil {
		logger.Warn("failed to load secrets, using fallback defaults", "error", err, "path", secretsPath)
		// Don't fail - use fallback defaults
	} else {
		logger.Info("loaded secrets", "path", secretsPath)
	}

	// Get secrets with fallbacks to env vars or static defaults
	// Priority: env var > generated secret > static fallback
	postgresPassword := getEnvOrSecret("BLOUD_POSTGRES_PASSWORD", secretsMgr.GetPostgresPassword(), "testpass123")
	ssoHostSecret := getEnvOrSecret("BLOUD_SSO_HOST_SECRET", secretsMgr.GetSSOHostSecret(), "dev-secret-change-in-production")
	authentikToken := getEnvOrSecret("BLOUD_AUTHENTIK_TOKEN", secretsMgr.GetAuthentikBootstrapToken(), "test-bootstrap-token-change-in-production")
	authentikAdminPassword := getEnvOrSecret("BLOUD_AUTHENTIK_ADMIN_PASSWORD", secretsMgr.GetAuthentikBootstrapPassword(), "password")
	ldapBindPassword := getEnvOrSecret("BLOUD_LDAP_BIND_PASSWORD", secretsMgr.GetLDAPBindPassword(), "ldap-bind-password-change-in-production")

	// Build database URL using postgres password
	defaultDatabaseURL := "postgres://apps:" + postgresPassword + "@localhost:5432/bloud?sslmode=disable"

	cfg := &Config{
		Port:                   getEnvAsInt("BLOUD_PORT", 3000),
		DataDir:                dataDir,
		AppsDir:                appsDir,
		NixConfigDir:           getEnv("BLOUD_NIX_CONFIG_DIR", filepath.Join(dataDir, "nix")),
		FlakePath:              getEnv("BLOUD_FLAKE_PATH", defaultFlakePath),
		FlakeTarget:            getEnv("BLOUD_FLAKE_TARGET", "vm-dev"),
		NixosPath:              getEnv("BLOUD_NIXOS_PATH", defaultNixosPath),
		DatabaseURL:            getEnv("DATABASE_URL", defaultDatabaseURL),
		SSOHostSecret:          ssoHostSecret,
		SSOBaseURL:             getEnv("BLOUD_SSO_BASE_URL", "http://localhost:8080"),
		SSOAuthentikURL:        getEnv("BLOUD_SSO_AUTHENTIK_URL", "http://localhost:8080"),
		AuthentikToken:         authentikToken,
		AuthentikPort:          getEnvAsInt("BLOUD_AUTHENTIK_PORT", 9001),
		AuthentikAdminPassword: authentikAdminPassword,
		AuthentikAdminEmail:    getEnv("BLOUD_AUTHENTIK_ADMIN_EMAIL", "admin@localhost"),
		LDAPBindPassword:       ldapBindPassword,
		Secrets:                secretsMgr,
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

// getEnvOrSecret returns the value from: env var > secret > fallback
func getEnvOrSecret(envKey, secretValue, fallback string) string {
	if value := os.Getenv(envKey); value != "" {
		return value
	}
	if secretValue != "" {
		return secretValue
	}
	return fallback
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
