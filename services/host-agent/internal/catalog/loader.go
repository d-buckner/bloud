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
	if err := validateIdentity(app); err != nil {
		return err
	}
	if err := validateSSO(app); err != nil {
		return err
	}
	if err := validateProvides(app); err != nil {
		return err
	}
	return validateIntegrations(app)
}

// validateIdentity checks the fields every catalog entry must carry to render.
func validateIdentity(app *App) error {
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
	return nil
}

// validateSSO checks the SSO declaration is internally consistent: each knob
// belongs to a strategy that can actually honor it, so a mistake reads as a
// validation error at load time rather than as an app that silently never
// joins the provider.
func validateSSO(app *App) error {
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
	if err := validateSSOScopeNames(app); err != nil {
		return err
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

// validateSSOScopeNames checks each declared extra scope is a single name the
// provider will not already have sent.
func validateSSOScopeNames(app *App) error {
	for _, scope := range app.SSO.Scopes {
		if strings.TrimSpace(scope) == "" || strings.ContainsAny(scope, " \t") {
			return fmt.Errorf("sso.scopes entries must be single non-empty scope names (got %q)", scope)
		}
		switch scope {
		case "openid", "profile", "email":
			return fmt.Errorf("sso.scopes must not list %q: every native-oidc provider already carries it", scope)
		}
	}
	return nil
}

// validateIntegrations checks the consumer-side declarations of a contract: what
// an app says it reads out of the payload has to be something the contract
// carries. A name the contract does not define would leave the app with an empty
// field it reads as "the provider has not published yet", which is the wrong
// diagnosis for a typo in its own metadata.
func validateIntegrations(app *App) error {
	for name, integration := range app.Integrations {
		for _, compatible := range integration.Compatible {
			if err := validateCompatibleProvider(name, compatible); err != nil {
				return err
			}
		}
		if err := validateRequiredDefault(name, integration); err != nil {
			return err
		}
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

// validateRequiredDefault enforces that a required integration names exactly
// one default provider.
//
// The default is what the orchestrator records as the provider of a required
// contract (buildIntegrationConfig reads choice.Recommended), and for a required
// integration the recorded choice is the *only* source of the graph edge:
// computeAppDeps skips the compatible scan entirely in that case. So an
// integration marked required but carrying no default records nothing, produces
// no edge, and the app installs with no dependency at all. It then resolves an
// empty credential on every pass and fails forever without ever producing a
// plan-time error, which is the worst shape a metadata mistake can have: the
// symptom is nowhere near the cause.
//
// Exactly one, not "at least one": two defaults make the recorded choice
// ambiguous, and which one wins would depend on iteration order over the
// compatible list.
func validateRequiredDefault(name string, integration Integration) error {
	if !integration.Required {
		return nil
	}
	defaults := make([]string, 0, 1)
	for _, compatible := range integration.Compatible {
		if compatible.Default {
			defaults = append(defaults, providerLabel(compatible))
		}
	}
	switch len(defaults) {
	case 1:
		return nil
	case 0:
		return fmt.Errorf("integrations.%s is required but declares no `default: true` compatible entry; it would install with no dependency and resolve an empty credential forever", name)
	default:
		return fmt.Errorf("integrations.%s is required but declares %d `default: true` entries (%s); exactly one is allowed",
			name, len(defaults), strings.Join(defaults, ", "))
	}
}

// providerLabel renders a compatible entry for an error message, covering both
// the catalog-app and instance-source forms.
func providerLabel(compatible CompatibleApp) string {
	if compatible.Source != "" {
		return "source:" + compatible.Source
	}
	return compatible.App
}

// validateCompatibleProvider checks the provider discriminator on one
// `compatible:` entry. Exactly one of `app` and `source` must name the provider:
// both is ambiguous about which one wins, and neither resolves to nothing at all,
// which the consumer would read as "no provider available" rather than as a
// metadata typo.
func validateCompatibleProvider(contract string, compatible CompatibleApp) error {
	hasApp := strings.TrimSpace(compatible.App) != ""
	hasSource := strings.TrimSpace(compatible.Source) != ""
	switch {
	case hasApp && hasSource:
		return fmt.Errorf("integrations.%s compatible entry names both app %q and source %q; exactly one must be set",
			contract, compatible.App, compatible.Source)
	case !hasApp && !hasSource:
		return fmt.Errorf("integrations.%s compatible entry names neither app nor source", contract)
	case hasSource && compatible.Source != InstanceProviderSource:
		return fmt.Errorf("integrations.%s compatible entry has source %q; the only source is %q (use app: to name a catalog app)",
			contract, compatible.Source, InstanceProviderSource)
	}
	return nil
}

// validateProvides checks the provider-side declarations against the contract
// registry. Everything here is a cross-file agreement that no compiler sees: a
// provider's `provides.pvr.secrets` has to carry the key the PVR contract names,
// an inference endpoint's path is concatenated onto an address, and a contract
// name that no consumer can use is a typo. Each of those fails the load instead
// of reaching a consumer as a binding that is silently half-empty.
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
// ones its contract requires, that a value meant to be concatenated onto an
// address is an absolute path on an app that publishes a port, and that the
// static and runtime halves of the offer do not both claim the same key.
//
// A required value may arrive either way, but it must have exactly one declared
// source. Accepting a silently missing value would let a provider hand every
// consumer an empty path that reads as "the contract had none" rather than as a
// provider that never published; accepting a key declared both ways would leave
// it ambiguous which one a later edit is supposed to change.
func validateContractValues(name string, contract Contract, offer ContractProvides, port int) error {
	for _, runtimeKey := range offer.RuntimeValues {
		if runtimeKey == "" || strings.ContainsAny(runtimeKey, " \t\n") {
			return fmt.Errorf("provides.%s.runtimeValues entries must be single non-empty names (got %q)", name, runtimeKey)
		}
		if !declaresValue(contract, runtimeKey) {
			return fmt.Errorf("provides.%s.runtimeValues names %q, which this contract does not carry (it carries %s)",
				name, runtimeKey, valueKeys(contract))
		}
		if _, static := offer.Values[runtimeKey]; static {
			return fmt.Errorf("provides.%s declares %q both in values and in runtimeValues; a value needs exactly one source",
				name, runtimeKey)
		}
	}
	for _, want := range contract.Values {
		if slices.Contains(offer.RuntimeValues, want.Key) {
			continue // supplied at runtime, so metadata cannot be the authority on it
		}
		value, ok := offer.Values[want.Key]
		if !ok || value == "" {
			if want.Optional {
				continue // an optional value a provider has no fact for is omitted
			}
			return fmt.Errorf("provides.%s.values must declare %q, which this contract carries (or list it under runtimeValues if the app mints it at runtime)", name, want.Key)
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

// declaresValue reports whether the contract carries the named value.
func declaresValue(contract Contract, key string) bool {
	for _, spec := range contract.Values {
		if spec.Key == key {
			return true
		}
	}
	return false
}

// valueKeys lists a contract's value names for an error message.
func valueKeys(contract Contract) string {
	names := make([]string, 0, len(contract.Values))
	for _, spec := range contract.Values {
		names = append(names, spec.Key)
	}
	return strings.Join(names, ", ")
}

// LoadGraph loads every catalog app (the same model LoadAll produces) and
// builds the dependency graph the install/remove planners read. It is a thin
// wrapper over LoadAll rather than a second walker: one loader, one model, so
// the planner and the reconciler can no longer disagree by construction.
func (l *Loader) LoadGraph() (*AppGraph, error) {
	apps, err := l.LoadAll()
	if err != nil {
		return nil, err
	}
	list := make([]*App, 0, len(apps))
	for _, app := range apps {
		list = append(list, app)
	}
	return NewGraph(list), nil
}
