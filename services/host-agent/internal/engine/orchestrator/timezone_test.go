// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/timezone"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTimezoneOrchestrator builds an orchestrator with a live clock and an
// in-memory settings store, plus a graph of the named nodes at the given status.
func newTimezoneOrchestrator(t *testing.T, zone string, nodes map[string]graph.NodeStatus,
) (*Orchestrator, *fakeSettings, *timezone.State, *graph.Graph) {
	t.Helper()
	settings := newFakeSettings()
	state := timezone.NewState(zone)
	g := graph.New(graph.NewMapRepository())
	for id, status := range nodes {
		seedRunningNode(t, g, id, status)
	}
	orch := NewOrchestrator(g, new(MockConfiguratorRegistry), NewFakeCatalogCache(), t.TempDir(),
		newTestLogger(), OrchestratorConfig{
			Runtime: RuntimeConfig{Timezone: state},
			Stores:  StoresConfig{Settings: settings},
		})
	return orch, settings, state, g
}

// A new zone is persisted, becomes the live value, and drops the running nodes so
// the pass that follows recreates their containers with the new TZ. A node that
// is not running has nothing to recreate.
func TestApplySetTimezoneIntent(t *testing.T) {
	orch, settings, state, g := newTimezoneOrchestrator(t, "Etc/UTC", map[string]graph.NodeStatus{
		"apps-jellyfin":      graph.StatusRunning,
		"apps-immich-server": graph.StatusRunning,
		"apps-traefik":       graph.StatusInitializing,
	})

	orch.applySetTimezoneIntent(NewSetTimezoneIntent("Europe/Berlin"))

	stored, err := settings.Get(store.SettingTimezone)
	require.NoError(t, err)
	assert.Equal(t, "Europe/Berlin", stored)
	assert.Equal(t, "Europe/Berlin", state.Get())

	for _, id := range []string{"apps-jellyfin", "apps-immich-server"} {
		node, err := g.GetNode(id)
		require.NoError(t, err)
		assert.Equal(t, graph.StatusInitializing, node.ActualStatus,
			"a running node must reset so its container is recreated with the new TZ")
	}
	node, err := g.GetNode("apps-traefik")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusInitializing, node.ActualStatus, "a node that was not running is untouched")
}

// A name nothing can resolve is refused before anything is written: the store,
// the live value, and the node statuses all stay as they were.
func TestApplySetTimezoneIntent_RejectsUnknownZone(t *testing.T) {
	orch, settings, state, g := newTimezoneOrchestrator(t, "Etc/UTC", map[string]graph.NodeStatus{
		"apps-jellyfin": graph.StatusRunning,
	})

	orch.applySetTimezoneIntent(NewSetTimezoneIntent("Mars/Olympus"))

	stored, err := settings.Get(store.SettingTimezone)
	require.NoError(t, err)
	assert.Equal(t, "", stored)
	assert.Equal(t, "Etc/UTC", state.Get())

	node, err := g.GetNode("apps-jellyfin")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus)
}

// Saving the zone that is already in force must not recreate every container on
// the instance for nothing.
func TestApplySetTimezoneIntent_NoOpWhenUnchanged(t *testing.T) {
	orch, settings, _, g := newTimezoneOrchestrator(t, "Europe/Berlin", map[string]graph.NodeStatus{
		"apps-jellyfin": graph.StatusRunning,
	})

	orch.applySetTimezoneIntent(NewSetTimezoneIntent("Europe/Berlin"))

	stored, err := settings.Get(store.SettingTimezone)
	require.NoError(t, err)
	assert.Equal(t, "", stored, "an unchanged save writes nothing")

	node, err := g.GetNode("apps-jellyfin")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus, "an unchanged save restarts nothing")
}

// Clearing the setting is a change like any other: it is persisted, and the
// containers are recreated so they come back on their image default.
func TestApplySetTimezoneIntent_Clear(t *testing.T) {
	orch, settings, state, g := newTimezoneOrchestrator(t, "Europe/Berlin", map[string]graph.NodeStatus{
		"apps-jellyfin": graph.StatusRunning,
	})

	orch.applySetTimezoneIntent(NewSetTimezoneIntent(""))

	stored, err := settings.Get(store.SettingTimezone)
	require.NoError(t, err)
	assert.Equal(t, "", stored)
	assert.Equal(t, "", state.Get())

	node, err := g.GetNode("apps-jellyfin")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusInitializing, node.ActualStatus)
}

