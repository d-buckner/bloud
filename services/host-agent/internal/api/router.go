package api

import (
	"context"
	"database/sql"
	"encoding/json"
	_ "embed"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/secrets"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sso"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
)

//go:embed dev_dashboard.html
var devDashboardHTML []byte

// routerOptions are optional overrides for NewRouter, used mainly in tests.
type routerOptions struct {
	catalog          catalog.CacheInterface
	appStore         store.AppStoreInterface
	positionStore    store.PositionStoreInterface
	prefsStore       store.PreferencesStoreInterface
	sessionStore     *store.SessionStore
	remoteAppStore   store.RemoteAppStoreInterface
	orch             interface{} // any orchestratorCaller implementation
	noOrchestrator   bool        // if true, skip creating a real orchestrator
	authConfig       *authConfigRef
}

// NewRouter builds a fully wired *chi.Mux with all domain modules and
// middleware. It is the single entry point for constructing the HTTP
// routing layer.
func NewRouter(
	db *sql.DB,
	cfg ServerConfig,
	logger *slog.Logger,
	opts ...func(*routerOptions),
) (*chi.Mux, *orchestrator.Orchestrator) {
	// ---- Defaults ----
	options := &routerOptions{}
	for _, opt := range opts {
		opt(options)
	}

	// ---- Dependencies ----

	appStore := options.appStore
	if appStore == nil {
		appStore = store.NewAppStore(db)
	}
	positionStore := options.positionStore
	if positionStore == nil {
		positionStore = store.NewPositionStore(db)
	}
	prefsStore := options.prefsStore
	if prefsStore == nil {
		prefsStore = store.NewPreferencesStore(db)
	}

	secretsPath := filepath.Join(cfg.DataDir, "secrets.json")
	secretsMgr := secrets.NewManager(secretsPath)
	if err := secretsMgr.Load(); err != nil {
		logger.Error("failed to load secrets", "error", err)
	}

	var authentikClient *authentik.Client
	if cfg.AuthentikToken != "" && cfg.AuthentikPort > 0 {
		internalURL := fmt.Sprintf("http://localhost:%d", cfg.AuthentikPort)
		authentikClient = authentik.NewClient(internalURL, cfg.AuthentikToken)
	}

	var sessionStore = options.sessionStore
	if sessionStore == nil && cfg.RedisAddr != "" {
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

	catalogCache := options.catalog
	if catalogCache == nil {
		catalogCache = catalog.NewMemoryCache()
		refreshCatalogHelper(catalogCache, logger, cfg.AppsDir)
	}

	// Orchestrator: use provided one if set, else create real (unless noOrchestrator is true).
	var ( 
		orchCaller  orchestratorCaller
		realOrch    *orchestrator.Orchestrator
	)
	if o, ok := options.orch.(orchestratorCaller); ok && o != nil {
		orchCaller = o
		if ro, isReal := o.(*orchestrator.Orchestrator); isReal {
			realOrch = ro
		}
	} else if !options.noOrchestrator {
		realOrch = initOrchestratorHelper(db, appStore, catalogCache, cfg, logger, tailnetStore, authentikClient)
		if realOrch != nil {
			orchCaller = realOrch
		}
	}

	authRef := options.authConfig
	if authRef == nil {
		authRef = newAuthConfigRef(nil)
	}
	authRef.SetEnsure(func() *AuthConfig {
		return initAuthHelper(authentikClient, sessionStore, cfg, logger)
	})
	authRef.Set(initAuthHelper(authentikClient, sessionStore, cfg, logger))

	launchPathsFn := func() map[string]string {
		paths := make(map[string]string)
		if catalogApps, err := catalogCache.GetAll(); err == nil {
			for _, ca := range catalogApps {
				if ca.SSO.LaunchPath != "" {
					paths[ca.CatalogID] = ca.SSO.LaunchPath
				}
			}
		}
		return paths
	}

	// ---- Create domain modules ----

	appsMod := NewAppsModule(catalogCache, appStore, orchCaller, logger)
	appsMod.SetAppsDir(cfg.AppsDir)

	authMod := NewAuthModule(authentikClient, authRef, prefsStore, sessionStore, logger)

	homeMod := NewHomeModule(positionStore, appStore, launchPathsFn, logger)

	logsMod := NewLogsModule(appStore, logger)

	remoteAppStore := options.remoteAppStore
	if remoteAppStore == nil {
		remoteAppStore = store.NewRemoteAppStore(db)
	}
	remoteAppsMod := NewRemoteAppsModule(remoteAppStore, catalogCache, orchCaller, logger)

	settingsMod := NewSettingsModule(tailnetStore, prefsStore, sessionStore, authentikClient, orchCaller, authRef, logger)

	gateway := sharing.NewGatewayManager(nil, nil, func() string { return "" }, sharing.DefaultGatewaySOCKSPort, cfg.TraefikPort, cfg.DataDir, logger)
	sharingMod := NewSharingModule(
		store.NewShareStore(db), store.NewGuestStore(db),
		appStore, catalogCache, nil,
		cfg.HostLabel, cfg.SSOHostSecret, logger,
	)

	systemMod := NewSystemModule(appStore, catalogCache, nil, gateway, tailnetStore, nil, logger)

	// ---- Wire middleware and routes ----

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173", "http://localhost:8080"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Public routes
	r.Get("/health", systemMod.HealthHandler())
	r.Get("/auth/login", authMod.LoginHandler())
	r.Get("/auth/callback", authMod.CallbackHandler())
	r.Post("/auth/logout", authMod.LogoutHandler())

	r.Route("/api", func(api chi.Router) {
		api.Get("/health", systemMod.HealthHandler())
		api.Get("/setup/status", settingsMod.SetupStatusHandler())
		api.Get("/auth/me", authMod.GetCurrentUserHandler())

		// System info (public, no auth required)
		NewSystemRouter(systemMod, api)

		// Authenticated routes
		api.Group(func(auth chi.Router) {
			auth.Use(authMiddlewareFn(sessionStore, logger))

			// User-accessible routes (registered directly)
			NewAppsRouter(appsMod, auth)
			NewHomeRouter(homeMod, auth)
			NewLogsRouter(logsMod, auth)

			// Admin-only routes
			auth.Group(func(admin chi.Router) {
				admin.Use(adminMiddlewareFn)

				// Admin routes (registered directly)
				admin.Post("/apps/refresh-catalog", appsMod.RefreshCatalogHandler())
				admin.Get("/system/rebuild/stream", rebuildStreamHandler())
				NewSettingsRouter(settingsMod, admin)
				NewSharingRouter(sharingMod, admin)
				NewRemoteAppsRouter(remoteAppsMod, admin)
			})
		})
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		respondError(w, http.StatusNotFound, "not found")
	})

	setupFrontendHelper(r, logger)
	return r, realOrch
}

// ---- Frontend ----

func setupFrontendHelper(r *chi.Mux, logger *slog.Logger) {
	buildDir := filepath.Join("web", "build")

	if _, err := os.Stat(buildDir); os.IsNotExist(err) {
		logger.Warn("frontend build directory not found, serving fallback HTML")
		r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			w.Write(devDashboardHTML)
		})
		return
	}

	logger.Info("serving frontend from filesystem", "path", buildDir)
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
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

