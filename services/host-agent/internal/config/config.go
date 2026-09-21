// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/secrets"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
)

// Config holds the application configuration
type Config struct {
	Port              int
	DataDir           string
	AppsDir           string // Path to apps/ directory containing app definitions
	TraefikDynamicDir string // Path to Traefik dynamic config directory (contains apps-routes.yml)
	// TrustedLocalNets lists CIDRs/IPs treated as local (loopback-equivalent)
	// for host-agent API requests. Used by dev VMs (e.g. QEMU slirp NAT where
	// host-forwarded connections arrive from the gateway, not loopback).
	TrustedLocalNets []string
	// SSO configuration
	SSOHostSecret string // Master secret for deriving client secrets
	// APIToken is the bearer credential for the admin API surface from a trusted
	// position (loopback / TrustedLocalNets). Empty disables that path.
	APIToken        string
	SSOBaseURL      string // Base URL for callbacks (e.g., "http://localhost:8080")
	SSOAuthentikURL string // Authentik external URL for discovery (e.g., "http://localhost:8080")
	SSOIssuerURL    string // OIDC issuer base URL reachable from app containers (e.g., "http://sso.localhost:8080"); empty falls back to SSOAuthentikURL
	AuthentikToken  string // Authentik API token for SSO cleanup
	// Traefik configuration
	BaseDomain string // Base domain for subdomain routing (default: "localhost")
	// TraefikPort is the canonical public entrypoint. It defaults to 80 (the
	// port a real deployment serves on); the dev VMs expose it on the host as
	// 8080. Backends that cannot bind a privileged port (native, CI) set
	// BLOUD_TRAEFIK_PORT=8080. See TraefikConfigurator.staticConfig for the
	// always-present compat entrypoint on 8080.
	TraefikPort int
	// Authentik bootstrap configuration
	AuthentikPort          int
	AuthentikAdminPassword string
	AuthentikAdminEmail    string
	// LDAP configuration
	LDAPHost         string // LDAP outpost hostname (default: apps-authentik-ldap)
	LDAPBindPassword string
	// Tailscale auth key for tailnet node containers (empty = sharing disabled)
	TSAuthKey string
	// HostLabel is the display name for this host in invite tokens (e.g. "Alice's Server")
	HostLabel string
	// PostgresPassword is the resolved password for the shared Postgres instance.
	// Exposed so bootstrapInfra can template it into the container spec.
	PostgresPassword string
	// Secrets manager for accessing generated secrets
	Secrets *secrets.Manager
}

// Load reads configuration from environment variables with sensible defaults.
// It initializes the secrets manager (auto-generating secrets when its file is
// missing) and returns an error if any required secret cannot be resolved to a
// non-empty value. There is no static fallback: a fault in the secrets path is
// fatal rather than a silent downgrade to known credentials.
func Load() (*Config, error) {
	return LoadWithLogger(slog.Default())
}

