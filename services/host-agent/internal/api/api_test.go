// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// FakeAppStore implements store.AppStoreInterface and appStoreHelper for testing.
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
		InstalledAt:       time.Now(),
		UpdatedAt:         time.Now(),
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
		app.UpdatedAt = time.Now()
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
		InstalledAt: time.Now(),
		UpdatedAt:   time.Now(),
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
		app.UpdatedAt = time.Now()
		f.notify()
	}
	return nil
}

func (f *FakeAppStore) UpdateDisplayName(name, displayName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[name]; ok {
		app.DisplayName = displayName
		app.UpdatedAt = time.Now()
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

// AddApp is a test helper to add an installed app
func (f *FakeAppStore) AddApp(app *store.InstalledApp) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[app.CatalogID] = app
}

// getAll satisfies the appStoreHelper interface used by the Server struct.
func (f *FakeAppStore) getAll() ([]*appEntry, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var entries []*appEntry
	for _, app := range f.apps {
		entries = append(entries, &appEntry{
			CatalogID: app.CatalogID,
			Status:    app.Status,
			IsSystem:  app.IsSystem,
		})
	}
	return entries, nil
}

// FakeRemoteAppStore implements store.RemoteAppStoreInterface for testing
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

// FakePreferencesStore implements store.PreferencesStoreInterface for testing
type FakePreferencesStore struct {
	users map[string]bool
}

func NewFakePreferencesStore() *FakePreferencesStore {
	return &FakePreferencesStore{users: make(map[string]bool)}
}

func (f *FakePreferencesStore) HasUsers() (bool, error) {
	return len(f.users) > 0, nil
}

func (f *FakePreferencesStore) EnsureUser(username string) error {
	f.users[username] = true
	return nil
}

func (f *FakePreferencesStore) DeleteUser(username string) error {
	delete(f.users, username)
	return nil
}

// FakePositionStore implements store.PositionStoreInterface for testing
type FakePositionStore struct {
	positions map[string][]store.Position
}

func NewFakePositionStore() *FakePositionStore {
	return &FakePositionStore{positions: make(map[string][]store.Position)}
}

func (f *FakePositionStore) GetForUser(username string) ([]store.Position, error) {
	return f.positions[username], nil
}

func (f *FakePositionStore) SetForUser(username string, positions []store.Position) error {
	f.positions[username] = positions
	return nil
}

// FakeCatalogCache implements catalog.CacheInterface for testing
type FakeCatalogCache struct {
	mu   sync.RWMutex
	apps map[string]*catalog.App
}

func NewFakeCatalogCache() *FakeCatalogCache {
	return &FakeCatalogCache{
		apps: make(map[string]*catalog.App),
	}
}

func (f *FakeCatalogCache) Get(name string) (*catalog.App, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if app, ok := f.apps[name]; ok {
		return app, nil
	}
	return nil, fmt.Errorf("app not found: %s", name)
}

func (f *FakeCatalogCache) GetAll() ([]*catalog.App, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var apps []*catalog.App
	for _, app := range f.apps {
		apps = append(apps, app)
	}
	return apps, nil
}

func (f *FakeCatalogCache) GetUserApps() ([]*catalog.App, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var apps []*catalog.App
	for _, app := range f.apps {
		if !catalog.IsSystemApp(app) {
			apps = append(apps, app)
		}
	}
	return apps, nil
}

func (f *FakeCatalogCache) IsSystemAppByName(name string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if app, ok := f.apps[name]; ok {
		return catalog.IsSystemApp(app)
	}
	return false
}

func (f *FakeCatalogCache) Refresh(loader *catalog.Loader) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	apps, err := loader.LoadAll()
	if err != nil {
		return err
	}
	f.apps = apps
	return nil
}

// AddApp is a test helper to add an app to the cache
func (f *FakeCatalogCache) AddApp(app *catalog.App) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[app.CatalogID] = app
}

// withFakes returns router options that inject test fakes into the router.
func withFakes(fCatalog catalog.CacheInterface, fAppStore store.AppStoreInterface) func(*routerOptions) {
	return func(o *routerOptions) {
		o.catalog = fCatalog
		o.appStore = fAppStore
	}
}

