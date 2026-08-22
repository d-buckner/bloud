// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
// allowing easy mocking in tests. Handlers submit intents (never enqueue
// directly) so the orchestrator can record user-visible state at submit
// time (e.g. the installing row behind an install 202).
type orchestratorCaller interface {
	Submit(intent orchestrator.Intent)
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
	// imageSizeResolver returns the on-disk size (bytes) of a locally
	// present image. Nil disables the catalog size fallback.
	imageSizeResolver func(ctx context.Context, image string) (int64, bool)
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

// SetAppsDir sets the apps directory (used for orphaned data cleanup and
// serving app icons).
func (m *appsModule) SetAppsDir(dir string) {
	m.appsDir = dir
}

// SetImageSizeResolver wires the local-image size lookup used to fill in
// catalog entries without a declared estimatedSizeMB.
func (m *appsModule) SetImageSizeResolver(fn func(ctx context.Context, image string) (int64, bool)) {
	m.imageSizeResolver = fn
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

// GetCatalog returns all user-facing apps, enriching entries without a
// declared size estimate with the summed local image sizes when the images
// are already present (so the catalog can set pull expectations either way).
func (m *appsModule) GetCatalog(ctx context.Context) ([]*catalog.App, error) {
	apps, err := m.catalog.GetUserApps()
	if err != nil {
		return nil, err
	}
	if m.imageSizeResolver == nil {
		return apps, nil
	}
	for _, app := range apps {
		if app.EstimatedSizeMB > 0 || len(app.Containers) == 0 {
			continue
		}
		var total int64
		seen := make(map[string]bool, len(app.Containers))
		for _, def := range app.Containers {
			if seen[def.Image] {
				continue // shared images (e.g. server+worker) pull once
			}
			seen[def.Image] = true
			if size, ok := m.imageSizeResolver(ctx, def.Image); ok {
				total += size
			}
		}
		if total > 0 {
			// Mutates the cached entry: the size is stable for the lifetime
			// of the local image and re-resolution is cheap when absent.
			app.EstimatedSizeMB = int(total / (1024 * 1024))
		}
	}
	return apps, nil
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

// Install submits an install intent for the named app. Because the
// orchestrator records the installing row synchronously at submit time, the
// current app record is read back and returned alongside the intent ref so
// the 202 response carries it.
func (m *appsModule) Install(name string) (*IntentRef, *installedAppResponse, error) {
	if _, err := m.catalog.Get(name); err != nil {
		return nil, nil, fmt.Errorf("%w: %s", errAppNotFound, name)
	}
	if m.orch == nil {
		return nil, nil, fmt.Errorf("orchestrator not available")
	}
	intent := orchestrator.NewInstallAppIntent(name)
	m.orch.Submit(intent)
	m.logger.Info("install intent submitted", "app", name, "intentId", intent.IntentID())

	var app *installedAppResponse
	if row, err := m.appStore.GetByCatalogID(name); err == nil && row != nil {
		app = &installedAppResponse{InstalledApp: row}
		if path, ok := m.buildLaunchPaths()[name]; ok {
			app.SSOLaunchPath = path
		}
	}
	return &IntentRef{ID: intent.IntentID()}, app, nil
}

// Uninstall enqueues an uninstall intent.
func (m *appsModule) Uninstall(name string, clearData bool) (*IntentRef, error) {
	if m.orch == nil {
		return nil, fmt.Errorf("orchestrator not available")
	}
	intent := orchestrator.NewUninstallAppIntent(name, clearData)
	m.orch.Submit(intent)
	m.logger.Info("uninstall intent submitted", "app", name, "clearData", clearData)
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
	m.orch.Submit(intent)
	m.logger.Info("rename intent submitted", "app", name, "displayName", displayName)
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
		m.orch.Submit(intent)
		m.logger.Info("clear data: submitted uninstall intent", "app", name)
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

// IconHandler serves the app icon (<appsDir>/<name>/icon.png). Missing
// icons return 404 so the frontend falls back to a letter avatar.
func (m *appsModule) IconHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if m.appsDir == "" || name == "" || strings.ContainsAny(name, "\\/") {
			http.NotFound(w, r)
			return
		}
		iconPath := filepath.Join(m.appsDir, name, "icon.png")
		if _, err := os.Stat(iconPath); os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, iconPath)
	}
}

// NewAppsRouter registers all app-related routes on the given router.
func NewAppsRouter(mod *appsModule, r chi.Router) {
	r.Get("/apps", mod.GetCatalogHandler())
	r.Get("/apps/installed", mod.GetInstalledHandler())
	r.Get("/apps/{name}/metadata", mod.AppMetadataHandler())
	r.Get("/apps/{name}/icon", mod.IconHandler())
	r.Post("/apps/{name}/install", mod.InstallHandler())
	r.Post("/apps/{name}/uninstall", mod.UninstallHandler())
	r.Patch("/apps/{name}/rename", mod.RenameHandler())
	r.Post("/apps/refresh-catalog", mod.RefreshCatalogHandler())
}

// GetCatalogHandler returns all user-facing apps from the catalog.
func (m *appsModule) GetCatalogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apps, err := m.GetCatalog(r.Context())
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

// InstallHandler submits an install intent. The 202 response includes the
// current app record (the orchestrator records the installing row at submit
// time) so the frontend can render the tile immediately without polling.
func (m *appsModule) InstallHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		ref, app, err := m.Install(name)
		if err != nil {
			if errors.Is(err, errAppNotFound) {
				respondError(w, http.StatusNotFound, "app not found in catalog")
				return
			}
			respondError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		resp := map[string]any{"intentId": ref.ID}
		if app != nil {
			resp["app"] = app
		}
		respondJSON(w, http.StatusAccepted, resp)
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
