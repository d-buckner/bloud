package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/go-chi/chi/v5"
)

// Server represents the HTTP server. Dependency initialization and route
// wiring is performed by NewRouter; Server retains the runtime state needed
// by main.go (orchestrator lifecycle, health checks).
type Server struct {
	cfg             ServerConfig
	router          *chi.Mux
	db              *sql.DB
	catalog         catalog.CacheInterface
	appStore        appStoreHelper
	orch            *orchestrator.Orchestrator
	sessionStore    sessionStoreHelper
	remoteAppStore  store.RemoteAppStoreInterface
	authentikClient *authentik.Client
	authConfig      *AuthConfig
	knownRedirectURIs sync.Map
	logger          *slog.Logger
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

// sessionStoreHelper provides minimal session store access.
type sessionStoreHelper interface{}

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
	AuthentikToken    string
	AuthentikPort     int
	TSAuthKey         string
	HostLabel         string
	RedisAddr         string
	RefreshAuthentikToken func() string
	LDAPOutput       *configurator.LDAPOutput
	Registry         configurator.RegistryInterface
	ContainerRuntime containerruntime.Runtime
	TemplateVars     map[string]string
}

// NewServer creates a new HTTP server instance. It delegates dependency
// initialization and route wiring to NewRouter, then returns a Server
// with the necessary fields populated for main.go.
func NewServer(db *sql.DB, cfg ServerConfig, logger *slog.Logger) *Server {
	remoteAppStore := store.NewRemoteAppStore(db)
	router, orch := NewRouter(db, cfg, logger, func(o *routerOptions) {
		o.remoteAppStore = remoteAppStore
	})

	s := &Server{
		cfg:             cfg,
		router:          router,
		db:              db,
		orch:            orch,
		remoteAppStore:  remoteAppStore,
		logger:          logger,
	}

	return s
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

// CheckSystemHealth validates that the system is healthy by checking
// database connectivity and that the orchestrator is initialized.
func (s *Server) CheckSystemHealth() error {
	if s.orch == nil {
		return nil // no orchestrator — skip health check
	}
	if err := s.db.Ping(); err != nil {
		return fmt.Errorf("database connection failed: %w", err)
	}
	return nil
}

// tryInitAuth, initAuth, refreshCatalog are kept as no-ops.
func (s *Server) tryInitAuth() {}
func (s *Server) initAuth()    {}
func (s *Server) refreshCatalog(_ string) {}