// setupTestServer creates a test server with real stores and a test catalog.
func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	tmpDir := t.TempDir()

	// Create test app directory with metadata.yaml
	testAppDir := filepath.Join(tmpDir, "test-app")
	require.NoError(t, os.MkdirAll(testAppDir, 0755))

	testAppYAML := `name: test-app
displayName: Test App
description: A test application
category: testing
version: 1.0.0
dependencies: []
resources:
  minRam: 128
  minDisk: 1
  gpu: false
sso:
  enabled: false
  protocol: ""
  blueprint: ""
defaultConfig: {}
healthCheck:
  path: /health
  interval: 30
  timeout: 5
docs:
  homepage: https://example.com
  source: https://github.com/example/test-app
tags:
  - test
`
	require.NoError(t, os.WriteFile(filepath.Join(testAppDir, "metadata.yaml"), []byte(testAppYAML), 0644))

	// Create logger
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	// Create a temporary SQLite database
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	// Initialize database tables
	require.NoError(t, initTestDB(db))

	// Create server config
	cfg := ServerConfig{
		AppsDir:             tmpDir,
		DataDir:             tmpDir,
		TraefikDynamicDir:   tmpDir,
		Port:                8080,
	}

	// Create a fake catalog cache with the test app
	fCatalog := NewFakeCatalogCache()
	loader := catalog.NewLoader(tmpDir)
	require.NoError(t, fCatalog.Refresh(loader))

	// Create a fake app store
	fAppStore := NewFakeAppStore()

	// Create a fake remote app store
	fRemoteStore := NewFakeRemoteAppStore()

	// Create a fake orchestrator (no-op, returns nil orchestrator for modules)
	router, _ := NewRouter(db, cfg, logger, func(o *routerOptions) {
		o.catalog = fCatalog
		o.appStore = fAppStore
		o.remoteAppStore = fRemoteStore
		o.noOrchestrator = true // modules get nil orchestrator
	})
	server := &Server{
		cfg:              cfg,
		router:           router,
		db:               db,
		catalog:          fCatalog,
		appStore:         fAppStore,
		orch:             nil,
		remoteAppStore:   fRemoteStore,
		logger:           logger,
	}
	return server, tmpDir
}

