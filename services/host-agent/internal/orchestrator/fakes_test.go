// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"context"
	"sync"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sso"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
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

func (f *FakeTraefikGenerator) GenerateAll(apps []*catalog.App, remoteApps []traefikgen.RemoteAppRoute, tailnetDomain string) error {
	return f.Generate(apps)
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

func (f *FakeAuthentikClient) EnsureLDAPInfrastructure(ldapBindPassword string) error {
	return nil
}

func (f *FakeAuthentikClient) GetLDAPOutpostToken() (string, error) {
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
	f.apps[app.CatalogID] = app
}

// ============================================================================
// FakeAppStore - In-memory app store for testing
// ============================================================================

type FakeAppStore struct {
	mu       sync.RWMutex
	apps     map[string]*store.InstalledApp
	onChange func()
}

func NewFakeAppStore() *FakeAppStore {
	return &FakeAppStore{
		apps: make(map[string]*store.InstalledApp),
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

func (f *FakeAppStore) UpdateStatus(name, status string) error {
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

func (f *FakeAppStore) SetTailnetID(name, tailnetID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[name]; ok {
		app.TailnetID = tailnetID
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

// ============================================================================
// FakeTailnetStore — in-memory implementation of store.TailnetStoreInterface
// ============================================================================

type FakeTailnetStore struct {
	mu    sync.RWMutex
	conns map[string]*store.TailnetConnection
}

func NewFakeTailnetStore() *FakeTailnetStore {
	return &FakeTailnetStore{
		conns: make(map[string]*store.TailnetConnection),
	}
}

func (f *FakeTailnetStore) Create(conn store.TailnetConnection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns[conn.ID] = &conn
	return nil
}

func (f *FakeTailnetStore) GetByID(id string) (*store.TailnetConnection, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.conns[id], nil
}

func (f *FakeTailnetStore) GetActive() (*store.TailnetConnection, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, c := range f.conns {
		if c.Status == "active" {
			return c, nil
		}
	}
	return nil, nil
}

func (f *FakeTailnetStore) List() ([]*store.TailnetConnection, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var result []*store.TailnetConnection
	for _, c := range f.conns {
		result = append(result, c)
	}
	return result, nil
}

func (f *FakeTailnetStore) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.conns, id)
	return nil
}

// ActiveConnection returns the active connection for test assertions.
func (f *FakeTailnetStore) ActiveConnection() *store.TailnetConnection {
	c, _ := f.GetActive()
	return c
}

// Compile-time assertion.
var _ store.TailnetStoreInterface = (*FakeTailnetStore)(nil)

// ============================================================================
// FakeRemoteAppStore — in-memory implementation of store.RemoteAppStoreInterface
// ============================================================================

type FakeRemoteAppStore struct {
	mu   sync.RWMutex
	apps map[string]*store.RemoteApp
}

func NewFakeRemoteAppStore() *FakeRemoteAppStore {
	return &FakeRemoteAppStore{
		apps: make(map[string]*store.RemoteApp),
	}
}

func (f *FakeRemoteAppStore) Create(app store.RemoteApp) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[app.ID] = &app
	return nil
}

func (f *FakeRemoteAppStore) GetByID(id string) (*store.RemoteApp, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if app, ok := f.apps[id]; ok {
		return app, nil
	}
	return nil, nil
}

func (f *FakeRemoteAppStore) List() ([]*store.RemoteApp, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var apps []*store.RemoteApp
	for _, app := range f.apps {
		apps = append(apps, app)
	}
	return apps, nil
}

func (f *FakeRemoteAppStore) SetCredential(id string, encryptedCred []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[id]; ok {
		app.EncryptedCred = encryptedCred
		app.Status = "active"
		return nil
	}
	return nil
}

func (f *FakeRemoteAppStore) SetStatus(id, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[id]; ok {
		app.Status = status
		return nil
	}
	return nil
}

func (f *FakeRemoteAppStore) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.apps, id)
	return nil
}

// Apps returns all remote apps for test assertions.
func (f *FakeRemoteAppStore) Apps() []*store.RemoteApp {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var apps []*store.RemoteApp
	for _, app := range f.apps {
		apps = append(apps, app)
	}
	return apps
}

// Compile-time assertion.
var _ store.RemoteAppStoreInterface = (*FakeRemoteAppStore)(nil)

// ============================================================================
// FakeTailnetNodeManager — records EnsureRunning/StopAndPurge calls
// ============================================================================

type FakeTailnetNodeManager struct {
	mu          sync.Mutex
	ensuredApps []string
	purgedApps  []string
}

func NewFakeTailnetNodeManager() *FakeTailnetNodeManager {
	return &FakeTailnetNodeManager{}
}

func (f *FakeTailnetNodeManager) EnsureRunning(ctx context.Context, appName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensuredApps = append(f.ensuredApps, appName)
	return nil
}

func (f *FakeTailnetNodeManager) StopAndPurge(ctx context.Context, appName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.purgedApps = append(f.purgedApps, appName)
	return nil
}

func (f *FakeTailnetNodeManager) EnsuredApps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.ensuredApps))
	copy(out, f.ensuredApps)
	return out
}

