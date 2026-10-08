// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sso"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

//go:embed dev_dashboard.html
var devDashboardHTML []byte

// routerOptions are optional overrides for NewRouter, used mainly in tests.
type routerOptions struct {
	catalog       catalog.CacheInterface
	appStore      store.AppStoreInterface
	positionStore store.PositionStoreInterface
	prefsStore    store.PreferencesStoreInterface
	sessionStore  store.SessionStoreInterface
	externalApps  store.ExternalAppStoreInterface
	orch          any // any orchestratorCaller implementation
	authConfig    *AuthRef
}

// routerDeps bundles every store, client, and collaborator that NewRouter's
// modules need. All the "use the provided override, else construct from the
// DB" defaulting lives here so NewRouter stays focused on wiring modules and
// routes.
type routerDeps struct {
	appStore      store.AppStoreInterface
	externalApps  store.ExternalAppStoreInterface
	eventsBus     *eventbus.Bus
	positionStore store.PositionStoreInterface
	prefsStore    store.PreferencesStoreInterface
	sessionStore  store.SessionStoreInterface
	catalogCache  catalog.CacheInterface
	authentik     *authentik.Client
	authRef       *AuthRef
	orchCaller    orchestratorCaller
	realOrch      *orchestrator.Orchestrator
	secrets       configurator.AppSecretsProvider
	settingsStore store.SettingsStoreInterface
}

func buildRouterDeps(
	db *sql.DB,
	cfg ServerConfig,
	logger *slog.Logger,
	options *routerOptions,
) *routerDeps {
	d := &routerDeps{}
	buildStoreDeps(d, db, cfg, options, logger)
	buildAuthDeps(d, cfg, logger, options)
	return d
}

// buildStoreDeps resolves the read-side stores the API handlers use. Each one
// comes from the router options when the caller supplied it, and from the
// database otherwise, so a test can hand over a fake and production needs
// none. The instance-scoped credential store and the settings KV come from
// config rather than being constructed here, so the API reads exactly the
// stores the orchestrator writes.
func buildStoreDeps(d *routerDeps, db *sql.DB, cfg ServerConfig, options *routerOptions, logger *slog.Logger) {
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
	d.sessionStore = options.sessionStore
	if d.sessionStore == nil {
		d.sessionStore = store.NewSessionStore(db)
	}
	d.secrets = cfg.Secrets
	d.settingsStore = cfg.Settings

	d.externalApps = options.externalApps
	if d.externalApps == nil {
		d.externalApps = store.NewExternalAppStore(db)
	}
	// Launcher changes reshape the home payload, so they must resnapshot the
	// SSE stream exactly like an app-store change does.
	d.externalApps.SetOnChange(func() {
		d.eventsBus.Publish(eventbus.Event{Type: eventbus.TypeAppsChanged})
	})

	d.catalogCache = options.catalog
	if d.catalogCache == nil {
		d.catalogCache = catalog.NewMemoryCache()
		refreshCatalogHelper(d.catalogCache, logger, cfg.AppsDir)
	}
}

