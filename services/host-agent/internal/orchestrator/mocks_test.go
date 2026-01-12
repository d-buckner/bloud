package orchestrator

import (
	"context"

	"github.com/stretchr/testify/mock"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/nixgen"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// MockAppStore implements store.AppStoreInterface for testing
type MockAppStore struct {
	mock.Mock
}

func (m *MockAppStore) GetAll() ([]*store.InstalledApp, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*store.InstalledApp), args.Error(1)
}

func (m *MockAppStore) GetByName(name string) (*store.InstalledApp, error) {
	args := m.Called(name)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*store.InstalledApp), args.Error(1)
}

func (m *MockAppStore) GetInstalledNames() ([]string, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]string), args.Error(1)
}

func (m *MockAppStore) Install(name, displayName, version string, integrationConfig map[string]string, opts *store.InstallOptions) error {
	args := m.Called(name, displayName, version, integrationConfig, opts)
	return args.Error(0)
}

func (m *MockAppStore) UpdateStatus(name, status string) error {
	args := m.Called(name, status)
	return args.Error(0)
}

func (m *MockAppStore) UpdateIntegrationConfig(name string, config map[string]string) error {
	args := m.Called(name, config)
	return args.Error(0)
}

func (m *MockAppStore) Uninstall(name string) error {
	args := m.Called(name)
	return args.Error(0)
}

func (m *MockAppStore) IsInstalled(name string) (bool, error) {
	args := m.Called(name)
	return args.Bool(0), args.Error(1)
}

func (m *MockAppStore) SetOnChange(fn func()) {
	m.Called(fn)
}

// MockCatalogCache implements catalog.CacheInterface for testing
type MockCatalogCache struct {
	mock.Mock
}

func (m *MockCatalogCache) Get(name string) (*catalog.App, error) {
	args := m.Called(name)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*catalog.App), args.Error(1)
}

func (m *MockCatalogCache) GetAll() ([]*catalog.App, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*catalog.App), args.Error(1)
}

func (m *MockCatalogCache) GetUserApps() ([]*catalog.App, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*catalog.App), args.Error(1)
}

func (m *MockCatalogCache) IsSystemAppByName(name string) bool {
	args := m.Called(name)
	return args.Bool(0)
}

func (m *MockCatalogCache) Refresh(loader *catalog.Loader) error {
	args := m.Called(loader)
	return args.Error(0)
}

// MockAppGraph implements catalog.AppGraphInterface for testing
type MockAppGraph struct {
	mock.Mock
}

func (m *MockAppGraph) PlanInstall(appName string) (*catalog.InstallPlan, error) {
	args := m.Called(appName)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*catalog.InstallPlan), args.Error(1)
}

func (m *MockAppGraph) PlanRemove(appName string) (*catalog.RemovePlan, error) {
	args := m.Called(appName)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*catalog.RemovePlan), args.Error(1)
}

func (m *MockAppGraph) SetInstalled(installed []string) {
	m.Called(installed)
}

func (m *MockAppGraph) IsInstalled(appName string) bool {
	args := m.Called(appName)
	return args.Bool(0)
}

func (m *MockAppGraph) FindDependents(appName string) []catalog.ConfigTask {
	args := m.Called(appName)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).([]catalog.ConfigTask)
}

func (m *MockAppGraph) GetCompatibleApps(appName string, integrationName string) (installed []catalog.CompatibleApp, available []catalog.CompatibleApp) {
	args := m.Called(appName, integrationName)
	if args.Get(0) != nil {
		installed = args.Get(0).([]catalog.CompatibleApp)
	}
	if args.Get(1) != nil {
		available = args.Get(1).([]catalog.CompatibleApp)
	}
	return installed, available
}

func (m *MockAppGraph) GetApps() map[string]*catalog.AppDefinition {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(map[string]*catalog.AppDefinition)
}

// MockNixGenerator implements nixgen.GeneratorInterface for testing
type MockNixGenerator struct {
	mock.Mock
}

func (m *MockNixGenerator) LoadCurrent() (*nixgen.Transaction, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*nixgen.Transaction), args.Error(1)
}

func (m *MockNixGenerator) Apply(tx *nixgen.Transaction) error {
	args := m.Called(tx)
	return args.Error(0)
}

