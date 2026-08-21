// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
)

// Sentinel errors for app lifecycle operations.
var (
	// errAppNotFound is returned when an app is missing from the catalog.
	errAppNotFound = errors.New("app not found in catalog")
	// errRemoteAppNotFound is returned when a remote app is missing from the store.
	errRemoteAppNotFound = errors.New("remote app not found")
)

// IntentRef is a lightweight reference to an enqueued orchestrator intent.
type IntentRef struct {
	ID string
}

// orchestratorCaller is a minimal interface for the orchestrator dependency,
// allowing easy mocking in tests.
type orchestratorCaller interface {
	Enqueue(intent orchestrator.Intent)
}

// AppsModule encapsulates all app catalog and lifecycle operations.
// It is a deep module: the implementation hides catalog lookups, store
// queries, orchestrator interactions, and filesystem operations for orphaned
// data cleanup.
type appsModule struct {
	catalog  catalog.CacheInterface
	appStore store.AppStoreInterface
	orch     orchestratorCaller
	logger   *slog.Logger
	appsDir  string
}

// NewAppsModule creates a new AppsModule.
func NewAppsModule(
	catalog catalog.CacheInterface,
	appStore store.AppStoreInterface,
	orch orchestratorCaller,
	logger *slog.Logger,
) *appsModule {
	return &appsModule{
		catalog:  catalog,
		appStore: appStore,
		orch:     orch,
		logger:   logger,
	}
}

// SetAppsDir sets the apps directory for orphaned data cleanup.
func (m *appsModule) SetAppsDir(dir string) {
	m.appsDir = dir
}

// RefreshCatalog reloads the catalog cache from disk.
func (m *appsModule) RefreshCatalog() error {
	loader := catalog.NewLoader(m.appsDir)
	if err := m.catalog.Refresh(loader); err != nil {
		m.logger.Error("failed to refresh catalog", "error", err)
		return err
	}
	m.logger.Info("catalog refreshed")
	return nil
}

// GetCatalog returns all user-facing apps.
func (m *appsModule) GetCatalog() ([]*catalog.App, error) {
	return m.catalog.GetUserApps()
}

// GetInstalled returns installed user apps enriched with SSO launch paths.
func (m *appsModule) GetInstalled() ([]installedAppResponse, error) {
	all, err := m.appStore.GetAll()
	if err != nil {
		return nil, fmt.Errorf("get installed apps: %w", err)
	}
	userApps := make([]*store.InstalledApp, 0, len(all))
	for _, app := range all {
		if !app.IsSystem {
			userApps = append(userApps, app)
		}
	}
	return enrichApps(userApps, m.buildLaunchPaths()), nil
}

// AppMetadata returns the catalog definition for an app by name.
func (m *appsModule) AppMetadata(name string) (*catalog.App, error) {
	app, err := m.catalog.Get(name)
	if err != nil {
		return nil, fmt.Errorf("app not found: %s", name)
	}
	return app, nil
}

// Install enqueues an install intent for the named app.
func (m *appsModule) Install(name string) (*IntentRef, error) {
	if _, err := m.catalog.Get(name); err != nil {
		return nil, fmt.Errorf("%w: %s", errAppNotFound, name)
	}
	if m.orch == nil {
		return nil, fmt.Errorf("orchestrator not available")
	}
	intent := orchestrator.NewInstallAppIntent(name)
	m.orch.Enqueue(intent)
	m.logger.Info("install intent enqueued", "app", name, "intentId", intent.IntentID())
	return &IntentRef{ID: intent.IntentID()}, nil
}

// Uninstall enqueues an uninstall intent.
func (m *appsModule) Uninstall(name string, clearData bool) (*IntentRef, error) {
	if m.orch == nil {
		return nil, fmt.Errorf("orchestrator not available")
	}
	intent := orchestrator.NewUninstallAppIntent(name, clearData)
	m.orch.Enqueue(intent)
	m.logger.Info("uninstall intent enqueued", "app", name, "clearData", clearData)
	return &IntentRef{ID: intent.IntentID()}, nil
}

// Rename enqueues a rename intent.
func (m *appsModule) Rename(name, displayName string) (*IntentRef, error) {
	if displayName == "" {
		return nil, fmt.Errorf("displayName is required")
	}
	if m.orch == nil {
		return nil, fmt.Errorf("orchestrator not available")
	}
	intent := orchestrator.NewRenameAppIntent(name, displayName)
	m.orch.Enqueue(intent)
	m.logger.Info("rename intent enqueued", "app", name, "displayName", displayName)
	return &IntentRef{ID: intent.IntentID()}, nil
}

