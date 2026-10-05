// SPDX-License-Identifier: AGPL-3.0-only

// Package wire builds the host-agent runtime's orchestrator. It owns the whole
// dependency set: the lifecycle graph, the catalog dependency graph, the
// container runtime, the Traefik route generator, the durable operation
// store, and the SSO provisioner.
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
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// Input is everything Build needs to construct a fully wired orchestrator.
//
// The zero value is not usable: Logger, DB, Registry, and CatalogCache are
// required. Optional collaborators disable the subsystem that reads them
// rather than failing the build, which is how a runtime without an
// identity provider still comes up.
type Input struct {
	// Logger is where construction and the orchestrator's own lifecycle log.
	Logger *slog.Logger

	// DB is the open SQLite handle. The operation store is built on it
	// inside Build, so callers need not know which stores the orchestrator
	// owns.
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

	runtime, err := resolveRuntime(in)
	if err != nil {
		return nil, err
	}

	config := buildOrchestratorConfig(in, runtime, loadCatalogGraph(in, logger), traefikConfigPath)

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
		Config:       config,
	}, nil
}

// resolveRuntime picks the container runtime: the one supplied, or a Podman
// runtime built here. An appliance that cannot start a container has nothing
// to converge, so a failure here is fatal rather than degraded.
func resolveRuntime(in Input) (containerruntime.Runtime, error) {
	if in.ContainerRuntime != nil {
		return in.ContainerRuntime, nil
	}
	client, err := podman.NewClient()
	if err != nil {
		return nil, fmt.Errorf("wire: container runtime unavailable: %w", err)
	}
	return containerruntime.NewPodmanRuntime(client), nil
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
// The SSO provisioner comes from the Authentik client when one was supplied;
// it stays nil otherwise, which disables SSO provisioning while the runtime
// still boots.
func buildOrchestratorConfig(
	in Input,
	runtime containerruntime.Runtime,
	catalogGraph *catalog.AppGraph,
	traefikConfigPath string,
) orchestrator.OrchestratorConfig {
	var ssoProvisioner orchestrator.SSOProvisioner
	if in.Authentik != nil {
		ssoProvisioner = in.Authentik
	}

	return orchestrator.OrchestratorConfig{
		Tuning: orchestrator.TuningConfig{
			SelfHealInterval: resolveSelfHealInterval(in.ReconcileInterval),
			Events:           in.EventsBus,
		},
		Runtime: orchestrator.RuntimeConfig{
			Containers:   runtime,
			TemplateVars: in.TemplateVars,
			TraefikGen:   traefikgen.NewGenerator(traefikConfigPath),
			TraefikPort:  in.TraefikPort,
		},
		Stores: orchestrator.StoresConfig{
			AppStore:   in.AppStore,
			Settings:   in.Settings,
			Operations: store.NewOperationStore(in.DB),
			Secrets:    in.Secrets,
		},
		SSO: orchestrator.SSOConfig{
			SSO:             ssoProvisioner,
			LDAPOutput:      in.LDAPOutput,
			SSOBaseURL:      in.SSOBaseURL,
			SSOHostSecret:   in.SSOHostSecret,
			SSOAuthentikURL: in.SSOAuthentikURL,
			SSOIssuerURL:    in.SSOIssuerURL,
		},
		Hosts: orchestrator.HostsConfig{
			Hosts:          in.Hosts,
			OnHostsChanged: in.OnHostsChanged,
		},
		CatalogGraph: catalogGraph,
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
