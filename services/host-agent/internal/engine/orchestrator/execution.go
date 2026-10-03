// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// runConfigurator drives a single node through its lifecycle, or re-runs
// PostStart only for a node that is already RUNNING (the resync case).
// Returns true only when the node successfully transitions to its target status
// for the first time this pass (a PostStart resync returns false).
func (o *Orchestrator) runConfigurator(ctx context.Context, id string) bool {
	node, err := o.graph.GetNode(id)
	if err != nil || node == nil {
		return false
	}

	// Already at target: re-run PostStart only, so the configurator gets its
	// diff against the outside world without disturbing the container.
	if node.ActualStatus == graph.StatusRunning && node.TargetStatus == graph.StatusRunning {
		o.logger.Info("dispatching PostStart resync", "app", id)
		o.runPostStartOnly(ctx, id)
		return false // resyncs don't propagate changedIDs further
	}

	o.logger.Info("dispatching full lifecycle", "app", id, "actual", node.ActualStatus, "target", node.TargetStatus)
	return o.runFullLifecycle(ctx, id, node)
}

// runPostStartOnly re-runs PostStart for an already-RUNNING node: because a
// direct dependency just became available, or because the periodic pass is
// giving the configurator its diff against the outside world. The container is
// left alone either way.
func (o *Orchestrator) runPostStartOnly(ctx context.Context, id string) {
	cfg := o.registry.Get(id)
	if cfg == nil {
		return
	}
	appID := o.ownerApp(id)
	state, err := o.buildAppState(id)
	if err != nil {
		o.logger.Warn("PostStart resync: failed to build state", "app", id, "error", err)
		return
	}
	o.logger.Info("PostStart resync: running PostStart", "app", id)
	if err := o.runPostStart(ctx, cfg, state); err != nil {
		o.logger.Warn("PostStart resync: PostStart failed", "app", id, "error", err)
		o.ensureOpDrive(appID)
		o.recordOpFail(appID, store.OpPhasePoststart, opCause(id, appID, err), true)
		return
	}
	o.healOp(appID)
	o.logger.Info("PostStart resync: PostStart complete", "app", id)
}

// runPostStart invokes a configurator's PostStart bounded by the framework's
// PostStartBudget (DefaultPostStartBudget when unset). The budget ctx is
// derived from the pass ctx, so a Stop()-cancellation propagates immediately
// while the budget independently caps a hung finalization. The framework, not
// the app, owns this ceiling; apps use the ctx they are given directly.
func (o *Orchestrator) runPostStart(ctx context.Context, cfg configurator.NodeLifecycle, state *configurator.AppState) error {
	budget := o.config.PostStartBudget
	if budget <= 0 {
		budget = DefaultPostStartBudget
	}
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return cfg.PostStart(bctx, state)
}

// ensureContainerFromDef ensures a container exists and is running from a ContainerDef,
// creating required networks and mount directories first.
func (o *Orchestrator) ensureContainerFromDef(ctx context.Context, def *catalog.ContainerDef, appCatalogID string) error {
	if o.config.Containers == nil {
		return nil
	}

	o.ensureNetworksForContainer(ctx, def)

	spec, err := o.computeContainerSpec(def, appCatalogID)
	if err != nil {
		return err
	}
	o.ensureMountDirs(def.Name, spec)

	if _, err := o.config.Containers.Ensure(ctx, spec); err != nil {
		return fmt.Errorf("ensure container: %w", err)
	}
	return nil
}

// computeContainerSpec renders the spec the current catalog produces for one
// container, including the issuer extra-host pin. It is pure: no networks or
// mount directories are created, so callers can use it to diff a desired spec
// against a running container without side effects.
func (o *Orchestrator) computeContainerSpec(def *catalog.ContainerDef, appCatalogID string) (containerruntime.Spec, error) {
	spec, err := ContainerSpecFromDef(*def, appCatalogID, o.dataDir, o.config.TemplateVars)
	if err != nil {
		return containerruntime.Spec{}, fmt.Errorf("build container spec: %w", err)
	}
	o.applyIssuerExtraHost(&spec, appCatalogID)
	return spec, nil
}

// ensureNetworksForContainer creates every user-defined network the
// container references ("host" mode needs no creation). Failures are
// logged, not fatal: Ensure() surfaces the real error later.
func (o *Orchestrator) ensureNetworksForContainer(ctx context.Context, def *catalog.ContainerDef) {
	var networks []string
	if def.Network != "" {
		networks = append(networks, def.Network)
	}
	for _, n := range def.Networks {
		if n != def.Network {
			networks = append(networks, n)
		}
	}
	for _, network := range networks {
		if network == "host" {
			continue // host network mode doesn't need to be created
		}
		if err := o.config.Containers.EnsureNetwork(ctx, network); err != nil {
			o.logger.Warn("failed to ensure network", "container", def.Name, "network", network, "error", err)
		}
	}
}

