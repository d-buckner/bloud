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
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/dirs"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/secrets"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
)

type Config struct {
	Port              int
	DataDir           string
	AppsDir           string // Path to apps/ directory containing app definitions
	TraefikDynamicDir string // Path to Traefik dynamic config directory (contains apps-routes.yml)
	// TrustedLocalNets lists CIDRs/IPs treated as local (loopback-equivalent)
	// for host-agent API requests. Used by dev VMs (e.g. QEMU slirp NAT where
	// host-forwarded connections arrive from the gateway, not loopback).
	TrustedLocalNets []string
	// TrustedProxyNets lists the addresses, as seen from Traefik, of the reverse
	// proxy sitting directly in front of Bloud (a TLS terminator such as nginx
	// proxy manager, Caddy, or a Cloudflare Tunnel). Traefik trusts
	// X-Forwarded-* from these sources only, so the original scheme reaches
	// Authentik instead of Traefik's own "http". It is a source-scope setting,
	// never blanket trust: Traefik keeps overwriting X-Forwarded-* from any peer
	// outside the list. Distinct from TrustedLocalNets, which is a host-agent
	// admin-position scope and grants nothing to Traefik.
	TrustedProxyNets []string
	// PublicScheme is the deployment-wide scheme applied to derived URLs for
	// hosts that carry no stored scheme of their own. Set it when a TLS
	// terminator sits in front of Bloud and the instance is reached over https.
	// It is the coarse knob: the per-host scheme saved in Settings wins over it,
	// and the legacy BLOUD_SSO_BASE_URL wins over both. Empty means http.
	PublicScheme string
	// ReconcileInterval is how often the orchestrator runs a self-healing
	// convergence pass with no intent behind it (BLOUD_RECONCILE_INTERVAL,
	// Go duration syntax: "60s", "5m"). Zero means "not configured": the
	// framework default (orchestrator.DefaultSelfHealInterval) applies.
	// A negative value disables the periodic pass; the literal "off" is the
	// spelling for it, so nobody has to type "-1s" to mean "never".
	ReconcileInterval time.Duration
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
	// CalDAV service account password, for the machine client (caldav-mcp)
	// that reads the operator's calendars on the agent's behalf.
	CalDAVServicePassword string
	// CalendarServicePassword is the credential of the account that owns the
	// shared calendar collections (the aggregated feeds and the family
	// calendar) which Radicale map-shares to every Bloud user.
	CalendarServicePassword string
	// PostgresPassword is the resolved password for the shared Postgres instance.
	// Exposed so bootstrapInfra can template it into the container spec.
	PostgresPassword string
	// Secrets manager for accessing generated secrets
	Secrets *secrets.Manager
}

// authentikApp is the catalog ID of the identity provider whose configurator
// drops its API token in the app data directory. Named here because config
// reads a file another component writes, so the two must agree on the path.
const authentikApp = "authentik"

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

	// Required secrets: env var > generated secret, with no static fallback.
	// A fault here is fatal rather than a silent downgrade to known
	// credentials.
	sec, err := loadRequiredSecrets(secretsMgr)
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
		Port:                    getEnvAsInt("BLOUD_PORT", 3000),
		DataDir:                 dataDir,
		AppsDir:                 appsDir,
		TraefikDynamicDir:       getEnv("BLOUD_TRAEFIK_DYNAMIC_DIR", filepath.Join(dataDir, "traefik", "dynamic")),
		TrustedLocalNets:        splitNets(getEnv("BLOUD_TRUSTED_LOCAL_NETS", "")),
		TrustedProxyNets:        splitNets(getEnv("BLOUD_TRUSTED_PROXY_NETS", "")),
		PublicScheme:            getEnv("BLOUD_PUBLIC_SCHEME", ""),
		ReconcileInterval:       getEnvDuration("BLOUD_RECONCILE_INTERVAL", 0),
		SSOHostSecret:           sec.ssoHostSecret,
		SSOBaseURL:              getEnv("BLOUD_SSO_BASE_URL", "http://localhost:8080"),
		SSOAuthentikURL:         getEnv("BLOUD_SSO_AUTHENTIK_URL", "http://localhost:8080"),
		SSOIssuerURL:            getEnv("BLOUD_SSO_ISSUER_URL", ""),
		AuthentikToken:          authentikToken,
		BaseDomain:              baseDomain,
		TraefikPort:             getEnvAsInt("BLOUD_TRAEFIK_PORT", 80),
		AuthentikPort:           getEnvAsInt("BLOUD_AUTHENTIK_PORT", 9001),
		AuthentikAdminPassword:  sec.authentikAdmin,
		AuthentikAdminEmail:     adminEmail,
		LDAPHost:                getEnv("BLOUD_LDAP_HOST", "apps-authentik-ldap"),
		LDAPBindPassword:        sec.ldapBindPassword,
		CalDAVServicePassword:   sec.caldavServicePassword,
		CalendarServicePassword: sec.calendarServicePassword,
		PostgresPassword:        sec.postgresPassword,
		APIToken:                sec.apiToken,
		Secrets:                 secretsMgr,
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

