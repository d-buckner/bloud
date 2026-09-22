// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
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
	catalog        catalog.CacheInterface
	appStore       store.AppStoreInterface
	positionStore  store.PositionStoreInterface
	prefsStore     store.PreferencesStoreInterface
	sessionStore   store.SessionStoreInterface
	remoteAppStore store.RemoteAppStoreInterface
	orch           interface{} // any orchestratorCaller implementation
	noOrchestrator bool        // if true, skip creating a real orchestrator
	authConfig     *authConfigRef
}

// routerDeps bundles every store, client, and collaborator that NewRouter's
// modules need. All the "use the provided override, else construct from the
// DB" defaulting lives here so NewRouter stays focused on wiring modules and
// routes.
type routerDeps struct {
	appStore       store.AppStoreInterface
	eventsBus      *eventbus.Bus
	positionStore  store.PositionStoreInterface
	prefsStore     store.PreferencesStoreInterface
	sessionStore   store.SessionStoreInterface
	tailnetStore   *store.TailnetStore
	remoteAppStore store.RemoteAppStoreInterface
	catalogCache   catalog.CacheInterface
	authentik      *authentik.Client
	authRef        *authConfigRef
	orchCaller     orchestratorCaller
	realOrch       *orchestrator.Orchestrator
}

func buildRouterDeps(
	db *sql.DB,
	cfg ServerConfig,
	logger *slog.Logger,
	options *routerOptions,
) *routerDeps {
	d := &routerDeps{}

	d.appStore = options.appStore
	if d.appStore == nil {
		d.appStore = store.NewAppStore(db)
	}

	// Event bus: broadcasts app-store changes and orchestrator events to the
	// SSE stream (/api/apps/events). The bus only reads state; the
	// orchestrator remains the single writer.
	d.eventsBus = cfg.EventsBus
	if d.eventsBus == nil {
		d.eventsBus = eventbus.New()
	}
	d.appStore.SetOnChange(func() {
		d.eventsBus.Publish(eventbus.Event{Type: eventbus.TypeAppsChanged})
	})
	d.positionStore = options.positionStore
	if d.positionStore == nil {
		d.positionStore = store.NewPositionStore(db)
	}
	d.prefsStore = options.prefsStore
	if d.prefsStore == nil {
		d.prefsStore = store.NewPreferencesStore(db)
	}

	if cfg.AuthentikToken != "" && cfg.AuthentikPort > 0 {
		internalURL := fmt.Sprintf("http://localhost:%d", cfg.AuthentikPort)
		d.authentik = authentik.NewClient(internalURL, cfg.AuthentikToken).
			WithUserEmailDomain(cfg.BaseDomain)
	}

	d.sessionStore = options.sessionStore
	if d.sessionStore == nil {
		d.sessionStore = store.NewSessionStore(db)
	}

	d.tailnetStore = store.NewTailnetStore(db)

	d.catalogCache = options.catalog
	if d.catalogCache == nil {
		d.catalogCache = catalog.NewMemoryCache()
		refreshCatalogHelper(d.catalogCache, logger, cfg.AppsDir)
	}

	d.remoteAppStore = options.remoteAppStore
	if d.remoteAppStore == nil {
		d.remoteAppStore = store.NewRemoteAppStore(db)
	}

	// Auth config ref: created before the orchestrator because the
	// orchestrator's OnHostsChanged hook re-ensures it after a host change.
	d.authRef = options.authConfig
	if d.authRef == nil {
		d.authRef = newAuthConfigRef(nil)
	}
	d.authRef.SetEnsure(func() *AuthConfig {
		return initAuthHelper(context.Background(), d.authentik, d.sessionStore, cfg, logger)
	})
	d.authRef.Set(initAuthHelper(context.Background(), d.authentik, d.sessionStore, cfg, logger))

	// Orchestrator: use provided one if set, else create real (unless noOrchestrator is true).
	if o, ok := options.orch.(orchestratorCaller); ok && o != nil {
		d.orchCaller = o
		if ro, isReal := o.(*orchestrator.Orchestrator); isReal {
			d.realOrch = ro
		}
	} else if !options.noOrchestrator {
		d.realOrch = initOrchestratorHelper(db, d.appStore, d.catalogCache, cfg, logger, d.tailnetStore, d.authentik, d.eventsBus, func() { d.authRef.Ensure() })
		if d.realOrch != nil {
			d.orchCaller = d.realOrch
		}
	}

	return d
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
	deps := buildRouterDeps(db, cfg, logger, options)
	appStore := deps.appStore
	eventsBus := deps.eventsBus
	positionStore := deps.positionStore
	prefsStore := deps.prefsStore
	sessionStore := deps.sessionStore
	tailnetStore := deps.tailnetStore
	catalogCache := deps.catalogCache
	authentikClient := deps.authentik
	authRef := deps.authRef
	orchCaller := deps.orchCaller
	realOrch := deps.realOrch

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
	// Catalog size fallback: resolve undeclared estimates from local images.
	if sizeClient, err := podman.NewClient(); err == nil {
		appsMod.SetImageSizeResolver(func(ctx context.Context, image string) (int64, bool) {
			size, found, err := sizeClient.ImageSize(ctx, image)
			if err != nil {
				return 0, false
			}
			return size, found
		})
	}

	authMod := NewAuthModule(authentikClient, authRef, prefsStore, sessionStore, logger, cfg.Port, cfg.Hosts)

	homeMod := NewHomeModule(positionStore, appStore, launchPathsFn, logger)
	eventsMod := NewEventsModule(eventsBus, homeMod.GetLayout, logger)

	logsMod := NewLogsModule(appStore, logger)

	remoteAppStore := deps.remoteAppStore
	remoteAppsMod := NewRemoteAppsModule(remoteAppStore, catalogCache, orchCaller, logger)

	settingsMod := NewSettingsModule(tailnetStore, prefsStore, sessionStore, authentikClient, orchCaller, authRef, cfg.Hosts, cfg.HostStore, logger)

	gateway := sharing.NewGatewayManager(nil, nil, func() string { return "" }, sharing.DefaultGatewaySOCKSPort, cfg.TraefikPort, cfg.DataDir, logger)
	sharingMod := NewSharingModule(
		store.NewShareStore(db), store.NewGuestStore(db),
		appStore, catalogCache, nil,
		cfg.HostLabel, cfg.SSOHostSecret, logger,
	)

	// The developer graph renders each app's containers with their live
	// lifecycle phase, so the system module needs the real orchestrator.
	// (Typed-nil would make a non-nil interface holding a nil pointer.)
	var systemOrch orchestratorStatusCaller
	if deps.realOrch != nil {
		systemOrch = deps.realOrch
	}
	systemMod := NewSystemModule(appStore, catalogCache, nil, gateway, tailnetStore, systemOrch, logger)
	// The health endpoint answers from the same check main.go runs, so a dead
	// intent loop cannot read healthy over HTTP.
	systemMod.SetHealthCheck(func() error {
		return checkSystemHealth(orchAsReconcilingLoop(realOrch), db)
	})

	// ---- Wire middleware and routes ----

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// NOTE: no middleware.RealIP. It rewrites r.RemoteAddr from client-supplied
	// True-Client-IP / X-Real-IP / X-Forwarded-For, and RemoteAddr is the input
	// to the trusted-position check below: trusting it made admin reachable by
	// anyone who could set a header (see authMiddlewareFn). The client address
	// is not used for anything else in host-agent.
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173", "http://localhost:8080"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	// NOTE: no global request timeout here: SSE streams (below) must
	// outlive a single request. Non-streaming routes opt into the timeout
	// explicitly via With(requestTimeout).
	requestTimeout := middleware.Timeout(60 * time.Second)
	authMiddleware := authMiddlewareFn(sessionStore, logger, cfg.TrustedLocalNets, cfg.APIToken)

	// Public routes
	pub := r.With(requestTimeout)
	pub.Get("/health", systemMod.HealthHandler())
	pub.Get("/auth/login", authMod.LoginHandler())
	pub.Get("/auth/callback", authMod.CallbackHandler())
	pub.Post("/auth/logout", authMod.LogoutHandler())

	r.Route("/api", func(api chi.Router) {
		// SSE streaming routes: authenticated, but exempt from the request
		// timeout: these are long-lived streams, not single requests.
		stream := api.With(authMiddleware)
		NewEventsRouter(eventsMod, stream)
		stream.Get("/apps/{name}/logs", logsMod.StreamLogsHandler())
		stream.Get("/system/status/stream", logsMod.SystemStatusStreamHandler())

		// Non-streaming public routes. The setup pair must be reachable before
		// any credential exists: first-run has no user to authenticate as.
		npub := api.With(requestTimeout)
		npub.Get("/health", systemMod.HealthHandler())
		NewSetupRouter(settingsMod, npub)
		npub.Get("/auth/me", authMod.GetCurrentUserHandler())

		// System info (public, no auth required)
		NewSystemRouter(systemMod, npub)

		// Authenticated non-streaming routes
		auth := api.With(requestTimeout, authMiddleware)

		// User-accessible routes (registered directly)
		NewAppsRouter(appsMod, auth)
		NewHomeRouter(homeMod, auth)

		// Admin-only routes
		admin := auth.With(adminMiddlewareFn)
		admin.Post("/apps/refresh-catalog", appsMod.RefreshCatalogHandler())
		admin.Get("/system/rebuild/stream", rebuildStreamHandler())
		NewSettingsRouter(settingsMod, admin)
		NewSharingRouter(sharingMod, admin)
		NewRemoteAppsRouter(remoteAppsMod, admin)
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		respondError(w, http.StatusNotFound, "not found")
	})

	setupFrontendHelper(r.With(requestTimeout), logger)
	return r, realOrch
}

