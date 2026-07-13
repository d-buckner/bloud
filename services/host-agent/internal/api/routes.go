package api

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/system"
	"github.com/go-chi/chi/v5"
)

//go:embed dev_dashboard.html
var devDashboardHTML []byte

// setupRoutes configures all HTTP routes
func (s *Server) setupRoutes() {
	s.router.Get("/auth/login", s.handleLogin)
	s.router.Get("/auth/callback", s.handleCallback)
	s.router.Post("/auth/logout", s.handleLogout)

	s.router.Route("/api", func(r chi.Router) {
		r.Get("/health", s.handleHealth)

		r.Route("/setup", func(r chi.Router) {
			r.Get("/status", s.handleSetupStatus)
			r.Post("/create-user", s.handleCreateUser)
		})

		r.Get("/auth/me", s.handleGetCurrentUser)

		r.Group(func(r chi.Router) {
			if s.sessionStore != nil {
				r.Use(s.authMiddleware)
			}

			// Member-accessible routes
			r.Get("/apps", s.handleListApps)
			r.Get("/apps/installed", s.handleListInstalledApps)
			r.Get("/apps/{name}/metadata", s.handleAppMetadata)
			r.Get("/apps/{name}/icon", s.handleAppIcon)
			r.Get("/apps/{name}/logs", s.handleAppLogs)
			r.Get("/system/status", s.handleSystemStatus)
			r.Get("/system/status/stream", s.handleSystemStatusStream)
			r.Get("/system/storage", s.handleStorage)
			r.Get("/system/developer", s.handleDeveloperGraph)
			r.Get("/user/home", s.handleGetHome)
			r.Put("/user/layout", s.handleSetLayout)

			// Admin-only routes
			r.Group(func(r chi.Router) {
				if s.sessionStore != nil {
					r.Use(s.adminMiddleware)
				}

				r.Post("/apps/refresh-catalog", s.handleRefreshCatalog)
				r.Post("/apps/{name}/install", s.handleInstall)
				r.Post("/apps/{name}/uninstall", s.handleUninstall)
				r.Post("/apps/{name}/clear-data", s.handleClearData)
				r.Patch("/apps/{name}/rename", s.handleRename)

				r.Get("/system/rebuild/stream", s.handleRebuildStream)

				r.Get("/settings/tailnet", s.handleGetTailnet)
				r.Post("/settings/tailnet", s.handleSetTailnet)
				r.Delete("/settings/tailnet", s.handleDeleteTailnet)

				r.Post("/sharing/invites", s.handleCreateInvite)
				r.Get("/sharing/shares", s.handleListShares)
				r.Delete("/sharing/shares/{id}", s.handleRevokeShare)
				r.Get("/sharing/guests", s.handleListGuests)
				r.Post("/sharing/guests", s.handleCreateGuest)
				r.Get("/sharing/community", s.handleCommunityGraph)
				r.Get("/sharing/remote-apps", s.handleListRemoteApps)
				r.Post("/sharing/remote-apps", s.handleAddRemoteApp)
				r.Delete("/sharing/remote-apps/{id}", s.handleDeleteRemoteApp)

				r.Get("/users", s.handleListUsers)
				r.Post("/users", s.handleCreateManagedUser)
				r.Delete("/users/{username}", s.handleDeleteManagedUser)
				r.Put("/users/{username}/role", s.handleSetUserRole)
			})
		})

		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			respondError(w, http.StatusNotFound, "not found")
		})
	})

	s.setupFrontend()
}

func (s *Server) setupFrontend() {
	buildDir := filepath.Join("web", "build")

	if _, err := os.Stat(buildDir); os.IsNotExist(err) {
		s.logger.Warn("frontend build directory not found, serving fallback HTML", "path", buildDir)
		s.router.Get("/*", s.handleRoot)
		return
	}

	s.logger.Info("serving frontend from filesystem", "path", buildDir)
	s.router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		urlPath := r.URL.Path
		if urlPath == "/" {
			urlPath = "/index.html"
		}
		filePath := filepath.Join(buildDir, filepath.Clean(urlPath))

		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFile(w, r, filepath.Join(buildDir, "index.html"))
			return
		}

		switch {
		case urlPath == "/index.html":
			w.Header().Set("Cache-Control", "no-store")
		case strings.HasPrefix(urlPath, "/_app/immutable/"):
			w.Header().Set("Cache-Control", "public, immutable, max-age=31536000")
		}

		http.ServeFile(w, r, filePath)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.catalog.GetUserApps()
	if err != nil {
		s.logger.Error("failed to get apps from catalog", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get apps")
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"apps": apps})
}

func (s *Server) handleRefreshCatalog(w http.ResponseWriter, r *http.Request) {
	s.refreshCatalog(s.cfg.AppsDir)
	respondJSON(w, http.StatusOK, map[string]string{"status": "catalog refreshed"})
}

// installedAppResponse extends InstalledApp with catalog-derived fields
type installedAppResponse struct {
	*store.InstalledApp
	SSOLaunchPath string `json:"sso_launch_path,omitempty"`
}