// getEnvDuration reads an environment variable as a Go duration. An unset or
// unparseable value yields defaultValue. The literals "off", "none", and
// "disabled" yield a negative duration, which is how a deployment says "no
// timer at all": zero already means "not configured" everywhere else in this
// config, so it cannot also mean "off".
func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	switch strings.ToLower(raw) {
	case "off", "none", "disabled":
		return -1
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return defaultValue
	}
	return value
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

// resolvedSecrets is the set of secrets config requires from the secrets
// manager before it can build a Config.
type resolvedSecrets struct {
	postgresPassword        string
	ssoHostSecret           string
	authentikAdmin          string
	ldapBindPassword        string
	caldavServicePassword   string
	calendarServicePassword string
	apiToken                string
}

// loadRequiredSecrets resolves every secret config refuses to start without.
// The secrets manager generates each one when its file is missing and migrates
// missing fields on load, so a successful Load guarantees non-empty; the guard
// in getSecret also catches an env var explicitly set to empty and any future
// secret not covered by migration.
//
// The API token is the admin credential for trusted-position callers (CLI,
// e2e). An empty value disables the position-based admin path entirely (see
// api.authMiddlewareFn), so a resolution failure fails closed.
func loadRequiredSecrets(mgr *secrets.Manager) (resolvedSecrets, error) {
	var out resolvedSecrets
	var err error
	if out.postgresPassword, err = getSecret("BLOUD_POSTGRES_PASSWORD", mgr.GetPostgresPassword()); err != nil {
		return out, err
	}
	if out.ssoHostSecret, err = getSecret("BLOUD_SSO_HOST_SECRET", mgr.GetSSOHostSecret()); err != nil {
		return out, err
	}
	if out.authentikAdmin, err = getSecret("BLOUD_AUTHENTIK_ADMIN_PASSWORD", mgr.GetAuthentikBootstrapPassword()); err != nil {
		return out, err
	}
	if out.ldapBindPassword, err = getSecret("BLOUD_LDAP_BIND_PASSWORD", mgr.GetLDAPBindPassword()); err != nil {
		return out, err
	}
	if out.caldavServicePassword, err = getSecret("BLOUD_CALDAV_SERVICE_PASSWORD", mgr.GetCalDAVServicePassword()); err != nil {
		return out, err
	}
	if out.calendarServicePassword, err = getSecret("BLOUD_CALENDAR_SERVICE_PASSWORD", mgr.GetCalendarServicePassword()); err != nil {
		return out, err
	}
	if out.apiToken, err = getSecret("BLOUD_API_TOKEN", mgr.GetAPIToken()); err != nil {
		return out, err
	}
	return out, nil
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
	// This token is created via Django shell and is always valid. The writer is
	// the Authentik configurator at AppState.DataPath/api-token, so this must
	// resolve through the same app-directory helper rather than a literal path.
	tokenPath := filepath.Join(dirs.AppDataDir(dataDir, authentikApp), "api-token")
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