// ---- Frontend ----

func setupFrontendHelper(r chi.Router, logger *slog.Logger) {
	buildDir := filepath.Join("web", "build")

	if _, err := os.Stat(buildDir); os.IsNotExist(err) {
		logger.Warn("frontend build directory not found, serving fallback HTML")
		r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(devDashboardHTML)
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
	eventsBus *eventbus.Bus,
	onHostsChanged func(),
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
			if err := tailnetStore.Create(store.TailnetConnection{
				ID:      uuid.New().String(),
				Name:    "Default",
				Type:    "tailscale",
				AuthKey: cfg.TSAuthKey,
				Status:  "active",
			}); err != nil {
				logger.Error("failed to migrate BLOUD_TS_AUTHKEY to tailnet_connections store", "error", err)
			} else {
				logger.Info("migrated BLOUD_TS_AUTHKEY to tailnet_connections store")
			}
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
			Secrets:          cfg.Secrets,
			AppStore:         appStore,
			Operations:       store.NewOperationStore(db),
			Events:           eventsBus,
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
			SSOHostSecret:    cfg.SSOHostSecret,
			SSOAuthentikURL:  cfg.SSOAuthentikURL,
			SSOIssuerURL:     cfg.SSOIssuerURL,
			TraefikGen:       traefikgen.NewGenerator(traefikConfigPath),
			ActiveTailnetID: func() string {
				conn, err := tailnetStore.GetActive()
				if err != nil || conn == nil {
					return ""
				}
				return conn.ID
			},
			Hosts:          cfg.Hosts,
			HostStore:      cfg.HostStore,
			OnHostsChanged: onHostsChanged,
		},
	)
	logger.Info("lifecycle orchestrator initialized")
	go orch.Start(context.Background())
	return orch
}

