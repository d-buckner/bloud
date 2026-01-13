package orchestrator

import (
	"context"
	"sync"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/nixgen"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/sso"
)

// Fakes are test doubles that capture calls for later inspection.
// Unlike mocks, they don't assert expectations - they just record what happened.
// This allows tests to verify behavior by inspecting the captured data.

// ============================================================================
// FakeGenerator - Captures Nix transactions
// ============================================================================

// FakeGenerator captures Apply calls for inspection
type FakeGenerator struct {
	mu                  sync.Mutex
	currentState        *nixgen.Transaction
	appliedTransactions []*nixgen.Transaction
	applyError          error
}

func NewFakeGenerator() *FakeGenerator {
	return &FakeGenerator{
		currentState: &nixgen.Transaction{
			Apps: make(map[string]nixgen.AppConfig),
		},
	}
}

func (f *FakeGenerator) LoadCurrent() (*nixgen.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Return a copy to prevent mutation
	copy := &nixgen.Transaction{
		Apps: make(map[string]nixgen.AppConfig),
	}
	for k, v := range f.currentState.Apps {
		copy.Apps[k] = v
	}
	return copy, nil
}

func (f *FakeGenerator) Apply(tx *nixgen.Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.applyError != nil {
		return f.applyError
	}

	// Store a copy of the transaction
	copy := &nixgen.Transaction{
		Apps: make(map[string]nixgen.AppConfig),
	}
	for k, v := range tx.Apps {
		copy.Apps[k] = v
	}
	f.appliedTransactions = append(f.appliedTransactions, copy)

	// Update current state to match
	f.currentState = copy
	return nil
}

func (f *FakeGenerator) Preview(tx *nixgen.Transaction) string {
	return "preview"
}

func (f *FakeGenerator) Diff(current, proposed *nixgen.Transaction) string {
	return "diff"
}

// Test helpers

func (f *FakeGenerator) SetCurrentState(tx *nixgen.Transaction) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.currentState = tx
}

func (f *FakeGenerator) SetApplyError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyError = err
}

func (f *FakeGenerator) LastTransaction() *nixgen.Transaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.appliedTransactions) == 0 {
		return nil
	}
	return f.appliedTransactions[len(f.appliedTransactions)-1]
}

func (f *FakeGenerator) TransactionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.appliedTransactions)
}

func (f *FakeGenerator) AllTransactions() []*nixgen.Transaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appliedTransactions
}

// ============================================================================
// FakeRebuilder - Always succeeds or fails as configured
// ============================================================================

type FakeRebuilder struct {
	mu           sync.Mutex
	switchResult *nixgen.RebuildResult
	switchError  error
	switchCount  int
}

func NewFakeRebuilder() *FakeRebuilder {
	return &FakeRebuilder{
		switchResult: &nixgen.RebuildResult{
			Success: true,
			Output:  "fake rebuild output",
		},
	}
}

func (f *FakeRebuilder) Switch(ctx context.Context) (*nixgen.RebuildResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.switchCount++
	if f.switchError != nil {
		return nil, f.switchError
	}
	return f.switchResult, nil
}

func (f *FakeRebuilder) Rollback(ctx context.Context) (*nixgen.RebuildResult, error) {
	return &nixgen.RebuildResult{Success: true}, nil
}

func (f *FakeRebuilder) SwitchStream(ctx context.Context, events chan<- nixgen.RebuildEvent) {
	close(events)
}

func (f *FakeRebuilder) StopUserService(ctx context.Context, appName string) error {
	return nil
}

func (f *FakeRebuilder) ReloadAndRestartApps(ctx context.Context) error {
	return nil
}

// Test helpers

func (f *FakeRebuilder) SetResult(result *nixgen.RebuildResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.switchResult = result
}

func (f *FakeRebuilder) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.switchError = err
}

func (f *FakeRebuilder) SwitchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.switchCount
}

// ============================================================================
// FakeTraefikGenerator - Captures route generation
// ============================================================================

type FakeTraefikGenerator struct {
	mu               sync.Mutex
	generatedApps    [][]*catalog.App
	authentikEnabled bool
	generateError    error
}

func NewFakeTraefikGenerator() *FakeTraefikGenerator {
	return &FakeTraefikGenerator{}
}

func (f *FakeTraefikGenerator) Generate(apps []*catalog.App) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.generateError != nil {
		return f.generateError
	}

	// Store a copy
	copy := make([]*catalog.App, len(apps))
	for i, a := range apps {
		copy[i] = a
	}
	f.generatedApps = append(f.generatedApps, copy)
	return nil
}

func (f *FakeTraefikGenerator) SetAuthentikEnabled(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authentikEnabled = enabled
}

func (f *FakeTraefikGenerator) Preview(apps []*catalog.App) string {
	return "traefik preview"
}

// Test helpers

func (f *FakeTraefikGenerator) LastGeneratedApps() []*catalog.App {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.generatedApps) == 0 {
		return nil
	}
	return f.generatedApps[len(f.generatedApps)-1]
}

func (f *FakeTraefikGenerator) WasAuthentikEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authentikEnabled
}

// ============================================================================
// FakeBlueprintGenerator - Captures SSO blueprint operations
// ============================================================================

type FakeBlueprintGenerator struct {
	mu               sync.Mutex
	generatedApps    []*catalog.App
	deletedBlueprint []string
	generateError    error
	deleteError      error
}

func NewFakeBlueprintGenerator() *FakeBlueprintGenerator {
	return &FakeBlueprintGenerator{}
}

func (f *FakeBlueprintGenerator) GenerateForApp(app *catalog.App) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.generateError != nil {
		return f.generateError
	}
	f.generatedApps = append(f.generatedApps, app)
	return nil
}

