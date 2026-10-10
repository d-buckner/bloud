// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/system"
	"github.com/go-chi/chi/v5"
)

// SystemModule encapsulates system-level operations: health check, system
// status, storage stats, and the developer lifecycle graph.
type systemModule struct {
	appStore store.AppStoreInterface
	// catalog is the disk-driven catalog cache, and the source the developer
	// graph draws its integration edges from: it is the live view of
	// apps/*/metadata.yaml, refreshed by POST /api/apps/refresh-catalog, so a
	// display never needs a second copy of the declarations kept in sync by
	// hand. It is also what ssoEdgeLabel reads.
	catalog catalog.CacheInterface
	orch    orchestratorStatusCaller
	// healthCheck reports whether the system is actually working (database
	// reachable, orchestrator intent loop alive). Wired by the router; nil
	// leaves the endpoint as a liveness echo.
	healthCheck func() error
	// aiSettings is the store behind Settings -> AI. The developer graph
	// reads it to decide whether the instance provides an AI model at all:
	// with no enabled upstream there is nothing to wire, so the AI Model node
	// stays out of the picture instead of showing a provider that answers
	// nothing. Wired by the router; nil means never shown.
	aiSettings store.SettingsStoreInterface
	// externalApps holds the AI upstreams as `contract:inference` records. The
	// developer graph reads it rather than a settings key so the node reflects
	// the same registry the resolver reads.
	externalApps store.ExternalAppStoreInterface
	// dnsDiagnostics answers /system/diagnostics. Wired by the router; nil
	// omits the endpoint.
	dnsDiagnostics *system.DNSDiagnostics
	// hostState is the live address. The developer graph names its ingress
	// node with it, so the picture shows the origin the operator configured
	// rather than a hardcoded word for one network path. Unwired, the graph
	// falls back to the default address every unconfigured install starts on.
	hostState *hostset.State
	logger    *slog.Logger
}

func NewSystemModule(
	appStore store.AppStoreInterface,
	catalog catalog.CacheInterface,
	orch orchestratorStatusCaller,
	logger *slog.Logger,
) *systemModule {
	return &systemModule{
		appStore: appStore,
		catalog:  catalog,
		orch:     orch,
		logger:   logger,
	}
}

// ---- Health ----

// SetHealthCheck wires the system health check behind the health endpoint, so
// the HTTP answer and Server.CheckSystemHealth come from one implementation.
func (m *systemModule) SetHealthCheck(check func() error) {
	m.healthCheck = check
}

// SetAISettings wires the settings store behind Settings -> AI, which is what
// the developer graph reads to decide whether the instance provides an AI
// model. Unwired, the graph never shows the AI Model node.
func (m *systemModule) SetAISettings(settingsStore store.SettingsStoreInterface) {
	m.aiSettings = settingsStore
}

// SetExternalApps wires the registry the AI upstreams live in. Unwired, the
// graph never shows the AI Model node.
func (m *systemModule) SetExternalApps(registry store.ExternalAppStoreInterface) {
	m.externalApps = registry
}

// SetDNSDiagnostics wires the container DNS diagnostic behind
// /system/diagnostics. Unwired, the endpoint answers 503.
func (m *systemModule) SetDNSDiagnostics(d *system.DNSDiagnostics) {
	m.dnsDiagnostics = d
}

// SetHostSet wires the live address the developer graph labels its ingress
// node with. Unwired, the node carries the default public address.
func (m *systemModule) SetHostSet(state *hostset.State) {
	m.hostState = state
}

// HealthHandler answers the health probe. 200 means the instance is up and
// reconciling. 503 with status "unhealthy" means the process answers but
// something it depends on is broken; the reason goes to the log, not the wire,
// because this endpoint is public and a driver error can name a path.
//
// That body is also what separates this 503 from the bootstrap gate's
// 503 {"error":"starting"}: "starting" means still coming up, "unhealthy"
// means up and broken. The bootstrap page keys off the difference.
func (m *systemModule) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.healthCheck == nil {
			respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		if err := m.healthCheck(); err != nil {
			m.logger.Warn("health check failed", "error", err)
			respondJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ---- System Status ----

// DiagnosticsHandler answers the container DNS diagnostic: whether the
// configured public host resolves inside a managed container the way it does
// on the host.
func (m *systemModule) DiagnosticsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.dnsDiagnostics == nil {
			respondError(w, http.StatusServiceUnavailable, "diagnostics unavailable")
			return
		}
		respondJSON(w, http.StatusOK, m.dnsDiagnostics.Check(r.Context()))
	}
}

// SystemStatusHandler returns system stats (CPU, memory, disk).
func (m *systemModule) SystemStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats, err := system.GetStats()
		if err != nil {
			m.logger.Error("failed to get system stats", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to get system stats")
			return
		}
		respondJSON(w, http.StatusOK, stats)
	}
}

// SystemStatusStreamHandler streams system stats via SSE.
func (m *systemModule) SystemStatusStreamHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		ctx := r.Context()
		m.logger.Info("SSE client connected for system stats")

		for {
			select {
			case <-ctx.Done():
				m.logger.Info("SSE client disconnected")
				return
			case <-ticker.C:
				stats, err := system.GetStats()
				if err != nil {
					m.logger.Error("failed to get system stats for SSE", "error", err)
					continue
				}
				data, err := json.Marshal(stats)
				if err != nil {
					m.logger.Error("failed to marshal stats for SSE", "error", err)
					continue
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
					// Client disconnected mid-stream.
					return
				}
				flusher.Flush()
			}
		}
	}
}

// ---- Storage ----

func (m *systemModule) StorageHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		storage, err := system.GetStorageStats()
		if err != nil {
			m.logger.Error("failed to get storage stats", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to get storage stats")
			return
		}
		respondJSON(w, http.StatusOK, storage)
	}
}

// ---- Router ----

// NewSystemRouter registers all system-related routes on the given router.
// /system/diagnostics is registered on the authenticated router, not here (see
// registerRoutes): the other system routes are public info, but resolver
// upstreams are operator-only.
func NewSystemRouter(mod *systemModule, r chi.Router) {
	r.Get("/health", mod.HealthHandler())
	r.Get("/system/status", mod.SystemStatusHandler())
	r.Get("/system/storage", mod.StorageHandler())
	r.Get("/system/developer", mod.DeveloperGraphHandler())
}