// Without the live state the intent cannot be honoured, so it is refused rather
// than persisted and then ignored.
func TestApplySetTimezoneIntent_WithoutState(t *testing.T) {
	orch, settings, _, _ := newTimezoneOrchestrator(t, "Etc/UTC", nil)
	orch.timezone = nil

	orch.applySetTimezoneIntent(NewSetTimezoneIntent("Europe/Berlin"))

	stored, err := settings.Get(store.SettingTimezone)
	require.NoError(t, err)
	assert.Equal(t, "", stored)
}

func TestApplySetTimezoneIntent_Trims(t *testing.T) {
	orch, _, state, _ := newTimezoneOrchestrator(t, "", nil)

	orch.applySetTimezoneIntent(NewSetTimezoneIntent("  Asia/Kolkata  "))

	assert.Equal(t, "Asia/Kolkata", state.Get())
}

// The clock reaches every container from the platform, so an app that declares a
// zone of its own in metadata is overridden rather than honoured. No app in the
// catalog pins a zone; this pins the rule for the day one tries.
func TestComputeContainerSpec_AppliesInstanceTimezone(t *testing.T) {
	orch, _, _, _ := newTimezoneOrchestrator(t, "Europe/Berlin", nil)
	def := catalog.ContainerDef{
		Name:        "apps-demo",
		Image:       "alpine:3.20",
		Environment: map[string]string{"TZ": "Etc/UTC", "HOME": "/data"},
	}

	spec, err := orch.computeContainerSpec(&def, "demo")
	require.NoError(t, err)
	assert.Equal(t, "Europe/Berlin", spec.Environment[timezone.EnvVar])
	assert.Equal(t, "/data", spec.Environment["HOME"], "the app's own env is left alone")
}

// No zone means no TZ: the image keeps its own default, and the absence is
// visible in the spec instead of faked by a value nobody chose.
func TestComputeContainerSpec_NoTimezone(t *testing.T) {
	orch, _, _, _ := newTimezoneOrchestrator(t, "", nil)
	def := catalog.ContainerDef{Name: "apps-demo", Image: "alpine:3.20"}

	spec, err := orch.computeContainerSpec(&def, "demo")
	require.NoError(t, err)
	_, ok := spec.Environment[timezone.EnvVar]
	assert.False(t, ok, "no zone must not invent a TZ value")
}

// A container that declares no environment at all still gets the zone, so the
// rule does not depend on the app having an environment block.
func TestComputeContainerSpec_AddsEnvMapWhenAbsent(t *testing.T) {
	orch, _, _, _ := newTimezoneOrchestrator(t, "Asia/Tokyo", nil)
	def := catalog.ContainerDef{Name: "apps-demo", Image: "alpine:3.20"}

	spec, err := orch.computeContainerSpec(&def, "demo")
	require.NoError(t, err)
	assert.Equal(t, "Asia/Tokyo", spec.Environment[timezone.EnvVar])
}

// The reset is not a special case the engine has to remember: the zone is part of
// the spec, and the runtime recreates a container when the spec revision moves.
// Two zones therefore mean two revisions, and Ensure does the rest.
func TestComputeContainerSpec_TimezoneMovesSpecRevision(t *testing.T) {
	orch, _, state, _ := newTimezoneOrchestrator(t, "Etc/UTC", nil)
	def := catalog.ContainerDef{Name: "apps-demo", Image: "alpine:3.20"}

	before, err := orch.computeContainerSpec(&def, "demo")
	require.NoError(t, err)
	beforeRev, err := before.Revision()
	require.NoError(t, err)

	state.Set("Europe/Berlin")
	after, err := orch.computeContainerSpec(&def, "demo")
	require.NoError(t, err)
	afterRev, err := after.Revision()
	require.NoError(t, err)

	assert.NotEqual(t, beforeRev, afterRev, "a zone change must move the spec revision so Ensure recreates")
}