// applyIssuerExtraHost adds the OIDC issuer host-gateway mapping to native
// OIDC app containers so they can reach the issuer by the same hostname
// browsers use (token exchange happens inside the container). Apps on the
// loopback issuer (sso.loopbackIssuer) are skipped: they share the host
// network namespace, where localhost already resolves to Traefik and the
// shared issuer hostname is never used.
func (o *Orchestrator) applyIssuerExtraHost(spec *containerruntime.Spec, appCatalogID string) {
	if o.hosts == nil || o.catalog == nil {
		return
	}
	catalogApp, err := o.catalog.Get(appCatalogID)
	if err != nil || catalogApp == nil || catalogApp.SSO.Strategy != "native-oidc" {
		return
	}
	if catalogApp.SSO.LoopbackIssuer {
		return
	}
	ehost := o.hosts.Get().IssuerExtraHost()
	if ehost == "" {
		// A https issuer gets no pin: the container resolves the real public
		// name and reaches the TLS terminator that serves it.
		return
	}
	if !hasExtraHost(spec.ExtraHosts, ehost) {
		spec.ExtraHosts = append(spec.ExtraHosts, ehost)
	}
}

// ensureMountDirs creates the source directory for each directory mount.
// File mounts (.yml/.yaml/.json/.conf) are skipped: their parents are
// created by whoever generates the file.
func (o *Orchestrator) ensureMountDirs(containerName string, spec containerruntime.Spec) {
	for _, mount := range spec.Mounts {
		if isFileMountPath(mount.Source) {
			continue
		}
		if err := os.MkdirAll(mount.Source, 0755); err != nil {
			o.logger.Warn("failed to create mount directory", "container", containerName, "path", mount.Source, "error", err)
		}
	}
}

// isFileMountPath reports whether a mount source names a config file rather
// than a directory.
func isFileMountPath(source string) bool {
	for _, ext := range []string{".yml", ".yaml", ".json", ".conf"} {
		if strings.HasSuffix(source, ext) {
			return true
		}
	}
	return false
}

// hasExtraHost reports whether the spec already carries the given host:target
// extraHosts entry.
func hasExtraHost(entries []string, want string) bool {
	for _, e := range entries {
		if e == want {
			return true
		}
	}
	return false
}