// buildAuthDeps resolves the identity and orchestrator references. The auth
// ref exists before the orchestrator because the orchestrator's OnHostsChanged
// hook re-ensures it after a host change. The orchestrator itself is supplied
// by the caller (main.go builds it in internal/wire) or by a test through the
// router options; the API cannot construct one. If nobody hands it over there
// is none, visibly, rather than a half-wired one conjured here.
func buildAuthDeps(d *routerDeps, cfg ServerConfig, logger *slog.Logger, options *routerOptions) {
	d.authentik = cfg.Authentik
	if d.authentik == nil {
		d.authentik = NewAuthentikClient(cfg.AuthentikPort, cfg.AuthentikToken, cfg.BaseDomain)
	}

	d.authRef = options.authConfig
	if d.authRef == nil {
		d.authRef = NewAuthRef(d.authentik, d.sessionStore, cfg, logger)
	}

	if o, ok := options.orch.(orchestratorCaller); ok && o != nil {
		d.orchCaller = o
		if ro, isReal := o.(*orchestrator.Orchestrator); isReal {
			d.realOrch = ro
		}
	} else if cfg.Orchestrator != nil {
		d.realOrch = cfg.Orchestrator
		d.orchCaller = d.realOrch
	}
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
	mods := buildRouterModules(db, cfg, logger, deps)

	// ---- Wire middleware and routes ----

	r := chi.NewRouter()
	applyRouterMiddleware(r)

	// Public routes
	pub := r.With(mods.requestTimeout)
	pub.Get("/health", mods.system.HealthHandler())
	pub.Get("/auth/login", mods.auth.LoginHandler())
	pub.Get("/auth/callback", mods.auth.CallbackHandler())
	pub.Post("/auth/logout", mods.auth.LogoutHandler())
	// The per-app waiting page. It is public because Traefik's error middleware
	// fetches it on behalf of a visitor to an app whose container is down,
	// before that visitor has a Bloud session, and because the page polls it
	// again to find out when the app is serving.
	pub.Get("/bloud-loading/{name}", mods.apps.AppLoadingHandler())

	mods.registerRoutes(r)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		respondError(w, http.StatusNotFound, "not found")
	})

	setupFrontendHelper(r.With(mods.requestTimeout), logger)
	return r, mods.realOrch
}

// routerModules is the set of domain modules the router registers, plus the
// two middleware chains they share.
type routerModules struct {
	apps     *appsModule
	auth     *authModule
	home     *homeModuleSimple
	events   *eventsModule
	settings *settingsModule
	ai       *aiSettingsModule
	external *externalAppsModule
	system   *systemModule
	// clientCreds serves the reveal surface for credentials a provider
	// declares under `clientAccess`. It is its own module rather than a route
	// on settings because its dependency set is the catalog plus the secret
	// store, and it is the only surface in the API whose whole job is to hand
	// out a secret under a policy.
	clientCreds *clientCredentialsModule

	requestTimeout func(http.Handler) http.Handler
	authMiddleware func(http.Handler) http.Handler
	realOrch       *orchestrator.Orchestrator
}

