// SPDX-License-Identifier: AGPL-3.0-only

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
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/dirs"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
)

// Sentinel errors for app lifecycle operations.
var (
	// errAppNotFound is returned when an app is missing from the catalog.
	errAppNotFound = errors.New("app not found in catalog")
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

// planSource supplies the dependency-graph plans behind the pre-flight
// install and uninstall views. The orchestrator implements it. The API reads
// a plan rather than computing one so the resolver stays single: the client
// has the catalog but not the orchestrator's graph, and a second
// implementation in TypeScript would drift on the first metadata edit.
type planSource interface {
	PlanInstall(appName string) (*catalog.InstallPlan, error)
	PlanRemove(appName string) (*catalog.RemovePlan, error)
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
	// dataDir is BLOUD_DATA_DIR, the writable root that holds each app's
	// private tree under apps/. Distinct from appsDir, which is the read-only
	// catalog. Orphaned-data cleanup must use this one: joining the catalog
	// instead deletes the installed app's source directory.
	dataDir string
	// imageSizeResolver returns the on-disk size (bytes) of a locally
	// present image. Nil disables the catalog size fallback.
	imageSizeResolver func(ctx context.Context, image string) (int64, bool)
	// plans answers what an install or uninstall would do before it is
	// committed. Nil means no plan source was wired, which the handlers
	// report as 503 rather than as an empty plan: "nothing to install" and
	// "nothing was able to tell me" must not look the same.
	plans planSource
}

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

// SetAppsDir sets the catalog directory (used for catalog reloads and for
// serving app icons). This is the read-only install location, not where app
// state lives; orphaned-data cleanup uses SetDataDir.
func (m *appsModule) SetAppsDir(dir string) {
	m.appsDir = dir
}

// SetDataDir sets BLOUD_DATA_DIR, the root that holds each app's private
// tree under apps/.
func (m *appsModule) SetDataDir(dir string) {
	m.dataDir = dir
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

// GetInstalled returns installed user apps enriched with SSO launch paths and
// a catalog-missing flag.
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
	return enrichApps(userApps, m.buildLaunchPaths(), m.catalogMissing(userApps), m.buildHeadlessSet()), nil
}

// catalogMissing returns the installed apps that have no catalog entry, so the
// dashboard can badge an app whose directory was removed or renamed.
func (m *appsModule) catalogMissing(apps []*store.InstalledApp) map[string]bool {
	missing := make(map[string]bool)
	if m.catalog == nil {
		return missing
	}
	for _, app := range apps {
		if _, err := m.catalog.Get(app.CatalogID); err != nil {
			missing[app.CatalogID] = true
		}
	}
	return missing
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
	def, err := m.catalog.Get(name)
	if err != nil {
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
		app = &installedAppResponse{InstalledApp: row, Headless: def.Headless}
		if path, ok := m.buildLaunchPaths()[name]; ok {
			app.SSOLaunchPath = path
		}
	}
	return &IntentRef{ID: intent.IntentID()}, app, nil
}

func (m *appsModule) Uninstall(name string, clearData bool) (*IntentRef, error) {
	if m.orch == nil {
		return nil, fmt.Errorf("orchestrator not available")
	}
	intent := orchestrator.NewUninstallAppIntent(name, clearData)
	m.orch.Submit(intent)
	m.logger.Info("uninstall intent submitted", "app", name, "clearData", clearData)
	return &IntentRef{ID: intent.IntentID()}, nil
}

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
		// App is installed: enqueue uninstall with clearData
		if m.orch == nil {
			return nil, fmt.Errorf("orchestrator not available")
		}
		intent := orchestrator.NewUninstallAppIntent(name, true)
		m.orch.Submit(intent)
		m.logger.Info("clear data: submitted uninstall intent", "app", name)
		return &IntentRef{ID: intent.IntentID()}, nil
	}

	// Orphaned data: remove the app's private data directory directly. This
	// is the writable tree under BLOUD_DATA_DIR, never the catalog.
	if m.dataDir != "" {
		appDataDir := dirs.AppDataDir(m.dataDir, name)
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

// buildHeadlessSet indexes the catalog ids of apps that declare no browser UI,
// so every installed-app read can mark them without re-walking the catalog per
// app. A nil catalog (the catalog-less test wiring) yields an empty set.
func (m *appsModule) buildHeadlessSet() map[string]bool {
	headless := make(map[string]bool)
	if m.catalog == nil {
		return headless
	}
	apps, err := m.catalog.GetAll()
	if err != nil {
		return headless
	}
	for _, a := range apps {
		if a.Headless {
			headless[a.CatalogID] = true
		}
	}
	return headless
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

// SetPlanSource wires the dependency-graph reader used by the install-plan
// and remove-plan routes. Callers must pass a non-nil implementation: a nil
// *Orchestrator assigned to this interface field would be a non-nil interface
// holding a nil pointer, and the 503 guard below would not fire.
func (m *appsModule) SetPlanSource(s planSource) {
	m.plans = s
}

// InstallPlanHandler reports what installing the named app would do, before
// anything is committed: the providers it needs, which of them are already
// installed, which integrations still need an operator choice, and which
// installed apps would be reconfigured as a result.
//
// The plan is advisory. It is computed now and the install happens later, and
// applyInstallIntent re-plans on its own rather than trusting this snapshot.
func (m *appsModule) InstallPlanHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if _, err := m.catalog.Get(name); err != nil {
			respondError(w, http.StatusNotFound, "app not found")
			return
		}
		if m.plans == nil {
			respondError(w, http.StatusServiceUnavailable, "install plans unavailable")
			return
		}
		plan, err := m.plans.PlanInstall(name)
		if err != nil {
			m.logger.Error("failed to compute install plan", "app", name, "error", err)
			respondError(w, http.StatusInternalServerError, "failed to compute install plan")
			return
		}
		respondJSON(w, http.StatusOK, plan)
	}
}

