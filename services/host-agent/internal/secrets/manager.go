// SPDX-License-Identifier: AGPL-3.0-only

package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Manager handles generation and persistence of deployment secrets.
// Secrets are generated on first run and stored in a JSON file.
type Manager struct {
	path    string
	secrets *Secrets
	mu      sync.RWMutex
}

// Secrets contains all generated secrets for the deployment.
type Secrets struct {
	// PostgreSQL password for the shared apps database
	PostgresPassword string `json:"postgresPassword"`

	// Authentik secrets
	AuthentikSecretKey         string `json:"authentikSecretKey"`
	AuthentikBootstrapPassword string `json:"authentikBootstrapPassword"`
	AuthentikBootstrapToken    string `json:"authentikBootstrapToken"`

	// LDAP outpost token
	LDAPOutpostToken string `json:"ldapOutpostToken"`

	// LDAP bind password for apps to authenticate via LDAP
	LDAPBindPassword string `json:"ldapBindPassword"`

	// CalDAV service account password for the machine client (caldav-mcp) that
	// reads the operator's calendars on the agent's behalf.
	CalDAVServicePassword string `json:"caldavServicePassword"`

	// CalendarServicePassword is the credential of the account that *owns* the
	// shared calendar collections: the aggregated feeds and the family calendar
	// that Radicale map-shares to every Bloud user. It is deliberately not the
	// CalDAV service account's password: that one is the agent's read-only
	// credential, and an owner that can write would make the agent's read-only
	// grant a policy note instead of a boundary.
	CalendarServicePassword string `json:"calendarServicePassword"`

	// Master secret for deriving per-app OAuth client secrets
	SSOHostSecret string `json:"ssoHostSecret"`

	// APIToken is the bearer credential for the admin API surface from a
	// trusted position (loopback / BLOUD_TRUSTED_LOCAL_NETS). It is the
	// CLI's and e2e's credential; browsers use sessions instead.
	APIToken string `json:"apiToken"`

	// Per-app secrets (generated during install)
	AppSecrets map[string]AppSecrets `json:"appSecrets,omitempty"`
}

// AppSecrets contains secrets specific to an individual app.
type AppSecrets struct {
	// Admin password for apps that have one (miniflux, jellyseerr, etc.)
	AdminPassword string `json:"adminPassword,omitempty"`

	// OAuth client secret derived from SSOHostSecret
	OAuthClientSecret string `json:"oauthClientSecret,omitempty"`

	// App-specific database password (if different from shared postgres)
	DatabasePassword string `json:"databasePassword,omitempty"`

	// Published holds the credentials an app generated for itself and stored
	// through SetAppSecret: the names it declares under a contract's `secrets`
	// in its catalog metadata, handed to that contract's consumers as the
	// payload field they read (the Servarr ApiKey, Authentik's API token).
	// A published credential reaches a consumer through a binding, never
	// through a container's environment.
	Published map[string]string `json:"published,omitempty"`

	// PublishedValues holds the non-secret facts a provider mints at runtime
	// for a contract it provides, keyed by contract name and then by value
	// key. It exists because a contract's `values:` in metadata is static and
	// some providers cannot know the value until their own app is up: AFFiNE's
	// MCP endpoint is /api/workspaces/<id>/mcp, and the workspace id is
	// minted by AFFiNE on first boot, not by Bloud's metadata.
	//
	// It is scoped by contract rather than flat because two contracts can name
	// the same value key (`path` is in both `mcp` and `modelSource`) while an
	// app provides both. A provider may only fill a key its offer declares
	// under `runtimeValues`; the catalog loader enforces that, so this bag
	// cannot become a place to smuggle undeclared facts to a consumer.
	//
	// Stored here rather than in a separate store because the durability and
	// lifecycle are identical to a published secret: it is written once by a
	// configurator, survives restart, and is deleted with the app.
	PublishedValues map[string]map[string]string `json:"publishedValues,omitempty"`
}

// APITokenFileName is the standalone file (owner-only, next to secrets.json)
// holding APIToken: the host-agent's own admin credential. The CLI and e2e
// helpers read it directly so they never have to parse secrets.json.
//
// Deliberately not "api-token": <dataDir>/authentik/api-token already holds the
// *Authentik* API token, and two credentials one directory apart should not
// share a name.
const APITokenFileName = "host-agent-api-token"

