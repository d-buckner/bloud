// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/api"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/appconfig"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/config"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/db"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/system"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/wire"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// systemConvergenceTimeout bounds how long the control plane waits for the
// first convergence pass before giving up on startup.
//
// It is derived from the per-node PostStart budget rather than picked
// independently, because the two are coupled: the budget is
// appclient.MaxWaitBudget, so a gate set at or below it would let a single
// node spending its full budget trip the startup timeout and take the whole
// control plane down. That is a worse failure than the per-node ERROR the
// budget exists to produce. The margin is for the other levels that have to
// converge in the same window.
const systemConvergenceTimeout = appclient.MaxWaitBudget + 5*time.Minute

func main() {
	// Check for subcommands
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init-secrets":
			os.Exit(runInitSecrets(os.Args[2:]))
		}
	}

	// Default: run the server
	runServer()
}

func runServer() {
	logger := setupLogging()
	logger.Info("starting Bloud host agent")

	stack := buildAgentStack(logger)
	defer func() { _ = stack.database.Close() }()

	fastGated := stack.applyDevFastGate()

	// The intent loop runs under its own cancellable context so the shutdown
	// path stops it deliberately instead of letting it outlive the process.
	orchCtx, stopOrchestrator := context.WithCancel(context.Background())
	defer stopOrchestrator()
	go stack.orch.Start(orchCtx)

	server := api.NewServer(stack.database, stack.serverCfg, logger)
	startAgentListener(server, logger)

	// The listener is open and the intent loop is running; now wait for the
	// first convergence pass to say the system is usable.
	if fastGated {
		watchConvergenceBehindFastGate(server, logger)
	} else {
		waitForSystemConvergence(server, logger)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startBackgroundCollectors(ctx, stack.database, logger)

	<-ctx.Done()
	logger.Info("shutdown signal received")
	shutdownAgentServer(server, logger)
}

// agentStack is everything startup builds once. The same store pointers go to
// both the orchestrator and the API: the catalog refresh endpoint has to
// refresh the cache the orchestrator reads, and app-status writes have to fire
// the change hook the SSE stream listens to.
type agentStack struct {
	cfg        *config.Config
	database   *sql.DB
	client     *podman.Client
	runtime    containerruntime.Runtime
	hosts      *hostset.State
	settings   *store.SettingsStore
	vars       *configurator.TemplateVars
	registry   *configurator.Registry
	eventsBus  *eventbus.Bus
	app        *store.AppStore
	catalog    *catalog.MemoryCache
	tailnet    *store.TailnetStore
	remoteApp  *store.RemoteAppStore
	authClient *authentik.Client
	authRef    *api.AuthRef
	orch       *orchestrator.Orchestrator
	serverCfg  api.ServerConfig
}

// buildAgentStack brings up every long-lived piece of the agent: config,
// database, container runtime, host state, configurator registry, stores, the
// auth handle, and the orchestrator.
func buildAgentStack(logger *slog.Logger) *agentStack {
	st := &agentStack{
		cfg:       loadAgentConfig(logger),
		eventsBus: eventbus.New(),
	}
	st.database = openAgentDatabase(st.cfg, logger)

	// Podman client and runtime: the client is shared by the runtime, the
	// configurator deps, and the warm-stack check the dev fast gate runs.
	st.client = newAgentPodmanClient(logger)
	st.runtime = containerruntime.NewPodmanRuntime(st.client)

	// Host state: the effective set of hostnames (built-ins + admin custom
	// hosts from the database, with legacy env fallbacks). Shared between the
	// configurators, the orchestrator, and the API so UI host changes apply
	// without a restart.
	address, settingsStore := resolveAddress(st.database, st.cfg, logger)
	st.settings = settingsStore
	st.hosts = hostset.NewState(address)

	// One template-var store, handed to both the orchestrator and the authentik
	// configurator, so the LDAP token PostStart records is visible to the
	// orchestrator without either of them holding a mutable map.
	st.vars = buildTemplateVars(st.cfg)
	st.registry = buildConfiguratorRegistry(st.cfg, logger, st.hosts, st.client, st.vars)

	openAgentStoresInto(st, logger)

	// The auth ref exists before the orchestrator because the orchestrator's
	// host-change hook re-ensures the dashboard OAuth app. The hook crosses
	// into the builder as a plain func(), so wire never imports the API
	// package.
	st.authClient = api.NewAuthentikClient(st.cfg.AuthentikPort, st.cfg.AuthentikToken, st.cfg.BaseDomain)
	st.serverCfg = agentServerConfig(st)
	st.authRef = api.NewAuthRef(st.authClient, store.NewSessionStore(st.database), st.serverCfg, logger)
	st.serverCfg.Authentik = st.authClient
	st.serverCfg.AuthRef = st.authRef

	// One builder owns the orchestrator wiring. Nothing else constructs one.
	out, err := wire.Build(st.wireInput())
	if err != nil {
		logger.Error("failed to build the orchestrator", "error", err)
		os.Exit(1)
	}
	st.orch = out.Orchestrator
	st.serverCfg.Orchestrator = out.Orchestrator
	return st
}

// openAgentStoresInto builds the four shared stores and loads the catalog.
func openAgentStoresInto(st *agentStack, logger *slog.Logger) {
	st.app = store.NewAppStore(st.database)
	st.catalog = catalog.NewMemoryCache()
	if err := st.catalog.Refresh(catalog.NewLoader(st.cfg.AppsDir)); err != nil {
		logger.Error("failed to load the app catalog", "apps_dir", st.cfg.AppsDir, "error", err)
		os.Exit(1)
	}
	st.tailnet = store.NewTailnetStore(st.database)
	st.remoteApp = store.NewRemoteAppStore(st.database)
}

// setupLogging installs the JSON logger the whole process writes through.
func setupLogging() *slog.Logger {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)
	return logger
}