// RemovePlanHandler reports what removing the named app would do: which
// installed apps would lose an integration, and whether the removal is
// blocked because some app requires this provider with no alternative.
//
// Read-only. Nothing in the uninstall path enforces CanRemove yet, so today
// this tells the operator what the planner believes without changing what a
// removal does. Enforcement is a separate, deliberate change.
func (m *appsModule) RemovePlanHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if _, err := m.catalog.Get(name); err != nil {
			respondError(w, http.StatusNotFound, "app not found")
			return
		}
		if m.plans == nil {
			respondError(w, http.StatusServiceUnavailable, "remove plans unavailable")
			return
		}
		plan, err := m.plans.PlanRemove(name)
		if err != nil {
			m.logger.Error("failed to compute remove plan", "app", name, "error", err)
			respondError(w, http.StatusInternalServerError, "failed to compute remove plan")
			return
		}
		respondJSON(w, http.StatusOK, plan)
	}
}

// NewAppsRouter registers all app-related routes on the given router. It is
// mounted on the member router; admin-only app routes (refresh-catalog) are
// registered by the caller on the admin router so the two sets cannot collide.
func NewAppsRouter(mod *appsModule, r chi.Router) {
	r.Get("/apps", mod.GetCatalogHandler())
	r.Get("/apps/installed", mod.GetInstalledHandler())
	r.Get("/apps/{name}/metadata", mod.AppMetadataHandler())
	r.Get("/apps/{name}/install-plan", mod.InstallPlanHandler())
	r.Get("/apps/{name}/remove-plan", mod.RemovePlanHandler())
	r.Get("/apps/{name}/icon", mod.IconHandler())
	r.Post("/apps/{name}/install", mod.InstallHandler())
	r.Post("/apps/{name}/uninstall", mod.UninstallHandler())
	r.Patch("/apps/{name}/rename", mod.RenameHandler())
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
		// Apply the refreshed catalog promptly rather than waiting for the
		// next periodic pass.
		if m.orch != nil {
			m.orch.Submit(orchestrator.NewReconcileIntent())
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "catalog refreshed"})
	}
}

// installedAppResponse extends InstalledApp with catalog-derived fields.
type installedAppResponse struct {
	*store.InstalledApp
	SSOLaunchPath string `json:"sso_launch_path,omitempty"`
	// Headless is the catalog's answer to "is there a UI to open?". The
	// dashboard keeps such an app off the grid but still reports its status.
	Headless       bool `json:"headless,omitempty"`
	CatalogMissing bool `json:"catalog_missing,omitempty"`
}

// enrichApps enriches installed apps with SSO launch paths, the
// catalog-missing flag, and the catalog's headless marking.
func enrichApps(apps []*store.InstalledApp, launchPaths map[string]string, catalogMissing map[string]bool, headless map[string]bool) []installedAppResponse {
	result := make([]installedAppResponse, 0, len(apps))
	for _, app := range apps {
		result = append(result, installedAppResponse{
			InstalledApp:   app,
			SSOLaunchPath:  launchPaths[app.CatalogID],
			Headless:       headless[app.CatalogID],
			CatalogMissing: catalogMissing[app.CatalogID],
		})
	}
	return result
}