// NewManager creates a new secrets manager that uses the given file path.
func NewManager(path string) *Manager {
	return &Manager{
		path: path,
	}
}

// Load reads secrets from file or generates new ones if the file doesn't exist.
func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Try to read existing secrets
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			// Generate new secrets
			return m.generateAndSave()
		}
		return fmt.Errorf("reading secrets file: %w", err)
	}

	// Parse existing secrets
	var secrets Secrets
	if err := json.Unmarshal(data, &secrets); err != nil {
		return fmt.Errorf("parsing secrets file: %w", err)
	}

	// Ensure AppSecrets map is initialized
	if secrets.AppSecrets == nil {
		secrets.AppSecrets = make(map[string]AppSecrets)
	}

	// Migrate: fill in any missing secrets, which is what happens when a new
	// secret is added to the schema after a deployment already has a file.
	updated := fillMissingSecrets(secretTable(&secrets))

	m.secrets = &secrets

	if updated {
		return m.saveLocked()
	}

	return nil
}

// secretField is one Secrets field paired with the length Bloud issues it at.
type secretField struct {
	field  *string
	length int
}

// secretTable lists every secret Bloud issues and how long it is. The
// fresh-deployment path and the migration path both read this table, so a new
// secret is added in exactly one place and the two can never disagree about a
// length.
func secretTable(s *Secrets) []secretField {
	return []secretField{
		{&s.PostgresPassword, 32},
		{&s.AuthentikSecretKey, 64},
		{&s.AuthentikBootstrapPassword, 32},
		{&s.AuthentikBootstrapToken, 48},
		{&s.LDAPOutpostToken, 48},
		{&s.LDAPBindPassword, 32},
		{&s.CalDAVServicePassword, 32},
		{&s.CalendarServicePassword, 32},
		{&s.SSOHostSecret, 64},
		{&s.APIToken, 48},
	}
}

// fillMissingSecrets generates the still-empty secrets in the table and
// reports whether any were written.
func fillMissingSecrets(fields []secretField) bool {
	updated := false
	for _, f := range fields {
		if *f.field == "" {
			*f.field = generateSecret(f.length)
			updated = true
		}
	}
	return updated
}

// generateAndSave generates all secrets and saves to file.
// Secrets are cryptographically random and unique per deployment.
func (m *Manager) generateAndSave() error {
	s := &Secrets{AppSecrets: make(map[string]AppSecrets)}
	for _, f := range secretTable(s) {
		*f.field = generateSecret(f.length)
	}
	m.secrets = s

	return m.saveLocked()
}

// saveLocked persists secrets to file. Caller must hold the lock.
func (m *Manager) saveLocked() error {
	// Ensure directory exists
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating secrets directory: %w", err)
	}

	data, err := json.MarshalIndent(m.secrets, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling secrets: %w", err)
	}

	// Write JSON with restrictive permissions (owner read/write only)
	if err := os.WriteFile(m.path, data, 0600); err != nil {
		return fmt.Errorf("writing secrets file: %w", err)
	}

	// Write the API token as a standalone owner-only file. The CLI reads it with
	// a plain `cat` inside the guest, so it never has to parse secrets.json or
	// carry the other secrets around.
	if err := os.WriteFile(filepath.Join(dir, APITokenFileName), []byte(m.secrets.APIToken+"\n"), 0600); err != nil {
		return fmt.Errorf("writing api token file: %w", err)
	}

	return nil
}

// Get returns a top-level secret by name.
func (m *Manager) Get(name string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.secrets == nil {
		return ""
	}

	switch name {
	case "postgresPassword":
		return m.secrets.PostgresPassword
	case "authentikSecretKey":
		return m.secrets.AuthentikSecretKey
	case "authentikBootstrapPassword":
		return m.secrets.AuthentikBootstrapPassword
	case "authentikBootstrapToken":
		return m.secrets.AuthentikBootstrapToken
	case "ldapOutpostToken":
		return m.secrets.LDAPOutpostToken
	case "ldapBindPassword":
		return m.secrets.LDAPBindPassword
	case "caldavServicePassword":
		return m.secrets.CalDAVServicePassword
	case "calendarServicePassword":
		return m.secrets.CalendarServicePassword
	case "ssoHostSecret":
		return m.secrets.SSOHostSecret
	case "apiToken":
		return m.secrets.APIToken
	default:
		return ""
	}
}