// ---- Auth initialization ----

func initAuthHelper(
	ctx context.Context,
	authentikClient *authentik.Client,
	sessionStore store.SessionStoreInterface,
	cfg ServerConfig,
	logger *slog.Logger,
) *AuthConfig {
	if authentikClient == nil || sessionStore == nil {
		logger.Info("authentication disabled (missing Authentik client or session store)")
		return nil
	}
	// Base URLs come from the live host set when configured, so redirect
	// URIs follow host changes made in the UI. Falls back to the legacy
	// single SSO base URL env value.
	var baseURLs []string
	if cfg.Hosts != nil {
		baseURLs = cfg.Hosts.Get().AllBaseURLs()
	} else if cfg.SSOBaseURL != "" {
		baseURLs = netutil.BuildBaseURLs(cfg.SSOBaseURL)
	}
	if len(baseURLs) == 0 {
		logger.Info("authentication disabled (no SSO base URLs configured)")
		return nil
	}
	if !authentikClient.IsAvailable(ctx) {
		logger.Warn("Authentik not available, auth will be initialized on first request")
		return nil
	}

	clientSecret := deriveSecretHelper(cfg.SSOHostSecret, "bloud-oauth", 32)
	logger.Info("registering OAuth redirect URIs", "baseURLs", baseURLs)

	oidcConfig, err := authentikClient.EnsureBloudOAuthApp(ctx, baseURLs, clientSecret)
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

// authMiddlewareFn authenticates a request by one of two credentials:
//
//  1. The API token, honoured only from a trusted position (loopback or
//     TrustedLocalNets). This is the CLI/automation credential and yields
//     RoleAdmin.
//  2. A session cookie, which carries its own role.
//
// Position is a *scope*, never a credential. Before PR 4 the trusted position
// alone granted admin, and because the position was derived from
// client-controlled forwarding headers (chi's RealIP reads True-Client-IP,
// X-Real-IP, X-Forwarded-For) any client could claim loopback and become
// admin. Two consequences are load-bearing here:
//
//   - The token check must not short-circuit the session path. Every browser
//     request arrives through Traefik, whose backend connection is loopback,
//     so a position-first implementation would force the dashboard through
//     the token path and lock users out.
//   - An empty configured token disables the position path entirely (fail
//     closed) rather than authenticating everything.
func authMiddlewareFn(sessionStore store.SessionStoreInterface, logger *slog.Logger, trustedNets []string, apiToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tokenGrantsAdmin(r, trustedNets, apiToken) {
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

			session, err := sessionStore.Get(cookie.Value)
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
				_ = sessionStore.Delete(session.ID)
				respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Session expired"})
				return
			}

			if session.Role == "" {
				_ = sessionStore.Delete(session.ID)
				http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
				respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Session expired"})
				return
			}

			user := &store.User{Username: session.Username, Role: session.Role}
			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// tokenGrantsAdmin reports whether the request presents the API token from a
// trusted position. Both halves are required: the token is the credential, the
// position only bounds where the credential is accepted.
func tokenGrantsAdmin(r *http.Request, trustedNets []string, apiToken string) bool {
	if apiToken == "" || !isLocalRequest(r, trustedNets) {
		return false
	}
	presented := bearerToken(r)
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(apiToken)) == 1
}

// bearerToken extracts the credential from an "Authorization: Bearer <token>"
// header, returning "" when the header is absent or uses another scheme.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
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
	_ = json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}