// ClearData handles clearing an app's data. If the app is installed, it
// enqueues an uninstall intent with clearData=true. For orphaned data, it
// removes the data directory directly.
func (m *appsModule) ClearData(name string) (*IntentRef, error) {
	// First check if it exists in the catalog
	if _, err := m.catalog.Get(name); err != nil {
		return nil, fmt.Errorf("%w: %s", errAppNotFound, name)
	}

	app, _ := m.appStore.GetByCatalogID(name)
	if app != nil {
		// App is installed — enqueue uninstall with clearData
		if m.orch == nil {
			return nil, fmt.Errorf("orchestrator not available")
		}
		intent := orchestrator.NewUninstallAppIntent(name, true)
		m.orch.Enqueue(intent)
		m.logger.Info("clear data: enqueued uninstall intent", "app", name)
		return &IntentRef{ID: intent.IntentID()}, nil
	}

	// Orphaned data: remove the data directory directly
	if m.appsDir != "" {
		appDataDir := filepath.Join(m.appsDir, name)
		if _, err := os.Stat(appDataDir); err == nil {
			if err := os.RemoveAll(appDataDir); err != nil {
				m.logger.Error("failed to remove orphaned data dir", "app", name, "error", err)
				return nil, fmt.Errorf("failed to remove data directory: %w", err)
			}
			m.logger.Info("removed orphaned app data", "app", name, "path", appDataDir)
			return &IntentRef{ID: ""}, nil
		}
	}

	return nil, fmt.Errorf("app %q not installed and no data directory found", name)
}

// buildLaunchPaths builds a map of catalog ID → SSO launch path.
func (m *appsModule) buildLaunchPaths() map[string]string {
	apps, err := m.catalog.GetAll()
	if err != nil {
		return nil
	}
	paths := make(map[string]string)
	for _, a := range apps {
		if a.SSO.LaunchPath != "" {
			paths[a.CatalogID] = a.SSO.LaunchPath
		}
	}
	return paths
}

// ---- HTTP handler methods (on concrete type, not interface) ----

// NewAppsRouter registers all app-related routes on the given router.
func NewAppsRouter(mod *appsModule, r chi.Router) {
	r.Get("/apps", mod.GetCatalogHandler())
	r.Get("/apps/installed", mod.GetInstalledHandler())
	r.Get("/apps/{name}/metadata", mod.AppMetadataHandler())
	r.Post("/apps/{name}/install", mod.InstallHandler())
	r.Post("/apps/{name}/uninstall", mod.UninstallHandler())
	r.Patch("/apps/{name}/rename", mod.RenameHandler())
	r.Post("/apps/refresh-catalog", mod.RefreshCatalogHandler())
}

// GetCatalogHandler returns all user-facing apps from the catalog.
func (m *appsModule) GetCatalogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apps, err := m.GetCatalog()
		if err != nil {
			m.logger.Error("failed to get apps from catalog", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to get apps")
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"apps": apps})
	}
}

// GetInstalledHandler returns installed user apps.
func (m *appsModule) GetInstalledHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		installed, err := m.GetInstalled()
		if err != nil {
			m.logger.Error("failed to get installed apps", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to get apps")
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"apps": installed})
	}
}

// AppMetadataHandler returns the catalog metadata for a single app.
func (m *appsModule) AppMetadataHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		app, err := m.AppMetadata(name)
		if err != nil {
			m.logger.Error("failed to get app metadata", "app", name, "error", err)
			respondError(w, http.StatusNotFound, "app not found")
			return
		}
		respondJSON(w, http.StatusOK, app)
	}
}

// InstallHandler enqueues an install intent.
func (m *appsModule) InstallHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		ref, err := m.Install(name)
		if err != nil {
			if errors.Is(err, errAppNotFound) {
				respondError(w, http.StatusNotFound, "app not found in catalog")
				return
			}
			respondError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		respondJSON(w, http.StatusAccepted, map[string]string{"intentId": ref.ID})
	}
}

// UninstallHandler enqueues an uninstall intent.
func (m *appsModule) UninstallHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		var req struct {
			ClearData bool `json:"clearData"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				respondError(w, http.StatusBadRequest, "invalid request body")
				return
			}
		}
		ref, err := m.Uninstall(name, req.ClearData)
		if err != nil {
			respondError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		respondJSON(w, http.StatusAccepted, map[string]string{"intentId": ref.ID})
	}
}

// RenameHandler enqueues a rename intent.
func (m *appsModule) RenameHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		var req struct {
			DisplayName string `json:"displayName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.DisplayName == "" {
			respondError(w, http.StatusBadRequest, "displayName is required")
			return
		}
		ref, err := m.Rename(name, req.DisplayName)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusAccepted, map[string]string{"intentId": ref.ID})
	}
}

// RefreshCatalogHandler reloads the catalog from disk.
func (m *appsModule) RefreshCatalogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := m.RefreshCatalog(); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to refresh catalog")
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "catalog refreshed"})
	}
}

// installedAppResponse extends InstalledApp with catalog-derived fields.
type installedAppResponse struct {
	*store.InstalledApp
	SSOLaunchPath string `json:"sso_launch_path,omitempty"`
}

// enrichApps enriches installed apps with SSO launch paths from the catalog.
func enrichApps(apps []*store.InstalledApp, launchPaths map[string]string) []installedAppResponse {
	result := make([]installedAppResponse, 0, len(apps))
	for _, app := range apps {
		result = append(result, installedAppResponse{
			InstalledApp:  app,
			SSOLaunchPath: launchPaths[app.CatalogID],
		})
	}
	return result
}
