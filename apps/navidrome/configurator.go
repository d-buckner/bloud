package navidrome

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

const appName = "navidrome"

// bootstrapAdminUsername is the internal-only admin used by the configurator.
// It never appears in Authentik and is not meant for end users.
const bootstrapAdminUsername = "bloud-admin"

// Configurator handles Navidrome configuration
type Configurator struct {
	port         int
	authentikURL string
	secrets      configurator.AppSecretsProvider
}

// NewConfigurator creates a new Navidrome configurator.
func NewConfigurator(port int, authentikURL string, secrets configurator.AppSecretsProvider) *Configurator {
	if port == 0 {
		port = 4533
	}
	return &Configurator{
		port:         port,
		authentikURL: authentikURL,
		secrets:      secrets,
	}
}

func (c *Configurator) Name() string {
	return appName
}

// PreStart creates the required data and music directories before the container starts.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	dirs := []string{
		filepath.Join(state.DataPath, "data"),
		filepath.Join(state.BloudDataPath, "media", "music"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}

// HealthCheck waits for Navidrome to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	url := fmt.Sprintf("http://localhost:%d/ping", c.port)
	return configurator.WaitForHTTP(ctx, url, 60*time.Second)
}

// PostStart syncs Authentik users into Navidrome so that forward-auth logins work.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if !state.SSOEnabled {
		return nil
	}

	token, err := c.ensureAdminAndLogin(ctx)
	if err != nil {
		return fmt.Errorf("navidrome: admin bootstrap: %w", err)
	}

	// Read the Authentik API token from disk (written by the Authentik configurator).
	authentikToken, err := c.readAuthentikToken(state)
	if err != nil {
		log.Printf("Navidrome: cannot read Authentik token, skipping user sync: %v", err)
		return nil
	}

	if err := c.syncUsersFromAuthentik(ctx, token, authentikToken); err != nil {
		return fmt.Errorf("navidrome: user sync: %w", err)
	}
	return nil
}

// --- Admin bootstrap ---

// ensureAdminAndLogin ensures the bootstrap admin exists and returns a valid token.
func (c *Configurator) ensureAdminAndLogin(ctx context.Context) (string, error) {
	if c.secrets == nil {
		return "", fmt.Errorf("no secrets provider")
	}
	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		return "", fmt.Errorf("generating admin password: %w", err)
	}

	// Fast path: try logging in with existing credentials.
	if token, err := c.login(ctx, bootstrapAdminUsername, password); err == nil {
		return token, nil
	}

	// No admin yet — bootstrap the first admin user.
	log.Println("Navidrome: bootstrapping admin user")
	token, err := c.createAdmin(ctx, bootstrapAdminUsername, password)
	if err != nil {
		return "", fmt.Errorf("creating admin: %w", err)
	}
	log.Println("Navidrome: admin user created")
	return token, nil
}

func (c *Configurator) baseURL() string {
	return fmt.Sprintf("http://localhost:%d", c.port)
}

// createAdmin calls /auth/createAdmin (only works when no users exist).
func (c *Configurator) createAdmin(ctx context.Context, username, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/auth/createAdmin", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}

	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Token, nil
}

// login exchanges credentials for a session token.
func (c *Configurator) login(ctx context.Context, username, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login failed: status %d", resp.StatusCode)
	}

	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("empty token")
	}
	return out.Token, nil
}

// --- Navidrome user API ---

type navidromeUser struct {
	ID       string `json:"id"`
	UserName string `json:"userName"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	IsAdmin  bool   `json:"isAdmin"`
}

// listNavidromeUsers returns all users from Navidrome.
func (c *Configurator) listNavidromeUsers(ctx context.Context, token string) ([]navidromeUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL()+"/api/user?_end=500&_start=0&_order=ASC&_sort=id", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ND-Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}

	var users []navidromeUser
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}
	return users, nil
}

// createNavidromeUser creates a user in Navidrome.
func (c *Configurator) createNavidromeUser(ctx context.Context, token, username, name, email string) error {
	body, _ := json.Marshal(map[string]interface{}{
		"userName": username,
		"name":     name,
		"email":    email,
		"isAdmin":  false,
		"password": "placeholder", // unused with forward-auth; required field
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/api/user", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ND-Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// --- Authentik user API ---

type authentikUser struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

// listAuthentikUsers returns internal active users from Authentik.
func (c *Configurator) listAuthentikUsers(ctx context.Context, token string) ([]authentikUser, error) {
	if c.authentikURL == "" {
		return nil, fmt.Errorf("no authentik URL configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.authentikURL+"/api/v3/core/users/?type=internal&is_active=true&page_size=100", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}

	var out struct {
		Results []authentikUser `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// readAuthentikToken reads the Authentik API token from disk.
func (c *Configurator) readAuthentikToken(state *configurator.AppState) (string, error) {
	tokenPath := filepath.Join(state.BloudDataPath, "authentik", "api-token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", err
	}
	token := string(bytes.TrimSpace(data))
	if token == "" {
		return "", fmt.Errorf("empty token file")
	}
	return token, nil
}

// --- Sync logic ---

// syncUsersFromAuthentik creates any Authentik users that don't yet exist in Navidrome.
// Existing users are left untouched. The bootstrap admin is excluded from sync.
func (c *Configurator) syncUsersFromAuthentik(ctx context.Context, naviToken, authentikToken string) error {
	navUsers, err := c.listNavidromeUsers(ctx, naviToken)
	if err != nil {
		return fmt.Errorf("listing navidrome users: %w", err)
	}

	existing := make(map[string]struct{}, len(navUsers))
	for _, u := range navUsers {
		existing[u.UserName] = struct{}{}
	}

	akUsers, err := c.listAuthentikUsers(ctx, authentikToken)
	if err != nil {
		return fmt.Errorf("listing authentik users: %w", err)
	}

	created := 0
	for _, u := range akUsers {
		// Skip the bootstrap admin (internal-only) and Authentik's own default admin.
		if u.Username == bootstrapAdminUsername || u.Username == "akadmin" {
			continue
		}
		if _, ok := existing[u.Username]; ok {
			continue
		}
		displayName := u.Name
		if displayName == "" {
			displayName = u.Username
		}
		log.Printf("Navidrome: creating user %q (%s)", u.Username, displayName)
		if err := c.createNavidromeUser(ctx, naviToken, u.Username, displayName, u.Email); err != nil {
			log.Printf("Navidrome: failed to create user %q: %v", u.Username, err)
			continue
		}
		created++
	}

	if created > 0 {
		log.Printf("Navidrome: synced %d user(s) from Authentik", created)
	} else {
		log.Println("Navidrome: all Authentik users already present")
	}
	return nil
}
