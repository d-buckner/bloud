package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

const (
	adminEmail    = "admin@bloud.local"
	immichAppName = "immich"
	clientID      = "immich-client"

	postgresSocketDir = "/run/postgresql"
	postgresOwner     = "apps"
)

// Configurator handles Immich configuration
type Configurator struct {
	port         int
	authentikURL string
	secrets      configurator.AppSecretsProvider
}

// NewConfigurator creates a new Immich configurator.
// secrets is used to generate/retrieve the admin password and OAuth client secret.
func NewConfigurator(port int, authentikURL string, secrets configurator.AppSecretsProvider) *Configurator {
	if port == 0 {
		port = 2283
	}
	return &Configurator{
		port:         port,
		authentikURL: authentikURL,
		secrets:      secrets,
	}
}

// Name returns the app name
func (c *Configurator) Name() string {
	return immichAppName
}

func (c *Configurator) baseURL() string {
	return fmt.Sprintf("http://localhost:%d", c.port)
}

// PreStart initialises the Immich postgres database and enables the pgvector extension.
// Postgres is guaranteed to be ready by the time PreStart runs (systemd ordering via
// nativeIntegrationDeps ensures the unix socket exists before this service starts).
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	log.Println("Immich: initializing database...")
	if err := configurator.EnsureDatabase(ctx, postgresSocketDir, postgresOwner, "immich", []string{"vector"}); err != nil {
		return fmt.Errorf("initializing immich database: %w", err)
	}
	log.Println("Immich: database ready")
	return nil
}

// HealthCheck waits for the Immich server API to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	url := c.baseURL() + "/api/server/ping"
	return configurator.WaitForHTTP(ctx, url, 120*time.Second)
}

// PostStart creates the admin user and configures OIDC if Authentik SSO is enabled
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	adminPassword, err := c.secrets.GenerateAppAdminPassword(immichAppName)
	if err != nil {
		return fmt.Errorf("getting admin password: %w", err)
	}

	if err := c.createAdminUser(ctx, adminPassword); err != nil {
		return fmt.Errorf("creating admin user: %w", err)
	}

	token, err := c.login(ctx, adminEmail, adminPassword)
	if err != nil {
		return fmt.Errorf("logging in: %w", err)
	}

	if !state.SSOEnabled {
		log.Println("Immich: no SSO integration, skipping OIDC configuration")
		return nil
	}

	if err := c.configureOIDC(ctx, token); err != nil {
		return fmt.Errorf("configuring OIDC: %w", err)
	}

	return nil
}

// AdminSignUpRequest is the payload for creating the first Immich admin user
type AdminSignUpRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

// createAdminUser attempts to sign up the first admin user.
// Immich returns 400 if an admin already exists — that is treated as success.
func (c *Configurator) createAdminUser(ctx context.Context, password string) error {
	url := c.baseURL() + "/api/auth/admin-sign-up"

	payload := AdminSignUpRequest{
		Email:    adminEmail,
		Password: password,
		Name:     "Bloud Admin",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 201 = created, 400 = admin already exists (idempotent)
	if resp.StatusCode == http.StatusCreated {
		log.Println("Immich: admin user created")
		return nil
	}
	if resp.StatusCode == http.StatusBadRequest {
		log.Println("Immich: admin user already exists")
		return nil
	}

	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
}

// LoginRequest is the payload for authenticating with Immich
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse is the Immich authentication response
type LoginResponse struct {
	AccessToken string `json:"accessToken"`
}

// login authenticates with Immich and returns an access token
func (c *Configurator) login(ctx context.Context, email, password string) (string, error) {
	url := c.baseURL() + "/api/auth/login"

	payload := LoginRequest{
		Email:    email,
		Password: password,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("login failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var loginResp LoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return "", fmt.Errorf("decoding login response: %w", err)
	}

	return loginResp.AccessToken, nil
}

// configureOIDC fetches the current system config, merges OIDC settings, and updates it
func (c *Configurator) configureOIDC(ctx context.Context, token string) error {
	clientSecret := c.secrets.GetAppSecret(immichAppName, "oauthClientSecret")
	if clientSecret == "" {
		log.Println("Immich: OAuth client secret not yet available, skipping OIDC configuration")
		return nil
	}

	issuerURL := c.authentikURL + "/application/o/immich/"

	currentConfig, err := c.getSystemConfig(ctx, token)
	if err != nil {
		return fmt.Errorf("fetching system config: %w", err)
	}

	oauth, ok := currentConfig["oauth"].(map[string]interface{})
	if !ok {
		oauth = make(map[string]interface{})
	}

	desiredOAuth := map[string]interface{}{
		"enabled":             true,
		"issuerUrl":           issuerURL,
		"clientId":            clientID,
		"scope":               "openid email profile",
		"buttonText":          "Sign in with Bloud",
		"autoRegister":        true,
		"autoLaunch":          false,
		"storageLabelClaim":   "preferred_username",
		"storageQuotaClaim":   "immich_quota",
		"defaultStorageQuota": float64(0),
	}

	if hasOAuthConfig(oauth, desiredOAuth) {
		log.Println("Immich: OIDC already configured")
		return nil
	}

	for key, value := range desiredOAuth {
		oauth[key] = value
	}
	oauth["clientSecret"] = clientSecret

	currentConfig["oauth"] = oauth

	if err := c.putSystemConfig(ctx, token, currentConfig); err != nil {
		return fmt.Errorf("updating system config: %w", err)
	}

	log.Println("Immich: OIDC configured successfully")
	return nil
}

func hasOAuthConfig(current, desired map[string]interface{}) bool {
	for key, value := range desired {
		if current[key] != value {
			return false
		}
	}
	return true
}

// getSystemConfig fetches the current Immich system configuration
func (c *Configurator) getSystemConfig(ctx context.Context, token string) (map[string]interface{}, error) {
	url := c.baseURL() + "/api/system/config"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var cfg map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decoding system config: %w", err)
	}

	return cfg, nil
}

// putSystemConfig updates the Immich system configuration
func (c *Configurator) putSystemConfig(ctx context.Context, token string, cfg map[string]interface{}) error {
	url := c.baseURL() + "/api/system/config"

	body, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
