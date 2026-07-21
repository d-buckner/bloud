package authentik

import (
	"context"
	_ "embed"
	"fmt"
	"os/exec"
	"strings"
)

//go:embed scripts/set_admin_password.py
var setAdminPasswordScript string

//go:embed scripts/ensure_api_token.py
var ensureAPITokenScript string

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
