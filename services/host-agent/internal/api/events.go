package api

import (
	"sync"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
)

// AppEventHub manages SSE subscribers for app state updates
type AppEventHub struct {
	subscribers map[chan []*store.InstalledApp]struct{}
	mu          sync.RWMutex
	appStore    store.AppStoreInterface
}

// NewAppEventHub creates a new app event hub
func NewAppEventHub(appStore store.AppStoreInterface) *AppEventHub {
	return &AppEventHub{
		subscribers: make(map[chan []*store.InstalledApp]struct{}),
		appStore:    appStore,
	}
}

// Subscribe creates a new subscription channel for app updates
func (h *AppEventHub) Subscribe() chan []*store.InstalledApp {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch := make(chan []*store.InstalledApp, 10)
	h.subscribers[ch] = struct{}{}
	return ch
}

// Unsubscribe removes a subscription channel
func (h *AppEventHub) Unsubscribe(ch chan []*store.InstalledApp) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.subscribers, ch)
	close(ch)
}

// Broadcast sends the current app list to all subscribers
func (h *AppEventHub) Broadcast() {
	apps, err := h.appStore.GetAll()
	if err != nil {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for ch := range h.subscribers {
		select {
		case ch <- apps:
		default:
			// Channel full, skip this subscriber
		}
	}
}

// SubscriberCount returns the number of active subscribers
func (h *AppEventHub) SubscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers)
}
