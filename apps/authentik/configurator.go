package authentik

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	authentikClient "codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

//go:embed scripts/set_admin_password.py
var setAdminPasswordScript string

//go:embed scripts/ensure_api_token.py
var ensureAPITokenScript string

// healthCheckTimeout allows for first-boot DB migrations which can take 3-5 minutes.
const healthCheckTimeout = 8 * time.Minute

// Configurator handles Authentik configuration
type Configurator struct {
	port              int
	bootstrapPassword string
	bootstrapEmail    string
	tokenKey          string // API token key for host-agent
	ldapBindPassword  string // LDAP bind password for service account
	dataPath          string // Path to write token file
	brandingCSS       string // Inline CSS to push to Authentik brand API
	baseURL           string // External base URL for embedded outpost host (e.g. http://localhost:8080)
}

// NewConfigurator creates a new Authentik configurator
func NewConfigurator(port int, bootstrapPassword, bootstrapEmail, tokenKey, ldapBindPassword, dataPath, brandingCSS string) *Configurator {
	return &Configurator{
		port:              port,
		bootstrapPassword: bootstrapPassword,
		bootstrapEmail:    bootstrapEmail,
		tokenKey:          tokenKey,
		ldapBindPassword:  ldapBindPassword,
		dataPath:          dataPath,
		brandingCSS:       brandingCSS,
	}
}

// WithBaseURL sets the external base URL used to configure the embedded outpost host.
// When set, PostStart will ensure the embedded outpost redirects browsers through this URL.
func (c *Configurator) WithBaseURL(baseURL string) *Configurator {
	c.baseURL = baseURL
	return c
}

// Name returns the app name
func (c *Configurator) Name() string {
	return "authentik"
}

// PreStart is a no-op for Authentik
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	return nil
}

// HealthCheck waits for Authentik to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	url := fmt.Sprintf("http://localhost:%d/-/health/ready/", c.port)
	return configurator.WaitForHTTP(ctx, url, healthCheckTimeout)
}

// PostStart ensures the admin user has the correct password
// This handles the case where Authentik creates default admin before our bootstrap config runs
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// Use Django shell via podman exec to ensure admin password is set.
	// This is reliable because it doesn't depend on having valid API credentials.
	// Password and email are passed via environment variables to avoid shell injection.
	if err := runDjangoShell(ctx, map[string]string{
		"BLOUD_ADMIN_PASSWORD": c.bootstrapPassword,
		"BLOUD_ADMIN_EMAIL":    c.bootstrapEmail,
	}, setAdminPasswordScript); err != nil {
		return fmt.Errorf("failed to set admin password: %w", err)
	}

	// Step 2: Ensure API token exists via Django shell
	// This is more reliable than AUTHENTIK_BOOTSTRAP_TOKEN which only works on first boot
	if err := c.ensureAPIToken(ctx); err != nil {
		return fmt.Errorf("failed to ensure API token: %w", err)
	}

	// Write token to file for host-agent to read
	tokenPath := filepath.Join(c.dataPath, "api-token")
	if err := os.MkdirAll(c.dataPath, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}
	if err := os.WriteFile(tokenPath, []byte(c.tokenKey), 0600); err != nil {
		return fmt.Errorf("failed to write token file: %w", err)
	}

	// Now that we have a valid token, use the API client
	client := authentikClient.NewClient(fmt.Sprintf("http://localhost:%d", c.port), c.tokenKey)

	// Step 3: Push branding CSS inline via API.
	// MUST run before EnsureLoginConfiguration — the smoke test uses the login page title
	// ("Sign in to Bloud") as its synchronization signal, polling until PostStart sets it.
	// CSS must already be applied by the time the title becomes correct, otherwise there
	// is a window where the page shows the right title but wrong styling.
	// The default brand always exists after DB migration, so this call is safe to make
	// before flows are created by blueprints.
	if c.brandingCSS != "" {
		if err := client.EnsureBranding(c.brandingCSS); err != nil {
			return fmt.Errorf("failed to ensure branding: %w", err)
		}
	}

	// Step 4: Apply login page configuration (flow title + username-only identification).
	// Has a built-in retry loop that waits for default blueprints to create flows.
	// Once it returns, the default flows are confirmed to exist — which LDAP infra also needs.
	if err := client.EnsureLoginConfiguration(); err != nil {
		return fmt.Errorf("failed to ensure login configuration: %w", err)
	}

	// Step 5: Create LDAP infrastructure via API.
	// Runs after EnsureLoginConfiguration so that default flows are guaranteed to exist.
	if err := client.EnsureLDAPInfrastructure(c.ldapBindPassword); err != nil {
		return fmt.Errorf("failed to ensure LDAP infrastructure: %w", err)
	}

	// Step 6: Set the embedded outpost's authentik_host so browsers are redirected
	// through the external base URL (e.g. Traefik) rather than the internal bind address.
	if c.baseURL != "" {
		if err := client.EnsureEmbeddedOutpostHost(c.baseURL); err != nil {
			return fmt.Errorf("failed to set embedded outpost host: %w", err)
		}
	}

	return nil
}

// ensureAPIToken creates or updates the API token via Django shell.
// Uses a dedicated bloud-api service account so the token survives akadmin deletion.
func (c *Configurator) ensureAPIToken(ctx context.Context) error {
	return runDjangoShell(ctx, map[string]string{
		"BLOUD_TOKEN_KEY": c.tokenKey,
	}, ensureAPITokenScript)
}

// runDjangoShell executes a Python script inside the Authentik container via `ak shell`.
// Environment variables are passed securely via podman exec -e flags.
// The script must print 'OK' on success or 'ERROR: ...' on failure.
func runDjangoShell(ctx context.Context, env map[string]string, script string) error {
	args := []string{"exec"}
	for k, v := range env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, "apps-authentik-server", "ak", "shell", "-c", script)

	output, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("django shell failed: %w (output: %s)", err, string(output))
	}

	if !strings.Contains(strings.TrimSpace(string(output)), "OK") {
		return fmt.Errorf("django shell failed: %s", string(output))
	}

	return nil
}
