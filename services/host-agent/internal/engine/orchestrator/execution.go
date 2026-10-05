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

// runConfigurator drives a single node through its lifecycle, or re-runs its
// config phases for a node that is already RUNNING (the resync case).
//
// Returns true when the node reached its target status this pass, which is what
// puts it in changedIDs and gets it promoted to RUNNING after routes sync. A
// resync that changed nothing returns false; a resync that recreated the
// container returns true, because the node left RUNNING on the way through the
// restart and has to come back.
func (o *Orchestrator) runConfigurator(ctx context.Context, id string) bool {
	node, err := o.graph.GetNode(id)
	if err != nil || node == nil {
		return false
	}

	// Already at target: re-run the config phases, so the configurator gets
	// its diff against the outside world. The container is left alone unless
	// PreStart says the config changed.
	if node.ActualStatus == graph.StatusRunning && node.TargetStatus == graph.StatusRunning {
		o.logger.Info("dispatching config resync", "app", id)
		return o.runResync(ctx, id)
	}

	o.logger.Info("dispatching full lifecycle", "app", id, "actual", node.ActualStatus, "target", node.TargetStatus)
	return o.runFullLifecycle(ctx, id, node)
}

// runResync re-runs the config phases for an already-RUNNING node: PreStart,
// then PostStart, with the container recreated only if PreStart reports that
// the config actually changed.
//
// PreStart belongs here for the same reason PostStart does: the inputs to the
// files it writes move without raising any intent that would drive this node.
// The case that motivated it is a provider installed after its consumer. Hermes
// renders its `mcp_servers` map in PreStart from the resolved `mcp` contract,
// and installing caldav-mcp afterwards resolves a new binding for it, but
// Hermes sits at RUNNING. A resync that only ran PostStart could observe the
// new binding and still leave Hermes running on the config written before that
// app existed, so the namespace never arrived. The same shape covers a
// provider's address changing or a credential being rotated.
//
// The cost is one PreStart per running app per pass, which is affordable
// because the same contract that makes the PostStart resync affordable makes
// this one: PreStart is required to be idempotent, and `managedfile.Write`
// reports changed=false when the bytes already match. The shared conformance
// harness asserts a second PreStart pass asks for no recreate for every app in
// the catalog, so a steady-state resync is a read-only diff.
//
// A restart is capped by the resync breaker, because the one failure mode this
// path can create that the old one could not is a restart loop: an app that
// rewrites the file Bloud manages while it is running makes every pass report a
// change. See resync_breaker.go.
func (o *Orchestrator) runResync(ctx context.Context, id string) bool {
	cfg := o.registry.Get(id)
	if cfg == nil {
		return false
	}
	appID := o.ownerApp(id)
	state, err := o.buildAppState(id)
	if err != nil {
		o.logger.Warn("resync: failed to build state", "app", id, "error", err)
		return false
	}

	prestart, ok := o.runResyncPreStart(ctx, id, appID, cfg, state)
	if !ok {
		return false
	}
	if !prestart.RestartNeeded {
		// Nothing changed: the app converged, so any earlier trip is stale.
		o.clearResyncBreaker(id)
		o.runResyncPostStart(ctx, id, appID, cfg, state)
		return false
	}
	return o.runResyncRestart(ctx, id, appID, cfg, state, prestart)
}

// runResyncPreStart runs PreStart on the resync path and reports whether the
// resync may continue.
//
// A failure here is handled differently from the full-lifecycle path on
// purpose. There, a failed PreStart parks the node in ERROR because the
// container has not started and must not start on a config that could not be
// written. Here the container is up and serving: a failed config diff means
// the wiring is stale, not that the app is down. Taking a working app to ERROR
// because an update failed would be a worse outcome than the status quo, so
// the failure is recorded as retryable on the operation row and the node
// stays RUNNING for the next pass to retry.
func (o *Orchestrator) runResyncPreStart(ctx context.Context, id, appID string, cfg configurator.NodeLifecycle, state *configurator.AppState) (configurator.PreStartResult, bool) {
	o.logger.Info("resync: running PreStart", "app", id)
	prestart, err := cfg.PreStart(ctx, state)
	if err != nil {
		o.logger.Warn("resync: PreStart failed", "app", id, "error", err)
		o.ensureOpDrive(appID)
		o.recordOpFail(appID, store.OpPhasePrestart, opCause(id, appID, err), true)
		return configurator.PreStartResult{}, false
	}
	o.logger.Info("resync: PreStart complete",
		"app", id,
		"restart_needed", prestart.RestartNeeded,
		"restart_reason", prestart.Reason)
	return prestart, true
}

