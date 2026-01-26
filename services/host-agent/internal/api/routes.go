package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/system"
	"github.com/go-chi/chi/v5"
)

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

			// Apps endpoints
			r.Route("/apps", func(r chi.Router) {
				r.Get("/", s.handleListApps)
				r.Get("/installed", s.handleListInstalledApps)
				r.Get("/events", s.handleAppEvents)
				r.Post("/refresh-catalog", s.handleRefreshCatalog)

				// Plan endpoints (use graph)
				r.Get("/{name}/plan-install", s.handlePlanInstall)
				r.Get("/{name}/plan-remove", s.handlePlanRemove)

				// Metadata endpoint
				r.Get("/{name}/metadata", s.handleAppMetadata)

				// Action endpoints (use orchestrator)
				r.Post("/{name}/install", s.handleInstall)
				r.Post("/{name}/uninstall", s.handleUninstall)
				r.Post("/{name}/clear-data", s.handleClearData)
				r.Patch("/{name}/rename", s.handleRename)

				// Logs streaming
				r.Get("/{name}/logs", s.handleAppLogs)

				// Static assets
				r.Get("/{name}/icon", s.handleAppIcon)
			})

			// System endpoints
			r.Post("/system/rollback", s.handleRollback)
			r.Route("/system", func(r chi.Router) {
				r.Get("/status", s.handleSystemStatus)
				r.Get("/status/stream", s.handleSystemStatusStream)
				r.Get("/storage", s.handleStorage)
				r.Get("/versions", s.handleListGenerations)
				r.Get("/rebuild/stream", s.handleRebuildStream)
			})

			// User preferences endpoints
			r.Route("/user", func(r chi.Router) {
				r.Get("/layout", s.handleGetLayout)
				r.Put("/layout", s.handleSetLayout)
			})
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
			http.ServeFile(w, r, filepath.Join(buildDir, "index.html"))
			return
		}

		// File exists - serve it directly
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
	s.refreshCatalog(s.appsDir)

	respondJSON(w, http.StatusOK, map[string]string{
		"status": "catalog refreshed",
	})
}


