package authentik

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	authentikClient "codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// Configurator handles Authentik configuration
type Configurator struct {
	port              int
	bootstrapPassword string
	bootstrapEmail    string
	tokenKey          string // API token key for host-agent
	ldapBindPassword  string // LDAP bind password for service account
	dataPath          string // Path to write token file
}

// NewConfigurator creates a new Authentik configurator
func NewConfigurator(port int, bootstrapPassword, bootstrapEmail, tokenKey, ldapBindPassword, dataPath string) *Configurator {
	return &Configurator{
		port:              port,
		bootstrapPassword: bootstrapPassword,
		bootstrapEmail:    bootstrapEmail,
		tokenKey:          tokenKey,
		ldapBindPassword:  ldapBindPassword,
		dataPath:          dataPath,
	}
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
	return configurator.WaitForHTTP(ctx, url, configurator.DefaultHealthCheckTimeout)
}

// PostStart ensures the admin user has the correct password
// This handles the case where Authentik creates default admin before our bootstrap config runs
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// Use Django shell via podman exec to ensure admin password is set
	// This is reliable because it doesn't depend on having valid API credentials
	// Password and email are passed via environment variables to avoid shell injection
	pythonCode := `
import os
from authentik.core.models import User
try:
    user = User.objects.get(username='akadmin')
    user.set_password(os.environ['BLOUD_ADMIN_PASSWORD'])
    user.email = os.environ['BLOUD_ADMIN_EMAIL']
    user.save()
    print('OK')
except User.DoesNotExist:
    print('OK')  # User will be created by bootstrap
except Exception as e:
    print(f'ERROR: {e}')
`

	// Run via podman exec with env vars for password/email (avoids shell injection)
	cmd := exec.CommandContext(ctx, "podman", "exec",
		"-e", fmt.Sprintf("BLOUD_ADMIN_PASSWORD=%s", c.bootstrapPassword),
		"-e", fmt.Sprintf("BLOUD_ADMIN_EMAIL=%s", c.bootstrapEmail),
		"apps-authentik-server", "ak", "shell", "-c", pythonCode)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to set admin password: %w (output: %s)", err, string(output))
	}

	outputStr := strings.TrimSpace(string(output))
	// Look for OK in the output (there may be logging before it)
	if !strings.Contains(outputStr, "OK") {
		return fmt.Errorf("unexpected output from password set: %s", outputStr)
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

	// Step 3: Create LDAP infrastructure via API
	// Now that we have a valid token, use the API client
	client := authentikClient.NewClient(fmt.Sprintf("http://localhost:%d", c.port), c.tokenKey)
	if err := client.EnsureLDAPInfrastructure(c.ldapBindPassword); err != nil {
		return fmt.Errorf("failed to ensure LDAP infrastructure: %w", err)
	}

	return nil
}

// ensureAPIToken creates or updates the API token via Django shell
// This is more reliable than AUTHENTIK_BOOTSTRAP_TOKEN which has known issues
func (c *Configurator) ensureAPIToken(ctx context.Context) error {
	pythonCode := `
import os
from authentik.core.models import Token, User
try:
    user = User.objects.get(username='akadmin')
    token, created = Token.objects.get_or_create(
        identifier='bloud-api-token',
        defaults={
            'user': user,
            'key': os.environ['BLOUD_TOKEN_KEY'],
            'intent': 'api',
            'expiring': False,
            'description': 'Bloud host-agent API token',
        }
    )
    if not created:
        # Update key and intent if they don't match
        needs_save = False
        if token.key != os.environ['BLOUD_TOKEN_KEY']:
            token.key = os.environ['BLOUD_TOKEN_KEY']
            needs_save = True
        if token.intent != 'api':
            token.intent = 'api'
            needs_save = True
        if needs_save:
            token.save()
    print('OK')
except Exception as e:
    print(f'ERROR: {e}')
`

	cmd := exec.CommandContext(ctx, "podman", "exec",
		"-e", fmt.Sprintf("BLOUD_TOKEN_KEY=%s", c.tokenKey),
		"apps-authentik-server", "ak", "shell", "-c", pythonCode)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("django shell failed: %w (output: %s)", err, string(output))
	}

	outputStr := strings.TrimSpace(string(output))
	if !strings.Contains(outputStr, "OK") {
		return fmt.Errorf("unexpected output: %s", outputStr)
	}

	return nil
}