// initTestDB creates the database tables needed by the stores.
func initTestDB(db *sql.DB) error {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS apps (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			catalog_id TEXT NOT NULL UNIQUE,
			display_name TEXT,
			version TEXT,
			status TEXT DEFAULT 'installing',
			last_error TEXT NOT NULL DEFAULT '',
			port INTEGER,
			is_system INTEGER DEFAULT 0,
			tailnet_id TEXT,
			integration_config TEXT,
			installed_at TEXT DEFAULT (datetime('now')),
			updated_at TEXT DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS remote_apps (
			id TEXT PRIMARY KEY,
			host_label TEXT NOT NULL,
			app_id TEXT NOT NULL,
			app_name TEXT,
			sso_strategy TEXT,
			bypass_paths TEXT,
			tailnet_addr TEXT,
			encrypted_cred BLOB,
			status TEXT DEFAULT 'inactive',
			created_at TEXT DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS shares (
			id TEXT PRIMARY KEY,
			app_id TEXT NOT NULL,
			sso_strategy TEXT,
			guest_id TEXT,
			node_share_link TEXT,
			status TEXT DEFAULT 'active',
			created_at TEXT DEFAULT (datetime('now')),
			revoked_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS guests (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at TEXT DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS user_app_positions (
			username TEXT NOT NULL,
			element_id TEXT NOT NULL,
			element_type TEXT NOT NULL,
			x INTEGER,
			y INTEGER,
			w INTEGER DEFAULT 1,
			h INTEGER DEFAULT 1,
			PRIMARY KEY (username, element_id)
		)`,
		`CREATE TABLE IF NOT EXISTS tailnet_connections (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			auth_key TEXT,
			control_url TEXT,
			status TEXT DEFAULT 'inactive',
			created_at TEXT DEFAULT (datetime('now')),
			updated_at TEXT DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS user_preferences (
			username TEXT PRIMARY KEY
		)`,
	}
	for _, tbl := range tables {
		if _, err := db.Exec(tbl); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
	}
	return nil
}

// setupTestServerWithFakes creates a test server with fake catalog and app store injected.
func setupTestServerWithFakes(t *testing.T) (*Server, string) {
	t.Helper()
	tmpDir := t.TempDir()

	// Create logger
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, initTestDB(db))

	cfg := ServerConfig{
		AppsDir:             tmpDir,
		DataDir:             tmpDir,
		TraefikDynamicDir:   tmpDir,
		Port:                8080,
	}

	fCatalog := NewFakeCatalogCache()
	fAppStore := NewFakeAppStore()
	fRemoteStore := NewFakeRemoteAppStore()

	router, _ := NewRouter(db, cfg, logger, func(o *routerOptions) {
		o.catalog = fCatalog
		o.appStore = fAppStore
		o.remoteAppStore = fRemoteStore
	})
	server := &Server{
		cfg:              cfg,
		router:           router,
		db:               db,
		catalog:          fCatalog,
		appStore:         fAppStore,
		remoteAppStore:   fRemoteStore,
		logger:           logger,
	}
	return server, tmpDir
}

// fakeOrchestratorForTest is a no-op orchestrator that satisfies the
// handler interface for router-level tests.
type fakeOrchestratorForTest struct{}

func (f *fakeOrchestratorForTest) Submit(intent orchestrator.Intent) {}

// setupTestServerWithWorkingOrchestrator creates a server with a non-nil orchestrator.
func setupTestServerWithWorkingOrchestrator(t *testing.T) (*Server, string) {
	t.Helper()
	tmpDir := t.TempDir()

	testAppDir := filepath.Join(tmpDir, "test-app")
	require.NoError(t, os.MkdirAll(testAppDir, 0755))

	testAppYAML := `name: test-app
displayName: Test App
description: A test application
category: testing
version: 1.0.0
dependencies: []
resources:
  minRam: 128
  minDisk: 1
  gpu: false
sso:
  enabled: false
  protocol: ""
  blueprint: ""
defaultConfig: {}
healthCheck:
  path: /health
  interval: 30
  timeout: 5
docs:
  homepage: https://example.com
  source: https://github.com/example/test-app
tags:
  - test
`
	require.NoError(t, os.WriteFile(filepath.Join(testAppDir, "metadata.yaml"), []byte(testAppYAML), 0644))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, initTestDB(db))

	cfg := ServerConfig{
		AppsDir:             tmpDir,
		DataDir:             tmpDir,
		TraefikDynamicDir:   tmpDir,
		Port:                8080,
	}

	fCatalog := NewFakeCatalogCache()
	loader := catalog.NewLoader(tmpDir)
	require.NoError(t, fCatalog.Refresh(loader))

	fAppStore := NewFakeAppStore()
	fRemoteStore := NewFakeRemoteAppStore()

	router, _ := NewRouter(db, cfg, logger, func(o *routerOptions) {
		o.catalog = fCatalog
		o.appStore = fAppStore
		o.remoteAppStore = fRemoteStore
		o.orch = &fakeOrchestratorForTest{}
	})
	server := &Server{
		cfg:              cfg,
		router:           router,
		db:               db,
		catalog:          fCatalog,
		appStore:         fAppStore,
		orch:             nil,
		remoteAppStore:   fRemoteStore,
		logger:           logger,
	}
	return server, tmpDir
}

// serverRequest is a test helper that sends an HTTP request to the test server.
// If noAuth is true, the request will not be treated as a local request (no auto-auth).
func serverRequest(t *testing.T, server *Server, method, path string, body *strings.Reader, noAuth ...bool) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, body)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	// Simulate localhost request for auth middleware to auto-authenticate as admin
	if len(noAuth) == 0 || !noAuth[0] {
		req.RemoteAddr = "127.0.0.1:1234"
	}
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	return w
}

// ── Health & Catalog Tests ──────────────────────────────────────────────

func TestAPI_Health(t *testing.T) {
	server, _ := setupTestServer(t)
	w := serverRequest(t, server, "GET", "/api/health", nil)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	assert.Equal(t, "ok", response["status"])
}

func TestAPI_ListApps(t *testing.T) {
	server, _ := setupTestServer(t)
	w := serverRequest(t, server, "GET", "/api/apps", nil)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	apps, ok := response["apps"].([]interface{})
	require.True(t, ok, "response should contain apps array")
	require.Len(t, apps, 1, "should have exactly 1 app")

	// Check first app
	app := apps[0].(map[string]interface{})
	assert.Equal(t, "test-app", app["catalogId"])
	assert.Equal(t, "Test App", app["displayName"])
}

func TestAPI_ListInstalledApps_Empty(t *testing.T) {
	server, _ := setupTestServer(t)
	w := serverRequest(t, server, "GET", "/api/apps/installed", nil)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	apps, ok := response["apps"].([]interface{})
	require.True(t, ok, "response should contain apps array")
	assert.Empty(t, apps, "should have 0 installed apps")
}

func TestAPI_SystemStatus(t *testing.T) {
	server, _ := setupTestServer(t)
	w := serverRequest(t, server, "GET", "/api/system/status", nil)

	assert.Equal(t, http.StatusOK, w.Code)

	var stats map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&stats)
	require.NoError(t, err)

	// Check that required fields exist
	assert.Contains(t, stats, "cpu", "response should contain cpu field")
	assert.Contains(t, stats, "memory", "response should contain memory field")
	assert.Contains(t, stats, "disk", "response should contain disk field")
}

func TestAPI_Storage(t *testing.T) {
	server, _ := setupTestServer(t)
	w := serverRequest(t, server, "GET", "/api/system/storage", nil)

	assert.Equal(t, http.StatusOK, w.Code)

	var storage map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&storage)
	require.NoError(t, err)

	// Check that required fields exist
	assert.Contains(t, storage, "used", "response should contain used field")
	assert.Contains(t, storage, "total", "response should contain total field")
	assert.Contains(t, storage, "free", "response should contain free field")
	assert.Contains(t, storage, "percentage", "response should contain percentage field")
	assert.Contains(t, storage, "path", "response should contain path field")

	// Verify values are sensible
	used := storage["used"].(float64)
	total := storage["total"].(float64)
	free := storage["free"].(float64)

	assert.Greater(t, total, float64(0), "total should be greater than 0")
	assert.GreaterOrEqual(t, free, float64(0), "free should be >= 0")
	assert.GreaterOrEqual(t, used, float64(0), "used should be >= 0")
	assert.Equal(t, "/", storage["path"], "path should be root")
}

func TestAPI_RefreshCatalog(t *testing.T) {
	server, appsDir := setupTestServer(t)

	// Add another app to catalog
	newAppDir := filepath.Join(appsDir, "new-app")
	require.NoError(t, os.MkdirAll(newAppDir, 0755))

	newAppYAML := `name: new-app
displayName: New App
description: A newly added app
category: testing
version: 2.0.0
dependencies: []
resources:
  minRam: 256
  minDisk: 2
  gpu: false
sso:
  enabled: false
  protocol: ""
  blueprint: ""
defaultConfig: {}
healthCheck:
  path: /
  interval: 30
  timeout: 5
docs:
  homepage: https://example.com
  source: https://github.com/example/new-app
tags:
  - new
`
	newAppFile := filepath.Join(newAppDir, "metadata.yaml")
	err := os.WriteFile(newAppFile, []byte(newAppYAML), 0644)
	require.NoError(t, err, "should be able to write new app file")

	// Refresh catalog
	w := serverRequest(t, server, "POST", "/api/apps/refresh-catalog", nil)
	assert.Equal(t, http.StatusOK, w.Code)

	// Verify new app is in catalog
	w = serverRequest(t, server, "GET", "/api/apps", nil)

	var response map[string]interface{}
	err = json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	apps, ok := response["apps"].([]interface{})
	require.True(t, ok, "response should contain apps array")
	assert.Len(t, apps, 2, "should have 2 apps after refresh")
}

func TestAPI_AppMetadata(t *testing.T) {
	server, _ := setupTestServer(t)
	w := serverRequest(t, server, "GET", "/api/apps/test-app/metadata", nil)

	assert.Equal(t, http.StatusOK, w.Code)

	var app map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&app)
	require.NoError(t, err)

	assert.Equal(t, "test-app", app["catalogId"])
	assert.Equal(t, "Test App", app["displayName"])
	assert.Equal(t, "A test application", app["description"])
}

func TestAPI_AppMetadata_NotFound(t *testing.T) {
	server, _ := setupTestServer(t)
	w := serverRequest(t, server, "GET", "/api/apps/nonexistent/metadata", nil)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestAPI_AppIcon removed — no icon handler exists in the current apps module.

func TestAPI_Install_NoReconciler(t *testing.T) {
	server, _ := setupTestServer(t) // default has nil orchestrator

	w := serverRequest(t, server, "POST", "/api/apps/test-app/install", nil)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAPI_Uninstall_NoReconciler(t *testing.T) {
	server, _ := setupTestServer(t) // default has nil orchestrator

	w := serverRequest(t, server, "POST", "/api/apps/test-app/uninstall", nil)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAPI_Install_Returns202(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)
	w := serverRequest(t, server, "POST", "/api/apps/test-app/install", nil)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.NotEmpty(t, response["intentId"], "response should contain intentId")
}

func TestAPI_Install_NotFound(t *testing.T) {
	server, _ := setupTestServer(t)
	w := serverRequest(t, server, "POST", "/api/apps/nonexistent/install", nil)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_Uninstall_Returns202(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)
	w := serverRequest(t, server, "POST", "/api/apps/test-app/uninstall", nil)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.NotEmpty(t, response["intentId"], "response should contain intentId")
}

func TestAPI_ClearData_NotFound(t *testing.T) {
	server, _ := setupTestServer(t)
	w := serverRequest(t, server, "POST", "/api/apps/nonexistent/clear-data", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// Note: clear-data endpoint no longer has an HTTP route in the deep modules refactor.
// The AppsModule.ClearData() method still exists for programmatic use.

// ── Utility function tests ─────────────────────────────────────────────

func TestRespondJSON(t *testing.T) {
	w := httptest.NewRecorder()

	respondJSON(w, http.StatusCreated, map[string]string{"key": "value"})

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.Equal(t, "value", response["key"])
}

func TestRespondError(t *testing.T) {
	w := httptest.NewRecorder()

	respondError(w, http.StatusBadRequest, "something went wrong")

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.Equal(t, "something went wrong", response["error"])
}

// ── Auth Middleware Tests ───────────────────────────────────────────────

func TestAPI_GetCurrentUser_Unauthenticated(t *testing.T) {
	server, _ := setupTestServer(t)
	w := serverRequest(t, server, "GET", "/api/auth/me", nil, true)

	assert.Equal(t, http.StatusUnauthorized, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.Equal(t, "Not authenticated", response["error"])
}

func TestAPI_Login_NoAuthConfig(t *testing.T) {
	server, _ := setupTestServer(t)
	w := serverRequest(t, server, "GET", "/auth/login", nil)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAPI_Callback_NoAuthConfig(t *testing.T) {
	server, _ := setupTestServer(t)
	w := serverRequest(t, server, "GET", "/auth/callback?code=test", nil)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAPI_Logout_ClearsCookie(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("POST", "/auth/logout", nil)
	req.Host = "localhost:8080"
	// No session cookie — handler just redirects to /
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	// Should redirect to home (fallback when auth is not configured)
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/", w.Header().Get("Location"))
}

// ── Context Tests ──────────────────────────────────────────────────────

func TestGetUserFromContext(t *testing.T) {
	t.Run("no user in context", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		user := getUserFromContext(req.Context())
		assert.Nil(t, user)
	})

	t.Run("user in context", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		expectedUser := &store.User{
			Username: "testuser",
		}
		ctx := req.Context()
		ctx = context.WithValue(ctx, userContextKey, expectedUser)
		req = req.WithContext(ctx)

		user := getUserFromContext(req.Context())
		require.NotNil(t, user)
		assert.Equal(t, "testuser", user.Username)
	})
}

// ── SSO Launch Path Tests ──────────────────────────────────────────────

func TestHandleListInstalledApps_IncludesSSOLaunchPath(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	// Add catalog app with SSO launch path
	server.catalog.(*FakeCatalogCache).AddApp(&catalog.App{
		CatalogID: "miniflux",
		SSO:       catalog.SSO{LaunchPath: "oauth2/oidc/redirect"},
	})

	// Add matching installed app
	server.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID: "miniflux",
		Status:    "running",
	})

	w := serverRequest(t, server, "GET", "/api/apps/installed", nil)

	require.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&response))

	apps, ok := response["apps"].([]interface{})
	require.True(t, ok)
	require.Len(t, apps, 1)
	app := apps[0].(map[string]interface{})
	assert.Equal(t, "oauth2/oidc/redirect", app["sso_launch_path"])
}

// ── Rename Tests ────────────────────────────────────────────────────────

func TestAPI_Rename_Returns202(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	server.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID:   "test-app",
		DisplayName: "Test App",
		Status:      "running",
	})

	body := strings.NewReader(`{"displayName":"My Custom Name"}`)
	w := serverRequest(t, server, "PATCH", "/api/apps/test-app/rename", body)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.NotEmpty(t, response["intentId"], "response should contain intentId")
}

func TestAPI_Rename_MissingDisplayName(t *testing.T) {
	server, _ := setupTestServerWithFakes(t)

	body := strings.NewReader(`{"displayName":""}`)
	w := serverRequest(t, server, "PATCH", "/api/apps/test-app/rename", body)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Test generateState
func TestGenerateState(t *testing.T) {
	state1, err := generateState()
	require.NoError(t, err)
	assert.Len(t, state1, 32) // 16 bytes = 32 hex chars

	state2, err := generateState()
	require.NoError(t, err)
	assert.Len(t, state2, 32)

	// Should be unique
	assert.NotEqual(t, state1, state2)
}
