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
	// Auth routes at root level (for OAuth redirects)
	s.router.Get("/auth/login", s.handleLogin)
	s.router.Get("/auth/callback", s.handleCallback)
	s.router.Post("/auth/logout", s.handleLogout)

	// API routes
	s.router.Route("/api", func(r chi.Router) {
		// Public routes (no auth required)
		r.Get("/health", s.handleHealth)

		// Setup endpoints (public - used before first user exists)
		r.Route("/setup", func(r chi.Router) {
			r.Get("/status", s.handleSetupStatus)
			r.Post("/create-user", s.handleCreateUser)
		})

		// Auth info endpoint (public - returns user or 401)
		r.Get("/auth/me", s.handleGetCurrentUser)

		// Protected routes (require auth when session store is available)
		r.Group(func(r chi.Router) {
			if s.sessionStore != nil {
				r.Use(s.authMiddleware)
			}

			// Member-accessible routes (any authenticated user)
			r.Get("/apps", s.handleListApps)
			r.Get("/apps/installed", s.handleListInstalledApps)
			r.Get("/apps/events", s.handleAppEvents)
			r.Get("/apps/{name}/metadata", s.handleAppMetadata)
			r.Get("/apps/{name}/icon", s.handleAppIcon)
			r.Get("/apps/{name}/logs", s.handleAppLogs)
			r.Get("/system/status", s.handleSystemStatus)
			r.Get("/system/status/stream", s.handleSystemStatusStream)
			r.Get("/system/storage", s.handleStorage)
			r.Get("/system/developer", s.handleDeveloperGraph)
			r.Get("/user/layout", s.handleGetLayout)
			r.Put("/user/layout", s.handleSetLayout)

			// Admin-only routes
			r.Group(func(r chi.Router) {
				if s.sessionStore != nil {
					r.Use(s.adminMiddleware)
				}

				// App management
				r.Post("/apps/refresh-catalog", s.handleRefreshCatalog)
				r.Post("/apps/{name}/install", s.handleInstall)
				r.Post("/apps/{name}/uninstall", s.handleUninstall)
				r.Post("/apps/{name}/clear-data", s.handleClearData)
				r.Patch("/apps/{name}/rename", s.handleRename)

				// System admin
				r.Get("/system/rebuild/stream", s.handleRebuildStream)

				// Settings
				r.Get("/settings/tailnet", s.handleGetTailnet)
				r.Post("/settings/tailnet", s.handleSetTailnet)
				r.Delete("/settings/tailnet", s.handleDeleteTailnet)

				// Sharing
				r.Post("/sharing/invites", s.handleCreateInvite)
				r.Get("/sharing/shares", s.handleListShares)
				r.Delete("/sharing/shares/{id}", s.handleRevokeShare)
				r.Get("/sharing/guests", s.handleListGuests)
				r.Post("/sharing/guests", s.handleCreateGuest)
				r.Get("/sharing/community", s.handleCommunityGraph)
				r.Get("/sharing/remote-apps", s.handleListRemoteApps)
				r.Post("/sharing/remote-apps", s.handleAddRemoteApp)
				r.Delete("/sharing/remote-apps/{id}", s.handleDeleteRemoteApp)

				// User management
				r.Get("/users", s.handleListUsers)
				r.Post("/users", s.handleCreateManagedUser)
				r.Delete("/users/{username}", s.handleDeleteManagedUser)
				r.Put("/users/{username}/role", s.handleSetUserRole)
			})
		})

		// Any unmatched /api/** path returns JSON 404, never HTML
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			respondError(w, http.StatusNotFound, "not found")
		})
	})

	// Serve frontend static files
	s.setupFrontend()
}

// setupFrontend configures serving the SvelteKit frontend
func (s *Server) setupFrontend() {
	// Try to find the frontend build directory
	// Look in the source location (for development)
	buildDir := filepath.Join("web", "build")

	// Check if build directory exists
	if _, err := os.Stat(buildDir); os.IsNotExist(err) {
		// Frontend not built yet, serve fallback HTML
		s.logger.Warn("frontend build directory not found, serving fallback HTML", "path", buildDir)
		s.router.Get("/*", s.handleRoot)
		return
	}

	// Serve frontend files from filesystem with SPA fallback
	s.logger.Info("serving frontend from filesystem", "path", buildDir)
	s.router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		// Clean path and build full file path
		urlPath := r.URL.Path
		if urlPath == "/" {
			urlPath = "/index.html"
		}
		filePath := filepath.Join(buildDir, filepath.Clean(urlPath))

		// Check if file exists
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			// File doesn't exist - serve index.html for SPA routing
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFile(w, r, filepath.Join(buildDir, "index.html"))
			return
		}

		// Set cache headers based on path:
		// - index.html must never be cached — it references versioned assets,
		//   and stale copies cause broken loads after updates.
		// - Hashed immutable assets can be cached forever; the hash in the filename
		//   guarantees a new URL on every content change.
		switch {
		case urlPath == "/index.html":
			w.Header().Set("Cache-Control", "no-store")
		case strings.HasPrefix(urlPath, "/_app/immutable/"):
			w.Header().Set("Cache-Control", "public, immutable, max-age=31536000")
		}

		http.ServeFile(w, r, filePath)
	})
}

// handleHealth returns the health status of the service
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