func (s *Server) buildLaunchPaths() map[string]string {
	launchPaths := make(map[string]string)
	if catalogApps, err := s.catalog.GetAll(); err == nil {
		for _, ca := range catalogApps {
			if ca.SSO.LaunchPath != "" {
				launchPaths[ca.CatalogID] = ca.SSO.LaunchPath
			}
		}
	}
	return launchPaths
}

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

func (s *Server) handleListInstalledApps(w http.ResponseWriter, r *http.Request) {
	all, err := s.appStore.GetAll()
	if err != nil {
		s.logger.Error("failed to get apps", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get apps")
		return
	}
	userApps := make([]*store.InstalledApp, 0, len(all))
	for _, app := range all {
		if !app.IsSystem {
			userApps = append(userApps, app)
		}
	}
	respondJSON(w, http.StatusOK, enrichApps(userApps, s.buildLaunchPaths()))
}

func (s *Server) handleAppMetadata(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	app, err := s.catalog.Get(name)
	if err != nil {
		s.logger.Error("failed to get app metadata", "app", name, "error", err)
		respondError(w, http.StatusNotFound, "app not found")
		return
	}
	respondJSON(w, http.StatusOK, app)
}

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	stats, err := system.GetStats()
	if err != nil {
		s.logger.Error("failed to get system stats", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get system stats")
		return
	}
	respondJSON(w, http.StatusOK, stats)
}

func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	storage, err := system.GetStorageStats()
	if err != nil {
		s.logger.Error("failed to get storage stats", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get storage stats")
		return
	}
	respondJSON(w, http.StatusOK, storage)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(devDashboardHTML)
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if s.orch == nil {
		respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
		return
	}
	if _, err := s.catalog.Get(name); err != nil {
		respondError(w, http.StatusNotFound, "app not found in catalog")
		return
	}
	intent := orchestrator.NewInstallAppIntent(name)
	s.orch.Enqueue(intent)
	respondJSON(w, http.StatusAccepted, map[string]string{"intentId": intent.IntentID()})
}

func (s *Server) handleUninstall(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if s.orch == nil {
		respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
		return
	}
	var req struct {
		ClearData bool `json:"clearData"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	intent := orchestrator.NewUninstallAppIntent(name, req.ClearData)
	s.orch.Enqueue(intent)
	respondJSON(w, http.StatusAccepted, map[string]string{"intentId": intent.IntentID()})
}

func (s *Server) handleClearData(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if _, err := s.catalog.Get(name); err != nil {
		respondError(w, http.StatusNotFound, "app not found in catalog")
		return
	}
	app, _ := s.appStore.GetByCatalogID(name)
	if app != nil {
		if s.orch == nil {
			respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
			return
		}
		intent := orchestrator.NewUninstallAppIntent(name, true)
		s.orch.Enqueue(intent)
		respondJSON(w, http.StatusAccepted, map[string]string{"intentId": intent.IntentID()})
		return
	}
	s.logger.Info("cleaning up orphaned app data", "app", name)
	appDataDir := filepath.Join(s.cfg.DataDir, name)
	if err := os.RemoveAll(appDataDir); err != nil {
		s.logger.Error("failed to remove app data directory", "app", name, "error", err)
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to remove data directory: %v", err))
		return
	}
	if err := s.dropAppDatabase(name); err != nil {
		s.logger.Warn("failed to drop app database", "app", name, "error", err)
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "data cleared", "app": name})
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
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
	if s.orch == nil {
		respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
		return
	}
	intent := orchestrator.NewRenameAppIntent(name, req.DisplayName)
	s.orch.Enqueue(intent)
	respondJSON(w, http.StatusAccepted, map[string]string{"intentId": intent.IntentID()})
}

func (s *Server) dropAppDatabase(appName string) error {
	appDatabases := map[string]string{
		"miniflux":  "miniflux",
		"immich":    "immich",
		"authentik": "authentik",
	}
	dbName, ok := appDatabases[appName]
	if !ok {
		return nil
	}
	s.logger.Info("dropping app database", "app", appName, "database", dbName)
	pgPassword := s.cfg.TemplateVars["postgresPassword"]
	if pgPassword == "" {
		return fmt.Errorf("postgres password not available for database drop")
	}
	pgURL := "postgres://apps:" + pgPassword + "@localhost:5432/bloud?sslmode=disable"
	conn, err := sql.Open("pgx", pgURL)
	if err != nil {
		return fmt.Errorf("failed to connect to postgres: %w", err)
	}
	defer conn.Close()
	_, err = conn.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
	if err != nil {
		return fmt.Errorf("failed to drop database %s: %w", dbName, err)
	}
	return nil
}

func (s *Server) handleAppIcon(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	iconPath := filepath.Join(s.cfg.AppsDir, name, "icon.png")
	if _, err := os.Stat(iconPath); os.IsNotExist(err) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, iconPath)
}

func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}
