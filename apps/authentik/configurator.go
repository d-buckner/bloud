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
	if err := runDjangoShell(ctx, map[string]string{
		"BLOUD_ADMIN_PASSWORD": c.bootstrapPassword,
		"BLOUD_ADMIN_EMAIL":    c.bootstrapEmail,
	}, pythonCode); err != nil {
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

	// Step 3: Create LDAP infrastructure via API
	// Now that we have a valid token, use the API client
	client := authentikClient.NewClient(fmt.Sprintf("http://localhost:%d", c.port), c.tokenKey)
	if err := client.EnsureLDAPInfrastructure(c.ldapBindPassword); err != nil {
		return fmt.Errorf("failed to ensure LDAP infrastructure: %w", err)
	}

	return nil
}

// ensureAPIToken creates or updates the API token via Django shell.
// Uses a dedicated bloud-api service account so the token survives akadmin deletion.
func (c *Configurator) ensureAPIToken(ctx context.Context) error {
	pythonCode := `
import os
from authentik.core.models import Token, User, Group
try:
    # Get or create a dedicated service account for the API token
    user, _ = User.objects.get_or_create(
        username='bloud-api',
        defaults={
            'name': 'Bloud API Service Account',
            'type': 'internal_service_account',
            'path': 'users',
            'is_active': True,
        }
    )

    # Add to authentik Admins group for API access
    group = Group.objects.get(name='authentik Admins')
    group.users.add(user)

    # Create or update the API token
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
        needs_save = False
        if token.user != user:
            token.user = user
            needs_save = True
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

	return runDjangoShell(ctx, map[string]string{
		"BLOUD_TOKEN_KEY": c.tokenKey,
	}, pythonCode)
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
