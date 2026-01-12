package authentik

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// Configurator handles Authentik configuration
type Configurator struct {
	port              int
	bootstrapPassword string
	bootstrapEmail    string
}

// NewConfigurator creates a new Authentik configurator
func NewConfigurator(port int, bootstrapPassword, bootstrapEmail string) *Configurator {
	return &Configurator{
		port:              port,
		bootstrapPassword: bootstrapPassword,
		bootstrapEmail:    bootstrapEmail,
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
		"authentik-server", "ak", "shell", "-c", pythonCode)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to set admin password: %w (output: %s)", err, string(output))
	}

	outputStr := strings.TrimSpace(string(output))
	// Look for OK in the output (there may be logging before it)
	if !strings.Contains(outputStr, "OK") {
		return fmt.Errorf("unexpected output from password set: %s", outputStr)
	}

	return nil
}