// loadAgentConfig loads configuration, or ends the process. There is no
// fallback configuration: a missing required value is a startup failure, not a
// silently defaulted one.
func loadAgentConfig(logger *slog.Logger) *config.Config {
	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}
	logger.Info("loaded configuration",
		"port", cfg.Port,
		"data_dir", cfg.DataDir,
		"apps_dir", cfg.AppsDir,
	)
	return cfg
}

// openAgentDatabase creates the data directory and opens SQLite. The database
// is instant to bring up: there is no postgres dependency at this layer.
func openAgentDatabase(cfg *config.Config, logger *slog.Logger) *sql.DB {
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		logger.Error("failed to create data directory", "error", err)
		os.Exit(1)
	}
	database, err := db.InitDB(cfg.DataDir)
	if err != nil {
		logger.Error("failed to initialize database", "error", err)
		os.Exit(1)
	}
	logger.Info("database initialized successfully")
	return database
}

// newAgentPodmanClient opens the podman connection the runtime, the
// configurator deps, and the warm-stack check all share.
func newAgentPodmanClient(logger *slog.Logger) *podman.Client {
	client, err := podman.NewClient()
	if err != nil {
		logger.Error("failed to create podman client", "error", err)
		os.Exit(1)
	}
	return client
}

// buildConfiguratorRegistry wires the app configurator dependencies. System
// configurators are registered eagerly; app configurators self-register
// factories (apps/<name>/registration.go) and are instantiated lazily on
// first lookup.
//
// vars is the same store the orchestrator renders container specs from, not a
// fresh one. The authentik configurator writes the LDAP outpost token into it
// during PostStart and the orchestrator reads {{authentikLdapToken}} when it
// builds the outpost's container spec two seconds later; a second store would
// take the write and the read to different objects, leave the placeholder
// unresolved, and the outpost would come up permanently unable to fetch its
// configuration. See the note on configurator.TemplateVars.
//
// restartContainer forces a running container to stop and start again, so its
// process re-execs and re-reads on-disk config. Configurators use this where
// the app's own in-app restart is unreliable under a container init (Home
// Assistant). Stop grace lets the app shut down cleanly before SIGKILL.
func buildConfiguratorRegistry(cfg *config.Config, logger *slog.Logger, hosts *hostset.State, client *podman.Client, vars *configurator.TemplateVars) *configurator.Registry {
	restartContainer := func(ctx context.Context, name string) error {
		if err := client.StopContainer(ctx, name, 30); err != nil {
			return err
		}
		return client.StartContainer(ctx, name)
	}
	registry := configurator.NewRegistry(logger,
		appconfig.AppDeps(cfg, logger, hosts, restartContainer, client.ExecWithEnv))
	appconfig.RegisterSystem(cfg, containerruntime.NewPodmanRuntime(client), vars)
	return registry
}