// buildRouterModules constructs every domain module from the resolved
// dependencies.
func buildRouterModules(db *sql.DB, cfg ServerConfig, logger *slog.Logger, deps *routerDeps) *routerModules {
	appsMod := NewAppsModule(deps.catalogCache, deps.appStore, deps.orchCaller, logger)
	appsMod.SetAppsDir(cfg.AppsDir)
	appsMod.SetDataDir(cfg.DataDir)
	// The pre-flight plans read the dependency graph the orchestrator owns,
	// so the API answers against the same installed set the next install
	// will be planned against rather than a second graph that can fall
	// behind. The nil check is on the concrete pointer: assigning a nil
	// *Orchestrator to the interface field would yield a non-nil interface
	// holding a nil pointer and defeat the handler's 503 guard.
	if deps.realOrch != nil {
		appsMod.SetPlanSource(deps.realOrch)
	}
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

	homeMod := NewHomeModule(deps.positionStore, deps.appStore, catalogLaunchPaths(deps.catalogCache), logger)
	// The home payload marks the apps the catalog says have no UI, so the
	// dashboard can leave them off the grid without learning the catalog.
	homeMod.SetHeadlessLookup(catalogHeadlessSet(deps.catalogCache))
	// And the apps that publish a clientAccess credential, so the right-click
	// menu offers the reveal surface only for those.
	homeMod.SetClientAccessLookup(catalogClientAccessSet(deps.catalogCache))
	homeMod.SetExternalApps(deps.externalApps)

	// The developer graph renders each app's containers with their live
	// lifecycle phase, so the system module needs the real orchestrator.
	// (Typed-nil would make a non-nil interface holding a nil pointer.)
	var systemOrch orchestratorStatusCaller
	if deps.realOrch != nil {
		systemOrch = deps.realOrch
	}
	systemMod := NewSystemModule(deps.appStore, deps.catalogCache, systemOrch, logger)
	// The health endpoint answers from the same check main.go runs, so a dead
	// intent loop cannot read healthy over HTTP.
	systemMod.SetHealthCheck(func() error {
		return checkSystemHealth(orchAsReconcilingLoop(deps.realOrch), db)
	})
	// The developer graph shows the AI Model node only while Settings -> AI
	// has an enabled upstream, so it reads the same store the settings module
	// writes.
	systemMod.SetAISettings(cfg.Settings)
	systemMod.SetExternalApps(deps.externalApps)
	// The container DNS diagnostic compares host and container resolution of
	// the configured public host; nil omits the endpoint.
	systemMod.SetDNSDiagnostics(cfg.DNSDiagnostics)

	return &routerModules{
		apps: appsMod,
		auth: NewAuthModule(deps.authentik, deps.authRef, deps.prefsStore, deps.sessionStore,
			logger, cfg.Port, cfg.Hosts),
		home: homeMod,
		// The SSE snapshot is the same payload the home route serves, so the
		// events module reads the home module rather than a second builder.
		events: NewEventsModule(deps.eventsBus, homeMod.GetLayout, logger),
		settings: NewSettingsModule(deps.prefsStore, deps.sessionStore, deps.authentik,
			deps.orchCaller, deps.authRef, cfg.Hosts, cfg.Settings, cfg.Port, logger),
		ai: &aiSettingsModule{
			settingsStore: cfg.Settings,
			secrets:       deps.secrets,
			appStore:      deps.appStore,
			catalog:       deps.catalogCache,
			orch:          deps.orchCaller,
			logger:        logger,
			externalApps:  deps.externalApps,
		},
		external: newExternalAppsModule(deps, logger),
		system:   systemMod,
		clientCreds: NewClientCredentialsModule(deps.catalogCache, deps.secrets,
			cfg.Settings, cfg.Hosts, deps.orchCaller, logger),
		realOrch:       deps.realOrch,
		authMiddleware: authMiddlewareFn(deps.sessionStore, logger, cfg.TrustedLocalNets, cfg.APIToken),
		// No global request timeout on the router: SSE streams must outlive a
		// single request. Non-streaming routes opt in explicitly via With().
		requestTimeout: middleware.Timeout(60 * time.Second),
	}
}

// catalogLaunchPaths indexes each app's SSO launch path by catalog id, so the
// home payload can hand the dashboard the right entry URL per app.
func catalogLaunchPaths(cache catalog.CacheInterface) func() map[string]string {
	return func() map[string]string {
		paths := make(map[string]string)
		if catalogApps, err := cache.GetAll(); err == nil {
			for _, ca := range catalogApps {
				if ca.SSO.LaunchPath != "" {
					paths[ca.CatalogID] = ca.SSO.LaunchPath
				}
			}
		}
		return paths
	}
}

// catalogHeadlessSet indexes the catalog ids of apps declared headless, so the
// home payload can carry the flag without the frontend knowing where it comes
// from. A missing catalog yields an empty set: nothing is hidden.
func catalogHeadlessSet(cache catalog.CacheInterface) func() map[string]bool {
	return func() map[string]bool {
		headless := make(map[string]bool)
		if cache == nil {
			return headless
		}
		if catalogApps, err := cache.GetAll(); err == nil {
			for _, ca := range catalogApps {
				if ca.Headless {
					headless[ca.CatalogID] = true
				}
			}
		}
		return headless
	}
}

// catalogClientAccessSet indexes the catalog ids of apps that publish a
// clientAccess credential, so the home payload can carry the flag and the
// dashboard's right-click menu can offer the reveal surface only where it is
// declared. A missing catalog yields an empty set: nothing is offered.
func catalogClientAccessSet(cache catalog.CacheInterface) func() map[string]bool {
	return func() map[string]bool {
		set := make(map[string]bool)
		if cache == nil {
			return set
		}
		if catalogApps, err := cache.GetAll(); err == nil {
			for _, ca := range catalogApps {
				if ca.HasClientAccess() {
					set[ca.CatalogID] = true
				}
			}
		}
		return set
	}
}

