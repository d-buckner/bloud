// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

// The resync cost watch: the second thing about a config resync that can be wrong
// without anything failing. resync_watchdog.go watches a node that keeps
// restarting; this watches a node whose no-op pass keeps costing seconds, which
// never restarts anything and so is invisible to the other watch. Both observe and
// report; neither withholds work.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func TestResyncCostWatch_SignalsAtThresholdAndRepeats(t *testing.T) {
	frozen := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	w := &resyncCostWatch{
		budget: time.Second,
		warnAt: 2,
		nowFn:  func() time.Time { return frozen },
	}

	raised, _ := w.note("apps-slow", 3*time.Second)
	assert.False(t, raised, "one slow pass is one slow pass; nothing to say yet")

	raised, signal := w.note("apps-slow", 5*time.Second)
	require.True(t, raised, "the second consecutive overrun crosses the threshold")
	assert.Equal(t, "apps-slow", signal.Node)
	assert.Equal(t, 2, signal.Overruns)
	assert.Equal(t, int64(5000), signal.DurationMS)
	assert.Equal(t, int64(5000), signal.MaxMS, "the slowest pass so far is the one being reported")
	assert.Equal(t, int64(1000), signal.BudgetMS)
	assert.Equal(t, frozen, signal.WarnedAt)

	raised, signal = w.note("apps-slow", 4*time.Second)
	assert.False(t, raised, "the boundary repeats every threshold, not every pass")
	assert.Equal(t, 3, w.streaks["apps-slow"])

	raised, signal = w.note("apps-slow", 9*time.Second)
	require.True(t, raised, "and the crossing at the next threshold is raised, because the cost has not stopped")
	assert.Equal(t, 4, signal.Overruns)
	assert.Equal(t, int64(9000), signal.MaxMS, "the max survives to the crossing that reports it")
}

func TestResyncCostWatch_AFastPassClearsTheStreak(t *testing.T) {
	w := &resyncCostWatch{budget: time.Second, warnAt: 2}

	w.note("apps-slow", 3*time.Second)
	raised, _ := w.note("apps-slow", 3*time.Second)
	require.True(t, raised)
	require.Len(t, w.snapshot(), 1)

	raised, _ = w.note("apps-slow", 500*time.Millisecond)
	assert.False(t, raised)
	assert.Empty(t, w.snapshot(), "a node that recovered must not still be reported")
	assert.Zero(t, w.streaks["apps-slow"], "and the count begins again from zero")

	raised, _ = w.note("apps-slow", 3*time.Second)
	assert.False(t, raised, "one slow pass after a fast one is not a streak")
}

func TestResyncCostWatch_DefaultsApplyWhenUnset(t *testing.T) {
	w := &resyncCostWatch{}

	assert.Equal(t, DefaultResyncCostBudget, w.costBudget())
	assert.Equal(t, DefaultResyncCostWarnAt, w.warnThreshold())
}

func TestResyncCostWatch_SnapshotIsSortedByNode(t *testing.T) {
	w := &resyncCostWatch{budget: time.Second, warnAt: 1}
	w.note("apps-zulu", 2*time.Second)
	w.note("apps-alpha", 2*time.Second)

	got := w.snapshot()
	require.Len(t, got, 2)
	assert.Equal(t, []string{"apps-alpha", "apps-zulu"}, []string{got[0].Node, got[1].Node})
}

// The watch is wired into the resync itself, not bolted on beside it: every pass
// a running node takes is measured, and a slow one that repeats is what raises.
func TestResync_CostWatchSignalsThroughThePass(t *testing.T) {
	h := newResyncHarnessWithCost(t, 0, 10*time.Millisecond, 2)
	h.cfg.setPrestart(configurator.NoRestart(), nil)
	h.cfg.setDelay(60 * time.Millisecond)

	h.pass(t)
	assert.Empty(t, h.orch.ResyncCostSignals(), "one slow pass is not yet a pattern")

	h.pass(t)
	require.Len(t, h.orch.ResyncCostSignals(), 1)
	assert.Equal(t, resyncNode, h.orch.ResyncCostSignals()[0].Node)

	require.Len(t, h.orch.Status().ResyncCostSignals, 1,
		"and the developer status carries it, so this is diagnosable without timing log lines")

	h.cfg.setDelay(0)
	h.pass(t)
	assert.Empty(t, h.orch.ResyncCostSignals(), "a pass back inside the budget clears the signal")
}

// A node that stays inside the budget says nothing, however many passes go by.
// This is the assertion that keeps the watch from being noise on a healthy box.
func TestResync_CostWatchStaysQuietInsideTheBudget(t *testing.T) {
	h := newResyncHarnessWithCost(t, 0, time.Minute, 1)
	h.cfg.setPrestart(configurator.NoRestart(), nil)

	for i := 0; i < 3; i++ {
		h.pass(t)
	}

	assert.Empty(t, h.orch.ResyncCostSignals())
	assert.Empty(t, h.orch.Status().ResyncCostSignals)
}