// agentServerConfig assembles the API server config from the stack.
func agentServerConfig(st *agentStack) api.ServerConfig {
	cfg := st.cfg
	return api.ServerConfig{
		RefreshAuthentikToken: func() string { return cfg.ReadAuthentikToken(slog.Default()) },
		AppsDir:               cfg.AppsDir,
		DataDir:               cfg.DataDir,
		TraefikDynamicDir:     cfg.TraefikDynamicDir,
		BaseDomain:            cfg.BaseDomain,
		TraefikPort:           cfg.TraefikPort,
		Port:                  cfg.Port,
		SSOHostSecret:         cfg.SSOHostSecret,
		SSOBaseURL:            cfg.SSOBaseURL,
		SSOAuthentikURL:       cfg.SSOAuthentikURL,
		SSOIssuerURL:          cfg.SSOIssuerURL,
		AuthentikToken:        cfg.AuthentikToken,
		AuthentikPort:         cfg.AuthentikPort,
		TSAuthKey:             cfg.TSAuthKey,
		HostLabel:             cfg.HostLabel,
		TrustedLocalNets:      cfg.TrustedLocalNets,
		APIToken:              cfg.APIToken,
		Hosts:                 st.hosts,
		EventsBus:             st.eventsBus,
		Settings:              st.settings,
		LDAPOutput:            cfg.LDAPOutput(),
		Registry:              st.registry,
		TemplateVars:          st.vars,
		Secrets:               cfg.Secrets,
		AppStore:              st.app,
		CatalogCache:          st.catalog,
		TailnetStore:          st.tailnet,
		RemoteAppStore:        st.remoteApp,
	}
}

// wireInput maps the stack onto the orchestrator builder's input.
func (st *agentStack) wireInput() wire.Input {
	cfg := st.cfg
	return wire.Input{
		Logger:            slog.Default(),
		DB:                st.database,
		AppStore:          st.app,
		CatalogCache:      st.catalog,
		Registry:          st.registry,
		ContainerRuntime:  st.runtime,
		EventsBus:         st.eventsBus,
		Authentik:         st.authClient,
		TailnetStore:      st.tailnet,
		Settings:          st.settings,
		Hosts:             st.hosts,
		AppsDir:           cfg.AppsDir,
		DataDir:           cfg.DataDir,
		TraefikDynamicDir: cfg.TraefikDynamicDir,
		TraefikPort:       cfg.TraefikPort,
		ReconcileInterval: cfg.ReconcileInterval,
		TSAuthKey:         cfg.TSAuthKey,
		LDAPOutput:        cfg.LDAPOutput(),
		TemplateVars:      st.vars,
		SSOBaseURL:        cfg.SSOBaseURL,
		SSOHostSecret:     cfg.SSOHostSecret,
		SSOAuthentikURL:   cfg.SSOAuthentikURL,
		SSOIssuerURL:      cfg.SSOIssuerURL,
		Secrets:           cfg.Secrets,
		OnHostsChanged:    st.authRef.Ensure,
	}
}

// applyDevFastGate decides whether the API opens before the first convergence
// pass. On a stack that is already up, it opens now instead of behind a full
// convergence pass. It falls back to the ordinary wait whenever the conditions
// are not met, so a cold boot is unchanged. See DevFastGateEnv for what this
// trades away and why it is opt-in.
func (st *agentStack) applyDevFastGate() bool {
	logger := slog.Default()
	if !devFastGateEnabled(os.Getenv) {
		return false
	}
	apps, catalogErr := st.catalog.GetAll()
	report, warmErr := checkWarmStack(context.Background(), st.client, systemContainerNames(apps))
	if catalogErr != nil {
		warmErr = catalogErr
	}
	decision := decideDevFastGate(report, warmErr, authReady(st.authRef))
	if !decision.Open {
		logger.Info("dev fast gate: not opening early, waiting for full convergence", "why", decision.Reason)
		return false
	}
	st.serverCfg.Gate = closedGate()
	logger.Info("dev fast gate: opening the API now, first convergence pass runs in the background", "why", decision.Reason)
	return true
}

