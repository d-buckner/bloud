// SPDX-License-Identifier: AGPL-3.0-only

// Package wire builds the host-agent runtime's orchestrator. It owns the whole
// dependency set: the lifecycle graph, the catalog dependency graph, the
// container runtime, the tailnet node, the gateway, the remote proxy, the
// proxy outpost, the Traefik route generator, the durable operation store,
// the SSO provisioner, and the one-shot auth-key migration.
//
// This is the only place a fully wired orchestrator is constructed. The API
// layer receives one instead of building its own, so there is no second copy
// of this wiring that can drift from the first. An earlier CLI path kept such
// a copy and set three of the twenty-odd fields here; it was deleted for
// having no caller.
//
// Build does not start the orchestrator loop. The caller starts it, under a
// context its shutdown path cancels, so construction stays a pure step that a
// test can run without a live goroutine racing its assertions.
package wire

import (
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/google/uuid"
)

// Input is everything Build needs to construct a fully wired orchestrator.
//
// The zero value is not usable: Logger, DB, Registry, CatalogCache, and
// TailnetStore are required. Optional collaborators disable the subsystem
// that reads them rather than failing the build, which is how a runtime
// without an identity provider, or without federation, still comes up.
type Input struct {
	// Logger is where construction and the orchestrator's own lifecycle log.
	Logger *slog.Logger

	// DB is the open SQLite handle. The operation store and the remote-app
	// store are built on it inside Build, so callers need not know which
	// stores the orchestrator owns.
	DB *sql.DB

	// AppStore is the installed-app store the orchestrator reads intent from
	// and writes lifecycle state to.
	AppStore store.AppStoreInterface

	// CatalogCache is the in-memory app catalog. Required: without it the
	// orchestrator cannot resolve an app's container definitions, so it
	// cannot create the containers it is meant to converge.
	CatalogCache catalog.CacheInterface

	// Registry resolves a graph node name to its configurator.
	Registry configurator.RegistryInterface

	// ContainerRuntime creates and manages app containers. When nil, Build
	// constructs a Podman runtime itself. If neither is available the build
	// fails: an appliance that cannot start a container has nothing to
	// converge.
	ContainerRuntime containerruntime.Runtime

	// EventsBus broadcasts lifecycle transitions to the SSE streams. Nil
	// disables event publishing.
	EventsBus *eventbus.Bus

	// Authentik is the identity-provider client. Nil means no SSO
	// provisioning: SSO-dependent apps will not come up, but the runtime
	// still boots.
	Authentik *authentik.Client

	// TailnetStore holds federation connections. Required: the active
	// connection drives the auth-key and tailnet-ID callbacks.
	TailnetStore *store.TailnetStore

	// HostStore persists admin-configured custom hosts. Nil disables the
	// host endpoints.
	HostStore store.HostStoreInterface

	// Hosts is the live host-set state. When non-nil it supersedes the
	// SSOBaseURL/SSOAuthentikURL/SSOIssuerURL strings, so admin host
	// changes take effect without a restart.
	Hosts *hostset.State

	// AppsDir is the catalog directory on disk.
	AppsDir string
	// DataDir is the Bloud data root (BLOUD_DATA_DIR).
	DataDir string
	// TraefikDynamicDir is where the generated app route file is written.
	TraefikDynamicDir string
	// TraefikPort is the port Traefik serves on inside the runtime.
	TraefikPort int

	// TSAuthKey is the legacy single tailscale auth key from the
	// environment. When set and no stored connection exists, Build migrates
	// it into the tailnet store.
	TSAuthKey string

	// LDAPOutput is the LDAP provider endpoint handed to apps whose SSO
	// strategy is ldap.
	LDAPOutput *configurator.LDAPOutput

	// TemplateVars are the variables container-spec templates render with
	// (postgresPassword and the authentik values). The map is shared by
	// reference with the authentik configurator, which writes the LDAP
	// outpost token into it at runtime.
	TemplateVars map[string]string

	// SSOBaseURL, SSOHostSecret, SSOAuthentikURL, and SSOIssuerURL are the
	// legacy single-host SSO settings. Hosts supersedes them when set.
	SSOBaseURL      string
	SSOHostSecret   string
	SSOAuthentikURL string
	SSOIssuerURL    string

	// Secrets resolves a provider's published credentials into an
	// integration binding. Nil disables publishing: bindings still carry
	// the provider's identity and address.
	Secrets configurator.AppSecretsProvider

	// OnHostsChanged runs after a SetHosts intent is applied. The API layer
	// passes a closure that re-ensures the dashboard OAuth app against the
	// new redirect URIs. It crosses this boundary as a plain func so wire
	// never imports the API package.
	OnHostsChanged func()
}

// Output is what Build hands back.
type Output struct {
	// Orchestrator is fully wired but not started. The caller starts it.
	Orchestrator *orchestrator.Orchestrator

	// Gateway and TailnetNode are the same instances the orchestrator
	// drives. They are exposed so other consumers can be pointed at the
	// real ones: the sharing module currently builds its own with nil
	// collaborators, which is why invite creation answers 503 (open ledger
	// item 15).
	Gateway     *sharing.GatewayManager
	TailnetNode *sharing.TailnetNodeManager

	// Config is the exact OrchestratorConfig the orchestrator was built
	// with. It is exposed so a test can assert that every field the type
	// declares was deliberately set, which is the guard that keeps a newly
	// added subsystem from being declared in the config type and forgotten
	// here. The built config is otherwise unreachable from outside the
	// orchestrator, and the alternative is a test-only accessor on the
	// orchestrator itself.
	Config orchestrator.OrchestratorConfig
}

