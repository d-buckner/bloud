// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"fmt"
	"time"

	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// setupStatusSync registers a graph event handler that keeps the app store's
// status field in sync with lifecycle graph transitions. This is the single
// authoritative path from graph state → DB status; no caller needs to call
// appStore.UpdateStatus for lifecycle-driven transitions.
func (o *Orchestrator) setupStatusSync() {
	if o.appStore == nil {
		return
	}
	o.graph.On(graph.EventNodeUpdated, func(node graph.Node) {
		appID := o.ownerApp(node.ID)
		if appID == node.ID {
			// Single-container node: direct status mapping.
			switch node.ActualStatus {
			case graph.StatusRunning:
				_ = o.appStore.UpdateStatus(appID, store.AppStatusRunning)
				_ = o.appStore.SetLastError(appID, "")
				o.recordOpComplete(appID)
			case graph.StatusError:
				_ = o.appStore.UpdateStatus(appID, store.AppStatusError)
				_ = o.appStore.SetLastError(appID, node.Error)
			}
			return
		}
		// Multi-container node: aggregate across all containers.
		// Error fires immediately on any container; running only when all are up.
		switch node.ActualStatus {
		case graph.StatusError:
			_ = o.appStore.UpdateStatus(appID, store.AppStatusError)
			_ = o.appStore.SetLastError(appID, node.Error)
		case graph.StatusRunning:
			if o.allContainersRunning(appID) {
				_ = o.appStore.UpdateStatus(appID, store.AppStatusRunning)
				_ = o.appStore.SetLastError(appID, "")
				o.recordOpComplete(appID)
			}
		}
	})
}

// setupNodeEvents broadcasts lifecycle node transitions on the event bus so
// API subscribers (the SSE stream) can surface per-container progress and
// errors without polling.
func (o *Orchestrator) setupNodeEvents() {
	if o.events == nil {
		return
	}
	o.graph.On(graph.EventNodeUpdated, func(node graph.Node) {
		o.events.Publish(eventbus.Event{
			Type: eventbus.TypeNode,
			Node: &eventbus.NodeInfo{
				App:       o.ownerApp(node.ID),
				Container: node.ID,
				Phase:     phaseForStatus(node.ActualStatus),
				Error:     node.Error,
			},
		})
	})
}

// setupPullEvents wires image pull progress from the container runtime into
// the event bus so SSE subscribers see live pull percentages. Runtimes that
// don't implement PullProgressReporter (e.g. test doubles) are skipped.
func (o *Orchestrator) setupPullEvents() {
	if o.events == nil || o.config.Containers == nil {
		return
	}
	reporter, ok := o.config.Containers.(containerruntime.PullProgressReporter)
	if !ok {
		return
	}
	reporter.SetPullProgressReporter(func(containerName, image string, p containerruntime.PullProgress) {
		o.events.Publish(eventbus.Event{
			Type: eventbus.TypePull,
			Pull: &eventbus.PullInfo{
				App:     o.ownerApp(containerName),
				Image:   image,
				Phase:   p.Phase,
				Percent: p.Percent,
				Detail:  pullDetail(p),
			},
		})
	})
}

// pullDetail renders a user-facing pull progress detail, e.g.
// "34% (356.5 MiB of 1.0 GiB)", falling back to the raw status line when no
// sizes are known.
func pullDetail(p containerruntime.PullProgress) string {
	if p.Total > 0 {
		return fmt.Sprintf("%d%% (%s of %s)", p.Percent, humanBytes(p.Current), humanBytes(p.Total))
	}
	return p.Detail
}

// humanBytes formats a byte count as a compact binary unit (1.0 GiB).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// phaseForStatus maps a lifecycle graph state to the user-facing phase name
// shown in the dashboard.
func phaseForStatus(s graph.NodeStatus) string {
	switch s {
	case graph.StatusInitializing:
		return "queued"
	case graph.StatusPreStartConfig:
		return "configuring"
	case graph.StatusStarting:
		return "starting"
	case graph.StatusPostStartConfig:
		return "finalizing"
	case graph.StatusRunning:
		return "running"
	case graph.StatusError:
		return "failed"
	default:
		return string(s)
	}
}

// allContainersRunning returns true when every container node for appID has
// reached StatusRunning. Returns true for single-container apps (no rollup needed).
func (o *Orchestrator) allContainersRunning(appID string) bool {
	if o.catalog == nil {
		return true
	}
	catalogApp, err := o.catalog.Get(appID)
	if err != nil || catalogApp == nil {
		return true
	}
	defs := catalogApp.ContainerDefs()
	if len(catalogApp.Containers) == 0 {
		return true
	}
	for _, def := range defs {
		node, err := o.graph.GetNode(def.Name)
		if err != nil || node == nil || node.ActualStatus != graph.StatusRunning {
			return false
		}
	}
	return true
}

// recordActivity appends an event to the ring buffer and, when an event bus
// is configured, publishes it to live API subscribers.
func (o *Orchestrator) recordActivity(event, detail string) {
	now := time.Now()
	o.activityMu.Lock()
	o.activityBuf[o.activityPos] = ActivityEvent{Time: now, Event: event, Detail: detail}
	o.activityPos = (o.activityPos + 1) % maxOrchestratorEvents
	o.activityMu.Unlock()
	if o.events != nil {
		o.events.Publish(eventbus.Event{
			Type:     eventbus.TypeActivity,
			Activity: &eventbus.ActivityInfo{Time: now, Event: event, Detail: detail},
		})
	}
}

// Status returns a snapshot of the orchestrator's current state.
func (o *Orchestrator) Status() OrchestratorStatus {
	o.activityMu.Lock()
	recent := make([]ActivityEvent, 0, maxOrchestratorEvents)
	for i := 0; i < maxOrchestratorEvents; i++ {
		idx := (o.activityPos - 1 - i + maxOrchestratorEvents) % maxOrchestratorEvents
		if o.activityBuf[idx].Event == "" {
			continue
		}
		recent = append(recent, o.activityBuf[idx])
	}
	o.activityMu.Unlock()

	return OrchestratorStatus{
		QueueDepth:     o.queue.PendingCount(),
		IsConverging:   o.converging.Load(),
		RecentActivity: recent,
		LoopStopped:    o.Stopped(),
		LastConverged:  o.LastConverged(),
	}
}

// NodePhases returns the user-facing phase (see phaseForStatus) of every
// lifecycle graph node, keyed by node ID: the container name for
// multi-container apps, the catalog ID for single-container apps. Read-only
// snapshot for the developer dashboard; nil when the graph is unavailable.
func (o *Orchestrator) NodePhases() map[string]string {
	if o.graph == nil {
		return nil
	}
	nodes, err := o.graph.Nodes()
	if err != nil {
		o.logger.Warn("failed to read graph nodes", "error", err)
		return nil
	}
	phases := make(map[string]string, len(nodes))
	for _, node := range nodes {
		phases[node.ID] = phaseForStatus(node.ActualStatus)
	}
	return phases
}
