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

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/secrets"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
)

// Server represents the HTTP server
type Server struct {
	cfg               ServerConfig
	router            *chi.Mux
	db                *sql.DB
	catalog           catalog.CacheInterface
	graph             catalog.AppGraphInterface
	appStore          store.AppStoreInterface
	prefsStore        *store.PreferencesStore
	sessionStore      *store.SessionStore
	appHub            *AppEventHub
	orch              *orchestrator.Orchestrator
	guestStore        store.GuestStoreInterface
	shareStore        store.ShareStoreInterface
	remoteAppStore    store.RemoteAppStoreInterface
	tailnetStore      store.TailnetStoreInterface
	tailnetNode       sharing.TailnetNodeManagerInterface
	gateway           sharing.GatewayManagerInterface
	remoteProxy       *sharing.RemoteProxyManager
	authentikClient   *authentik.Client
	podmanClient      *podman.Client
	authConfig        *AuthConfig
	knownRedirectURIs sync.Map // tracks redirect URIs already registered in Authentik
	logger            *slog.Logger
	secrets           *secrets.Manager
}

// ServerConfig holds paths for server initialization
type ServerConfig struct {
	RuntimeMode string
	AppsDir     string
	DataDir           string // Path to bloud data directory
	TraefikDynamicDir string // Path to Traefik dynamic config directory (contains apps-routes.yml)
	BaseDomain        string // Base domain for subdomain routing (e.g., "localhost")
	TraefikPort       int    // Traefik entrypoint port (default 8080)
	Port              int
	// SSO configuration
	SSOHostSecret   string // Master secret for deriving client secrets (required for SSO)
	SSOBaseURL      string // Base URL for callbacks (e.g., "http://localhost:8080")
	SSOAuthentikURL string // Authentik external URL for browser OAuth discovery
	AuthentikToken  string // Authentik API token for SSO cleanup
	AuthentikPort   int    // Authentik API port (default 9001)
	// Tailscale auth key for sharing tailnet nodes (empty = sharing disabled)
	TSAuthKey string
	// HostLabel is the display name for this host in invite tokens
	HostLabel string
	// Redis for session storage
	RedisAddr string // Redis address (e.g., "localhost:6379")
	// LDAP configuration
	LDAPOutput *configurator.LDAPOutput
	// Registry holds app configurators for reconciliation
	Registry configurator.RegistryInterface
	// ContainerRuntime optionally injects the portable container backend.
	ContainerRuntime containerruntime.Runtime
	// TemplateVars are extra template variables for container spec rendering (e.g. postgresPassword).
	TemplateVars map[string]string
}

// NewServer creates a new HTTP server instance
func NewServer(db *sql.DB, cfg ServerConfig, logger *slog.Logger) *Server {
	appStore := store.NewAppStore(db)
	prefsStore := store.NewPreferencesStore(db)
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

	tailnetStore := store.NewTailnetStore(db)

	s := &Server{
		cfg:             cfg,
		router:          chi.NewRouter(),
		db:              db,
		catalog:         catalog.NewMemoryCache(),
		appStore:        appStore,
		prefsStore:      prefsStore,
		sessionStore:    sessionStore,
		appHub:          appHub,
		guestStore:      store.NewGuestStore(db),
		shareStore:      store.NewShareStore(db),
		remoteAppStore:  store.NewRemoteAppStore(db),
		tailnetStore:    tailnetStore,
		authentikClient: authentikClient,
		logger:          logger,
		secrets:         secretsMgr,
	}

	// Initialize catalog and graph on startup
	s.refreshCatalog(s.cfg.AppsDir)

	// Initialize the selected runtime orchestrator.
	s.initOrchestrator(appStore)

	// Initialize authentication (OAuth2 app in Authentik)
	s.initAuth()

	s.setupMiddleware()
	s.setupRoutes()

	return s
}

