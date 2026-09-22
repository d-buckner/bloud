// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/go-chi/chi/v5"
)

// Server represents the HTTP server. Dependency initialization and route
// wiring is performed by NewRouter; Server retains the runtime state needed
// by main.go (orchestrator lifecycle, health checks).
type Server struct {
	cfg            ServerConfig
	router         *chi.Mux
	db             *sql.DB
	catalog        catalog.CacheInterface
	appStore       appStoreHelper
	orch           *orchestrator.Orchestrator
	remoteAppStore store.RemoteAppStoreInterface
	authConfig     *authConfigRef
	logger         *slog.Logger
}

// appStoreHelper provides minimal app store access for health checks.
type appStoreHelper interface {
	getAll() ([]*appEntry, error)
}

type appEntry struct {
	CatalogID string
	Status    string
	IsSystem  bool
}

// ServerConfig holds paths and configuration for server initialization.
type ServerConfig struct {
	AppsDir           string
	DataDir           string
	TraefikDynamicDir string
	BaseDomain        string
	TraefikPort       int
	Port              int
	SSOHostSecret     string
	SSOBaseURL        string
	SSOAuthentikURL   string
	SSOIssuerURL      string
	AuthentikToken    string
	AuthentikPort     int
	TSAuthKey         string
	HostLabel         string
	// Hosts is the live host-set state (multi-host SSO); nil disables the
	// host endpoints and multi-host URL resolution.
	Hosts     *hostset.State
	HostStore store.HostStoreInterface
	// TrustedLocalNets lists CIDRs/IPs treated as local (loopback-equivalent)
	// for host-agent API requests (e.g. QEMU slirp NAT gateway).
	TrustedLocalNets []string
	// APIToken is the bearer credential that grants admin from a trusted
	// position (loopback or TrustedLocalNets). Position alone is never a
	// credential. An empty APIToken disables the position-based admin path
	// entirely (fail closed).
	APIToken              string
	RefreshAuthentikToken func() string
	LDAPOutput            *configurator.LDAPOutput
	Registry              configurator.RegistryInterface
	ContainerRuntime      containerruntime.Runtime
	TemplateVars          map[string]string
	// Secrets is the host secret store, handed to the orchestrator so
	// integration bindings can resolve a provider's published credentials.
	// Nil disables that half of a binding.
	Secrets configurator.AppSecretsProvider
	// EventsBus is the shared event bus (SSE streams + app-change
	// publishing). Nil creates one internally.
	EventsBus *eventbus.Bus
}

// NewServer creates a new HTTP server instance. It delegates dependency
// initialization and route wiring to NewRouter, then returns a Server
// with the necessary fields populated for main.go.
func NewServer(db *sql.DB, cfg ServerConfig, logger *slog.Logger) *Server {
	remoteAppStore := store.NewRemoteAppStore(db)
	authRef := newAuthConfigRef(nil)
	router, orch := NewRouter(db, cfg, logger, func(o *routerOptions) {
		o.remoteAppStore = remoteAppStore
		o.authConfig = authRef
	})

	s := &Server{
		cfg:            cfg,
		router:         router,
		db:             db,
		orch:           orch,
		remoteAppStore: remoteAppStore,
		authConfig:     authRef,
		logger:         logger,
	}

	return s
}

// Start starts the HTTP server. While the orchestrator is still converging,
// requests are answered by the bootstrap loading page (see bootstrapGate), so
// Traefik never surfaces its own 502 for the catch-all UI during startup.
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.cfg.Port)
	s.logger.Info("starting HTTP server", "addr", addr)

	handler := http.Handler(s.router)
	if s.orch != nil {
		handler = bootstrapGate(s.orch.Ready(), handler)
	}

	server := &http.Server{
		Addr:        addr,
		Handler:     handler,
		ReadTimeout: 15 * time.Second,
		IdleTimeout: 60 * time.Second,
	}

	return server.ListenAndServe()
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(_ context.Context) error {
	s.logger.Info("shutting down HTTP server")
	return nil
}

// OrchestratorReady returns a channel that is closed after the first
// convergence pass completes (system apps have reached their target state).
func (s *Server) OrchestratorReady() <-chan struct{} {
	if s.orch == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return s.orch.Ready()
}

// reconcilingLoop is the slice of the orchestrator the health check needs:
// whether the intent loop is still running.
type reconcilingLoop interface {
	Stopped() bool
}

// checkSystemHealth is the single implementation behind both
// Server.CheckSystemHealth and the HTTP health endpoint, so the two can never
// disagree. A dead intent loop is invisible to a DB ping: every Submit keeps
// answering 202 while nothing reconciles, so both surfaces have to look at the
// loop, not just at the database.
func checkSystemHealth(orch reconcilingLoop, db *sql.DB) error {
	if orch != nil && orch.Stopped() {
		return errors.New("orchestrator intent loop has exited; nothing is reconciling")
	}
	if db == nil {
		return errors.New("no database connection")
	}
	if err := db.Ping(); err != nil {
		return fmt.Errorf("database connection failed: %w", err)
	}
	return nil
}

// CheckSystemHealth validates that the system is healthy: the database is
// reachable and, when an orchestrator is wired, its intent loop has not
// exited. A dead loop is otherwise indistinguishable from an idle one:
// every Submit keeps answering 202 while nothing reconciles.
func (s *Server) CheckSystemHealth() error {
	return checkSystemHealth(orchAsReconcilingLoop(s.orch), s.db)
}

// orchAsReconcilingLoop avoids the typed-nil trap: a nil *orchestrator.Orchestrator
// assigned straight to the interface would arrive as a non-nil interface holding
// a nil pointer, and the Stopped() call would dereference it.
func orchAsReconcilingLoop(o *orchestrator.Orchestrator) reconcilingLoop {
	if o == nil {
		return nil
	}
	return o
}

// InitAuth re-attempts auth initialization once system apps have converged.
// At construction time Authentik is usually still booting, so initAuthHelper
// returns nil and auth stays disabled; this method lets main.go re-run the
// OIDC bootstrap after OrchestratorReady so AuthReady becomes true. Safe to
// call multiple times: EnsureBloudOAuthApp is idempotent.
func (s *Server) InitAuth() {
	if s.authConfig == nil {
		return
	}
	s.authConfig.Ensure()
}