// LoadWithLogger is like Load but allows specifying a logger.
func LoadWithLogger(logger *slog.Logger) (*Config, error) {
	dataDir := getEnv("BLOUD_DATA_DIR", getDefaultDataDir())
	appsDir := getEnv("BLOUD_APPS_DIR", "../../apps")

	// Initialize secrets manager
	secretsPath := filepath.Join(dataDir, "secrets.json")
	secretsMgr := secrets.NewManager(secretsPath)
	if err := secretsMgr.Load(); err != nil {
		return nil, fmt.Errorf("loading secrets from %s: %w", secretsPath, err)
	}
	logger.Info("loaded secrets", "path", secretsPath)

	// Required secrets: env var > generated secret. An empty resolution is a
	// configuration fault with no safe fallback. The secrets manager generates
	// every secret when its file is missing and migrates missing fields on load,
	// so a successful Load guarantees non-empty; the guard also catches an env var
	// explicitly set to empty and any future secret not covered by migration.
	postgresPassword, err := getSecret("BLOUD_POSTGRES_PASSWORD", secretsMgr.GetPostgresPassword())
	if err != nil {
		return nil, err
	}
	ssoHostSecret, err := getSecret("BLOUD_SSO_HOST_SECRET", secretsMgr.GetSSOHostSecret())
	if err != nil {
		return nil, err
	}
	authentikAdminPassword, err := getSecret("BLOUD_AUTHENTIK_ADMIN_PASSWORD", secretsMgr.GetAuthentikBootstrapPassword())
	if err != nil {
		return nil, err
	}
	ldapBindPassword, err := getSecret("BLOUD_LDAP_BIND_PASSWORD", secretsMgr.GetLDAPBindPassword())
	if err != nil {
		return nil, err
	}
	// The admin API credential for trusted-position callers (CLI, e2e). An empty
	// value disables the position-based admin path entirely (see
	// api.authMiddlewareFn), so a resolution failure fails closed.
	apiToken, err := getSecret("BLOUD_API_TOKEN", secretsMgr.GetAPIToken())
	if err != nil {
		return nil, err
	}

	// Authentik token priority: env var > api-token file (created by configurator) > secrets.json bootstrap token.
	// The api-token file is created by the Authentik configurator via Django shell and is always valid,
	// whereas the bootstrap token in secrets.json only works on first Authentik boot.
	authentikToken := getAuthentikToken(dataDir, secretsMgr, logger)

	baseDomain := getEnv("BLOUD_BASE_DOMAIN", "localhost")
	// The bootstrap admin's identity email must be a valid RFC-style email
	// (SSO apps validate it); "admin@localhost" fails that check, so derive
	// a TLD-bearing domain. An explicit BLOUD_AUTHENTIK_ADMIN_EMAIL always wins.
	adminEmail := getEnv("BLOUD_AUTHENTIK_ADMIN_EMAIL", "")
	if adminEmail == "" {
		adminEmail = "admin@" + authentik.UserEmailDomain(baseDomain)
	}

	cfg := &Config{
		Port:                   getEnvAsInt("BLOUD_PORT", 3000),
		DataDir:                dataDir,
		AppsDir:                appsDir,
		TraefikDynamicDir:      getEnv("BLOUD_TRAEFIK_DYNAMIC_DIR", filepath.Join(dataDir, "traefik", "dynamic")),
		TrustedLocalNets:       splitNets(getEnv("BLOUD_TRUSTED_LOCAL_NETS", "")),
		SSOHostSecret:          ssoHostSecret,
		SSOBaseURL:             getEnv("BLOUD_SSO_BASE_URL", "http://localhost:8080"),
		SSOAuthentikURL:        getEnv("BLOUD_SSO_AUTHENTIK_URL", "http://localhost:8080"),
		SSOIssuerURL:           getEnv("BLOUD_SSO_ISSUER_URL", ""),
		AuthentikToken:         authentikToken,
		BaseDomain:             baseDomain,
		TraefikPort:            getEnvAsInt("BLOUD_TRAEFIK_PORT", 80),
		AuthentikPort:          getEnvAsInt("BLOUD_AUTHENTIK_PORT", 9001),
		AuthentikAdminPassword: authentikAdminPassword,
		AuthentikAdminEmail:    adminEmail,
		LDAPHost:               getEnv("BLOUD_LDAP_HOST", "apps-authentik-ldap"),
		LDAPBindPassword:       ldapBindPassword,
		TSAuthKey:              getEnv("BLOUD_TS_AUTHKEY", ""),
		HostLabel:              getEnv("BLOUD_HOST_LABEL", hostname()),
		PostgresPassword:       postgresPassword,
		APIToken:               apiToken,
		Secrets:                secretsMgr,
	}

	return cfg, nil
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
// splitNets parses a comma-separated list of IPs/CIDRs into a slice.
// Invalid entries are dropped; an empty value yields no trusted nets.
func splitNets(raw string) []string {
	var nets []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, _, err := net.ParseCIDR(part)
		if err != nil {
			if ip := net.ParseIP(part); ip == nil {
				continue // neither CIDR nor bare IP: drop
			}
		}
		nets = append(nets, part)
	}
	return nets
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getSecret resolves a required secret from the environment or the generated
// store. Priority: env var > generated secret. An empty resolution is a fatal
// configuration fault: there is no static fallback.
func getSecret(envKey, secretValue string) (string, error) {
	if value := os.Getenv(envKey); value != "" {
		return value, nil
	}
	if secretValue != "" {
		return secretValue, nil
	}
	return "", fmt.Errorf(
		"required secret %s unresolved: set %s or initialize secrets.json (host-agent init-secrets)",
		envKey, envKey)
}

// hostname returns the OS hostname or "bloud" as fallback.
func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "bloud"
	}
	return h
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

// ReadAuthentikToken returns the best available Authentik API token.
// It checks the same sources as the initial load (see getAuthentikToken),
// so callers can use this to pick up a token written by the Authentik
// configurator after the server first started.
func (c *Config) ReadAuthentikToken(logger *slog.Logger) string {
	return getAuthentikToken(c.DataDir, c.Secrets, logger)
}

// getAuthentikToken returns the Authentik API token with the following priority:
// 1. BLOUD_AUTHENTIK_TOKEN env var
// 2. api-token file created by Authentik configurator (always valid)
// 3. Bootstrap token from secrets.json (only works on first Authentik boot)
// Returns empty when none is available (e.g. before the configurator first runs);
// callers guard on non-empty. There is no static fallback token.
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
	return ""
}