// handleListInstalledApps returns the list of installed apps
// Uses the same data source as SSE for consistency
func (s *Server) handleListInstalledApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.appStore.GetAll()
	if err != nil {
		s.logger.Error("failed to get apps", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get apps")
		return
	}

	respondJSON(w, http.StatusOK, apps)
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

// handleRoot serves a simple welcome message
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
    <title>Bloud Host Agent</title>
</head>
<body>
    <h1>Bloud Host Agent</h1>
    <p>The host agent is running!</p>
    <p>API endpoints:</p>
    <ul>
        <li><a href="/api/health">/api/health</a> - Health check</li>
        <li><a href="/api/apps">/api/apps</a> - List available apps</li>
        <li><a href="/api/apps/installed">/api/apps/installed</a> - List installed apps</li>
        <li><a href="/api/system/status">/api/system/status</a> - System status</li>
    </ul>
</body>
</html>
	`))
}

// handlePlanInstall returns the installation plan for an app
func (s *Server) handlePlanInstall(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	if s.graph == nil {
		respondError(w, http.StatusServiceUnavailable, "catalog not loaded")
		return
	}

	plan, err := s.graph.PlanInstall(name)
	if err != nil {
		s.logger.Error("failed to plan install", "app", name, "error", err)
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, plan)
}

// handlePlanRemove returns the removal plan for an app
func (s *Server) handlePlanRemove(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	if s.graph == nil {
		respondError(w, http.StatusServiceUnavailable, "catalog not loaded")
		return
	}

	plan, err := s.graph.PlanRemove(name)
	if err != nil {
		s.logger.Error("failed to plan remove", "app", name, "error", err)
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, plan)
}

// handleInstall installs an app
func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	nixOrch, ok := s.orchestrator.(*orchestrator.Orchestrator)
	if !ok || nixOrch == nil {
		respondError(w, http.StatusServiceUnavailable, "orchestrator not available (podman not running?)")
		return
	}

	// Parse request body for choices
	var req struct {
		Choices map[string]string `json:"choices"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	// Use the queue to serialize concurrent install requests
	result, err := nixOrch.EnqueueInstall(r.Context(), orchestrator.InstallRequest{
		App:     name,
		Choices: req.Choices,
	})
	if err != nil {
		s.logger.Error("install failed", "app", name, "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if !result.IsSuccess() {
		respondJSON(w, http.StatusBadRequest, result)
		return
	}

	// Trigger reconciliation to configure dependent apps
	s.triggerReconcile()

	respondJSON(w, http.StatusOK, result)
}

// handleUninstall removes an app
func (s *Server) handleUninstall(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	nixOrch, ok := s.orchestrator.(*orchestrator.Orchestrator)
	if !ok || nixOrch == nil {
		respondError(w, http.StatusServiceUnavailable, "orchestrator not available (podman not running?)")
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

	// Use the queue to serialize concurrent uninstall requests
	result, err := nixOrch.EnqueueUninstall(r.Context(), orchestrator.UninstallRequest{
		App:       name,
		ClearData: req.ClearData,
	})
	if err != nil {
		s.logger.Error("uninstall failed", "app", name, "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if !result.IsSuccess() {
		respondJSON(w, http.StatusBadRequest, result)
		return
	}

	// Trigger reconciliation to update dependent apps
	s.triggerReconcile()

	respondJSON(w, http.StatusOK, result)
}

// handleClearData removes all data for an app (data directory and database)
// This is equivalent to calling uninstall with clearData=true
func (s *Server) handleClearData(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	// Check if app exists in catalog
	_, err := s.catalog.Get(name)
	if err != nil {
		respondError(w, http.StatusNotFound, "app not found in catalog")
		return
	}

	// Check if app is installed
	app, _ := s.appStore.GetByName(name)
	nixOrch, ok := s.orchestrator.(*orchestrator.Orchestrator)
	if app != nil && ok && nixOrch != nil {
		// App is installed - uninstall with clearData=true using the queue
		s.logger.Info("uninstalling app with data cleanup", "app", name)
		result, err := nixOrch.EnqueueUninstall(r.Context(), orchestrator.UninstallRequest{
			App:       name,
			ClearData: true,
		})
		if err != nil {
			s.logger.Error("uninstall failed during clear-data", "app", name, "error", err)
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !result.IsSuccess() {
			respondJSON(w, http.StatusBadRequest, result)
			return
		}
	} else {
		// App not installed - just clean up any orphaned data
		s.logger.Info("cleaning up orphaned app data", "app", name)
		appDataDir := filepath.Join(s.dataDir, name)
		if err := os.RemoveAll(appDataDir); err != nil {
			s.logger.Error("failed to remove app data directory", "app", name, "error", err)
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to remove data directory: %v", err))
			return
		}
		if err := s.dropAppDatabase(name); err != nil {
			s.logger.Warn("failed to drop app database", "app", name, "error", err)
		}
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

	if err := s.appStore.UpdateDisplayName(name, req.DisplayName); err != nil {
		s.logger.Error("failed to rename app", "app", name, "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":      "renamed",
		"app":         name,
		"displayName": req.DisplayName,
	})
}

// dropAppDatabase drops the database for an app if it uses shared postgres
func (s *Server) dropAppDatabase(appName string) error {
	// Apps that use shared postgres and their database names
	// TODO: Move this to catalog metadata
	appDatabases := map[string]string{
		"actual-budget": "actual_budget",
		"miniflux":      "miniflux",
		"authentik":     "authentik", // Uses its own postgres, but include for completeness
	}

	dbName, ok := appDatabases[appName]
	if !ok {
		return nil // App doesn't use shared postgres
	}

	s.logger.Info("dropping app database", "app", appName, "database", dbName)

	// Connect to postgres and drop the database
	// Note: We use the bloud database connection to run the DROP command
	_, err := s.db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
	if err != nil {
		return fmt.Errorf("failed to drop database %s: %w", dbName, err)
	}

	return nil
}

// handleAppIcon serves the icon.png for an app
func (s *Server) handleAppIcon(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	iconPath := filepath.Join(s.appsDir, name, "icon.png")

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

// handleRollback reverts to the previous NixOS generation
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	// Only Orchestrator supports rollback
	nixOrch, ok := s.orchestrator.(*orchestrator.Orchestrator)
	if !ok {
		respondError(w, http.StatusServiceUnavailable, "rollback only available with Nix orchestrator")
		return
	}

	s.logger.Info("rollback requested")

	result, err := nixOrch.Rollback(r.Context())
	if err != nil {
		s.logger.Error("rollback failed", "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":      result.Success,
		"output":       result.Output,
		"errorMessage": result.ErrorMessage,
		"changes":      result.Changes,
		"duration":     result.Duration.String(),
	})
}

// handleListGenerations lists NixOS system generations
func (s *Server) handleListGenerations(w http.ResponseWriter, r *http.Request) {
	// Run nixos-rebuild list-generations
	output, err := system.ListGenerations()
	if err != nil {
		s.logger.Error("failed to list generations", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to list generations")
		return
	}

	generations, err := system.ParseGenerations(output)
	if err != nil {
		s.logger.Error("failed to parse generations", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to parse generations")
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"generations": generations,
	})
}

// handleGetLayout returns the user's layout
func (s *Server) handleGetLayout(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		respondError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	elements, err := s.userStore.GetLayout(user.ID)
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

	if err := s.userStore.SetLayout(user.ID, elements); err != nil {
		s.logger.Error("failed to set layout", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to save layout")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status": "saved",
	})
}
