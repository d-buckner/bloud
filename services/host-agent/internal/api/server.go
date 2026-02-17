package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/netutil"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/secrets"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// Server represents the HTTP server
type Server struct {
	cfg                  ServerConfig
	router               *chi.Mux
	db                   *sql.DB
	catalog              catalog.CacheInterface
	graph                catalog.AppGraphInterface
	appStore             store.AppStoreInterface
	userStore            *store.UserStore
	sessionStore         *store.SessionStore
	appHub               *AppEventHub
	orchestrator         orchestrator.AppOrchestrator
	reconciler           *orchestrator.Reconciler
	authentikClient      *authentik.Client
	authConfig           *AuthConfig
	knownRedirectURIs    sync.Map // tracks redirect URIs already registered in Authentik
	logger               *slog.Logger
	secrets              *secrets.Manager
}

// ServerConfig holds paths for server initialization
type ServerConfig struct {
	AppsDir     string
	ConfigDir   string
	DataDir     string // Path to bloud data directory (for Traefik config, etc.)
	FlakePath   string
	FlakeTarget string // Flake target for nixos-rebuild (e.g., "vm-dev", "vm-test")
	NixosPath   string
	Port        int
	// SSO configuration
	SSOHostSecret   string // Master secret for deriving client secrets (required for SSO)
	SSOBaseURL      string // Base URL for callbacks (e.g., "http://localhost:8080")
	SSOAuthentikURL string // Authentik external URL for browser OAuth discovery
	AuthentikToken  string // Authentik API token for SSO cleanup
	AuthentikPort   int    // Authentik API port (default 9001)
	// Redis for session storage
	RedisAddr string // Redis address (e.g., "localhost:6379")
	// Registry holds app configurators for reconciliation
	Registry configurator.RegistryInterface
}