func (m *Manager) GetPostgresPassword() string {
	return m.Get("postgresPassword")
}

// GetAuthentikSecretKey returns the Authentik secret key.
func (m *Manager) GetAuthentikSecretKey() string {
	return m.Get("authentikSecretKey")
}

// GetAuthentikBootstrapPassword returns the Authentik admin bootstrap password.
func (m *Manager) GetAuthentikBootstrapPassword() string {
	return m.Get("authentikBootstrapPassword")
}

// GetAuthentikBootstrapToken returns the Authentik API bootstrap token.
func (m *Manager) GetAuthentikBootstrapToken() string {
	return m.Get("authentikBootstrapToken")
}

// GetLDAPOutpostToken returns the LDAP outpost API token.
func (m *Manager) GetLDAPOutpostToken() string {
	return m.Get("ldapOutpostToken")
}

// GetLDAPBindPassword returns the LDAP bind password.
func (m *Manager) GetLDAPBindPassword() string {
	return m.Get("ldapBindPassword")
}

// GetCalDAVServicePassword returns the CalDAV service account password.
func (m *Manager) GetCalDAVServicePassword() string {
	return m.Get("caldavServicePassword")
}

// GetCalendarServicePassword returns the shared-calendar owner account password.
func (m *Manager) GetCalendarServicePassword() string {
	return m.Get("calendarServicePassword")
}

// GetSSOHostSecret returns the master secret for OAuth client secret derivation.
func (m *Manager) GetSSOHostSecret() string {
	return m.Get("ssoHostSecret")
}

// GetAPIToken returns the admin API bearer credential.
func (m *Manager) GetAPIToken() string {
	return m.Get("apiToken")
}

// GetAppSecret returns a specific secret for an app.
func (m *Manager) GetAppSecret(appName, key string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.secrets == nil || m.secrets.AppSecrets == nil {
		return ""
	}

	appSecrets, ok := m.secrets.AppSecrets[appName]
	if !ok {
		return ""
	}

	switch key {
	case "adminPassword":
		return appSecrets.AdminPassword
	case "oauthClientSecret":
		return appSecrets.OAuthClientSecret
	case "databasePassword":
		return appSecrets.DatabasePassword
	default:
		return appSecrets.Published[key]
	}
}

// SetAppSecret sets a specific secret for an app and saves to file.
//
// A key outside the known set above is stored in the app's published bag; the
// app is expected to have declared it under a contract's `secrets` in its
// catalog metadata, which is what puts it in that contract's binding payload.
// Writing a value that is already stored is a no-op: configurators publish on
// every reconciliation, and rewriting secrets.json on each pass would be pure
// churn.
func (m *Manager) SetAppSecret(appName, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.secrets == nil {
		return fmt.Errorf("secrets not loaded")
	}

	if m.secrets.AppSecrets == nil {
		m.secrets.AppSecrets = make(map[string]AppSecrets)
	}

	appSecrets := m.secrets.AppSecrets[appName]

	// A value that is already stored is a no-op: configurators publish on
	// every reconciliation, and rewriting secrets.json on each pass would be
	// pure churn.
	switch key {
	case "adminPassword":
		if appSecrets.AdminPassword == value {
			return nil
		}
		appSecrets.AdminPassword = value
	case "oauthClientSecret":
		if appSecrets.OAuthClientSecret == value {
			return nil
		}
		appSecrets.OAuthClientSecret = value
	case "databasePassword":
		if appSecrets.DatabasePassword == value {
			return nil
		}
		appSecrets.DatabasePassword = value
	default:
		if appSecrets.Published[key] == value {
			return nil
		}
		if appSecrets.Published == nil {
			appSecrets.Published = make(map[string]string, 1)
		}
		appSecrets.Published[key] = value
	}

	m.secrets.AppSecrets[appName] = appSecrets

	return m.saveLocked()
}