// ---- Orchestrator initialization ----

func initOrchestratorHelper(
	db *sql.DB,
	appStore store.AppStoreInterface,
	catalogCache catalog.CacheInterface,
	cfg ServerConfig,
	logger *slog.Logger,
	tailnetStore *store.TailnetStore,
	authentikClient *authentik.Client,
) *orchestrator.Orchestrator {
	traefikConfigPath := filepath.Join(cfg.TraefikDynamicDir, "apps-routes.yml")
	logger.Info("orchestrator paths", "traefikConfigPath", traefikConfigPath)

	lifecycleGraph := graph.New(graph.NewMapRepository())

	client, err := podman.NewClient()
	if err != nil {
		logger.Warn("podman client unavailable for API", "error", err)
	}

	runtime := cfg.ContainerRuntime
	if runtime == nil {
		if client == nil {
			logger.Error("container runtime unavailable (no podman client)")
			return nil
		}
		runtime = containerruntime.NewPodmanRuntime(client)
	}

	var ssoProvisioner orchestrator.SSOProvisioner
	if authentikClient != nil {
		ssoProvisioner = authentikClient
	}

	if cfg.TSAuthKey != "" {
		active, _ := tailnetStore.GetActive()
		if active == nil {
			tailnetStore.Create(store.TailnetConnection{
				ID:      uuid.New().String(),
				Name:    "Default",
				Type:    "tailscale",
				AuthKey: cfg.TSAuthKey,
				Status:  "active",
			})
			logger.Info("migrated BLOUD_TS_AUTHKEY to tailnet_connections store")
		}
	}

	authKeyFn := func() string {
		conn, err := tailnetStore.GetActive()
		if err != nil || conn == nil {
			return ""
		}
		return conn.AuthKey
	}

	var exec sharing.ContainerExec
	if client != nil {
		exec = client
	}
	tailnetNode := sharing.NewTailnetNodeManager(runtime, exec, authKeyFn, cfg.TraefikPort, cfg.DataDir, logger)

	gateway := sharing.NewGatewayManager(runtime, exec, authKeyFn, sharing.DefaultGatewaySOCKSPort, cfg.TraefikPort, cfg.DataDir, logger)

	socksAddr := fmt.Sprintf("localhost:%d", sharing.DefaultGatewaySOCKSPort)
	remoteProxy := sharing.NewRemoteProxyManager(socksAddr, sharing.DefaultRemoteProxyBasePort, logger)

	var forwardDomainSSO orchestrator.ForwardDomainProvisioner
	if authentikClient != nil {
		forwardDomainSSO = authentikClient
	}

	// Build the catalog dependency graph (planner) used by install/uninstall
	// intents to resolve integrations and auto-install required providers.
	// This is the missing wiring that made installs no-op in production.
	catalogGraph, err := catalog.NewLoader(cfg.AppsDir).LoadGraph()
	if err != nil {
		logger.Error("failed to build catalog graph", "error", err)
	} else {
		logger.Info("catalog dependency graph built", "apps", len(catalogGraph.GetApps()))
	}

	orch := orchestrator.NewOrchestrator(
		lifecycleGraph,
		cfg.Registry,
		catalogCache,
		cfg.DataDir,
		logger,
		orchestrator.OrchestratorConfig{
			LDAPOutput:       cfg.LDAPOutput,
			Containers:       runtime,
			TemplateVars:     cfg.TemplateVars,
			AppStore:         appStore,
			CatalogGraph:     catalogGraph,
			TailnetStore:     tailnetStore,
			RemoteAppStore:   store.NewRemoteAppStore(db),
			TailnetNode:      tailnetNode,
			Gateway:          gateway,
			RemoteProxy:      remoteProxy,
			ProxyOutpost:     sharing.NewProxyOutpostManager(runtime, logger),
			ForwardDomainSSO: forwardDomainSSO,
			SSO:              ssoProvisioner,
			SSOBaseURL:       cfg.SSOBaseURL,
			TraefikGen:       traefikgen.NewGenerator(traefikConfigPath),
			ActiveTailnetID: func() string {
				conn, err := tailnetStore.GetActive()
				if err != nil || conn == nil {
					return ""
				}
				return conn.ID
			},
		},
	)
	logger.Info("lifecycle orchestrator initialized")
	go orch.Start(context.Background())
	return orch
}