func (f *FakeBlueprintGenerator) DeleteBlueprint(appName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.deleteError != nil {
		return f.deleteError
	}
	f.deletedBlueprint = append(f.deletedBlueprint, appName)
	return nil
}

func (f *FakeBlueprintGenerator) GetSSOEnvVars(app *catalog.App) map[string]string {
	return nil
}

func (f *FakeBlueprintGenerator) GenerateOutpostBlueprint(providers []sso.ForwardAuthProvider) error {
	return nil
}

// Test helpers

func (f *FakeBlueprintGenerator) GeneratedApps() []*catalog.App {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.generatedApps
}

func (f *FakeBlueprintGenerator) DeletedBlueprints() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletedBlueprint
}

func (f *FakeBlueprintGenerator) SetGenerateError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generateError = err
}

// ============================================================================
// FakeAuthentikClient - Captures SSO cleanup calls
// ============================================================================

type FakeAuthentikClient struct {
	mu          sync.Mutex
	deletedApps []struct {
		appName     string
		displayName string
		strategy    string
	}
	deleteError error
	available   bool
}

func NewFakeAuthentikClient() *FakeAuthentikClient {
	return &FakeAuthentikClient{
		available: true,
	}
}

func (f *FakeAuthentikClient) DeleteAppSSO(appName, displayName, ssoStrategy string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.deleteError != nil {
		return f.deleteError
	}
	f.deletedApps = append(f.deletedApps, struct {
		appName     string
		displayName string
		strategy    string
	}{appName, displayName, ssoStrategy})
	return nil
}

func (f *FakeAuthentikClient) AddProviderToEmbeddedOutpost(providerName string) error {
	return nil
}

func (f *FakeAuthentikClient) IsAvailable() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available
}

func (f *FakeAuthentikClient) DeleteApplication(slug string) error {
	return nil
}

func (f *FakeAuthentikClient) DeleteOAuth2Provider(providerName string) error {
	return nil
}

func (f *FakeAuthentikClient) DeleteProxyProvider(providerName string) error {
	return nil
}

// Test helpers

func (f *FakeAuthentikClient) DeletedApps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for _, d := range f.deletedApps {
		names = append(names, d.appName)
	}
	return names
}

func (f *FakeAuthentikClient) SetDeleteError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteError = err
}

// ============================================================================
// FakeAppGraph - Provides controlled install/remove plans
// ============================================================================

type FakeAppGraph struct {
	mu            sync.Mutex
	installPlans  map[string]*catalog.InstallPlan
	removePlans   map[string]*catalog.RemovePlan
	installedApps []string
	apps          map[string]*catalog.AppDefinition
}

func NewFakeAppGraph() *FakeAppGraph {
	return &FakeAppGraph{
		installPlans: make(map[string]*catalog.InstallPlan),
		removePlans:  make(map[string]*catalog.RemovePlan),
		apps:         make(map[string]*catalog.AppDefinition),
	}
}

func (f *FakeAppGraph) PlanInstall(appName string) (*catalog.InstallPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if plan, ok := f.installPlans[appName]; ok {
		return plan, nil
	}
	// Default: can install
	return &catalog.InstallPlan{
		App:        appName,
		CanInstall: true,
	}, nil
}

func (f *FakeAppGraph) PlanRemove(appName string) (*catalog.RemovePlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if plan, ok := f.removePlans[appName]; ok {
		return plan, nil
	}
	// Default: can remove
	return &catalog.RemovePlan{
		App:       appName,
		CanRemove: true,
	}, nil
}

func (f *FakeAppGraph) SetInstalled(installed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installedApps = installed
}

func (f *FakeAppGraph) IsInstalled(appName string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.installedApps {
		if a == appName {
			return true
		}
	}
	return false
}

func (f *FakeAppGraph) FindDependents(appName string) []catalog.ConfigTask {
	return nil
}

func (f *FakeAppGraph) GetCompatibleApps(appName string, integrationName string) ([]catalog.CompatibleApp, []catalog.CompatibleApp) {
	return nil, nil
}

func (f *FakeAppGraph) GetApps() map[string]*catalog.AppDefinition {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apps
}

// Test helpers

func (f *FakeAppGraph) SetInstallPlan(appName string, plan *catalog.InstallPlan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installPlans[appName] = plan
}

func (f *FakeAppGraph) SetRemovePlan(appName string, plan *catalog.RemovePlan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removePlans[appName] = plan
}

func (f *FakeAppGraph) InstalledApps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installedApps
}

// ============================================================================
// FakeCatalogCache - Provides app metadata
// ============================================================================

type FakeCatalogCache struct {
	mu   sync.Mutex
	apps map[string]*catalog.App
}

func NewFakeCatalogCache() *FakeCatalogCache {
	return &FakeCatalogCache{
		apps: make(map[string]*catalog.App),
	}
}

func (f *FakeCatalogCache) Get(name string) (*catalog.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[name]; ok {
		return app, nil
	}
	return nil, nil
}

func (f *FakeCatalogCache) GetAll() ([]*catalog.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var apps []*catalog.App
	for _, a := range f.apps {
		apps = append(apps, a)
	}
	return apps, nil
}

func (f *FakeCatalogCache) GetUserApps() ([]*catalog.App, error) {
	return f.GetAll()
}

func (f *FakeCatalogCache) IsSystemAppByName(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[name]; ok {
		return app.IsSystem
	}
	return false
}

func (f *FakeCatalogCache) Refresh(loader *catalog.Loader) error {
	return nil
}

// Test helpers

func (f *FakeCatalogCache) AddApp(app *catalog.App) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[app.Name] = app
}
