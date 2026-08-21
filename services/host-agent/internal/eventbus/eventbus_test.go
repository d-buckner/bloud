// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package eventbus

import (
	"testing"
	"time"
)

func TestPublishFansOutToAllSubscribers(t *testing.T) {
	b := New()
	ch1, cancel1 := b.Subscribe()
	defer cancel1()
	ch2, cancel2 := b.Subscribe()
	defer cancel2()

	if got := b.SubscriberCount(); got != 2 {
		t.Fatalf("SubscriberCount = %d, want 2", got)
	}

	b.Publish(Event{Type: TypeActivity, Activity: &ActivityInfo{Event: "converge_start", Detail: "1"}})

	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case evt := <-ch:
			if evt.Type != TypeActivity || evt.Activity.Event != "converge_start" {
				t.Fatalf("subscriber %d: unexpected event %+v", i, evt)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for event", i)
		}
	}
}

func TestPublishWithNoSubscribersDoesNotPanic(t *testing.T) {
	b := New()
	b.Publish(Event{Type: TypeAppsChanged})
	b.Publish(Event{Type: TypeNode, Node: &NodeInfo{App: "jellyfin"}})
}

func TestOverflowDropsEventAndForcesResync(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	defer cancel()

	// Fill the buffer to capacity.
	for i := 0; i < subscriberBuffer; i++ {
		b.Publish(Event{Type: TypeNode, Node: &NodeInfo{App: "fill"}})
	}
	// One more event: buffer full → dropped, resync marked pending.
	b.Publish(Event{Type: TypeNode, Node: &NodeInfo{App: "overflow"}})

	// The buffer holds exactly the 64 fill events; none lost, none extra.
	for i := 0; i < subscriberBuffer; i++ {
		select {
		case evt := <-ch:
			if evt.Type != TypeNode || evt.Node.App != "fill" {
				t.Fatalf("position %d: unexpected event %+v", i, evt)
			}
		default:
			t.Fatalf("drain stopped early at %d", i)
		}
	}
	select {
	case evt := <-ch:
		t.Fatalf("expected empty channel, got %+v", evt)
	default:
	}

	// The next publish must deliver the forced resync before the event.
	b.Publish(Event{Type: TypeNode, Node: &NodeInfo{App: "after"}})
	select {
	case evt := <-ch:
		if evt.Type != TypeAppsChanged {
			t.Fatalf("expected forced apps-changed resync, got %+v", evt)
		}
	default:
		t.Fatal("resync not delivered after consumer drained")
	}
	select {
	case evt := <-ch:
		if evt.Type != TypeNode || evt.Node.App != "after" {
			t.Fatalf("expected the post-overflow event, got %+v", evt)
		}
	default:
		t.Fatal("event after resync not delivered")
	}
}

func TestUnsubscribeClosesChannelAndStopsDelivery(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel was not closed after cancel")
	}

	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("SubscriberCount = %d after cancel, want 0", got)
	}

	// Publishing after cancel must not panic.
	b.Publish(Event{Type: TypeAppsChanged})
}

func TestPublishIsNonBlockingForSlowSubscriber(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	defer cancel()

	// Publish well beyond the buffer without ever reading; must not block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*4; i++ {
			b.Publish(Event{Type: TypeActivity, Activity: &ActivityInfo{Event: "x"}})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	_ = ch
}