// handleListApps returns the list of available user-facing apps from the catalog
// System/infrastructure apps are filtered out by default
func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.catalog.GetUserApps()
	if err != nil {
		s.logger.Error("failed to get apps from catalog", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get apps")
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"apps": apps,
	})
}

// handleRefreshCatalog reloads the app catalog from YAML files
func (s *Server) handleRefreshCatalog(w http.ResponseWriter, r *http.Request) {
	s.refreshCatalog(s.cfg.AppsDir)

	respondJSON(w, http.StatusOK, map[string]string{
		"status": "catalog refreshed",
	})
}

// installedAppResponse extends InstalledApp with catalog-derived fields
type installedAppResponse struct {
	*store.InstalledApp
	SSOLaunchPath string `json:"sso_launch_path,omitempty"`
}

// buildLaunchPaths returns a map of app name → SSO launch path from the catalog.
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

// enrichApps wraps store apps with catalog-derived fields (e.g. sso_launch_path).
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

// handleListInstalledApps returns the list of user-installed apps.
// System apps (traefik, postgres, redis, authentik) are filtered out since
// they are managed by the host agent, not installed by the user.
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

// handleAppMetadata returns the full catalog metadata for a single app
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

// handleSystemStatus returns system metrics
func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	stats, err := system.GetStats()
	if err != nil {
		s.logger.Error("failed to get system stats", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get system stats")
		return
	}

	respondJSON(w, http.StatusOK, stats)
}

// handleStorage returns detailed storage information
func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	storage, err := system.GetStorageStats()
	if err != nil {
		s.logger.Error("failed to get storage stats", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get storage stats")
		return
	}

	respondJSON(w, http.StatusOK, storage)
}

// handleRoot serves the dev dashboard when no frontend build is present
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(devDashboardHTML)
}

// handleInstall enqueues an install intent and returns 202 Accepted.
func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	if s.orch == nil {
		respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
		return
	}

	// Validate app exists in catalog
	if _, err := s.catalog.Get(name); err != nil {
		respondError(w, http.StatusNotFound, "app not found in catalog")
		return
	}

	intent := orchestrator.NewInstallAppIntent(name)
	s.orch.Enqueue(intent)

	respondJSON(w, http.StatusAccepted, map[string]string{
		"intentId": intent.IntentID(),
	})
}

// handleUninstall enqueues an uninstall intent and returns 202 Accepted.
func (s *Server) handleUninstall(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	if s.orch == nil {
		respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
		return
	}

	// Parse optional clearData from request body
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

	respondJSON(w, http.StatusAccepted, map[string]string{
		"intentId": intent.IntentID(),
	})
}

// handleClearData removes all data for an app (data directory and database).
// If the app is installed, it enqueues an uninstall intent with clearData=true.
// If not installed, it directly cleans up orphaned data.
func (s *Server) handleClearData(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	// Check if app exists in catalog
	_, err := s.catalog.Get(name)
	if err != nil {
		respondError(w, http.StatusNotFound, "app not found in catalog")
		return
	}

	// Check if app is installed
	app, _ := s.appStore.GetByCatalogID(name)
	if app != nil {
		// App is installed — enqueue uninstall with clearData=true
		if s.orch == nil {
			respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
			return
		}

		intent := orchestrator.NewUninstallAppIntent(name, true)
		s.orch.Enqueue(intent)

		respondJSON(w, http.StatusAccepted, map[string]string{
			"intentId": intent.IntentID(),
		})
		return
	}

	// App not installed — clean up orphaned data directly
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

	respondJSON(w, http.StatusOK, map[string]string{
		"status": "data cleared",
		"app":    name,
	})
}

// handleRename updates the display name of an installed app
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

	respondJSON(w, http.StatusAccepted, map[string]string{
		"intentId": intent.IntentID(),
	})
}

// dropAppDatabase drops the database for an app if it uses shared postgres
func (s *Server) dropAppDatabase(appName string) error {
	// Apps that use shared postgres and their database names
	// TODO: Move this to catalog metadata
	appDatabases := map[string]string{
		"miniflux":  "miniflux",
		"immich":    "immich",
		"authentik": "authentik", // Uses its own postgres, but include for completeness
	}

	dbName, ok := appDatabases[appName]
	if !ok {
		return nil // App doesn't use shared postgres
	}

	s.logger.Info("dropping app database", "app", appName, "database", dbName)

	// Connect to shared postgres to drop the app database.
	// s.db is SQLite (bloud state), so we open a fresh postgres connection.
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

// handleAppIcon serves the icon.png for an app
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

// Helper functions for JSON responses

func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{
		"error": message,
	})
}

// handleGetLayout returns the user's layout
func (s *Server) handleGetLayout(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		respondError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	elements, err := s.prefsStore.GetLayout(user.Username)
	if err != nil {
		s.logger.Error("failed to get layout", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get layout")
		return
	}

	if elements == nil {
		elements = []store.GridElement{}
	}

	respondJSON(w, http.StatusOK, elements)
}

// handleSetLayout updates the user's layout
func (s *Server) handleSetLayout(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		respondError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var elements []store.GridElement
	if err := json.NewDecoder(r.Body).Decode(&elements); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.prefsStore.SetLayout(user.Username, elements); err != nil {
		s.logger.Error("failed to set layout", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to save layout")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status": "saved",
	})
}
