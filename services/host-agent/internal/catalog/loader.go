// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Loader handles loading app definitions from YAML files
type Loader struct {
	appsDir string
}

// NewLoader creates a new catalog loader
// appsDir should be the path to the apps/ directory containing app subdirectories
func NewLoader(appsDir string) *Loader {
	return &Loader{
		appsDir: appsDir,
	}
}

// LoadAll loads all app definitions from the apps directory
// Each app has its own subdirectory with a metadata.yaml file
func (l *Loader) LoadAll() (map[string]*App, error) {
	apps := make(map[string]*App)

	entries, err := os.ReadDir(l.appsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read apps directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		metadataPath := filepath.Join(l.appsDir, entry.Name(), "metadata.yaml")
		if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
			continue
		}

		app, err := l.loadAppFromFile(metadataPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load %s: %w", entry.Name(), err)
		}

		apps[app.CatalogID] = app
	}

	return apps, nil
}

// loadAppFromFile loads a single app definition from a YAML file
func (l *Loader) loadAppFromFile(filePath string) (*App, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var app App
	if err := yaml.Unmarshal(data, &app); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	if err := l.validateApp(&app); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	return &app, nil
}

// validateApp validates that an app definition has all required fields
func (l *Loader) validateApp(app *App) error {
	if app.CatalogID == "" {
		return fmt.Errorf("app name is required")
	}
	if app.DisplayName == "" {
		return fmt.Errorf("displayName is required")
	}
	if app.Description == "" {
		return fmt.Errorf("description is required")
	}
	if app.Category == "" {
		return fmt.Errorf("category is required")
	}
	if len(app.SSO.BypassPaths) > 0 && app.SSO.Strategy != "forward-auth" {
		return fmt.Errorf("sso.bypassPaths is only valid for strategy: forward-auth (got %q)", app.SSO.Strategy)
	}
	if app.SSO.Strategy != "native-oidc" {
		if len(app.SSO.Scopes) > 0 {
			return fmt.Errorf("sso.scopes is only valid for strategy: native-oidc (got %q)", app.SSO.Strategy)
		}
		if app.SSO.AccessTokenMinutes != 0 {
			return fmt.Errorf("sso.accessTokenMinutes is only valid for strategy: native-oidc (got %q)", app.SSO.Strategy)
		}
	}
	if app.SSO.AccessTokenMinutes < 0 {
		return fmt.Errorf("sso.accessTokenMinutes must not be negative (got %d)", app.SSO.AccessTokenMinutes)
	}
	for _, scope := range app.SSO.Scopes {
		if strings.TrimSpace(scope) == "" || strings.ContainsAny(scope, " \t") {
			return fmt.Errorf("sso.scopes entries must be single non-empty scope names (got %q)", scope)
		}
		switch scope {
		case "openid", "profile", "email":
			return fmt.Errorf("sso.scopes must not list %q: every native-oidc provider already carries it", scope)
		}
	}
	// The loopback issuer is http://localhost:<Traefik port>, which reaches
	// Traefik only from inside the host network namespace. Without it the
	// dashboard's provider registers and the app looks healthy, yet every
	// login fails when discovery cannot reach the issuer: catch that here.
	if app.SSO.LoopbackIssuer && !app.HasHostNetworkedContainer() {
		return fmt.Errorf("sso.loopbackIssuer requires a container with network: host " +
			"(the issuer is http://localhost:<Traefik port>, which resolves to Traefik only in the host network namespace)")
	}
	return nil
}

// LoadGraph loads app definitions and builds an AppGraph
func (l *Loader) LoadGraph() (*AppGraph, error) {
	entries, err := os.ReadDir(l.appsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read apps directory: %w", err)
	}

	var apps []*AppDefinition

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		metadataPath := filepath.Join(l.appsDir, entry.Name(), "metadata.yaml")
		if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
			continue
		}

		app, err := l.loadAppDefinition(metadataPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load %s: %w", entry.Name(), err)
		}

		apps = append(apps, app)
	}

	return NewGraph(apps), nil
}

// loadAppDefinition loads a single AppDefinition from a YAML file
func (l *Loader) loadAppDefinition(filePath string) (*AppDefinition, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var app AppDefinition
	if err := yaml.Unmarshal(data, &app); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	if app.Name == "" {
		return nil, fmt.Errorf("app name is required")
	}

	return &app, nil
}