// startAgentListener opens the listener before convergence. Until the
// orchestrator reports ready the server answers with a static loading page
// (and 503 for /api), so a browser hitting Traefik during bootstrap sees the
// page instead of Traefik's 502.
func startAgentListener(server *api.Server, logger *slog.Logger) {
	go func() {
		if err := server.Start(); err != nil {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()
}

// startBackgroundCollectors runs the periodic housekeeping that has no owner
// in the request path.
func startBackgroundCollectors(ctx context.Context, database *sql.DB, logger *slog.Logger) {
	// Background system stats collector.
	system.StartStatsCollector(ctx)
	// Background purge of expired sessions (SQLite has no TTL).
	store.StartSessionPurger(ctx, store.NewSessionStore(database), logger)
}

// shutdownAgentServer drains in-flight requests on the shutdown signal.
func shutdownAgentServer(server *api.Server, logger *slog.Logger) {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("server stopped gracefully")
}

// resolveAddress computes the effective address from the stored public URL and
// the legacy env fallbacks, and returns it with the settings store the caller
// wires into the API. Resolution failures degrade to the built-in default
// rather than aborting boot; the instance must still come up reachable on
// localhost.
func resolveAddress(database *sql.DB, cfg *config.Config, logger *slog.Logger) (hostset.HostSet, *store.SettingsStore) {
	settingsStore := store.NewSettingsStore(database)
	storedURL, err := settingsStore.Get(store.SettingPublicURL)
	if err != nil {
		logger.Warn("failed to load the stored public url, using defaults", "error", err)
		storedURL = ""
	}
	hostSet, err := hostset.Resolve(hostset.Input{
		StoredURL:    storedURL,
		BaseDomain:   cfg.BaseDomain,
		SSOBaseURL:   cfg.SSOBaseURL,
		PublicScheme: cfg.PublicScheme,
		ServedPort:   cfg.TraefikPort,
	})
	if err != nil {
		logger.Warn("failed to resolve the public address, using defaults", "error", err)
		def, derr := hostset.ParsePublicURL(hostset.DefaultPublicURL)
		if derr != nil {
			def = hostset.PublicURL{Scheme: hostset.SchemeHTTP, Host: "localhost", Port: 8080}
		}
		hostSet = hostset.New(def)
	}
	logger.Info("address resolved",
		"url", hostSet.PrimaryBaseURL(),
		"issuer", hostSet.IssuerBaseURL())

	// Report the proxy layers that disagree before any user hits a stalled
	// login. Each issue names one setting, so "SSO does not work behind my
	// proxy" is a one-line answer instead of three candidates. tlsAtTraefik is
	// false because Bloud ships no certificate resolver at Traefik; when a
	// deployment does terminate there, this is the call site that changes.
	for _, issue := range hostSet.ProxyConsistency(cfg.TrustedProxyNets, false) {
		logger.Warn("address configuration issue",
			"code", issue.Code,
			"issuer", hostSet.IssuerBaseURL(),
			"detail", issue.Message)
	}
	return hostSet, settingsStore
}

// buildTemplateVars builds the template-variable store shared by the
// orchestrator and the authentik configurator. The LDAP outpost token is not
// in the static set: it is issued at runtime and recorded through the store's
// named setter, which is what keeps that one mutable value guarded.
func buildTemplateVars(cfg *config.Config) *configurator.TemplateVars {
	return configurator.NewTemplateVars(map[string]string{
		"postgresPassword":        cfg.PostgresPassword,
		"authentikSecretKey":      cfg.Secrets.GetAuthentikSecretKey(),
		"authentikBootstrapToken": cfg.Secrets.GetAuthentikBootstrapToken(),
		"authentikAdminPassword":  cfg.AuthentikAdminPassword,
		"authentikAdminEmail":     cfg.AuthentikAdminEmail,
	})
}

// waitForSystemConvergence blocks until the orchestrator reports ready and the
// system apps pass their health check, then initializes auth. It aborts the
// process on a failed health check or a systemConvergenceTimeout. The
// listener is already open here, but bootstrapGate keeps the API unavailable
// until this returns: the API must not serve before the system apps it depends
// on are running.
// authReady reports whether the dashboard's OIDC client is initialized. The
// fast gate requires it: opening the API without a working login would trade
// one kind of broken dashboard for another.
func authReady(ref *api.AuthRef) bool {
	cfg := ref.Get()
	return cfg != nil && cfg.OIDCConfig != nil
}

// watchConvergenceBehindFastGate finishes the startup duties the blocking path
// would have done, but after the gate is already open. The API is live; this
// only waits for the first pass to land so it can report the result.
//
// Unlike waitForSystemConvergence a failure here is a warning, not a fatal.
// Killing a dev process for a background pass that finished badly would take
// down the very editor loop the fast gate exists to keep fast.
func watchConvergenceBehindFastGate(server *api.Server, logger *slog.Logger) {
	go func() {
		<-server.OrchestratorReady()
		server.InitAuth()
		if err := server.CheckSystemHealth(); err != nil {
			logger.Warn("dev fast gate: background convergence finished with an unhealthy system", "error", err)
			return
		}
		logger.Info("dev fast gate: background convergence finished cleanly")
	}()
}

func waitForSystemConvergence(server *api.Server, logger *slog.Logger) {
	logger.Info("waiting for system apps to converge")
	readyCtx, cancel := context.WithTimeout(context.Background(), systemConvergenceTimeout)
	defer cancel()
	select {
	case <-server.OrchestratorReady():
		if err := server.CheckSystemHealth(); err != nil {
			logger.Error("system app failed during startup", "error", err)
			os.Exit(1)
		}
		logger.Info("system apps converged successfully")
		server.InitAuth()
	case <-readyCtx.Done():
		logger.Error("system startup timed out")
		os.Exit(1)
	}
}
