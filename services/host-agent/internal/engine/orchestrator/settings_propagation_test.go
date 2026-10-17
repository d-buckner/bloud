// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Submit -> drain -> live state.
//
// The API handlers read the live clock and the live address, and the dashboard's
// convergence poll compares its answer against the value the save reported. That
// makes one chain load-bearing: an intent submitted through the public entry
// point must reach the same state object the handler reads.
//
// Both halves have their own tests: `applySetTimezoneIntent` is tested directly,
// and the handlers are tested against a state a test sets. Neither covers the
// queue between them. These cases do, by using the drain the engine uses.

func TestSubmitSetTimezoneIntent_ReachesLiveState(t *testing.T) {
	orch, settings, state, _ := newTimezoneOrchestrator(t, "Etc/UTC", nil)

	orch.Submit(NewSetTimezoneIntent("Europe/Berlin"))
	orch.applyIntents(orch.queue.Drain(), map[string]bool{})

	assert.Equal(t, "Europe/Berlin", state.Get(), "the live clock the API reads")
	assert.Equal(t, "Europe/Berlin", settings.values[store.SettingTimezone], "the stored value")
}

func TestSubmitSetPublicURLIntent_ReachesLiveState(t *testing.T) {
	settings := newFakeSettings()
	public, err := hostset.ParsePublicURL("http://localhost:8080")
	require.NoError(t, err)
	hosts := hostset.NewState(hostset.New(public))

	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		new(MockConfiguratorRegistry),
		NewFakeCatalogCache(),
		t.TempDir(),
		newTestLogger(),
		OrchestratorConfig{
			Hosts:  HostsConfig{Hosts: hosts},
			Stores: StoresConfig{Settings: settings},
		},
	)

	orch.Submit(NewSetPublicURLIntent("https://bloud.example.com"))
	orch.applyIntents(orch.queue.Drain(), map[string]bool{})

	assert.Equal(t, "https://bloud.example.com", hosts.Get().PrimaryBaseURL(), "the live address the API reads")
	assert.Equal(t, "https://bloud.example.com", settings.values[store.SettingPublicURL], "the stored value")
}

// A save of the value already in force must not move the state or reset nodes,
// so the dashboard's poll settles on its first check with no read at all.
func TestSubmitSetTimezoneIntent_SameZoneIsANoOp(t *testing.T) {
	orch, _, state, g := newTimezoneOrchestrator(t, "Europe/Berlin", map[string]graph.NodeStatus{
		"apps-jellyfin": graph.StatusRunning,
	})

	orch.Submit(NewSetTimezoneIntent("Europe/Berlin"))
	orch.applyIntents(orch.queue.Drain(), map[string]bool{})

	assert.Equal(t, "Europe/Berlin", state.Get())

	node, err := g.GetNode("apps-jellyfin")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus, "no reset for a no-op save")
}
