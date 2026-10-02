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
	"time"

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

	// Settings persists the instance settings, including the public address.
	// Nil disables the address endpoints.
	Settings store.SettingsStoreInterface

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
	// (postgresPassword and the authentik values). A store rather than a map
	// because the authentik configurator writes the LDAP outpost token into it
	// at runtime while the orchestrator reads the rest.
	TemplateVars *configurator.TemplateVars

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

	// ReconcileInterval is how often the self-healing convergence pass
	// runs (BLOUD_RECONCILE_INTERVAL). Zero means "not configured": Build
	// substitutes orchestrator.DefaultSelfHealInterval. A negative value
	// disables the periodic pass entirely.
	ReconcileInterval time.Duration
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

// validateInput refuses a half-wired Build. Every field here is something the
// orchestrator cannot recover from at runtime, so a missing one is a
// programming error caught at startup rather than a nil deref mid-reconcile.
func validateInput(in Input) error {
	switch {
	case in.Logger == nil:
		return fmt.Errorf("wire: Logger is required")
	case in.DB == nil:
		return fmt.Errorf("wire: DB is required")
	case in.Registry == nil:
		return fmt.Errorf("wire: Registry is required")
	case in.CatalogCache == nil:
		return fmt.Errorf("wire: CatalogCache is required")
	case in.TailnetStore == nil:
		return fmt.Errorf("wire: TailnetStore is required")
	}
	return nil
}

// Build constructs the orchestrator and everything it owns.
func Build(in Input) (*Output, error) {
	if err := validateInput(in); err != nil {
		return nil, err
	}

	logger := in.Logger
	traefikConfigPath := filepath.Join(in.TraefikDynamicDir, "apps-routes.yml")
	logger.Info("orchestrator paths", "traefikConfigPath", traefikConfigPath)

	runtime, client, err := resolveRuntime(in)
	if err != nil {
		return nil, err
	}

	if err := migrateLegacyAuthKey(in.TailnetStore, in.TSAuthKey, logger); err != nil {
		return nil, err
	}

	managers := buildSharingManagers(in, runtime, client)
	config := buildOrchestratorConfig(in, runtime, managers, loadCatalogGraph(in, logger), traefikConfigPath)

	orch := orchestrator.NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		in.Registry,
		in.CatalogCache,
		in.DataDir,
		logger,
		config,
	)
	logger.Info("lifecycle orchestrator initialized")

	return &Output{
		Orchestrator: orch,
		Gateway:      managers.Gateway,
		TailnetNode:  managers.Node,
		Config:       config,
	}, nil
}

// resolveRuntime picks the container runtime: the one supplied, or a Podman
// runtime over a client built here. The client comes back either way because it
// also backs the exec callback the sharing managers need, which the runtime
// abstraction does not carry.
func resolveRuntime(in Input) (containerruntime.Runtime, *podman.Client, error) {
	client, err := podman.NewClient()
	if err != nil {
		in.Logger.Warn("podman client unavailable", "error", err)
	}
	if in.ContainerRuntime != nil {
		return in.ContainerRuntime, client, nil
	}
	if client == nil {
		return nil, nil, fmt.Errorf("wire: container runtime unavailable (no podman client)")
	}
	return containerruntime.NewPodmanRuntime(client), client, nil
}

// sharingManagers bundles the tailnet-facing managers the orchestrator drives
// on behalf of an app.
type sharingManagers struct {
	Node        *sharing.TailnetNodeManager
	Gateway     *sharing.GatewayManager
	RemoteProxy *sharing.RemoteProxyManager
}

// buildSharingManagers wires the tailnet node, the SOCKS gateway, and the
// remote-proxy pool. All three read the active connection through the store on
// every call rather than capturing the key at build time, so a rotation takes
// effect without a restart.
func buildSharingManagers(in Input, runtime containerruntime.Runtime, client *podman.Client) sharingManagers {
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
	socksAddr := fmt.Sprintf("localhost:%d", sharing.DefaultGatewaySOCKSPort)
	return sharingManagers{
		Node:        sharing.NewTailnetNodeManager(runtime, exec, authKeyFn, in.TraefikPort, in.DataDir, in.Logger),
		Gateway:     sharing.NewGatewayManager(runtime, exec, authKeyFn, sharing.DefaultGatewaySOCKSPort, in.TraefikPort, in.DataDir, in.Logger),
		RemoteProxy: sharing.NewRemoteProxyManager(socksAddr, sharing.DefaultRemoteProxyBasePort, in.Logger),
	}
}

// loadCatalogGraph builds the dependency graph the install and uninstall
// planners use to resolve integrations and auto-install required providers. A
// load failure leaves it nil, which makes those intents unable to plan rather
// than plan against a stale graph.
func loadCatalogGraph(in Input, logger *slog.Logger) *catalog.AppGraph {
	g, err := catalog.NewLoader(in.AppsDir).LoadGraph()
	if err != nil {
		logger.Error("failed to build catalog graph", "error", err)
		return nil
	}
	logger.Info("catalog dependency graph built", "apps", len(g.GetApps()))
	return g
}

// buildOrchestratorConfig fills the orchestrator's config from the wire input.
// The two SSO interfaces come from the same Authentik client when one was
// supplied; both stay nil otherwise, which disables SSO provisioning while the
// runtime still boots.
func buildOrchestratorConfig(
	in Input,
	runtime containerruntime.Runtime,
	managers sharingManagers,
	catalogGraph *catalog.AppGraph,
	traefikConfigPath string,
) orchestrator.OrchestratorConfig {
	var ssoProvisioner orchestrator.SSOProvisioner
	var forwardDomainSSO orchestrator.ForwardDomainProvisioner
	if in.Authentik != nil {
		ssoProvisioner = in.Authentik
		forwardDomainSSO = in.Authentik
	}

	return orchestrator.OrchestratorConfig{
		SelfHealInterval: resolveSelfHealInterval(in.ReconcileInterval),
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
		TailnetNode:      managers.Node,
		Gateway:          managers.Gateway,
		RemoteProxy:      managers.RemoteProxy,
		ProxyOutpost:     sharing.NewProxyOutpostManager(runtime, in.Logger),
		ForwardDomainSSO: forwardDomainSSO,
		SSO:              ssoProvisioner,
		SSOBaseURL:       in.SSOBaseURL,
		SSOHostSecret:    in.SSOHostSecret,
		SSOAuthentikURL:  in.SSOAuthentikURL,
		SSOIssuerURL:     in.SSOIssuerURL,
		TraefikPort:      in.TraefikPort,
		TraefikGen:       traefikgen.NewGenerator(traefikConfigPath),
		ActiveTailnetID: func() string {
			conn, err := in.TailnetStore.GetActive()
			if err != nil || conn == nil {
				return ""
			}
			return conn.ID
		},
		Hosts:          in.Hosts,
		Settings:       in.Settings,
		OnHostsChanged: in.OnHostsChanged,
	}
}

// resolveSelfHealInterval turns the configured reconcile interval into the
// value the orchestrator runs with. Zero means the caller had no opinion, so
// the framework default applies. A negative value is a deliberate "no
// periodic pass" and passes straight through: the zero value cannot carry
// both meanings, and silently disabling the feature because nobody set an
// environment variable is the worse failure by a wide margin.
func resolveSelfHealInterval(in time.Duration) time.Duration {
	if in == 0 {
		return orchestrator.DefaultSelfHealInterval
	}
	return in
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
