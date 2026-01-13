package miniflux

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// Traefik config for SSO redirect (redirects /embed/miniflux/login to SSO)
const traefikSSOConfig = `# Auto-redirect Miniflux login to SSO
http:
  routers:
    miniflux-login-redirect:
      rule: "Path(` + "`" + `/embed/miniflux/login` + "`" + `)"
      middlewares:
        - miniflux-sso-redirect
      service: miniflux
      priority: 200

  middlewares:
    miniflux-sso-redirect:
      redirectRegex:
        regex: ".*"
        replacement: "/embed/miniflux/oauth2/oidc/redirect"
        permanent: false
`

// Configurator handles Miniflux configuration
type Configurator struct {
	port          int
	adminUsername string
	adminPassword string
	traefikDir    string // Path to traefik dynamic config dir
}

// NewConfigurator creates a new Miniflux configurator
func NewConfigurator(port int, adminUsername, adminPassword, traefikDir string) *Configurator {
	return &Configurator{
		port:          port,
		adminUsername: adminUsername,
		adminPassword: adminPassword,
		traefikDir:    traefikDir,
	}
}

// Name returns the app name
func (c *Configurator) Name() string {
	return "miniflux"
}

// PreStart creates the SSO redirect config if Authentik integration is enabled.
// SSO wait is handled automatically by the framework.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	// Check if SSO integration is enabled
	if _, hasSSO := state.Integrations["sso"]; !hasSSO {
		return nil
	}

	// Ensure traefik dynamic config directory exists
	if err := os.MkdirAll(c.traefikDir, 0755); err != nil {
		return fmt.Errorf("failed to create traefik config dir: %w", err)
	}

	// Write SSO redirect config
	configPath := filepath.Join(c.traefikDir, "miniflux-sso.yml")
	if err := os.WriteFile(configPath, []byte(traefikSSOConfig), 0644); err != nil {
		return fmt.Errorf("failed to write traefik SSO config: %w", err)
	}

	return nil
}

// HealthCheck waits for Miniflux to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	// Miniflux requires the BASE_URL path prefix for all endpoints
	url := fmt.Sprintf("http://localhost:%d/embed/miniflux/healthcheck", c.port)
	return configurator.WaitForHTTP(ctx, url, configurator.DefaultHealthCheckTimeout)
}

// PostStart configures Miniflux settings via API (e.g., admin theme)
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// Set light theme for admin user (user ID 1 is always the initial admin)
	// Miniflux requires the BASE_URL path prefix for all endpoints
	// Note: PUT /v1/me doesn't exist, must use /v1/users/{id}
	url := fmt.Sprintf("http://localhost:%d/embed/miniflux/v1/users/1", c.port)

	payload := map[string]string{"theme": "light_serif"}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.adminUsername, c.adminPassword)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to configure miniflux: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("miniflux API returned status %d", resp.StatusCode)
	}

	return nil
}