func (f *FakeTailnetNodeManager) PurgedApps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.purgedApps))
	copy(out, f.purgedApps)
	return out
}

// Compile-time assertion.
var _ TailnetNodeEnsurer = (*FakeTailnetNodeManager)(nil)

// ============================================================================
// FakeGatewayManager — records StopAndPurge/EnsureRunning/GetTailnetDomain calls
// ============================================================================

type FakeGatewayManager struct {
	mu           sync.Mutex
	purgeCalled  bool
	ensureCalled bool
	domain       string
	domainErr    error
	domainCalled bool
}

func NewFakeGatewayManager() *FakeGatewayManager {
	return &FakeGatewayManager{}
}

func (f *FakeGatewayManager) EnsureRunning(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalled = true
	return nil
}

func (f *FakeGatewayManager) StopAndPurge(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.purgeCalled = true
	return nil
}

func (f *FakeGatewayManager) GetTailnetDomain(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.domainCalled = true
	return f.domain, f.domainErr
}

// SetDomain configures the domain and error returned by GetTailnetDomain.
func (f *FakeGatewayManager) SetDomain(domain string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.domain = domain
	f.domainErr = err
}

func (f *FakeGatewayManager) WasPurgeCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.purgeCalled
}

func (f *FakeGatewayManager) WasDomainCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.domainCalled
}

// Compile-time assertion.
var _ GatewayManager = (*FakeGatewayManager)(nil)

// ============================================================================
// FakeRemoteProxy — records StopAll and Reconcile calls
// ============================================================================

type FakeRemoteProxy struct {
	mu         sync.Mutex
	stopCalled bool
	portMap    map[string]int
}

func NewFakeRemoteProxy() *FakeRemoteProxy {
	return &FakeRemoteProxy{portMap: make(map[string]int)}
}

func (f *FakeRemoteProxy) StopAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalled = true
}

func (f *FakeRemoteProxy) Reconcile(targets []sharing.ProxyTarget) map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.portMap
}

func (f *FakeRemoteProxy) WasStopCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopCalled
}

// Compile-time assertion.
var _ RemoteProxyManager = (*FakeRemoteProxy)(nil)

// ============================================================================
// FakeForwardDomainProvisioner — records EnsureForwardDomainAuth calls
// ============================================================================

type FakeForwardDomainProvisioner struct {
	mu           sync.Mutex
	calledDomain string
	token        string
	err          error
}

func NewFakeForwardDomainProvisioner(token string, err error) *FakeForwardDomainProvisioner {
	return &FakeForwardDomainProvisioner{token: token, err: err}
}

func (f *FakeForwardDomainProvisioner) EnsureForwardDomainAuth(cookieDomain string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calledDomain = cookieDomain
	return f.token, f.err
}

func (f *FakeForwardDomainProvisioner) CalledDomain() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calledDomain
}

// Compile-time assertion.
var _ ForwardDomainProvisioner = (*FakeForwardDomainProvisioner)(nil)

// ============================================================================
// FakeProxyOutpost — records EnsureRunning/Stop calls
// ============================================================================

type FakeProxyOutpost struct {
	mu          sync.Mutex
	running     bool
	token       string
	domain      string
	stopCalled  bool
	ensureError error
}

func NewFakeProxyOutpost() *FakeProxyOutpost {
	return &FakeProxyOutpost{}
}

func (f *FakeProxyOutpost) EnsureRunning(ctx context.Context, token, tailnetDomain string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ensureError != nil {
		return f.ensureError
	}
	f.running = true
	f.token = token
	f.domain = tailnetDomain
	return nil
}

func (f *FakeProxyOutpost) Stop(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = false
	f.stopCalled = true
	return nil
}

func (f *FakeProxyOutpost) IsRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *FakeProxyOutpost) WasStopCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopCalled
}

func (f *FakeProxyOutpost) RecordedToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token
}

func (f *FakeProxyOutpost) RecordedDomain() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.domain
}

// Compile-time assertion.
var _ ProxyOutpostEnsurer = (*FakeProxyOutpost)(nil)