// ---- Auth initialization ----

func initAuthHelper(
	authentikClient *authentik.Client,
	sessionStore *store.SessionStore,
	cfg ServerConfig,
	logger *slog.Logger,
) *AuthConfig {
	if authentikClient == nil || sessionStore == nil || cfg.SSOBaseURL == "" {
		logger.Info("authentication disabled (missing Authentik client, Redis, or base URL)")
		return nil
	}
	if !authentikClient.IsAvailable() {
		logger.Warn("Authentik not available, auth will be initialized on first request")
		return nil
	}

	clientSecret := deriveSecretHelper(cfg.SSOHostSecret, "bloud-oauth", 32)
	baseURLs := netutil.BuildBaseURLs(cfg.SSOBaseURL)
	logger.Info("registering OAuth redirect URIs", "baseURLs", baseURLs)

	oidcConfig, err := authentikClient.EnsureBloudOAuthApp(baseURLs, clientSecret)
	if err != nil {
		logger.Error("failed to ensure Bloud OAuth app", "error", err)
		return nil
	}
	logger.Info("authentication initialized", "clientID", oidcConfig.ClientID)
	return &AuthConfig{OIDCConfig: oidcConfig}
}

// ---- Shared helpers ----

func refreshCatalogHelper(cache catalog.CacheInterface, logger *slog.Logger, appsDir string) {
	logger.Info("refreshing app catalog", "apps_dir", appsDir)
	loader := catalog.NewLoader(appsDir)
	if err := cache.Refresh(loader); err != nil {
		logger.Error("failed to refresh catalog cache", "error", err)
	}
	logger.Info("catalog refreshed successfully")
}

func deriveSecretHelper(hostSecret, appName string, keyLen int) string {
	if hostSecret == "" {
		return ""
	}
	return sso.DeriveSecret(hostSecret, "oauth-client-secret:"+appName, keyLen)
}

func rebuildStreamHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Rebuild not supported (Nix runtime removed)", http.StatusGone)
	}
}

// ---- Middleware ----

func authMiddlewareFn(sessionStore *store.SessionStore, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isLocalRequest(r) {
				user := &store.User{Username: "_cli", Role: store.RoleAdmin}
				ctx := context.WithValue(r.Context(), userContextKey, user)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			cookie, err := r.Cookie(sessionCookieName)
			if err != nil || cookie.Value == "" {
				respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Not authenticated"})
				return
			}

			ctx := r.Context()
			session, err := sessionStore.Get(ctx, cookie.Value)
			if err != nil {
				logger.Error("failed to get session", "error", err)
				respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to validate session"})
				return
			}

			if session == nil {
				http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
				respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Session expired"})
				return
			}

			if time.Now().After(session.ExpiresAt) {
				sessionStore.Delete(ctx, session.ID)
				respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Session expired"})
				return
			}

			if session.Role == "" {
				sessionStore.Delete(ctx, session.ID)
				http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
				respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Session expired"})
				return
			}

			user := &store.User{Username: session.Username, Role: session.Role}
			ctx = context.WithValue(ctx, userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func adminMiddlewareFn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := getUserFromContext(r.Context())
		if user == nil || !user.IsAdmin() {
			respondJSON(w, http.StatusForbidden, map[string]string{"error": "Admin access required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- HTTP response helpers ----

func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}
