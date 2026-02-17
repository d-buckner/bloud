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

	// Master secret for deriving per-app OAuth client secrets
	SSOHostSecret string `json:"ssoHostSecret"`

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
}

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

	// Migrate: fill in any missing secrets (in case new secrets were added)
	updated := false
	if secrets.PostgresPassword == "" {
		secrets.PostgresPassword = generateSecret(32)
		updated = true
	}
	if secrets.AuthentikSecretKey == "" {
		secrets.AuthentikSecretKey = generateSecret(64)
		updated = true
	}
	if secrets.AuthentikBootstrapPassword == "" {
		secrets.AuthentikBootstrapPassword = generateSecret(32)
		updated = true
	}
	if secrets.AuthentikBootstrapToken == "" {
		secrets.AuthentikBootstrapToken = generateSecret(48)
		updated = true
	}
	if secrets.LDAPOutpostToken == "" {
		secrets.LDAPOutpostToken = generateSecret(48)
		updated = true
	}
	if secrets.LDAPBindPassword == "" {
		secrets.LDAPBindPassword = generateSecret(32)
		updated = true
	}
	if secrets.SSOHostSecret == "" {
		secrets.SSOHostSecret = generateSecret(64)
		updated = true
	}

	m.secrets = &secrets

	if updated {
		return m.saveLocked()
	}

	return nil
}

// generateAndSave generates all secrets and saves to file.
// Secrets are cryptographically random and unique per deployment.
func (m *Manager) generateAndSave() error {
	m.secrets = &Secrets{
		PostgresPassword:           generateSecret(32),
		AuthentikSecretKey:         generateSecret(64),
		AuthentikBootstrapPassword: generateSecret(32),
		AuthentikBootstrapToken:    generateSecret(48),
		LDAPOutpostToken:           generateSecret(48),
		LDAPBindPassword:           generateSecret(32),
		SSOHostSecret:              generateSecret(64),
		AppSecrets:                 make(map[string]AppSecrets),
	}

	return m.saveLocked()
}

// saveLocked saves secrets to file and generates environment files. Caller must hold the lock.
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

	// Write environment files for systemd services
	if err := m.writeEnvFiles(dir); err != nil {
		return fmt.Errorf("writing env files: %w", err)
	}

	return nil
}

// writeEnvFiles writes .env files for systemd services to load via EnvironmentFile=
func (m *Manager) writeEnvFiles(dir string) error {
	// PostgreSQL environment
	postgresEnv := fmt.Sprintf("POSTGRES_PASSWORD=%s\n", m.secrets.PostgresPassword)
	if err := os.WriteFile(filepath.Join(dir, "postgres.env"), []byte(postgresEnv), 0600); err != nil {
		return fmt.Errorf("writing postgres.env: %w", err)
	}

	// Authentik environment
	authentikEnv := fmt.Sprintf(`AUTHENTIK_SECRET_KEY=%s
AUTHENTIK_BOOTSTRAP_PASSWORD=%s
AUTHENTIK_BOOTSTRAP_TOKEN=%s
AUTHENTIK_POSTGRESQL__PASSWORD=%s
AUTHENTIK_REDIS__PASSWORD=
`, m.secrets.AuthentikSecretKey, m.secrets.AuthentikBootstrapPassword,
		m.secrets.AuthentikBootstrapToken, m.secrets.PostgresPassword)
	if err := os.WriteFile(filepath.Join(dir, "authentik.env"), []byte(authentikEnv), 0600); err != nil {
		return fmt.Errorf("writing authentik.env: %w", err)
	}

	// Shared database credentials for apps that need postgres access
	dbEnv := fmt.Sprintf("DATABASE_PASSWORD=%s\nPGPASSWORD=%s\n",
		m.secrets.PostgresPassword, m.secrets.PostgresPassword)
	if err := os.WriteFile(filepath.Join(dir, "database.env"), []byte(dbEnv), 0600); err != nil {
		return fmt.Errorf("writing database.env: %w", err)
	}

	// Write per-app environment files
	// Always generate env files for known apps (they need DATABASE_URL etc.)
	knownApps := []string{"miniflux", "actual-budget", "affine"}
	for _, appName := range knownApps {
		appSecrets := m.secrets.AppSecrets[appName] // May be empty struct
		if err := m.writeAppEnvFile(dir, appName, appSecrets); err != nil {
			return fmt.Errorf("writing %s.env: %w", appName, err)
		}
	}

	// Also write env files for any other apps with stored secrets
	for appName, appSecrets := range m.secrets.AppSecrets {
		// Skip if already written above
		isKnown := false
		for _, known := range knownApps {
			if appName == known {
				isKnown = true
				break
			}
		}
		if isKnown {
			continue
		}
		if err := m.writeAppEnvFile(dir, appName, appSecrets); err != nil {
			return fmt.Errorf("writing %s.env: %w", appName, err)
		}
	}

	return nil
}

