// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"sort"
	"sync"
	"time"
)

// DefaultResyncCostBudget is the wall clock one node's config resync is expected
// to fit inside.
//
// It is a budget for the pass that changes nothing, which is the only kind the
// reconciler runs forever. The bargain in readyForConfigResync is that an
// idempotent config phase finding nothing to change costs a handful of reads, so
// a no-op that takes seconds is a broken bargain even though nothing is failing:
// the node is not stuck, it is just expensive, and the one mechanism that watched
// resync cost before this watched restarts, which a slow no-op never raises.
//
// The value is calibrated between two measured anchors on an idle instance: every
// healthy app's steady state cost under half a second, and the outlier that
// motivated this ran 15.6s of every 60s pass. Five seconds is an order of
// magnitude above the healthy ones, which leaves a slow box room to breathe, and
// a third of the pass that was eating the queue. It is a starting point chosen
// from those two anchors, not a measurement of any particular configurator's
// no-op path: a node that legitimately needs a dozen provider round trips per pass
// will land near it, and the honest response to a signal that fires forever is to
// make the no-op cheaper, not to move the number.
const DefaultResyncCostBudget = 5 * time.Second

// DefaultResyncCostWarnAt is how many consecutive overruns one node accumulates
// before the watch raises a signal.
//
// Consecutive, for the reason the restart watchdog gives at length: a burst of
// legitimate changes looks exactly like a runaway loop from here, and a single
// slow pass is a cold start, a provider that was just installed, or a busy box.
// Three in a row is the point where "slow" stops being an event and starts being
// a property.
const DefaultResyncCostWarnAt = 3

// ResyncCostSignal is the surfaced record of one node whose config resync keeps
// exceeding the budget. Like the restart signal it is an observation, not a
// verdict: the resync still runs, because the diff is the point.
type ResyncCostSignal struct {
	// Node is the graph node raising it.
	Node string `json:"node"`
	// DurationMS is the wall clock of the pass that crossed the threshold.
	DurationMS int64 `json:"durationMs"`
	// MaxMS is the slowest pass recorded for this node since the streak began.
	MaxMS int64 `json:"maxMs"`
	// Overruns is the consecutive-overrun count at the crossing.
	Overruns int `json:"overruns"`
	// BudgetMS is the budget the passes are being measured against.
	BudgetMS int64 `json:"budgetMs"`
	// WarnedAt is when the crossing was recorded.
	WarnedAt time.Time `json:"warnedAt"`
}

// resyncCostWatch counts consecutive over-budget resyncs per node and keeps the
// signals raised. It lives beside resyncWatch for the same reason: it observes the
// running process, so a host-agent restart gives every node a fresh streak.
type resyncCostWatch struct {
	mu      sync.Mutex
	budget  time.Duration
	warnAt  int
	streaks map[string]int
	maxes   map[string]time.Duration
	signals map[string]ResyncCostSignal
	nowFn   func() time.Time
}

func (w *resyncCostWatch) costBudget() time.Duration {
	if w.budget <= 0 {
		return DefaultResyncCostBudget
	}
	return w.budget
}

func (w *resyncCostWatch) warnThreshold() int {
	if w.warnAt <= 0 {
		return DefaultResyncCostWarnAt
	}
	return w.warnAt
}

func (w *resyncCostWatch) now() time.Time {
	if w.nowFn != nil {
		return w.nowFn()
	}
	return time.Now()
}

// note records one resync's wall clock for node and reports whether this one
// crosses a warning boundary, with the record to surface.
//
// A pass inside the budget clears the streak rather than being ignored. The
// condition being watched for is "this node is always slow", and one fast pass
// disproves it; leaving the streak standing would report a node that recovered as
// though it had not.
//
// The boundary repeats every warnAt overruns, for the reason the restart watchdog
// gives: the condition does not stop on its own, and a signal that goes quiet after
// the first crossing is one the operator misses while the cost is still being paid.
func (w *resyncCostWatch) note(node string, d time.Duration) (bool, ResyncCostSignal) {
	w.mu.Lock()
	defer w.mu.Unlock()

	budget := w.costBudget()
	if d <= budget {
		delete(w.streaks, node)
		delete(w.maxes, node)
		delete(w.signals, node)
		return false, ResyncCostSignal{}
	}

	if w.streaks == nil {
		w.streaks = map[string]int{}
	}
	if w.maxes == nil {
		w.maxes = map[string]time.Duration{}
	}

	streak := w.streaks[node] + 1
	w.streaks[node] = streak
	if d > w.maxes[node] {
		w.maxes[node] = d
	}

	threshold := w.warnThreshold()
	if streak < threshold || (streak-threshold)%threshold != 0 {
		return false, ResyncCostSignal{}
	}

	record := ResyncCostSignal{
		Node:       node,
		DurationMS: d.Milliseconds(),
		MaxMS:      w.maxes[node].Milliseconds(),
		Overruns:   streak,
		BudgetMS:   budget.Milliseconds(),
		WarnedAt:   w.now(),
	}
	if w.signals == nil {
		w.signals = map[string]ResyncCostSignal{}
	}
	w.signals[node] = record
	return true, record
}

// clear drops the node's streak and signal. A full lifecycle drive is an
// intentional event, so the accounting starts again afterwards the same way the
// restart accounting does.
func (w *resyncCostWatch) clear(node string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.streaks, node)
	delete(w.maxes, node)
	delete(w.signals, node)
}

// snapshot returns every raised signal, sorted by node so API output is stable.
func (w *resyncCostWatch) snapshot() []ResyncCostSignal {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.signals) == 0 {
		return nil
	}
	out := make([]ResyncCostSignal, 0, len(w.signals))
	for _, rec := range w.signals {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

// noteResyncCost records one resync's wall clock and reports whether it crossed a
// warning boundary. It changes nothing about the pass that just happened.
func (o *Orchestrator) noteResyncCost(node string, d time.Duration) (bool, ResyncCostSignal) {
	return o.resyncCostWatch().note(node, d)
}

// clearResyncCostWatch resets the node's cost accounting alongside its restart
// accounting, so a full drive gives a node a clean slate on both watches.
func (o *Orchestrator) clearResyncCostWatch(node string) {
	o.resyncCostWatch().clear(node)
}

// ResyncCostSignals reports every raised resync cost signal. The developer status
// surfaces this so a configurator whose no-op costs seconds is diagnosable rather
// than something a reader has to infer by timing log lines.
func (o *Orchestrator) ResyncCostSignals() []ResyncCostSignal {
	return o.resyncCostWatch().snapshot()
}

// resyncCostWatch returns the orchestrator's cost watch, built on first use so a
// hand-constructed Orchestrator (tests, embedded use) has a working one without
// every construction site having to remember it.
func (o *Orchestrator) resyncCostWatch() *resyncCostWatch {
	o.resyncCostOnce.Do(func() {
		o.resyncCostValue = &resyncCostWatch{
			budget:  o.config.Tuning.ResyncCostBudget,
			warnAt:  o.config.Tuning.ResyncCostWarnAt,
			streaks: map[string]int{},
			maxes:   map[string]time.Duration{},
		}
	})
	return o.resyncCostValue
}