// Build constructs the orchestrator and everything it owns.
func Build(in Input) (*Output, error) {
	if in.Logger == nil {
		return nil, fmt.Errorf("wire: Logger is required")
	}
	if in.DB == nil {
		return nil, fmt.Errorf("wire: DB is required")
	}
	if in.Registry == nil {
		return nil, fmt.Errorf("wire: Registry is required")
	}
	if in.CatalogCache == nil {
		return nil, fmt.Errorf("wire: CatalogCache is required")
	}
	if in.TailnetStore == nil {
		return nil, fmt.Errorf("wire: TailnetStore is required")
	}

	logger := in.Logger
	traefikConfigPath := filepath.Join(in.TraefikDynamicDir, "apps-routes.yml")
	logger.Info("orchestrator paths", "traefikConfigPath", traefikConfigPath)

	lifecycleGraph := graph.New(graph.NewMapRepository())

	// The podman client backs the exec callback and the runtime fallback, so
	// it is built even when a runtime was supplied.
	client, err := podman.NewClient()
	if err != nil {
		logger.Warn("podman client unavailable", "error", err)
	}

	runtime := in.ContainerRuntime
	if runtime == nil {
		if client == nil {
			return nil, fmt.Errorf("wire: container runtime unavailable (no podman client)")
		}
		runtime = containerruntime.NewPodmanRuntime(client)
	}

	var ssoProvisioner orchestrator.SSOProvisioner
	var forwardDomainSSO orchestrator.ForwardDomainProvisioner
	if in.Authentik != nil {
		ssoProvisioner = in.Authentik
		forwardDomainSSO = in.Authentik
	}

	if err := migrateLegacyAuthKey(in.TailnetStore, in.TSAuthKey, logger); err != nil {
		return nil, err
	}

	authKeyFn := func() string {
		conn, err := in.TailnetStore.GetActive()
		if err != nil || conn == nil {
			return ""
		}
		return conn.AuthKey
	}

	var exec sharing.ContainerExec
	if client != nil {
		exec = client
	}
	tailnetNode := sharing.NewTailnetNodeManager(runtime, exec, authKeyFn, in.TraefikPort, in.DataDir, logger)
	gateway := sharing.NewGatewayManager(runtime, exec, authKeyFn, sharing.DefaultGatewaySOCKSPort, in.TraefikPort, in.DataDir, logger)

	socksAddr := fmt.Sprintf("localhost:%d", sharing.DefaultGatewaySOCKSPort)
	remoteProxy := sharing.NewRemoteProxyManager(socksAddr, sharing.DefaultRemoteProxyBasePort, logger)

	// The catalog dependency graph is the planner install and uninstall
	// intents use to resolve integrations and auto-install required
	// providers. A load failure leaves it nil, which makes those intents
	// unable to plan rather than planning against a stale graph.
	catalogGraph, err := catalog.NewLoader(in.AppsDir).LoadGraph()
	if err != nil {
		logger.Error("failed to build catalog graph", "error", err)
	} else {
		logger.Info("catalog dependency graph built", "apps", len(catalogGraph.GetApps()))
	}

	config := orchestrator.OrchestratorConfig{
		LDAPOutput:       in.LDAPOutput,
		Containers:       runtime,
		TemplateVars:     in.TemplateVars,
		Secrets:          in.Secrets,
		AppStore:         in.AppStore,
		Operations:       store.NewOperationStore(in.DB),
		Events:           in.EventsBus,
		CatalogGraph:     catalogGraph,
		TailnetStore:     in.TailnetStore,
		RemoteAppStore:   store.NewRemoteAppStore(in.DB),
		TailnetNode:      tailnetNode,
		Gateway:          gateway,
		RemoteProxy:      remoteProxy,
		ProxyOutpost:     sharing.NewProxyOutpostManager(runtime, logger),
		ForwardDomainSSO: forwardDomainSSO,
		SSO:              ssoProvisioner,
		SSOBaseURL:       in.SSOBaseURL,
		SSOHostSecret:    in.SSOHostSecret,
		SSOAuthentikURL:  in.SSOAuthentikURL,
		SSOIssuerURL:     in.SSOIssuerURL,
		TraefikGen:       traefikgen.NewGenerator(traefikConfigPath),
		ActiveTailnetID: func() string {
			conn, err := in.TailnetStore.GetActive()
			if err != nil || conn == nil {
				return ""
			}
			return conn.ID
		},
		Hosts:          in.Hosts,
		HostStore:      in.HostStore,
		OnHostsChanged: in.OnHostsChanged,
	}

	orch := orchestrator.NewOrchestrator(
		lifecycleGraph,
		in.Registry,
		in.CatalogCache,
		in.DataDir,
		logger,
		config,
	)
	logger.Info("lifecycle orchestrator initialized")

	return &Output{
		Orchestrator: orch,
		Gateway:      gateway,
		TailnetNode:  tailnetNode,
		Config:       config,
	}, nil
}

// migrateLegacyAuthKey moves a BLOUD_TS_AUTHKEY environment value into the
// tailnet connections store, so a deployment configured the old way keeps
// working once connections are managed through the store. It is a no-op when
// no key is set or a connection already exists.
func migrateLegacyAuthKey(tailnetStore *store.TailnetStore, authKey string, logger *slog.Logger) error {
	if authKey == "" {
		return nil
	}
	active, _ := tailnetStore.GetActive()
	if active != nil {
		return nil
	}
	if err := tailnetStore.Create(store.TailnetConnection{
		ID:      uuid.New().String(),
		Name:    "Default",
		Type:    "tailscale",
		AuthKey: authKey,
		Status:  "active",
	}); err != nil {
		return fmt.Errorf("wire: migrate BLOUD_TS_AUTHKEY to tailnet_connections store: %w", err)
	}
	logger.Info("migrated BLOUD_TS_AUTHKEY to tailnet_connections store")
	return nil
}
