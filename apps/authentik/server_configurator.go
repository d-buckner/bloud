// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package authentik

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	authentikClient "codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

// Params are the Authentik-specific values the system configurator needs. They
// come from the host-agent config (and the shared templateVars map) rather than
// from Deps, which carries only the generic host services.
type Params struct {
	Port              int
	BootstrapPassword string
	BootstrapEmail    string
	TokenKey          string            // API token key for host-agent
	LDAPBindPassword  string            // LDAP bind password for service account
	BrandingCSS       string            // Inline CSS to push to Authentik brand API
	AppsDir           string            // Path to the apps directory (for auth.yaml blueprint)
	TemplateVars      map[string]string // Shared map; PostStart writes authentikLdapToken
}

// ServerConfigurator handles the apps-authentik-server container lifecycle.
// Container creation is managed declaratively via metadata.yaml; this configurator
// handles PreStart setup and PostStart API configuration only.
type ServerConfigurator struct {
	deps   configurator.Deps
	params Params
	logger *slog.Logger
}

// NewServerConfigurator builds the Authentik server configurator from the shared
// host Deps and the Authentik-specific Params. Container exec goes through
// Deps.Exec and the API client through Deps.HTTP, so nothing here holds a podman
// handle of its own.
func NewServerConfigurator(deps configurator.Deps, params Params) *ServerConfigurator {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &ServerConfigurator{
		deps:   deps,
		params: params,
		logger: logger.With("app", "authentik"),
	}
}

// Name returns the container name this configurator handles.
func (c *ServerConfigurator) Name() string {
	return "apps-authentik-server"
}

// PreStart prepares the Authentik server environment:
//   - Copies the custom auth flow blueprint to the data directory
//   - Creates media and templates directories with correct permissions
//
// The blueprint write goes through managedfile, so an unchanged file reports no
// change and does not trigger a container recreate.
func (c *ServerConfigurator) PreStart(_ context.Context, state *configurator.AppState) (bool, error) {
	srcPath := filepath.Join(c.params.AppsDir, "authentik", "auth.yaml")
	src, err := os.ReadFile(srcPath)
	if err != nil {
		return false, fmt.Errorf("read auth.yaml: %w", err)
	}

	// The server container mounts this file read-only at
	// /blueprints/default/flow-default-authentication-flow.yaml.
	dstPath := filepath.Join(state.DataPath, "authentik-auth-flow.yaml")
	blueprintChanged, err := managedfile.Write(dstPath, src, 0644)
	if err != nil {
		return false, fmt.Errorf("write auth flow blueprint: %w", err)
	}

	// Authentik runs as a non-root user and needs write access to these dirs.
	for _, dir := range []string{
		filepath.Join(state.DataPath, "media"),
		filepath.Join(state.DataPath, "templates"),
	} {
		if err := os.MkdirAll(dir, 0777); err != nil {
			return false, fmt.Errorf("create dir %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0777); err != nil {
			return false, fmt.Errorf("chmod dir %s: %w", dir, err)
		}
	}

	return blueprintChanged, nil
}

// PostStart configures Authentik after it is healthy:
//  1. Sets the admin user password and email via Django shell
//  2. Ensures the API token for host-agent exists
//  3. Pushes branding CSS to the Authentik brand API
//  4. Applies the login page configuration (flow title, username-only identification)
//  5. Creates LDAP provider, application, outpost, and service account
//  6. Sets the embedded outpost's authentik_host to the external base URL
//  7. Retrieves the LDAP outpost token and writes it to the shared templateVars map
//
// All steps are idempotent. Per the framework's PostStart contract, a returned
// error is terminal for the node; the calls here are single-shot by design (the
// container health check gates this phase), and a transient failure is retried
// by the next reconciliation pass.
func (c *ServerConfigurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// Step 1: Set admin password via Django shell.
	if err := c.runDjangoShell(ctx, map[string]string{
		"BLOUD_ADMIN_PASSWORD": c.params.BootstrapPassword,
		"BLOUD_ADMIN_EMAIL":    c.params.BootstrapEmail,
	}, setAdminPasswordScript); err != nil {
		return fmt.Errorf("set admin password: %w", err)
	}

	// Step 2: Ensure API token via Django shell.
	if err := c.runDjangoShell(ctx, map[string]string{
		"BLOUD_TOKEN_KEY": c.params.TokenKey,
	}, ensureAPITokenScript); err != nil {
		return fmt.Errorf("ensure API token: %w", err)
	}

	// Write token to file for host-agent to read.
	tokenPath := filepath.Join(state.DataPath, "api-token")
	if _, err := managedfile.Write(tokenPath, []byte(c.params.TokenKey), 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}

	client := authentikClient.NewClient(fmt.Sprintf("http://localhost:%d", c.params.Port), c.params.TokenKey).
		WithClientFactory(c.deps.HTTP)

	// Step 3: Push branding CSS.
	if c.params.BrandingCSS != "" {
		if err := client.EnsureBranding(ctx, c.params.BrandingCSS); err != nil {
			return fmt.Errorf("ensure branding: %w", err)
		}
	}

	// Step 4: Apply login page configuration.
	if err := client.EnsureLoginConfiguration(ctx); err != nil {
		return fmt.Errorf("ensure login configuration: %w", err)
	}

	// Step 5: Create LDAP infrastructure.
	if err := client.EnsureLDAPInfrastructure(ctx, c.params.LDAPBindPassword); err != nil {
		return fmt.Errorf("ensure LDAP infrastructure: %w", err)
	}

	// Step 6: Set embedded outpost host.
	if c.deps.PrimaryBaseURL != nil {
		if baseURL := c.deps.PrimaryBaseURL(); baseURL != "" {
			if err := client.EnsureEmbeddedOutpostHost(ctx, baseURL); err != nil {
				return fmt.Errorf("set embedded outpost host: %w", err)
			}
		}
	}

	// Step 7: Get LDAP outpost token and write to shared template vars.
	// The apps-authentik-ldap container spec uses {{authentikLdapToken}}; the
	// orchestrator resolves this map at container spec build time, which happens
	// after this PostStart (ldap depends on server via metadata dependsOn).
	ldapToken, err := client.GetLDAPOutpostToken(ctx)
	if err != nil {
		return fmt.Errorf("get LDAP outpost token: %w", err)
	}
	if c.params.TemplateVars != nil {
		c.params.TemplateVars["authentikLdapToken"] = ldapToken
	}

	return nil
}

// runDjangoShell executes a Python script inside the Authentik container via
// `ak shell`. It runs through Deps.Exec, so no podman command is hardcoded here:
// the host runtime owns the call, and the environment variables travel as exec
// env rather than on the command line. The script must print "OK" on success.
func (c *ServerConfigurator) runDjangoShell(ctx context.Context, env map[string]string, script string) error {
	if c.deps.Exec == nil {
		return fmt.Errorf("no container exec available (no host runtime wired)")
	}
	out, err := c.deps.Exec(ctx, c.Name(), env, []string{"ak", "shell", "-c", script})
	if err != nil {
		return fmt.Errorf("django shell failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(strings.TrimSpace(string(out)), "OK") {
		return fmt.Errorf("django shell failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
