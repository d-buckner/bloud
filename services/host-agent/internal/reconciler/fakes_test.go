package reconciler

import (
	"context"
	"fmt"
	"sync"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// ============================================================================
// FakeLifecycleManager — records calls for inspection
// ============================================================================

type FakeLifecycleManager struct {
	mu               sync.Mutex
	ensuredApps      []string
	removedApps      []removeCall
	syncCalled       bool
	regenerateCalled bool
	ensureError      map[string]error // applies to EnsureContainer phase
	removeError      map[string]error
	regenerateError  error
}

type removeCall struct {
	AppName   string
	ClearData bool
}

func NewFakeLifecycleManager() *FakeLifecycleManager {
	return &FakeLifecycleManager{
		ensureError: make(map[string]error),
		removeError: make(map[string]error),
	}
}

func (f *FakeLifecycleManager) PreStartApp(ctx context.Context, appName string) error {
	return nil
}

func (f *FakeLifecycleManager) EnsureContainer(ctx context.Context, appName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.ensureError[appName]; ok {
		return err
	}
	return nil
}

func (f *FakeLifecycleManager) HealthCheckApp(ctx context.Context, appName string) error {
	return nil
}

func (f *FakeLifecycleManager) PostStartApp(ctx context.Context, appName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Record on the last fatal phase so EnsuredApps() captures all apps
	// that completed the full lifecycle.
	f.ensuredApps = append(f.ensuredApps, appName)
	return nil
}

func (f *FakeLifecycleManager) ProvisionSSO(ctx context.Context, appName string) error {
	return nil
}

func (f *FakeLifecycleManager) RemoveApp(ctx context.Context, appName string, clearData bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.removeError[appName]; ok {
		return err
	}
	f.removedApps = append(f.removedApps, removeCall{AppName: appName, ClearData: clearData})
	return nil
}

func (f *FakeLifecycleManager) SyncContainerState(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncCalled = true
}

func (f *FakeLifecycleManager) RegenerateRoutes() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.regenerateCalled = true
	return f.regenerateError
}

// Test helpers

func (f *FakeLifecycleManager) EnsuredApps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.ensuredApps))
	copy(out, f.ensuredApps)
	return out
}

func (f *FakeLifecycleManager) RemovedApps() []removeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]removeCall, len(f.removedApps))
	copy(out, f.removedApps)
	return out
}

func (f *FakeLifecycleManager) WasSyncCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syncCalled
}

func (f *FakeLifecycleManager) WasRegenerateCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.regenerateCalled
}

// Compile-time assertion.
var _ AppLifecycleManager = (*FakeLifecycleManager)(nil)

// ============================================================================
// FakeAppStore — in-memory implementation of store.AppStoreInterface
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

func (f *FakeAppStore) GetByCatalogID(name string) (*store.InstalledApp, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.apps[name], nil
}

func (f *FakeAppStore) GetInstalledCatalogIDs() ([]string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var names []string
	for name := range f.apps {
		names = append(names, name)
	}
	return names, nil
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

func (f *FakeAppStore) SetTailnetID(name, tailnetID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[name]; ok {
		app.TailnetID = tailnetID
	}
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

// AddApp is a test helper to add an installed app directly.
func (f *FakeAppStore) AddApp(app *store.InstalledApp) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[app.CatalogID] = app
}

// Compile-time assertion.
var _ store.AppStoreInterface = (*FakeAppStore)(nil)

// ============================================================================
// FakeCatalogCache — returns pre-configured *catalog.App by name
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
	return nil, fmt.Errorf("app not found: %s", name)
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

// AddApp is a test helper.
func (f *FakeCatalogCache) AddApp(app *catalog.App) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[app.CatalogID] = app
}

// Compile-time assertion.
var _ catalog.CacheInterface = (*FakeCatalogCache)(nil)

// ============================================================================
// FakeAppGraph — returns pre-configured install/remove plans
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

// Compile-time assertion.
var _ catalog.AppGraphInterface = (*FakeAppGraph)(nil)

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
	return fmt.Errorf("remote app not found: %s", id)
}

func (f *FakeRemoteAppStore) SetStatus(id, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[id]; ok {
		app.Status = status
		return nil
	}
	return fmt.Errorf("remote app not found: %s", id)
}

func (f *FakeRemoteAppStore) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.apps[id]; !ok {
		return fmt.Errorf("remote app not found: %s", id)
	}
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
	ensureError map[string]error
}

func NewFakeTailnetNodeManager() *FakeTailnetNodeManager {
	return &FakeTailnetNodeManager{
		ensureError: make(map[string]error),
	}
}

func (f *FakeTailnetNodeManager) EnsureRunning(ctx context.Context, appName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.ensureError[appName]; ok {
		return err
	}
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
// FakeGatewayManager — records StopAndPurge calls
// ============================================================================

type FakeGatewayManager struct {
	mu          sync.Mutex
	purgeCalled bool
}

func NewFakeGatewayManager() *FakeGatewayManager {
	return &FakeGatewayManager{}
}

func (f *FakeGatewayManager) StopAndPurge(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.purgeCalled = true
	return nil
}

func (f *FakeGatewayManager) WasPurgeCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.purgeCalled
}

// Compile-time assertion.
var _ GatewayEnsurer = (*FakeGatewayManager)(nil)

// ============================================================================
// FakeProxyStopper — records StopAll calls
// ============================================================================

type FakeProxyStopper struct {
	mu         sync.Mutex
	stopCalled bool
}

func NewFakeProxyStopper() *FakeProxyStopper {
	return &FakeProxyStopper{}
}

func (f *FakeProxyStopper) StopAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalled = true
}

func (f *FakeProxyStopper) WasStopCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopCalled
}

// Compile-time assertion.
var _ ProxyStopper = (*FakeProxyStopper)(nil)

// ============================================================================
// FakeTailnetDomainDiscoverer — returns a configured domain or error
// ============================================================================

type FakeTailnetDomainDiscoverer struct {
	mu     sync.Mutex
	domain string
	err    error
	called bool
}

func NewFakeTailnetDomainDiscoverer(domain string, err error) *FakeTailnetDomainDiscoverer {
	return &FakeTailnetDomainDiscoverer{domain: domain, err: err}
}

func (f *FakeTailnetDomainDiscoverer) GetTailnetDomain(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	return f.domain, f.err
}

func (f *FakeTailnetDomainDiscoverer) WasCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.called
}

// Compile-time assertion.
var _ TailnetDomainDiscoverer = (*FakeTailnetDomainDiscoverer)(nil)

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