// runFullLifecycle executes all lifecycle phases for a node that has not yet
// reached its target status.
func (o *Orchestrator) runFullLifecycle(ctx context.Context, id string, node *graph.Node) bool {
	// Target INITIALIZING means "unmanage": snap actual to match and stop.
	if node.TargetStatus == graph.StatusInitializing {
		_ = o.graph.SetActualStatus(id, graph.StatusInitializing, "")
		return false
	}

	def, appCatalogID := o.containerDefForNode(id)
	cfg := o.registry.Get(id)

	if cfg == nil && def == nil {
		o.logger.Info("no configurator registered, will mark RUNNING after route generation", "app", id)
		return true
	}

	owner := o.ownerApp(id)
	o.ensureOpDrive(owner)

	state, err := o.buildAppState(id)
	if err != nil {
		o.logger.Error("failed to build app state", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		o.recordOpFail(owner, store.OpPhasePlanning, opCause(id, owner, err), true)
		return false
	}

	prestart, ok := o.runPreStartPhase(ctx, id, owner, cfg, state)
	if !ok {
		return false
	}

	// SSO provisioning: ensure the forward-auth provider exists in Authentik before the
	// container starts, so requests can be authenticated immediately on first boot.
	if err := o.ensureSSO(ctx, id); err != nil {
		o.failNode(id, owner, store.OpPhasePrestart, err, "SSO provisioning failed")
		return false
	}

	// If the catalog changed this app's SSO strategy, deprovision the old one
	// now that the new one is live, then record the new strategy.
	o.reconcileSSOStrategy(ctx, id)

	if def != nil && !o.runContainerPhases(ctx, id, owner, def, appCatalogID, prestart) {
		return false
	}

	if cfg != nil && !o.runPostStartPhase(ctx, id, owner, cfg, state) {
		return false
	}

	o.logger.Info("lifecycle phases complete, will mark RUNNING after route generation", "app", id)
	return true
}

// failNode parks a node in ERROR and records the failure on its operation row.
// Every phase failure goes through here so the log line, the status write, and
// the ledger entry cannot drift apart.
func (o *Orchestrator) failNode(id, owner, phase string, err error, msg string) {
	o.logger.Warn(msg, "app", id, "error", err)
	_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
	o.recordOpFail(owner, phase, opCause(id, owner, err), true)
}

// runPreStartPhase runs the configurator's PreStart and reports whether the
// lifecycle may continue. The result is empty when there is no configurator.
func (o *Orchestrator) runPreStartPhase(ctx context.Context, id, owner string, cfg configurator.NodeLifecycle, state *configurator.AppState) (configurator.PreStartResult, bool) {
	if cfg == nil {
		return configurator.PreStartResult{}, true
	}
	o.logger.Info("lifecycle phase: PreStart", "app", id)
	o.recordOpPhase(owner, store.OpPhasePrestart)
	_ = o.graph.SetActualStatus(id, graph.StatusPreStartConfig, "")
	prestart, err := cfg.PreStart(ctx, state)
	if err != nil {
		o.failNode(id, owner, store.OpPhasePrestart, err, "PreStart failed")
		return configurator.PreStartResult{}, false
	}
	o.logger.Info("lifecycle phase: PreStart complete",
		"app", id,
		"restart_needed", prestart.RestartNeeded,
		"restart_reason", prestart.Reason)
	return prestart, true
}

// runContainerPhases recreates the container when PreStart asked for it, then
// brings it up and waits for its health check.
func (o *Orchestrator) runContainerPhases(ctx context.Context, id, owner string, def *catalog.ContainerDef, appCatalogID string, prestart configurator.PreStartResult) bool {
	// If PreStart asked for a recreate, remove the existing container first so
	// Ensure() creates a fresh one that picks up the change.
	if prestart.RestartNeeded {
		o.logger.Info("PreStart requires recreate, removing container",
			"app", id, "reason", prestart.Reason)
		_ = o.config.Containers.Remove(ctx, def.Name)
	}
	o.logger.Info("lifecycle phase: EnsureContainer", "app", id)
	o.recordOpPhase(owner, store.OpPhaseTopology)
	_ = o.graph.SetActualStatus(id, graph.StatusStarting, "")
	if err := o.ensureContainerFromDef(ctx, def, appCatalogID); err != nil {
		o.failNode(id, owner, store.OpPhaseTopology, err, "EnsureContainer failed")
		return false
	}
	o.logger.Info("lifecycle phase: EnsureContainer complete", "app", id)
	return o.runHealthPhase(ctx, id, owner, def)
}

// runHealthPhase waits for the container's own health check under a bounded
// context, so a container that never becomes healthy cannot pin the pass.
func (o *Orchestrator) runHealthPhase(ctx context.Context, id, owner string, def *catalog.ContainerDef) bool {
	o.logger.Info("lifecycle phase: HealthCheck", "app", id)
	o.recordOpPhase(owner, store.OpPhaseHealth)
	healthCtx := ctx
	if o.config.HealthCheckTimeout > 0 {
		var cancel context.CancelFunc
		healthCtx, cancel = context.WithTimeout(ctx, o.config.HealthCheckTimeout)
		defer cancel()
	}
	if def.HealthCheck != nil {
		if err := o.runContainerHealthCheck(healthCtx, def.Name, def.HealthCheck); err != nil {
			o.failNode(id, owner, store.OpPhaseHealth, err, "HealthCheck failed")
			return false
		}
	}
	o.logger.Info("lifecycle phase: HealthCheck complete", "app", id)
	return true
}

// runPostStartPhase runs finalization under the framework's PostStartBudget so
// the wait is bounded and Stop() can interrupt it (apps no longer detach their
// own contexts). A failure whose cause is the canceled pass context is an
// interruption, not a fault: leave the node where it is so the next start
// re-converges, rather than parking a shutdown in ERROR (R3).
func (o *Orchestrator) runPostStartPhase(ctx context.Context, id, owner string, cfg configurator.NodeLifecycle, state *configurator.AppState) bool {
	o.logger.Info("lifecycle phase: PostStart", "app", id)
	o.recordOpPhase(owner, store.OpPhasePoststart)
	_ = o.graph.SetActualStatus(id, graph.StatusPostStartConfig, "")
	if err := o.runPostStart(ctx, cfg, state); err != nil {
		if ctx.Err() != nil {
			o.logger.Info("PostStart interrupted by shutdown; leaving status for re-converge", "app", id, "error", err)
			o.recordOpFail(owner, store.OpPhasePoststart, opCause(id, owner, fmt.Errorf("interrupted by shutdown: %w", err)), true)
			return false
		}
		o.failNode(id, owner, store.OpPhasePoststart, err, "PostStart failed")
		return false
	}
	o.logger.Info("lifecycle phase: PostStart complete", "app", id)
	return true
}