// applyRouterMiddleware installs the shared middleware stack.
func applyRouterMiddleware(r *chi.Mux) {
	r.Use(middleware.RequestID)
	// NOTE: no middleware.RealIP. It rewrites r.RemoteAddr from client-supplied
	// True-Client-IP / X-Real-IP / X-Forwarded-For, and RemoteAddr is the input
	// to the trusted-position check in authMiddlewareFn: trusting it made admin
	// reachable by anyone who could set a header. The client address is not used
	// for anything else in host-agent.
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
}

// registerRoutes mounts the /api subtree: the long-lived streams, the
// non-streaming public routes that first-run needs before any credential
// exists, and the authenticated and admin tiers above them.
func (m *routerModules) registerRoutes(r chi.Router) {
	r.Route("/api", func(api chi.Router) {
		// SSE streaming routes: authenticated, but exempt from the request
		// timeout: these are long-lived streams, not single requests.
		stream := api.With(m.authMiddleware)
		NewEventsRouter(m.events, stream)
		stream.Get("/system/status/stream", m.system.SystemStatusStreamHandler())

		// Non-streaming public routes. The setup pair must be reachable before
		// any credential exists: first-run has no user to authenticate as.
		npub := api.With(m.requestTimeout)
		npub.Get("/health", m.system.HealthHandler())
		NewSetupRouter(m.settings, npub)
		npub.Get("/auth/me", m.auth.GetCurrentUserHandler())

		// System info (public, no auth required)
		NewSystemRouter(m.system, npub)

		// Authenticated non-streaming routes
		auth := api.With(m.requestTimeout, m.authMiddleware)

		// Operator-only diagnostics: unlike the public system-info routes,
		// this reports resolver upstreams and container names.
		auth.Get("/system/diagnostics", m.system.DiagnosticsHandler())

		// User-accessible routes (registered directly)
		NewAppsRouter(m.apps, auth)
		NewHomeRouter(m.home, auth)

		// Admin-only routes
		admin := auth.With(adminMiddlewareFn)
		admin.Post("/apps/refresh-catalog", m.apps.RefreshCatalogHandler())
		admin.Get("/system/rebuild/stream", rebuildStreamHandler())
		NewSettingsRouter(m.settings, admin)
		RegisterAIRoutes(m.ai, admin)
		m.external.Register(admin)

		// Client credentials: reveal and rotate for credentials a provider
		// declares under `clientAccess`. Admin-only, because the reveal
		// endpoint returns a live credential, and the admin position is the
		// only thing standing between that and any authenticated user.
		m.clientCreds.Register(admin)
	})
}

// ---- Frontend ----

func setupFrontendHelper(r chi.Router, logger *slog.Logger) {
	buildDir := filepath.Join("web", "build")

	// Dev switch: serve the dashboard from a live `vite dev` server so a saved
	// file reaches the browser without a rebuild. Everything else about the
	// origin stays as it is in a real deployment, which is what keeps the OIDC
	// round trip working (see DevViteURLEnv).
	if proxy, configured, err := viteDevProxyFromEnv(logger); configured {
		if err != nil {
			logger.Error("ignoring unusable vite dev proxy target", "error", err)
			r.Get("/*", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				http.Error(w, viteProxyErrorBody(err), http.StatusServiceUnavailable)
			})
			return
		}
		logger.Info("proxying frontend to the vite dev server", "env", DevViteURLEnv)
		r.Get("/*", proxy.ServeHTTP)
		return
	}

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
		baseURLs = append([]string{cfg.SSOBaseURL}, netutil.LANBaseURLs(cfg.TraefikPort)...)
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
//  1. The API token, honored only from a trusted position (loopback or
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

func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}
