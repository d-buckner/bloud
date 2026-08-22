// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package eventbus provides a small in-process pub/sub hub for broadcasting
// host-agent state changes (app lifecycle, orchestrator activity) to
// subscribers such as SSE stream handlers.
//
// Publish is non-blocking: a subscriber that falls behind (full buffer)
// drops the event and receives a forced apps-changed event on the next
// publish so it can resync from a full snapshot.
package eventbus

import (
	"sync"
	"time"
)

// Type identifies the kind of bus event.
type Type string

const (
	// TypeAppsChanged indicates the app store changed; consumers should
	// resync from a fresh snapshot. It carries no payload.
	TypeAppsChanged Type = "apps-changed"
	// TypeNode indicates a lifecycle graph node changed phase or error.
	TypeNode Type = "node"
	// TypeActivity indicates a new orchestrator activity entry.
	TypeActivity Type = "activity"
	// TypePull indicates a container image pull progress update.
	TypePull Type = "pull"
)

// NodeInfo describes a single lifecycle graph node transition.
type NodeInfo struct {
	App       string `json:"app"`
	Container string `json:"container"`
	Phase     string `json:"phase"`
	Error     string `json:"error,omitempty"`
}

// ActivityInfo is an orchestrator activity-log entry.
type ActivityInfo struct {
	Time   time.Time `json:"time"`
	Event  string    `json:"event"`
	Detail string    `json:"detail"`
}

// PullInfo is a container image pull progress update.
type PullInfo struct {
	App     string `json:"app"`
	Image   string `json:"image"`
	Phase   string `json:"phase"` // "pulling" | "done"
	Percent int    `json:"percent,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Event is a single bus event. Exactly one payload field is set, selected by
// Type (TypeAppsChanged carries no payload).
type Event struct {
	Type     Type
	Node     *NodeInfo
	Activity *ActivityInfo
	Pull     *PullInfo
}

// subscriberBuffer is the per-subscriber event buffer capacity before
// drop-and-resync kicks in.
const subscriberBuffer = 64

type subscriber struct {
	ch chan Event
	// resyncPending is set when an event was dropped for this subscriber;
	// the next publish delivers a forced resync first so the subscriber
	// re-reads full state.
	resyncPending bool
}

// Bus is an in-process fan-out of state-change events. It is safe for
// concurrent use.
type Bus struct {
	mu   sync.Mutex
	subs map[int]*subscriber
	next int
}

// New creates an empty Bus.
func New() *Bus {
	return &Bus{subs: make(map[int]*subscriber)}
}

// Publish fans an event out to all subscribers without blocking the
// publisher. A subscriber whose buffer is full drops the event and is marked
// for resync: the next publish sends it a forced TypeAppsChanged first, so it
// re-reads full state from a snapshot.
func (b *Bus) Publish(evt Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sub := range b.subs {
		if sub.resyncPending {
			select {
			case sub.ch <- Event{Type: TypeAppsChanged}:
				sub.resyncPending = false
			default:
				// Still full: keep the resync pending for the next publish.
			}
		}
		select {
		case sub.ch <- evt:
		default:
			// Subscriber is behind: drop and force a resync instead of
			// blocking the publisher.
			sub.resyncPending = true
		}
	}
}

// Subscribe registers a new subscriber and returns its event channel plus a
// cancel function that unregisters it (closing the channel).
func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub := &subscriber{ch: make(chan Event, subscriberBuffer)}
	id := b.next
	b.next++
	b.subs[id] = sub
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if s, ok := b.subs[id]; ok {
			close(s.ch)
			delete(b.subs, id)
		}
	}
	return sub.ch, cancel
}

// SubscriberCount returns the number of active subscribers (for tests and
// diagnostics).
func (b *Bus) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
