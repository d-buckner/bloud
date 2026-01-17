package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// Server represents the HTTP server
type Server struct {
	router         *chi.Mux
	db             *sql.DB
	catalog        catalog.CacheInterface
	graph          catalog.AppGraphInterface
	appStore       store.AppStoreInterface
	appHub         *AppEventHub
	orchestrator   orchestrator.AppOrchestrator
	reconciler     *orchestrator.Reconciler
	appsDir        string
	nixConfigDir   string
	dataDir        string
	flakePath      string
	flakeTarget    string
	nixosPath      string
	port            int
	ssoHostSecret   string
	ssoBaseURL      string
	ssoAuthentikURL string
	authentikToken  string
	registry       configurator.RegistryInterface
	logger         *slog.Logger
}

// ServerConfig holds paths for server initialization
type ServerConfig struct {
	AppsDir      string
	ConfigDir string
	DataDir      string // Path to bloud data directory (for Traefik config, etc.)
	FlakePath    string
	FlakeTarget  string // Flake target for nixos-rebuild (e.g., "vm-dev", "vm-test")
	NixosPath    string
	Port         int
	// SSO configuration
	SSOHostSecret   string // Master secret for deriving client secrets (required for SSO)
	SSOBaseURL      string // Base URL for callbacks (e.g., "http://localhost:8080")
	SSOAuthentikURL string // Authentik URL for discovery (e.g., "http://auth.localhost:8080")
	AuthentikToken  string // Authentik API token for SSO cleanup
	// Registry holds app configurators for reconciliation
	Registry configurator.RegistryInterface
}

// NewServer creates a new HTTP server instance
func NewServer(db *sql.DB, cfg ServerConfig, logger *slog.Logger) *Server {
	appStore := store.NewAppStore(db)
	appHub := NewAppEventHub(appStore)

	// Wire up automatic broadcasts when app state changes
	appStore.SetOnChange(appHub.Broadcast)

	s := &Server{
		router:         chi.NewRouter(),
		db:             db,
		catalog:        catalog.NewCache(db),
		appStore:       appStore,
		appHub:         appHub,
		appsDir:        cfg.AppsDir,
		nixConfigDir:   cfg.ConfigDir,
		dataDir:        cfg.DataDir,
		flakePath:      cfg.FlakePath,
		flakeTarget:    cfg.FlakeTarget,
		nixosPath:      cfg.NixosPath,
		port:            cfg.Port,
		ssoHostSecret:   cfg.SSOHostSecret,
		ssoBaseURL:      cfg.SSOBaseURL,
		ssoAuthentikURL: cfg.SSOAuthentikURL,
		authentikToken:  cfg.AuthentikToken,
		registry:       cfg.Registry,
		logger:         logger,
	}

	// Initialize catalog and graph on startup
	s.refreshCatalog(cfg.AppsDir)

	// Initialize orchestrator (Podman client may not be available in tests)
	s.initOrchestrator(appStore)

	// Initialize reconciler if registry is provided
	if cfg.Registry != nil {
		s.reconciler = orchestrator.NewReconciler(
			cfg.Registry,
			appStore,
			s.catalog,
			cfg.DataDir,
			logger,
			orchestrator.DefaultReconcileConfig(),
		)
	}

	// Regenerate Traefik routes on startup to ensure they're in sync
	if s.orchestrator != nil {
		if err := s.orchestrator.RegenerateRoutes(); err != nil {
			logger.Warn("failed to regenerate Traefik routes on startup", "error", err)
		}
	}

	s.setupMiddleware()
	s.setupRoutes()

	return s
}

// initOrchestrator sets up the orchestrator - prefers Nix, falls back to Podman
func (s *Server) initOrchestrator(appStore *store.AppStore) {
	// Use configured paths (set via env vars or defaults)
	configPath := filepath.Join(s.nixConfigDir, "apps.nix")
	traefikConfigPath := filepath.Join(s.dataDir, "traefik", "dynamic", "apps-routes.yml")

	s.logger.Info("orchestrator paths",
		"flakePath", s.flakePath,
		"nixosPath", s.nixosPath,
		"configPath", configPath,
		"traefikConfigPath", traefikConfigPath,
	)

	// SSO blueprints directory
	ssoBlueprintsDir := filepath.Join(s.dataDir, "authentik-blueprints")

	// Try to initialize Nix-based orchestrator (preferred)
	nixOrch := orchestrator.New(orchestrator.Config{
		Graph:             s.graph,
		CatalogCache:      s.catalog,
		AppStore:          appStore,
		Logger:            s.logger,
		ConfigPath:        configPath,
		TraefikConfigPath: traefikConfigPath,
		NixosPath:         s.nixosPath,
		FlakePath:         s.flakePath,
		Hostname:          s.flakeTarget,
		DataDir:           s.dataDir,
		// SSO configuration
		SSOHostSecret:    s.ssoHostSecret,
		SSOBaseURL:       s.ssoBaseURL,
		SSOAuthentikURL:  s.ssoAuthentikURL,
		SSOBlueprintsDir: ssoBlueprintsDir,
		AuthentikToken:   s.authentikToken,
	})

	s.orchestrator = nixOrch
	s.logger.Info("Nix orchestrator initialized")

	// Reconcile database state with actual system state
	// This handles apps stuck in transitional states from server crashes
	nixOrch.ReconcileState()

	// Recheck health of any apps that were in failed/error state
	// This recovers from stale failure states after server restart
	nixOrch.RecheckFailedApps()

	// Start background watchdog to detect and recover stuck states
	nixOrch.StartStateWatchdog(orchestrator.DefaultWatchdogConfig())

	// Note: For development/testing without NixOS, we could fall back to Podman orchestrator
	// But for production, we always use Nix for robustness
}