// NewServer creates a new HTTP server instance
func NewServer(db *sql.DB, cfg ServerConfig, logger *slog.Logger) *Server {
	appStore := store.NewAppStore(db)
	userStore := store.NewUserStore(db)
	appHub := NewAppEventHub(appStore)

	// Wire up automatic broadcasts when app state changes
	appStore.SetOnChange(appHub.Broadcast)

	// Initialize secrets manager
	secretsPath := filepath.Join(cfg.DataDir, "secrets.json")
	secretsMgr := secrets.NewManager(secretsPath)
	if err := secretsMgr.Load(); err != nil {
		logger.Error("failed to load secrets", "error", err)
	}

	// Initialize Authentik client if token is available
	// Uses localhost:{port} for server-side API calls. SSOAuthentikURL is the
	// browser-facing external URL used for OAuth discovery/redirects.
	var authentikClient *authentik.Client
	if cfg.AuthentikToken != "" && cfg.AuthentikPort > 0 {
		internalURL := fmt.Sprintf("http://localhost:%d", cfg.AuthentikPort)
		authentikClient = authentik.NewClient(internalURL, cfg.AuthentikToken)
	}

	// Initialize session store if Redis is configured
	var sessionStore *store.SessionStore
	if cfg.RedisAddr != "" {
		// Retry Redis connection with backoff (Redis may still be starting)
		maxRetries := 10
		for i := 0; i < maxRetries; i++ {
			var err error
			sessionStore, err = store.NewSessionStore(cfg.RedisAddr)
			if err == nil {
				break
			}

			if i < maxRetries-1 {
				logger.Info("waiting for Redis...", "attempt", i+1, "error", err)
				time.Sleep(time.Duration(i+1) * time.Second)
				continue
			}

			logger.Warn("failed to connect to Redis after retries", "error", err)
		}
	}

	s := &Server{
		cfg:             cfg,
		router:          chi.NewRouter(),
		db:              db,
		catalog:         catalog.NewCache(db),
		appStore:        appStore,
		userStore:       userStore,
		sessionStore:    sessionStore,
		appHub:          appHub,
		authentikClient: authentikClient,
		logger:          logger,
		secrets:         secretsMgr,
	}

	// Initialize catalog and graph on startup
	s.refreshCatalog(s.cfg.AppsDir)

	// Initialize orchestrator (Podman client may not be available in tests)
	s.initOrchestrator(appStore)

	// Initialize reconciler if registry is provided
	if s.cfg.Registry != nil {
		s.reconciler = orchestrator.NewReconciler(
			s.cfg.Registry,
			appStore,
			s.catalog,
			s.cfg.DataDir,
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

	// Initialize authentication (OAuth2 app in Authentik)
	s.initAuth()

	s.setupMiddleware()
	s.setupRoutes()

	return s
}

// initOrchestrator sets up the orchestrator - prefers Nix, falls back to Podman
func (s *Server) initOrchestrator(appStore *store.AppStore) {
	// Use configured paths (set via env vars or defaults)
	configPath := filepath.Join(s.cfg.ConfigDir, "apps.nix")
	traefikConfigPath := filepath.Join(s.cfg.DataDir, "traefik", "dynamic", "apps-routes.yml")

	s.logger.Info("orchestrator paths",
		"flakePath", s.cfg.FlakePath,
		"nixosPath", s.cfg.NixosPath,
		"configPath", configPath,
		"traefikConfigPath", traefikConfigPath,
	)

	// SSO blueprints directory
	ssoBlueprintsDir := filepath.Join(s.cfg.DataDir, "authentik-blueprints")

	// Try to initialize Nix-based orchestrator (preferred)
	nixOrch := orchestrator.New(orchestrator.Config{
		Graph:             s.graph,
		CatalogCache:      s.catalog,
		AppStore:          appStore,
		Logger:            s.logger,
		ConfigPath:        configPath,
		TraefikConfigPath: traefikConfigPath,
		NixosPath:         s.cfg.NixosPath,
		FlakePath:         s.cfg.FlakePath,
		Hostname:          s.cfg.FlakeTarget,
		DataDir:           s.cfg.DataDir,
		// SSO configuration
		SSOHostSecret:    s.cfg.SSOHostSecret,
		SSOBaseURLs:      netutil.BuildBaseURLs(s.cfg.SSOBaseURL),
		SSOAuthentikURL:  s.cfg.SSOAuthentikURL,
		SSOBlueprintsDir: ssoBlueprintsDir,
		AuthentikToken:   s.cfg.AuthentikToken,
		Secrets:          s.secrets,
	})

	s.orchestrator = nixOrch
	s.logger.Info("Nix orchestrator initialized")

	// Reconcile database state with actual system state
	// This handles apps stuck in transitional states from server crashes
	nixOrch.ReconcileState()

	// Note: Configuration now runs via systemd hooks (ExecStartPre/ExecStartPost)
	// rather than background watchdogs. See podman-service.nix for hook setup.
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
	addr := fmt.Sprintf(":%d", s.cfg.Port)
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
	return nil
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

// tryInitAuth attempts to initialize authentication, refreshing the token if needed.
// This is called lazily on first auth request to handle the case where the Authentik
// configurator runs after server start and creates the api-token file.
func (s *Server) tryInitAuth() {
	// Already initialized
	if s.authConfig != nil {
		return
	}

	// Try to read fresh token from api-token file (created by Authentik configurator)
	tokenPath := filepath.Join(s.cfg.DataDir, "authentik", "api-token")
	if data, err := os.ReadFile(tokenPath); err == nil {
		token := string(data)
		if token != "" && token != s.cfg.AuthentikToken {
			s.logger.Info("found new Authentik API token from configurator", "path", tokenPath)
			s.cfg.AuthentikToken = token
			// Create new client with fresh token (internal URL for server-side API calls)
			if s.cfg.AuthentikPort > 0 {
				internalURL := fmt.Sprintf("http://localhost:%d", s.cfg.AuthentikPort)
				s.authentikClient = authentik.NewClient(internalURL, token)
			}
		}
	}

	// Now try to initialize
	s.initAuth()
}

// initAuth initializes authentication by ensuring the Bloud OAuth2 app exists in Authentik.
// Registers redirect URIs for the configured base URL plus all detected local IPs,
// so OAuth works regardless of which host the user accesses.
func (s *Server) initAuth() {
	// Skip if required components aren't available
	if s.authentikClient == nil || s.sessionStore == nil || s.cfg.SSOBaseURL == "" {
		s.logger.Info("authentication disabled (missing Authentik client, Redis, or base URL)")
		return
	}

	// Check if Authentik is available
	if !s.authentikClient.IsAvailable() {
		s.logger.Warn("Authentik not available, auth will be initialized on first request")
		return
	}

	// Generate a client secret from the host secret
	clientSecret := s.deriveClientSecret("bloud-oauth")

	// Build base URLs: configured host + detected local IPs.
	// Port is extracted from SSOBaseURL via net/url.Parse inside BuildBaseURLs.
	baseURLs := netutil.BuildBaseURLs(s.cfg.SSOBaseURL)
	s.logger.Info("registering OAuth redirect URIs", "baseURLs", baseURLs)

	// Ensure the Bloud OAuth2 app exists with redirect URIs for all base URLs
	oidcConfig, err := s.authentikClient.EnsureBloudOAuthApp(baseURLs, clientSecret)
	if err != nil {
		s.logger.Error("failed to ensure Bloud OAuth app", "error", err)
		return
	}

	s.authConfig = &AuthConfig{
		OIDCConfig: oidcConfig,
	}

	// Seed known redirect URIs so we skip lazy registration for these hosts
	for _, baseURL := range baseURLs {
		s.knownRedirectURIs.Store(baseURL+"/auth/callback", true)
	}

	s.logger.Info("authentication initialized", "clientID", oidcConfig.ClientID)
}

// deriveClientSecret generates a deterministic client secret from the host secret
func (s *Server) deriveClientSecret(appName string) string {
	// Use the secrets manager if available
	if s.secrets != nil {
		// Check if we already have a secret stored
		secret := s.secrets.GetAppSecret(appName, "oauthClientSecret")
		if secret != "" {
			return secret
		}

		// Generate a new secret based on the host secret
		if s.cfg.SSOHostSecret != "" {
			// Use HMAC-like derivation: hostSecret + appName
			// In production, consider using proper HKDF
			secret = s.cfg.SSOHostSecret[:32] + "-" + appName
			if err := s.secrets.SetAppSecret(appName, "oauthClientSecret", secret); err != nil {
				s.logger.Warn("failed to save client secret", "error", err)
			}
			return secret
		}
	}

	// Fallback: derive from host secret using simple concatenation
	if s.cfg.SSOHostSecret != "" {
		return s.cfg.SSOHostSecret[:32] + "-" + appName
	}

	return ""
}
