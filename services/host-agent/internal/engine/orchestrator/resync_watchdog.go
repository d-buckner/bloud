// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"sort"
	"sync"
	"time"
)

// DefaultResyncRestartWarnAt is how many consecutive resync-triggered container
// restarts one node accumulates before the watchdog starts raising a signal.
//
// It is a warning threshold and not a limit: the watchdog never withholds a
// restart. Withholding was tried first, and it is unsound. Consecutive resync
// restarts are also exactly what a sequence of legitimate changes looks like,
// and the engine cannot tell the two apart, because a configurator's inputs
// include live reads the engine never sees.
//
// The case that proved it is the calendar aggregation test. Creating the
// operator, then the Radarr and Sonarr feeds landing, then a second user are
// three consecutive changes to Radicale's sharing.csv, and Radicale reads that
// file only at startup -- a share written while the container runs is invisible
// until it restarts, verified by hand: the same bytes on disk yield no shared
// collection before a restart and the shared collection after one. A cap of two
// denied the third restart and left the container serving a tree that disagreed
// with the file on disk, silently, until the next install or reboot. That is
// worse than the loop the cap was invented to prevent.
//
// So the loop is left running and made loud instead. The threshold sits above
// any sequence a healthy instance produces -- one restart is what a change
// costs, a handful is what an install storm with several providers and new
// users costs -- while a configurator that races the app it manages produces one
// restart per pass forever and crosses it within minutes.
const DefaultResyncRestartWarnAt = 5

// ResyncRestartSignal is the surfaced record of one node whose consecutive
// resync restarts crossed the warning threshold. It exists so a configurator
// that never converges is visible on the developer status instead of having to
// be inferred from an app that keeps failing to pick up new wiring.
type ResyncRestartSignal struct {
	// Node is the graph node raising it.
	Node string `json:"node"`
	// Reason is the restart reason the configurator gave on the pass that
	// crossed the threshold.
	Reason string `json:"reason"`
	// Restarts is the consecutive resync restart count at the crossing.
	Restarts int `json:"restarts"`
	// WarnedAt is when the crossing was recorded.
	WarnedAt time.Time `json:"warnedAt"`
}

// resyncWatch counts consecutive resync-triggered restarts per node and keeps
// the signals raised. It lives in the orchestrator rather than in the store
// because it observes a property of the running process: the restarts it counts
// are this process recreating a container. Restarting host-agent gives every app
// a fresh count, which is right, since the operator who just restarted
// host-agent is exactly the person who should get to watch the count climb from
// zero again.
type resyncWatch struct {
	mu      sync.Mutex
	warnAt  int
	counts  map[string]int
	signals map[string]ResyncRestartSignal
	nowFn   func() time.Time
}

func (w *resyncWatch) warnThreshold() int {
	if w.warnAt <= 0 {
		return DefaultResyncRestartWarnAt
	}
	return w.warnAt
}

func (w *resyncWatch) now() time.Time {
	if w.nowFn != nil {
		return w.nowFn()
	}
	return time.Now()
}

// noteRestart records one resync-triggered restart for node and reports whether
// this one crosses a warning boundary, with the record to surface.
//
// The boundary repeats every warnAt restarts rather than firing once, because
// the condition it watches for does not stop on its own. A signal that goes
// quiet after the first crossing is a signal the operator misses while the loop
// is still running, so 5, 10, and 15 all raise.
//
// Every call still leaves the caller free to restart. This function observes; it
// decides nothing about whether the container gets recreated.
func (w *resyncWatch) noteRestart(node, reason string) (bool, ResyncRestartSignal) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.counts == nil {
		w.counts = map[string]int{}
	}

	count := w.counts[node] + 1
	w.counts[node] = count

	threshold := w.warnThreshold()
	if count < threshold || (count-threshold)%threshold != 0 {
		return false, ResyncRestartSignal{}
	}

	record := ResyncRestartSignal{
		Node:     node,
		Reason:   reason,
		Restarts: count,
		WarnedAt: w.now(),
	}
	if w.signals == nil {
		w.signals = map[string]ResyncRestartSignal{}
	}
	w.signals[node] = record
	return true, record
}

// clear drops the node's consecutive count and its signal. A resync that found
// nothing to change is the signal that the app converged, and a full lifecycle
// drive is an intentional event that deserves a fresh count.
func (w *resyncWatch) clear(node string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.counts, node)
	delete(w.signals, node)
}

// snapshot returns every raised signal, sorted by node so the API output is
// stable across calls.
func (w *resyncWatch) snapshot() []ResyncRestartSignal {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.signals) == 0 {
		return nil
	}
	out := make([]ResyncRestartSignal, 0, len(w.signals))
	for _, rec := range w.signals {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

// noteResyncRestart records one resync-triggered restart and reports whether it
// crossed the warning threshold. It never reports whether the restart may
// happen: the caller already decided that, by PreStart reporting a change.
func (o *Orchestrator) noteResyncRestart(node, reason string) (bool, ResyncRestartSignal) {
	return o.resyncWatch().noteRestart(node, reason)
}

// clearResyncWatch resets the node's restart accounting.
func (o *Orchestrator) clearResyncWatch(node string) {
	o.resyncWatch().clear(node)
}

// ResyncRestartSignals reports every raised resync restart signal. The
// developer API surfaces this so a configurator that never converges is
// diagnosable rather than guessed at.
func (o *Orchestrator) ResyncRestartSignals() []ResyncRestartSignal {
	return o.resyncWatch().snapshot()
}

// resyncWatch returns the orchestrator's watchdog, built on first use so a
// hand-constructed Orchestrator (tests, embedded use) has a working one without
// every construction site having to remember it.
func (o *Orchestrator) resyncWatch() *resyncWatch {
	o.resyncWatchOnce.Do(func() {
		o.resyncWatchValue = &resyncWatch{
			warnAt:  o.config.Tuning.ResyncRestartWarnAt,
			counts:  map[string]int{},
			signals: map[string]ResyncRestartSignal{},
		}
	})
	return o.resyncWatchValue
}