// writeAppEnvFile writes an environment file for a specific app
func (m *Manager) writeAppEnvFile(dir, appName string, appSecrets AppSecrets) error {
	var env string

	if appSecrets.AdminPassword != "" {
		env += fmt.Sprintf("ADMIN_PASSWORD=%s\n", appSecrets.AdminPassword)
	}
	if appSecrets.OAuthClientSecret != "" {
		// Use app-specific env var names for OAuth secrets
		oauthEnvName := getOAuthEnvVarName(appName)
		env += fmt.Sprintf("%s=%s\n", oauthEnvName, appSecrets.OAuthClientSecret)
	}
	if appSecrets.DatabasePassword != "" {
		env += fmt.Sprintf("DATABASE_PASSWORD=%s\n", appSecrets.DatabasePassword)
	}

	// Add postgres password for apps that need database connection strings
	env += fmt.Sprintf("PGPASSWORD=%s\n", m.secrets.PostgresPassword)

	// Generate DATABASE_URL with the correct hostname for this app's network mode
	dbURL := getDatabaseURL(appName, m.secrets.PostgresPassword)
	env += fmt.Sprintf("DATABASE_URL=%s\n", dbURL)

	return os.WriteFile(filepath.Join(dir, appName+".env"), []byte(env), 0600)
}

// getDatabaseURL returns the DATABASE_URL for an app based on its network mode
func getDatabaseURL(appName, password string) string {
	// Apps using bridge networking connect to apps-postgres
	// Apps using host networking connect to localhost
	bridgeNetworkApps := map[string]bool{
		"affine": true,
		// Add other bridge-networked apps here
	}

	if bridgeNetworkApps[appName] {
		return fmt.Sprintf("postgresql://apps:%s@apps-postgres:5432/%s", password, appName)
	}
	// Default: host networking
	return fmt.Sprintf("postgres://apps:%s@localhost:5432/%s?sslmode=disable", password, appName)
}

// getOAuthEnvVarName returns the app-specific environment variable name for OAuth client secret
func getOAuthEnvVarName(appName string) string {
	// Map app names to their expected OAuth env var names
	appOAuthEnvVars := map[string]string{
		"miniflux":      "OAUTH2_CLIENT_SECRET",
		"actual-budget": "ACTUAL_OPENID_CLIENT_SECRET",
		"affine":        "OAUTH_OIDC_CLIENT_SECRET",
	}
	if envName, ok := appOAuthEnvVars[appName]; ok {
		return envName
	}
	// Default to generic name
	return "OAUTH_CLIENT_SECRET"
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
	case "ssoHostSecret":
		return m.secrets.SSOHostSecret
	default:
		return ""
	}
}

// GetPostgresPassword returns the PostgreSQL password.
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

// GetSSOHostSecret returns the master secret for OAuth client secret derivation.
func (m *Manager) GetSSOHostSecret() string {
	return m.Get("ssoHostSecret")
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
		return ""
	}
}

// SetAppSecret sets a specific secret for an app and saves to file.
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

	switch key {
	case "adminPassword":
		appSecrets.AdminPassword = value
	case "oauthClientSecret":
		appSecrets.OAuthClientSecret = value
	case "databasePassword":
		appSecrets.DatabasePassword = value
	default:
		return fmt.Errorf("unknown secret key: %s", key)
	}

	m.secrets.AppSecrets[appName] = appSecrets

	return m.saveLocked()
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

// GetAllSecrets returns a copy of all secrets (for NixOS generation).
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
			copy.AppSecrets[k] = v
		}
	}

	return &copy
}

// Path returns the file path where secrets are stored.
func (m *Manager) Path() string {
	return m.path
}

// WriteEnvFiles regenerates all env files from the current secrets.
// This ensures env files are always in sync with secrets.json.
func (m *Manager) WriteEnvFiles() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.secrets == nil {
		return fmt.Errorf("secrets not loaded")
	}

	dir := filepath.Dir(m.path)
	return m.writeEnvFiles(dir)
}

// AppendEnvVars appends additional environment variables to an app's env file.
// This is used to write host-dependent SSO env vars during prestart,
// after the base env file has been generated by WriteEnvFiles.
func (m *Manager) AppendEnvVars(appName string, vars map[string]string) error {
	if len(vars) == 0 {
		return nil
	}

	dir := filepath.Dir(m.path)
	envPath := filepath.Join(dir, appName+".env")

	f, err := os.OpenFile(envPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("opening env file for append: %w", err)
	}
	defer f.Close()

	for k, v := range vars {
		if _, err := fmt.Fprintf(f, "%s=%s\n", k, v); err != nil {
			return fmt.Errorf("writing env var %s: %w", k, err)
		}
	}

	return nil
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
