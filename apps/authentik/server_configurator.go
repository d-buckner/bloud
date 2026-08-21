// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package authentik

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	authentikClient "codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// ServerConfigurator handles the apps-authentik-server container lifecycle.
// Container creation is managed declaratively via metadata.yaml; this configurator
// handles PreStart setup and PostStart API configuration only.
type ServerConfigurator struct {
	port              int
	bootstrapPassword string
	bootstrapEmail    string
	tokenKey          string // API token key for host-agent
	ldapBindPassword  string // LDAP bind password for service account
	brandingCSS       string // Inline CSS to push to Authentik brand API
	baseURL           string // External base URL for embedded outpost host
	appsDir           string // Path to the apps directory (for auth.yaml blueprint)
	templateVars      map[string]string // Shared mutable map; PostStart writes authentikLdapToken
}

// NewServerConfigurator creates a new Authentik server configurator.
func NewServerConfigurator(
	port int,
	bootstrapPassword, bootstrapEmail, tokenKey, ldapBindPassword, brandingCSS, appsDir string,
	templateVars map[string]string,
) *ServerConfigurator {
	return &ServerConfigurator{
		port:              port,
		bootstrapPassword: bootstrapPassword,
		bootstrapEmail:    bootstrapEmail,
		tokenKey:          tokenKey,
		ldapBindPassword:  ldapBindPassword,
		brandingCSS:       brandingCSS,
		appsDir:           appsDir,
		templateVars:      templateVars,
	}
}

// WithBaseURL sets the external base URL used to configure the embedded outpost host.
func (c *ServerConfigurator) WithBaseURL(baseURL string) *ServerConfigurator {
	c.baseURL = baseURL
	return c
}

// Name returns the container name this configurator handles.
func (c *ServerConfigurator) Name() string {
	return "apps-authentik-server"
}

// PreStart prepares the Authentik server environment:
// - Copies the custom auth flow blueprint to the data directory
// - Creates media and templates directories with correct permissions
func (c *ServerConfigurator) PreStart(_ context.Context, state *configurator.AppState) (bool, error) {
	// Copy auth flow blueprint from apps dir to data dir.
	// The server container mounts this file read-only at /blueprints/default/...
	srcPath := filepath.Join(c.appsDir, "authentik", "auth.yaml")
	dstPath := filepath.Join(state.DataPath, "authentik-auth-flow.yaml")
	src, err := os.ReadFile(srcPath)
	if err != nil {
		return false, fmt.Errorf("read auth.yaml: %w", err)
	}

	// Detect whether the blueprint content actually changed.
	blueprintChanged := true
	if existing, err := os.ReadFile(dstPath); err == nil {
		blueprintChanged = !bytes.Equal(existing, src)
	}

	if blueprintChanged {
		if err := os.WriteFile(dstPath, src, 0644); err != nil {
			return false, fmt.Errorf("write auth flow blueprint: %w", err)
		}
	}

	// Create media and templates dirs with world-writable permissions.
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
// 1. Sets the admin user password and email via Django shell
// 2. Ensures the API token for host-agent exists
// 3. Pushes branding CSS to the Authentik brand API
// 4. Applies the login page configuration (flow title, username-only identification)
// 5. Creates LDAP provider, application, outpost, and service account
// 6. Sets the embedded outpost's authentik_host to the external base URL
// 7. Retrieves the LDAP outpost token and writes it to the shared templateVars map
//
// All steps are idempotent.
func (c *ServerConfigurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// Step 1: Set admin password via Django shell.
	if err := runDjangoShell(ctx, map[string]string{
		"BLOUD_ADMIN_PASSWORD": c.bootstrapPassword,
		"BLOUD_ADMIN_EMAIL":    c.bootstrapEmail,
	}, setAdminPasswordScript); err != nil {
		return fmt.Errorf("set admin password: %w", err)
	}

	// Step 2: Ensure API token via Django shell.
	if err := runDjangoShell(ctx, map[string]string{
		"BLOUD_TOKEN_KEY": c.tokenKey,
	}, ensureAPITokenScript); err != nil {
		return fmt.Errorf("ensure API token: %w", err)
	}

	// Write token to file for host-agent to read.
	tokenPath := filepath.Join(state.DataPath, "api-token")
	if err := os.WriteFile(tokenPath, []byte(c.tokenKey), 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}

	client := authentikClient.NewClient(fmt.Sprintf("http://localhost:%d", c.port), c.tokenKey)

	// Step 3: Push branding CSS.
	if c.brandingCSS != "" {
		if err := client.EnsureBranding(c.brandingCSS); err != nil {
			return fmt.Errorf("ensure branding: %w", err)
		}
	}

	// Step 4: Apply login page configuration.
	if err := client.EnsureLoginConfiguration(); err != nil {
		return fmt.Errorf("ensure login configuration: %w", err)
	}

	// Step 5: Create LDAP infrastructure.
	if err := client.EnsureLDAPInfrastructure(c.ldapBindPassword); err != nil {
		return fmt.Errorf("ensure LDAP infrastructure: %w", err)
	}

	// Step 6: Set embedded outpost host.
	if c.baseURL != "" {
		if err := client.EnsureEmbeddedOutpostHost(c.baseURL); err != nil {
			return fmt.Errorf("set embedded outpost host: %w", err)
		}
	}

	// Step 7: Get LDAP outpost token and write to shared template vars.
	// The apps-authentik-ldap container spec uses {{authentikLdapToken}}; the
	// orchestrator resolves this map at container spec build time, which happens
	// after this PostStart (ldap depends on server via metadata dependsOn).
	ldapToken, err := client.GetLDAPOutpostToken()
	if err != nil {
		return fmt.Errorf("get LDAP outpost token: %w", err)
	}
	if c.templateVars != nil {
		c.templateVars["authentikLdapToken"] = ldapToken
	}

	return nil
}

// Remove is a no-op: container removal is handled by the orchestrator via metadata.
func (c *ServerConfigurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}
