// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"sort"
	"sync"
	"time"
)

// DefaultResyncRestartCap is how many consecutive resync-triggered container
// restarts one node may be granted before the breaker stops granting them.
//
// Two is the smallest number that separates "the config changed" from "the
// config never stops changing". One restart is what a real change costs; a
// second is what a change that landed on top of the first looks like. A third
// request in a row with nothing else happening means the diff is not
// converging, and the most likely cause is an app that rewrites the file Bloud
// manages while it runs: Bloud writes, the app writes back, Bloud restarts,
// forever, once a minute. The conformance harness cannot see that, because it
// runs PreStart against a data directory with no live app inside it.
const DefaultResyncRestartCap = 2

// resyncDecision is what the breaker says about one restart request.
type resyncDecision int

const (
	// resyncGranted: go ahead and restart.
	resyncGranted resyncDecision = iota
	// resyncTrippedNow: this request tripped the breaker, so do not restart,
	// and this is the first refusal (the one worth a WARN).
	resyncTrippedNow
	// resyncAlreadyTripped: the breaker was tripped before this pass, so do
	// not restart and do not repeat the WARN.
	resyncAlreadyTripped
)

// ResyncBreakerState is the surfaced record of one node whose resync restart
// breaker has tripped. It is what makes a stopped restart visible instead of
// leaving the operator with an app that is quietly running stale config.
type ResyncBreakerState struct {
	// Node is the graph node the breaker guards.
	Node string `json:"node"`
	// Reason is the restart reason the configurator gave on the pass that
	// tripped the breaker.
	Reason string `json:"reason"`
	// Restarts is how many consecutive resync restarts were granted before
	// the breaker tripped.
	Restarts int `json:"restarts"`
	// TrippedAt is when the breaker fired.
	TrippedAt time.Time `json:"trippedAt"`
}

// resyncBreaker tracks, per node, the consecutive resync-triggered restarts
// and the record of a trip. It lives in the orchestrator rather than in the
// store because it guards a property of the running process: the loop it stops
// is this process restarting a container over and over. Restarting host-agent
// gives every app a fresh allowance, which is the right thing, since the
// operator who just restarted host-agent is exactly the person who should get
// another attempt at converging.
type resyncBreaker struct {
	mu      sync.Mutex
	limit   int
	counts  map[string]int
	tripped map[string]ResyncBreakerState
	nowFn   func() time.Time
}

func (b *resyncBreaker) restartCap() int {
	if b.limit <= 0 {
		return DefaultResyncRestartCap
	}
	return b.limit
}

func (b *resyncBreaker) now() time.Time {
	if b.nowFn != nil {
		return b.nowFn()
	}
	return time.Now()
}

// allowRestart grants or refuses one resync-triggered restart for node, and
// returns the trip record alongside the decision so the caller owns the
// logging. The breaker itself stays a pure accounting object.
func (b *resyncBreaker) allowRestart(node, reason string) (resyncDecision, ResyncBreakerState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.counts == nil {
		b.counts = map[string]int{}
	}
	if b.tripped == nil {
		b.tripped = map[string]ResyncBreakerState{}
	}

	count := b.counts[node] + 1
	if count <= b.restartCap() {
		b.counts[node] = count
		return resyncGranted, ResyncBreakerState{}
	}

	record := ResyncBreakerState{
		Node:      node,
		Reason:    reason,
		Restarts:  b.counts[node],
		TrippedAt: b.now(),
	}
	if _, already := b.tripped[node]; already {
		return resyncAlreadyTripped, record
	}
	b.tripped[node] = record
	return resyncTrippedNow, record
}

// clear drops the node's consecutive count and any trip record. A resync that
// found nothing to change is the signal that the app converged, and a full
// lifecycle drive is an intentional event that deserves a fresh allowance.
func (b *resyncBreaker) clear(node string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.counts, node)
	delete(b.tripped, node)
}

// snapshot returns every tripped breaker, sorted by node so the API output is
// stable across calls.
func (b *resyncBreaker) snapshot() []ResyncBreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.tripped) == 0 {
		return nil
	}
	out := make([]ResyncBreakerState, 0, len(b.tripped))
	for _, rec := range b.tripped {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

// allowResyncRestart is the orchestrator-facing gate for one resync restart.
// It reports the decision and, when the breaker tripped, the record to log.
func (o *Orchestrator) allowResyncRestart(node, reason string) (resyncDecision, ResyncBreakerState) {
	return o.breaker().allowRestart(node, reason)
}

// clearResyncBreaker resets the node's restart accounting.
func (o *Orchestrator) clearResyncBreaker(node string) {
	o.breaker().clear(node)
}

// ResyncBreakers reports every tripped resync restart breaker. The developer
// API surfaces this so a stopped restart is visible rather than inferred from
// an app that never picks up new wiring.
func (o *Orchestrator) ResyncBreakers() []ResyncBreakerState {
	return o.breaker().snapshot()
}

// breaker returns the orchestrator's resync breaker, built on first use so a
// hand-constructed Orchestrator (tests, embedded use) has a working one
// without every construction site having to remember it.
func (o *Orchestrator) breaker() *resyncBreaker {
	o.breakerOnce.Do(func() {
		o.breakerValue = &resyncBreaker{
			limit:   o.config.Tuning.ResyncRestartCap,
			counts:  map[string]int{},
			tripped: map[string]ResyncBreakerState{},
		}
	})
	return o.breakerValue
}