func (m *MockNixGenerator) Preview(tx *nixgen.Transaction) string {
	args := m.Called(tx)
	return args.String(0)
}

func (m *MockNixGenerator) Diff(current, proposed *nixgen.Transaction) string {
	args := m.Called(current, proposed)
	return args.String(0)
}

// MockRebuilder implements nixgen.RebuilderInterface for testing
type MockRebuilder struct {
	mock.Mock
}

func (m *MockRebuilder) Switch(ctx context.Context) (*nixgen.RebuildResult, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*nixgen.RebuildResult), args.Error(1)
}

func (m *MockRebuilder) Rollback(ctx context.Context) (*nixgen.RebuildResult, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*nixgen.RebuildResult), args.Error(1)
}

func (m *MockRebuilder) SwitchStream(ctx context.Context, events chan<- nixgen.RebuildEvent) {
	m.Called(ctx, events)
}

func (m *MockRebuilder) StopUserService(ctx context.Context, appName string) error {
	args := m.Called(ctx, appName)
	return args.Error(0)
}

func (m *MockRebuilder) ReloadAndRestartApps(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// MockTraefikGenerator implements traefikgen.GeneratorInterface for testing
type MockTraefikGenerator struct {
	mock.Mock
}

func (m *MockTraefikGenerator) Generate(apps []*catalog.App) error {
	args := m.Called(apps)
	return args.Error(0)
}

func (m *MockTraefikGenerator) SetAuthentikEnabled(enabled bool) {
	m.Called(enabled)
}

func (m *MockTraefikGenerator) Preview(apps []*catalog.App) string {
	args := m.Called(apps)
	return args.String(0)
}

// MockBlueprintGenerator implements sso.BlueprintGeneratorInterface for testing
type MockBlueprintGenerator struct {
	mock.Mock
}

func (m *MockBlueprintGenerator) GenerateForApp(app *catalog.App) error {
	args := m.Called(app)
	return args.Error(0)
}

func (m *MockBlueprintGenerator) DeleteBlueprint(appName string) error {
	args := m.Called(appName)
	return args.Error(0)
}

func (m *MockBlueprintGenerator) GetSSOEnvVars(app *catalog.App) map[string]string {
	args := m.Called(app)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(map[string]string)
}

// MockAuthentikClient implements authentik.ClientInterface for testing
type MockAuthentikClient struct {
	mock.Mock
}

func (m *MockAuthentikClient) DeleteAppSSO(appName, displayName, ssoStrategy string) error {
	args := m.Called(appName, displayName, ssoStrategy)
	return args.Error(0)
}

func (m *MockAuthentikClient) AddProviderToEmbeddedOutpost(providerName string) error {
	args := m.Called(providerName)
	return args.Error(0)
}

func (m *MockAuthentikClient) IsAvailable() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *MockAuthentikClient) DeleteApplication(slug string) error {
	args := m.Called(slug)
	return args.Error(0)
}

func (m *MockAuthentikClient) DeleteOAuth2Provider(providerName string) error {
	args := m.Called(providerName)
	return args.Error(0)
}

func (m *MockAuthentikClient) DeleteProxyProvider(providerName string) error {
	args := m.Called(providerName)
	return args.Error(0)
}

// MockConfiguratorRegistry implements configurator.RegistryInterface for testing
type MockConfiguratorRegistry struct {
	mock.Mock
}

func (m *MockConfiguratorRegistry) Get(appName string) configurator.Configurator {
	args := m.Called(appName)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(configurator.Configurator)
}

func (m *MockConfiguratorRegistry) Has(appName string) bool {
	args := m.Called(appName)
	return args.Bool(0)
}

func (m *MockConfiguratorRegistry) All() []configurator.Configurator {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).([]configurator.Configurator)
}

func (m *MockConfiguratorRegistry) Names() []string {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).([]string)
}

func (m *MockConfiguratorRegistry) Register(c configurator.Configurator) {
	m.Called(c)
}

// MockConfigurator implements configurator.Configurator for testing
type MockConfigurator struct {
	mock.Mock
}

func (m *MockConfigurator) Name() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockConfigurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	args := m.Called(ctx, state)
	return args.Error(0)
}

func (m *MockConfigurator) HealthCheck(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockConfigurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	args := m.Called(ctx, state)
	return args.Error(0)
}