// SetAppContractValue records a non-secret value a provider minted at runtime
// for one contract it provides, so a consumer of that contract can read it. The
// counterpart of SetAppSecret for the `values` half of a contract: the provider's
// metadata declares the key, the provider's configurator supplies the value once
// its own app has produced it.
//
// The catalog loader is what bounds this: a provider may only fill a key its offer
// lists under `runtimeValues`, so a configurator cannot publish an undeclared
// fact into a consumer's binding. Writing the value that is already stored is a
// no-op, for the same reason SetAppSecret is one: configurators publish on every
// reconciliation pass.
func (m *Manager) SetAppContractValue(appName, contract, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.secrets == nil {
		return fmt.Errorf("secrets not loaded")
	}
	if m.secrets.AppSecrets == nil {
		m.secrets.AppSecrets = make(map[string]AppSecrets)
	}

	appSecrets := m.secrets.AppSecrets[appName]
	if appSecrets.PublishedValues != nil && appSecrets.PublishedValues[contract] != nil &&
		appSecrets.PublishedValues[contract][key] == value {
		return nil
	}
	if appSecrets.PublishedValues == nil {
		appSecrets.PublishedValues = make(map[string]map[string]string, 1)
	}
	if appSecrets.PublishedValues[contract] == nil {
		appSecrets.PublishedValues[contract] = make(map[string]string, 1)
	}
	appSecrets.PublishedValues[contract][key] = value

	m.secrets.AppSecrets[appName] = appSecrets

	return m.saveLocked()
}

// GetAppContractValue returns a runtime-published value for one contract, or ""
// when the provider has not published it yet. The empty string means "not ready",
// the same reading as a published secret that has not landed.
func (m *Manager) GetAppContractValue(appName, contract, key string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.secrets == nil || m.secrets.AppSecrets == nil {
		return ""
	}
	appSecrets, ok := m.secrets.AppSecrets[appName]
	if !ok || appSecrets.PublishedValues == nil {
		return ""
	}
	return appSecrets.PublishedValues[contract][key]
}

// DeleteAppSecrets removes all secrets for an app and saves to file.
func (m *Manager) DeleteAppSecrets(appName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.secrets == nil || m.secrets.AppSecrets == nil {
		return nil
	}

	delete(m.secrets.AppSecrets, appName)

	return m.saveLocked()
}

// GetAllSecrets returns a copy of all secrets.
func (m *Manager) GetAllSecrets() *Secrets {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.secrets == nil {
		return nil
	}

	// Return a copy
	copy := *m.secrets
	if m.secrets.AppSecrets != nil {
		copy.AppSecrets = make(map[string]AppSecrets, len(m.secrets.AppSecrets))
		for k, v := range m.secrets.AppSecrets {
			if v.Published != nil {
				published := make(map[string]string, len(v.Published))
				for name, value := range v.Published {
					published[name] = value
				}
				v.Published = published
			}
			if v.PublishedValues != nil {
				publishedValues := make(map[string]map[string]string, len(v.PublishedValues))
				for contract, values := range v.PublishedValues {
					cp := make(map[string]string, len(values))
					for name, value := range values {
						cp[name] = value
					}
					publishedValues[contract] = cp
				}
				v.PublishedValues = publishedValues
			}
			copy.AppSecrets[k] = v
		}
	}

	return &copy
}

// Path returns the file path where secrets are stored.
func (m *Manager) Path() string {
	return m.path
}

// generateSecret generates a cryptographically random secret of the given length.
// The result is base64 URL-encoded for safe use in configs.
func generateSecret(length int) string {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		// This should never happen with crypto/rand
		panic(fmt.Sprintf("failed to generate random bytes: %v", err))
	}
	return base64.URLEncoding.EncodeToString(bytes)[:length]
}

// GenerateAppAdminPassword generates a new admin password for an app if one doesn't exist.
func (m *Manager) GenerateAppAdminPassword(appName string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.secrets == nil {
		return "", fmt.Errorf("secrets not loaded")
	}

	if m.secrets.AppSecrets == nil {
		m.secrets.AppSecrets = make(map[string]AppSecrets)
	}

	appSecrets := m.secrets.AppSecrets[appName]
	if appSecrets.AdminPassword != "" {
		return appSecrets.AdminPassword, nil
	}

	appSecrets.AdminPassword = generateSecret(24)
	m.secrets.AppSecrets[appName] = appSecrets

	if err := m.saveLocked(); err != nil {
		return "", err
	}

	return appSecrets.AdminPassword, nil
}