// runResyncRestart carries out the recreate PreStart asked for, then runs
// PostStart. Returns true when the node came back through the restart and
// finalization cleanly, so the pass promotes it to RUNNING.
//
// After the restart the node sits at STARTING, not RUNNING: it left RUNNING on
// the way in, so it has to be promoted on the way out the same way a normal
// drive promotes. Returning true is what puts it in changedIDs.
func (o *Orchestrator) runResyncRestart(ctx context.Context, id, appID string, cfg configurator.NodeLifecycle, state *configurator.AppState, prestart configurator.PreStartResult) bool {
	def, appCatalogID := o.containerDefForNode(id)
	if def == nil {
		// No container to recreate: the config change is on disk, and
		// PostStart still gets its diff. Nothing to restart means nothing to
		// count against the breaker.
		o.logger.Info("resync: PreStart asks for a restart but the node has no container def",
			"app", id, "reason", prestart.Reason)
		o.runResyncPostStart(ctx, id, appID, cfg, state)
		return false
	}

	decision, record := o.allowResyncRestart(id, prestart.Reason)
	switch decision {
	case resyncTrippedNow:
		o.logger.Warn("resync restart denied by the breaker; the config diff is not converging",
			"app", id, "restarts", record.Restarts, "reason", record.Reason,
			"note", "this is the shape of an app that rewrites the file Bloud manages; "+
				"Bloud will stop restarting it until a resync converges or an install drives it")
		o.recordActivity("resync_breaker_tripped", id+": "+record.Reason)
		return false
	case resyncAlreadyTripped:
		o.logger.Info("resync restart still suppressed by the breaker", "app", id, "reason", prestart.Reason)
		return false
	}

	if !o.runContainerPhases(ctx, id, appID, def, appCatalogID, prestart) {
		return false
	}
	return o.runPostStartPhase(ctx, id, appID, cfg, state)
}

// runResyncPostStart re-runs PostStart for a resync that needed no restart:
// the periodic pass giving the configurator its diff against the outside
// world. The container is left alone.
//
// A failure is recorded as retryable and leaves the node RUNNING, the same
// convention as before: the app is serving, only the finalization did not
// complete.
func (o *Orchestrator) runResyncPostStart(ctx context.Context, id, appID string, cfg configurator.NodeLifecycle, state *configurator.AppState) {
	o.logger.Info("resync: running PostStart", "app", id)
	if err := o.runPostStart(ctx, cfg, state); err != nil {
		o.logger.Warn("resync: PostStart failed", "app", id, "error", err)
		o.ensureOpDrive(appID)
		o.recordOpFail(appID, store.OpPhasePoststart, opCause(id, appID, err), true)
		return
	}
	o.healOp(appID)
	o.logger.Info("resync: PostStart complete", "app", id)
}

// runPostStart invokes a configurator's PostStart bounded by the framework's
// PostStartBudget (DefaultPostStartBudget when unset). The budget ctx is
// derived from the pass ctx, so a Stop()-cancellation propagates immediately
// while the budget independently caps a hung finalization. The framework, not
// the app, owns this ceiling; apps use the ctx they are given directly.
func (o *Orchestrator) runPostStart(ctx context.Context, cfg configurator.NodeLifecycle, state *configurator.AppState) error {
	budget := o.config.Tuning.PostStartBudget
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
	if o.config.Runtime.Containers == nil {
		return nil
	}

	o.ensureNetworksForContainer(ctx, def)

	spec, err := o.computeContainerSpec(def, appCatalogID)
	if err != nil {
		return err
	}
	o.ensureMountDirs(def.Name, spec)

	if _, err := o.config.Runtime.Containers.Ensure(ctx, spec); err != nil {
		return fmt.Errorf("ensure container: %w", err)
	}
	return nil
}

// computeContainerSpec renders the spec the current catalog produces for one
// container, including the issuer extra-host pin. It is pure: no networks or
// mount directories are created, so callers can use it to diff a desired spec
// against a running container without side effects.
func (o *Orchestrator) computeContainerSpec(def *catalog.ContainerDef, appCatalogID string) (containerruntime.Spec, error) {
	spec, err := ContainerSpecFromDef(*def, appCatalogID, o.dataDir, o.config.Runtime.TemplateVars)
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
		if err := o.config.Runtime.Containers.EnsureNetwork(ctx, network); err != nil {
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

	// A full drive is an intentional event: an install, a reboot, a crash
	// recovery, or an explicit reset. Whatever the resync breaker was
	// guarding, this pass is not the loop it tripped on, so the node gets a
	// fresh allowance.
	o.clearResyncBreaker(id)

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
		_ = o.config.Runtime.Containers.Remove(ctx, def.Name)
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
	if o.config.Tuning.HealthCheckTimeout > 0 {
		var cancel context.CancelFunc
		healthCtx, cancel = context.WithTimeout(ctx, o.config.Tuning.HealthCheckTimeout)
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