// refreshCatalog loads apps from YAML files and updates the cache and graph
func (s *Server) refreshCatalog(appsDir string) {
	s.logger.Info("refreshing app catalog", "apps_dir", appsDir)

	loader := catalog.NewLoader(appsDir)

	// Refresh the legacy catalog cache
	if err := s.catalog.Refresh(loader); err != nil {
		s.logger.Error("failed to refresh catalog cache", "error", err)
	}

	// Load the graph for integration planning
	graph, err := loader.LoadGraph()
	if err != nil {
		s.logger.Error("failed to load app graph", "error", err)
		return
	}
	s.graph = graph

	// Sync installed state from database to graph
	if err := s.syncInstalledState(); err != nil {
		s.logger.Error("failed to sync installed state", "error", err)
	}

	s.logger.Info("catalog refreshed successfully", "app_count", len(s.graph.GetApps()))
}

// syncInstalledState loads installed apps from DB and updates the graph
func (s *Server) syncInstalledState() error {
	if s.graph == nil {
		return nil
	}

	names, err := s.appStore.GetInstalledNames()
	if err != nil {
		return err
	}

	s.graph.SetInstalled(names)
	s.logger.Info("synced installed state", "installed_count", len(names))

	// Reconcile health status for apps stuck in "starting"
	go s.reconcileAppHealth()

	return nil
}

// reconcileAppHealth checks apps stuck in "starting" status and updates based on actual health
func (s *Server) reconcileAppHealth() {
	apps, err := s.appStore.GetAll()
	if err != nil {
		s.logger.Error("failed to get apps for health reconciliation", "error", err)
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for _, app := range apps {
		// Re-check apps that are starting or in error state (they may have recovered)
		if app.Status != "starting" && app.Status != "error" {
			continue
		}

		s.logger.Info("reconciling health for app", "app", app.Name, "status", app.Status)

		// Get health check config from catalog
		catalogApp, err := s.catalog.Get(app.Name)
		if err != nil || catalogApp.HealthCheck.Path == "" {
			// No health check configured, assume running
			s.appStore.UpdateStatus(app.Name, "running")
			continue
		}

		// Check health endpoint
		url := fmt.Sprintf("http://localhost:%d%s", catalogApp.Port, catalogApp.HealthCheck.Path)
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			// Accept 2xx, 3xx, and auth errors (401/403) as healthy
			// Auth errors mean the service is running but requires authentication
			if resp.StatusCode < 500 {
				s.logger.Info("app health check passed", "app", app.Name, "status", resp.StatusCode)
				s.appStore.UpdateStatus(app.Name, "running")
				continue
			}
		}

		// Health check failed - service not responding or 5xx error
		s.logger.Warn("app health check failed, marking as error", "app", app.Name, "error", err)
		s.appStore.UpdateStatus(app.Name, "error")
	}
}

// setupMiddleware configures the middleware stack
func (s *Server) setupMiddleware() {
	// Request logging
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)

	// Timeouts
	s.router.Use(middleware.Timeout(60 * time.Second))

	// CORS configuration
	s.router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173", "http://localhost:8080"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
}

// Start starts the HTTP server
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	s.logger.Info("starting HTTP server", "addr", addr)

	server := &http.Server{
		Addr:        addr,
		Handler:     s.router,
		ReadTimeout: 15 * time.Second,
		IdleTimeout: 60 * time.Second,
	}

	return server.ListenAndServe()
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down HTTP server")
	s.StopReconciler()
	return nil
}

// StartReconciler starts the reconciliation watchdog
func (s *Server) StartReconciler(ctx context.Context) {
	if s.reconciler != nil {
		s.reconciler.StartWatchdog(ctx)
	}
}

// StopReconciler stops the reconciliation watchdog
func (s *Server) StopReconciler() {
	if s.reconciler != nil {
		s.reconciler.StopWatchdog()
	}
}

// triggerReconcile runs reconciliation in the background.
// Called after successful install/uninstall to reconfigure dependent apps.
func (s *Server) triggerReconcile() {
	if s.reconciler == nil {
		return
	}
	go func() {
		if err := s.reconciler.Reconcile(context.Background()); err != nil {
			s.logger.Warn("background reconciliation failed", "error", err)
		}
	}()
}
