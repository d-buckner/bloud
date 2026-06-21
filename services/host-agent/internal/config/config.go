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
	RuntimeMode       string
	SystemdScope      string
	QuadletDir        string
	Port              int
	DataDir           string
	AppsDir           string // Path to apps/ directory containing app definitions
	NixConfigDir      string
	TraefikDynamicDir string // Path to Traefik dynamic config directory (contains apps-routes.yml)
	FlakePath         string // Path to flake.nix for nixos-rebuild
	FlakeTarget       string // Flake target for nixos-rebuild (e.g., "vm-dev", "vm-test")
	NixosPath         string // Path to nixos/ modules directory
	RedisAddr string // Redis address for session storage
	// SSO configuration
	SSOHostSecret   string // Master secret for deriving client secrets
	SSOBaseURL      string // Base URL for callbacks (e.g., "http://localhost:8080")
	SSOAuthentikURL string // Authentik external URL for discovery (e.g., "http://localhost:8080")
	AuthentikToken  string // Authentik API token for SSO cleanup
	// Traefik configuration
	BaseDomain  string // Base domain for subdomain routing (default: "localhost")
	TraefikPort int    // Traefik entrypoint port (default: 8080)
	// Authentik bootstrap configuration
	AuthentikPort          int
	AuthentikAdminPassword string
	AuthentikAdminEmail    string
	// LDAP configuration
	LDAPHost         string // LDAP outpost hostname (default: apps-authentik-ldap)
	LDAPBindPassword string
	// PostgresPassword is the resolved password for the shared Postgres instance.
	// Exposed so bootstrapInfra can template it into the container spec.
	PostgresPassword string
	// Secrets manager for accessing generated secrets
	Secrets *secrets.Manager
}

// PostgresURL returns a connection string for the shared Postgres instance.
// Used by bootstrap code that creates app-specific databases (e.g. authentik).
func (c *Config) PostgresURL() string {
	return "postgres://apps:" + c.PostgresPassword + "@localhost:5432/bloud?sslmode=disable"
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

	// FlakePath and NixosPath are only used by the nix runtime orchestrator.
	// Default to empty so they don't appear in portable runtime logs.
	defaultFlakePath := ""
	defaultNixosPath := ""
	if getEnv("BLOUD_RUNTIME", "portable") == "nix" {
		defaultFlakePath = filepath.Clean(filepath.Join(appsDir, ".."))
		defaultNixosPath = filepath.Clean(filepath.Join(appsDir, "..", "nixos"))
	}

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
	authentikAdminPassword := getEnvOrSecret("BLOUD_AUTHENTIK_ADMIN_PASSWORD", secretsMgr.GetAuthentikBootstrapPassword(), "password")
	ldapBindPassword := getEnvOrSecret("BLOUD_LDAP_BIND_PASSWORD", secretsMgr.GetLDAPBindPassword(), "ldap-bind-password-change-in-production")

	// Authentik token priority: env var > api-token file (created by configurator) > secrets.json > fallback
	// The api-token file is created by the Authentik configurator via Django shell and is always valid,
	// whereas the bootstrap token in secrets.json only works on first Authentik boot.
	authentikToken := getAuthentikToken(dataDir, secretsMgr, logger)

	systemdScope := getEnv("BLOUD_SYSTEMD_SCOPE", defaultSystemdScope())
	cfg := &Config{
		RuntimeMode:            getEnv("BLOUD_RUNTIME", "portable"),
		SystemdScope:           systemdScope,
		QuadletDir:             getEnv("BLOUD_QUADLET_DIR", defaultQuadletDir(systemdScope)),
		Port:                   getEnvAsInt("BLOUD_PORT", 3000),
		DataDir:                dataDir,
		AppsDir:                appsDir,
		NixConfigDir:           getEnv("BLOUD_NIX_CONFIG_DIR", filepath.Join(dataDir, "nix")),
		TraefikDynamicDir:      getEnv("BLOUD_TRAEFIK_DYNAMIC_DIR", filepath.Join(dataDir, "traefik", "dynamic")),
		FlakePath:              getEnv("BLOUD_FLAKE_PATH", defaultFlakePath),
		FlakeTarget:            getEnv("BLOUD_FLAKE_TARGET", "vm-dev"),
		NixosPath:              getEnv("BLOUD_NIXOS_PATH", defaultNixosPath),
		RedisAddr:              getEnv("BLOUD_REDIS_ADDR", "localhost:6379"),
		SSOHostSecret:          ssoHostSecret,
		SSOBaseURL:             getEnv("BLOUD_SSO_BASE_URL", "http://localhost:8080"),
		SSOAuthentikURL:        getEnv("BLOUD_SSO_AUTHENTIK_URL", "http://localhost:8080"),
		AuthentikToken:         authentikToken,
		BaseDomain:             getEnv("BLOUD_BASE_DOMAIN", "localhost"),
		TraefikPort:            getEnvAsInt("BLOUD_TRAEFIK_PORT", 8080),
		AuthentikPort:          getEnvAsInt("BLOUD_AUTHENTIK_PORT", 9001),
		AuthentikAdminPassword: authentikAdminPassword,
		AuthentikAdminEmail:    getEnv("BLOUD_AUTHENTIK_ADMIN_EMAIL", "admin@localhost"),
		LDAPHost:               getEnv("BLOUD_LDAP_HOST", "apps-authentik-ldap"),
		LDAPBindPassword:       ldapBindPassword,
		PostgresPassword:       postgresPassword,
		Secrets:                secretsMgr,
	}

	return cfg
}

func defaultSystemdScope() string {
	if os.Getuid() == 0 {
		return "system"
	}
	return "user"
}

func defaultQuadletDir(scope string) string {
	if scope == "system" {
		return "/etc/containers/systemd"
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(getDefaultDataDir(), "quadlet")
	}
	return filepath.Join(homeDir, ".config", "containers", "systemd")
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

// getAuthentikToken returns the Authentik API token with the following priority:
// 1. BLOUD_AUTHENTIK_TOKEN env var
// 2. api-token file created by Authentik configurator (always valid)
// 3. Bootstrap token from secrets.json (only works on first Authentik boot)
// 4. Static fallback for development
func getAuthentikToken(dataDir string, secretsMgr *secrets.Manager, logger *slog.Logger) string {
	// Check env var first
	if value := os.Getenv("BLOUD_AUTHENTIK_TOKEN"); value != "" {
		return value
	}

	// Check api-token file created by Authentik configurator
	// This token is created via Django shell and is always valid
	tokenPath := filepath.Join(dataDir, "authentik", "api-token")
	if data, err := os.ReadFile(tokenPath); err == nil {
		token := string(data)
		if token != "" {
			logger.Info("using Authentik API token from configurator", "path", tokenPath)
			return token
		}
	}

	// Fall back to bootstrap token from secrets.json
	// Note: This only works if Authentik was bootstrapped with this token
	if token := secretsMgr.GetAuthentikBootstrapToken(); token != "" {
		logger.Info("using Authentik bootstrap token from secrets.json")
		return token
	}

	// Static fallback for development
	return "test-bootstrap-token-change-in-production"
}
