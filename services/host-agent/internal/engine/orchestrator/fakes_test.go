// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"fmt"
	"sync"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sso"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// Fakes are test doubles that capture calls for later inspection.
// Unlike mocks, they don't assert expectations - they just record what happened.
// This allows tests to verify behavior by inspecting the captured data.

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

func (f *FakeBlueprintGenerator) GenerateOutpostBlueprint(providers []sso.ForwardAuthProvider) error {
	return nil
}

func (f *FakeBlueprintGenerator) GenerateLDAPOutpostBlueprint(apps []sso.LDAPApp, ldapBindPassword string) error {
	return nil
}

func (f *FakeBlueprintGenerator) GetLDAPBindPassword() string {
	return "test-ldap-bind-password"
}

func (f *FakeBlueprintGenerator) GetLDAPOutpostToken(ctx context.Context, authentikURL, apiToken string) (string, error) {
	return "test-ldap-outpost-token", nil
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

func (f *FakeAuthentikClient) DeleteAppSSO(ctx context.Context, appName, displayName, ssoStrategy string) error {
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

func (f *FakeAuthentikClient) AddProviderToEmbeddedOutpost(ctx context.Context, providerName string) error {
	return nil
}

func (f *FakeAuthentikClient) IsAvailable(ctx context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available
}

func (f *FakeAuthentikClient) DeleteApplication(ctx context.Context, slug string) error {
	return nil
}

func (f *FakeAuthentikClient) DeleteOAuth2Provider(ctx context.Context, providerName string) error {
	return nil
}

func (f *FakeAuthentikClient) DeleteProxyProvider(ctx context.Context, providerName string) error {
	return nil
}

func (f *FakeAuthentikClient) EnsureLDAPInfrastructure(ctx context.Context, ldapBindPassword string) error {
	return nil
}

func (f *FakeAuthentikClient) GetLDAPOutpostToken(ctx context.Context) (string, error) {
	return "fake-ldap-outpost-token", nil
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
	apps          map[string]*catalog.App
}

func NewFakeAppGraph() *FakeAppGraph {
	return &FakeAppGraph{
		installPlans: make(map[string]*catalog.InstallPlan),
		removePlans:  make(map[string]*catalog.RemovePlan),
		apps:         make(map[string]*catalog.App),
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

func (f *FakeAppGraph) GetApps() map[string]*catalog.App {
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
	f.apps[app.CatalogID] = app
}

// ============================================================================
// FakeAppStore - In-memory app store for testing
// ============================================================================

type FakeAppStore struct {
	mu          sync.RWMutex
	apps        map[string]*store.InstalledApp
	ssoStrategy map[string]string
	onChange    func()
}

func NewFakeAppStore() *FakeAppStore {
	return &FakeAppStore{
		apps:        make(map[string]*store.InstalledApp),
		ssoStrategy: make(map[string]string),
	}
}

func (f *FakeAppStore) GetAll() ([]*store.InstalledApp, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var apps []*store.InstalledApp
	for _, app := range f.apps {
		apps = append(apps, app)
	}
	return apps, nil
}

func (f *FakeAppStore) GetByCatalogID(catalogID string) (*store.InstalledApp, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.apps[catalogID], nil
}

func (f *FakeAppStore) GetInstalledCatalogIDs() ([]string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var ids []string
	for id := range f.apps {
		ids = append(ids, id)
	}
	return ids, nil
}

func (f *FakeAppStore) Install(catalogID, displayName, version string, integrationConfig map[string]string, opts *store.InstallOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	app := &store.InstalledApp{
		CatalogID:         catalogID,
		DisplayName:       displayName,
		Version:           version,
		Status:            "installing",
		IntegrationConfig: integrationConfig,
	}
	if opts != nil {
		app.Port = opts.Port
		app.IsSystem = opts.IsSystem
	}
	f.apps[catalogID] = app
	f.notify()
	return nil
}

func (f *FakeAppStore) UpdateStatus(name string, status store.AppStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[name]; ok {
		app.Status = status
		f.notify()
	}
	return nil
}

func (f *FakeAppStore) EnsureSystemApp(catalogID, displayName string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[catalogID] = &store.InstalledApp{
		CatalogID:   catalogID,
		DisplayName: displayName,
		Port:        port,
		Status:      "running",
		IsSystem:    true,
	}
	f.notify()
	return nil
}

func (f *FakeAppStore) UpdateIntegrationConfig(name string, config map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[name]; ok {
		app.IntegrationConfig = config
	}
	return nil
}

func (f *FakeAppStore) UpdateDisplayName(name, displayName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[name]; ok {
		app.DisplayName = displayName
		f.notify()
	}
	return nil
}

func (f *FakeAppStore) GetSSOStrategy(name string) (string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.ssoStrategy[name], nil
}

func (f *FakeAppStore) SetSSOStrategy(name, strategy string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ssoStrategy[name] = strategy
	return nil
}

func (f *FakeAppStore) Uninstall(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.apps, name)
	f.notify()
	return nil
}

func (f *FakeAppStore) IsInstalled(name string) (bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	_, ok := f.apps[name]
	return ok, nil
}

func (f *FakeAppStore) SetOnChange(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onChange = fn
}

func (f *FakeAppStore) SetLastError(name, lastError string) error {
	f.mu.Lock()
	app, ok := f.apps[name]
	if !ok {
		f.mu.Unlock()
		return fmt.Errorf("app not found: %s", name)
	}
	app.LastError = lastError
	f.mu.Unlock()
	f.notify()
	return nil
}

func (f *FakeAppStore) notify() {
	if f.onChange != nil {
		f.onChange()
	}
}

// AddApp is a test helper to add an installed app directly
func (f *FakeAppStore) AddApp(app *store.InstalledApp) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[app.CatalogID] = app
}

// Compile-time assertions.
var _ store.AppStoreInterface = (*FakeAppStore)(nil)
var _ catalog.AppGraphInterface = (*FakeAppGraph)(nil)
var _ catalog.CacheInterface = (*FakeCatalogCache)(nil)
