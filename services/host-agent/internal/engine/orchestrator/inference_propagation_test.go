// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
)

// The resolution tests cover what a binding says. These cover what a settings
// change *does*: the intent reaches the drain switch, the consumer node is
// dropped out of RUNNING so its own PreStart rewrites it, and the no-op guard
// keeps a meaningless save from churning the stack.

// propagationHarness wires a consumer with one container node parked in RUNNING,
// which is the state a live install sits in.
func propagationHarness(t *testing.T) (*Orchestrator, *fakeSettings) {
	t.Helper()

	appStore := NewFakeAppStore()
	install(t, appStore, "hermes", nil)

	consumer := inferenceConsumer("hermes", litellmSource, instanceSrc)
	consumer.Containers = []catalog.ContainerDef{{Name: "apps-hermes", Image: "example/hermes"}}

	orch, _ := bindingsOrchestrator(t, appStore, consumer)
	require.NoError(t, orch.graph.AddNode("apps-hermes"))
	require.NoError(t, orch.graph.SetActualStatus("apps-hermes", graph.StatusRunning, ""))
	return orch, orch.settings.(*fakeSettings)
}

func upstreamPayload(t *testing.T, baseURL, defaultModel string) SetInferenceIntent {
	t.Helper()
	upstreams, err := inference.EncodeUpstreams([]inference.Upstream{
		{ID: "default", Name: "Primary", BaseURL: baseURL, Enabled: true},
	})
	require.NoError(t, err)
	return NewSetInferenceIntent(upstreams, defaultModel, nil)
}

// A settings change must drop the consumer out of RUNNING. That transition is the
// whole propagation mechanism: the next pass re-runs PreStart, which rewrites
// whatever the app manages from the new binding.
func TestApplySetInferenceIntent_ResetsRunningConsumer(t *testing.T) {
	orch, _ := propagationHarness(t)

	orch.applySetInferenceIntent(upstreamPayload(t, "http://one.example.test/v1", "model-a"))

	node, err := orch.graph.GetNode("apps-hermes")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusInitializing, node.ActualStatus,
		"a settings change must reset a running consumer so its PreStart re-runs")
}

// The drain switch has to dispatch the intent explicitly. Falling through to the
// default arm would log "unhandled intent type" on every settings save, which is
// how a real warning becomes noise.
func TestApplyIntents_DispatchesSetInferenceIntent(t *testing.T) {
	orch, settings := propagationHarness(t)

	orch.applyIntents(
		[]Intent{upstreamPayload(t, "http://two.example.test/v1", "model-b")},
		map[string]bool{},
	)

	node, err := orch.graph.GetNode("apps-hermes")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusInitializing, node.ActualStatus)

	// And the values it persisted are the canonical ones the API echoed.
	assert.Equal(t, "model-b", settings.values[inference.SettingDefaultModel])
	assert.Contains(t, settings.values[inference.SettingUpstreams], "two.example.test")
}

// The no-op guard is what keeps the UI from restarting every wired app when a
// save only reformatted the same configuration.
func TestApplySetInferenceIntent_NoOpGuardSkipsReset(t *testing.T) {
	orch, _ := propagationHarness(t)

	orch.applySetInferenceIntent(upstreamPayload(t, "http://same.example.test/v1", "model-x"))
	require.Equal(t, graph.StatusInitializing, mustNode(t, orch, "apps-hermes").ActualStatus)
	// Park it back at RUNNING so the second save has something it must not touch.
	require.NoError(t, orch.graph.SetActualStatus("apps-hermes", graph.StatusRunning, ""))

	orch.applySetInferenceIntent(upstreamPayload(t, "http://same.example.test/v1", "model-x"))

	assert.Equal(t, graph.StatusRunning, mustNode(t, orch, "apps-hermes").ActualStatus,
		"an identical save must not reset the consumer")
}

// A consumer that is not RUNNING is left alone. This is deliberate: forcing a
// failing app back to Initializing fights whatever is broken, and the periodic
// self-healing pass (#137) is the right place that case gets picked up. The
// assertion documents the contract rather than blessing it silently.
func TestApplySetInferenceIntent_LeavesNonRunningConsumerAlone(t *testing.T) {
	orch, _ := propagationHarness(t)
	require.NoError(t, orch.graph.SetActualStatus("apps-hermes", graph.StatusError, "boom"))

	orch.applySetInferenceIntent(upstreamPayload(t, "http://three.example.test/v1", "model-c"))

	node := mustNode(t, orch, "apps-hermes")
	assert.Equal(t, graph.StatusError, node.ActualStatus,
		"a failed consumer is not forced; the self-healing pass owns that case")
	// The settings themselves still land, so the pass has them when it comes.
	assert.Equal(t, "model-c", orch.settings.(*fakeSettings).values[inference.SettingDefaultModel])
}

// Clearing the configuration must not be swallowed by the guard: empty is a real
// change from non-empty, and consumers have to be reset so they strip the wiring.
func TestApplySetInferenceIntent_ClearingConfigResetsConsumer(t *testing.T) {
	orch, _ := propagationHarness(t)
	orch.applySetInferenceIntent(upstreamPayload(t, "http://four.example.test/v1", "model-d"))
	require.NoError(t, orch.graph.SetActualStatus("apps-hermes", graph.StatusRunning, ""))

	orch.applySetInferenceIntent(NewSetInferenceIntent("[]", "", nil))

	assert.Equal(t, graph.StatusInitializing, mustNode(t, orch, "apps-hermes").ActualStatus,
		"removing the upstream is a change, not a no-op")
}

func mustNode(t *testing.T, orch *Orchestrator, id string) *graph.Node {
	t.Helper()
	n, err := orch.graph.GetNode(id)
	require.NoError(t, err)
	require.NotNil(t, n)
	return n
}
