// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	if err := validateProvides(app); err != nil {
		return err
	}
	return validateIntegrations(app)
}

// validateIntegrations checks the consumer-side declarations of a contract: what
// an app says it reads out of the payload has to be something the contract
// carries. A name the contract does not define would leave the app with an empty
// field it reads as "the provider has not published yet", which is the wrong
// diagnosis for a typo in its own metadata.
func validateIntegrations(app *App) error {
	for name, integration := range app.Integrations {
		if len(integration.Requires) == 0 {
			continue
		}
		contract, known := ContractFor(name)
		if !known {
			return fmt.Errorf("integrations.%s.requires needs a contract the framework knows (known: %s)",
				name, strings.Join(ContractNames(), ", "))
		}
		for _, want := range integration.Requires {
			found := false
			for _, carried := range contract.Secrets {
				if carried == want {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("integrations.%s.requires lists %q, which this contract does not carry (it carries %v)",
					name, want, contract.Secrets)
			}
		}
	}
	return nil
}

// validateProvides checks the provider-side declarations against the contract
// registry. Everything here is a cross-file agreement that no compiler sees: a
// provider's `provides.pvr.secrets` has to carry the key the PVR contract names,
// an MCP endpoint's path is concatenated onto an address, and a contract name
// that no consumer can use is a typo. Each of those fails the load instead of
// reaching a consumer as a binding that is silently half-empty.
func validateProvides(app *App) error {
	seen := make(map[string]bool, len(app.Provides))
	for name, offer := range app.Provides {
		contract, known := ContractFor(name)
		if !known {
			return fmt.Errorf("provides.%s is not a known contract (known: %s)",
				name, strings.Join(ContractNames(), ", "))
		}
		if seen[name] {
			return fmt.Errorf("provides lists %s twice", name)
		}
		seen[name] = true

		if err := validateContractSecrets(name, contract, offer); err != nil {
			return err
		}
		if err := validateContractValues(name, contract, offer, app.Port); err != nil {
			return err
		}
	}
	return nil
}

// validateContractSecrets checks the credentials an offer carries against what
// its contract names. A required name that is missing leaves every consumer of
// this contract with an empty payload field, which it reads as "not published
// yet"; a name the contract does not define can never reach a consumer at all,
// because a consumer can only require what the contract names.
func validateContractSecrets(name string, contract Contract, offer ContractProvides) error {
	if len(slices.Compact(slices.Clone(offer.Secrets))) != len(offer.Secrets) {
		return fmt.Errorf("provides.%s.secrets lists a name twice (%v)", name, offer.Secrets)
	}
	for _, key := range offer.Secrets {
		if key == "" || strings.ContainsAny(key, " \t\n") {
			return fmt.Errorf("provides.%s.secrets entries must be single non-empty names (got %q)", name, key)
		}
		if !slices.Contains(contract.Secrets, key) {
			return fmt.Errorf("provides.%s publishes %q, which this contract does not carry (it carries %v)",
				name, key, contract.Secrets)
		}
	}
	for _, want := range contract.Secrets {
		if !slices.Contains(offer.Secrets, want) {
			return fmt.Errorf("provides.%s must publish the %q secret this contract carries (got %v)",
				name, want, offer.Secrets)
		}
	}
	return nil
}

// validateContractValues checks the non-secret values an offer declares: the
// ones its contract requires, and that a value meant to be concatenated onto an
// address is an absolute path on an app that publishes a port (otherwise the
// consumer would be handed an address it cannot use).
func validateContractValues(name string, contract Contract, offer ContractProvides, port int) error {
	for _, want := range contract.Values {
		value, ok := offer.Values[want.Key]
		if !ok || value == "" {
			return fmt.Errorf("provides.%s.values must declare %q, which this contract carries", name, want.Key)
		}
		if want.AbsolutePath && !strings.HasPrefix(value, "/") {
			return fmt.Errorf("provides.%s.values.%s must be an absolute path (got %q)", name, want.Key, value)
		}
	}
	if port <= 0 && len(offer.Values) > 0 {
		return fmt.Errorf("provides.%s declares values that are resolved against the app's address, so the app needs a port", name)
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