// initOrchestrator sets up the portable runtime orchestrator.
func (s *Server) initOrchestrator(appStore *store.AppStore) {
	traefikConfigPath := filepath.Join(s.cfg.TraefikDynamicDir, "apps-routes.yml")
	s.logger.Info("orchestrator paths", "traefikConfigPath", traefikConfigPath)

	// Create the lifecycle graph and the Orchestrator that drives it.
	// MapRepository is used so the graph is rebuilt fresh on every startup —
	// convergeFromStores always populates nodes and targets from the app store,
	// so persistence across restarts is not needed and avoids stale-state issues.
	lifecycleGraph := graph.New(graph.NewMapRepository())
	orch := orchestrator.NewOrchestrator(
		lifecycleGraph,
		s.cfg.Registry,
		s.catalog,
		s.cfg.DataDir,
		s.logger,
		orchestrator.OrchestratorConfig{LDAPOutput: s.cfg.LDAPOutput},
	)

	if s.cfg.RuntimeMode == "portable" {
		// Always create a podman client for the API (developer endpoint, etc.)
		client, err := podman.NewClient()
		if err != nil {
			s.logger.Warn("podman client unavailable for API", "error", err)
		} else {
			s.podmanClient = client
		}

		runtime := s.cfg.ContainerRuntime
		if runtime == nil {
			if client == nil {
				s.logger.Error("portable container runtime unavailable (no podman client)")
				return
			}
			runtime = containerruntime.NewPodmanRuntime(client)
		}

		var ssoProvisioner orchestrator.SSOProvisioner
		if s.authentikClient != nil {
			ssoProvisioner = s.authentikClient
		}

		// Migrate BLOUD_TS_AUTHKEY env var to tailnet_connections table.
		if s.cfg.TSAuthKey != "" {
			active, _ := s.tailnetStore.GetActive()
			if active == nil {
				s.tailnetStore.Create(store.TailnetConnection{
					ID:      uuid.New().String(),
					Name:    "Default",
					Type:    "tailscale",
					AuthKey: s.cfg.TSAuthKey,
					Status:  "active",
				})
				s.logger.Info("migrated BLOUD_TS_AUTHKEY to tailnet_connections store")
			}
		}

		// Build authKeyFn that reads the active tailnet connection from the store.
		authKeyFn := func() string {
			conn, err := s.tailnetStore.GetActive()
			if err != nil || conn == nil {
				return ""
			}
			return conn.AuthKey
		}

		// Always create the TailnetNodeManager — authKeyFn reads the active
		// connection from the store at creation time, so adding a tailnet
		// connection via Settings takes effect without restart.
		// EnsureRunning returns an error (non-fatal) when authKeyFn() == "".
		var exec sharing.ContainerExec
		if client != nil {
			exec = client
		}
		tailnetNode := sharing.NewTailnetNodeManager(runtime, exec, authKeyFn, s.cfg.TraefikPort, s.cfg.DataDir, s.logger)
		s.tailnetNode = tailnetNode
		s.logger.Info("tailnet node manager initialized")

		// Create gateway manager for remote app proxying.
		gateway := sharing.NewGatewayManager(runtime, exec, authKeyFn, sharing.DefaultGatewaySOCKSPort, s.cfg.TraefikPort, s.cfg.DataDir, s.logger)
		s.gateway = gateway

		// Create remote proxy manager.
		socksAddr := fmt.Sprintf("localhost:%d", sharing.DefaultGatewaySOCKSPort)
		remoteProxy := sharing.NewRemoteProxyManager(socksAddr, sharing.DefaultRemoteProxyBasePort, s.logger)
		s.remoteProxy = remoteProxy

		orch.WithContainerRuntime(runtime, s.cfg.TemplateVars)

		// Coalescing signal channel: multiple rapid EventTargetUpdated events collapse
		// into a single Reconcile pass. The channel is buffered to avoid blocking the
		// event emitter.
		orchSignal := make(chan struct{}, 1)
		lifecycleGraph.On(graph.EventTargetUpdated, func(node graph.Node) {
			s.logger.Info("graph target updated, signalling reconcile", "app", node.ID, "target", node.TargetStatus)
			select {
			case orchSignal <- struct{}{}:
			default:
				s.logger.Info("reconcile signal coalesced (pass already pending)", "app", node.ID)
			}
		})
		go func() {
			for range orchSignal {
				s.logger.Info("reconcile triggered by graph target update")
				if err := orch.Reconcile(context.Background()); err != nil {
					s.logger.Error("orchestrator reconcile failed", "error", err)
				}
			}
		}()
		s.logger.Info("lifecycle orchestrator initialized")

		// Build forward-domain SSO provisioner (nil when Authentik not installed).
		var forwardDomainSSO orchestrator.ForwardDomainProvisioner
		if s.authentikClient != nil {
			forwardDomainSSO = s.authentikClient
		}

		// Create proxy outpost manager for tailnet forward-auth.
		proxyOutpost := sharing.NewProxyOutpostManager(runtime, s.logger)

		// Wire converge dependencies into the orchestrator.
		orch.WithConvergeConfig(orchestrator.ConvergeConfig{
			AppStore:         appStore,
			CatalogGraph:     s.graph,
			TailnetStore:     s.tailnetStore,
			RemoteAppStore:   s.remoteAppStore,
			TailnetNode:      tailnetNode,
			Gateway:          gateway,
			RemoteProxy:      remoteProxy,
			ProxyOutpost:     proxyOutpost,
			ForwardDomainSSO: forwardDomainSSO,
			SSO:              ssoProvisioner,
			SSOBaseURL:       s.cfg.SSOBaseURL,
			TraefikGen:       traefikgen.NewGenerator(traefikConfigPath),
			ActiveTailnetID: func() string {
				conn, err := s.tailnetStore.GetActive()
				if err != nil || conn == nil {
					return ""
				}
				return conn.ID
			},
		})

		s.orch = orch
		go orch.Start(context.Background())

		// Trigger an initial convergence pass once everything is wired up.
		// convergeFromStores will sync container state, set graph targets,
		// converge tailnet, and regenerate routes.
		go s.orch.Enqueue(orchestrator.NewConvergeIntent())
		return
	}

	// Non-portable mode: start the orchestrator in stub mode (no appStore →
	// converge is a no-op) so Enqueue calls still work without panicking.
	s.orch = orch
	go orch.Start(context.Background())

	s.logger.Warn("unknown runtime mode, orchestrator not initialized", "mode", s.cfg.RuntimeMode)
}

// refreshCatalog loads apps from YAML files and updates the cache and graph
func (s *Server) refreshCatalog(appsDir string) {
	s.logger.Info("refreshing app catalog", "apps_dir", appsDir)

	loader := catalog.NewLoader(appsDir)

	// Refresh the catalog cache
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

	names, err := s.appStore.GetInstalledCatalogIDs()
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

		s.logger.Info("reconciling health for app", "app", app.CatalogID, "status", app.Status)

		// Get health check config from catalog
		catalogApp, err := s.catalog.Get(app.CatalogID)
		if err != nil || catalogApp.HealthCheck.Path == "" {
			// No health check configured, assume running
			s.appStore.UpdateStatus(app.CatalogID, "running")
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
				s.logger.Info("app health check passed", "app", app.CatalogID, "status", resp.StatusCode)
				s.appStore.UpdateStatus(app.CatalogID, "running")
				continue
			}
		}

		// Health check failed - service not responding or 5xx error
		s.logger.Warn("app health check failed, marking as error", "app", app.CatalogID, "error", err)
		s.appStore.UpdateStatus(app.CatalogID, "error")
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

	if s.orch != nil {
		s.orch.Stop()
	}

	return nil
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
