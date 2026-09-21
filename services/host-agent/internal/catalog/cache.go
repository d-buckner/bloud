// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package catalog

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// MemoryCache handles caching app catalog in memory.
//
// All access goes through mu. Refresh runs (POST /api/apps/refresh-catalog,
// boot) while orchestrator goroutines read the map from graph event handlers
// and applyIssuerExtraHost; unsynchronized, that races into the unrecoverable
// "fatal error: concurrent map read and map write".
type MemoryCache struct {
	mu   sync.RWMutex
	apps map[string]*App
}

// NewMemoryCache creates a new in-memory catalog cache
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{apps: make(map[string]*App)}
}

// Refresh loads all apps from the catalog and updates the in-memory cache.
// The new map is built off the lock and swapped in as a single write-locked
// operation: readers never observe a half-filled cache, and the disk load
// does not block them.
func (c *MemoryCache) Refresh(loader *Loader) error {
	apps, err := loader.LoadAll()
	if err != nil {
		return fmt.Errorf("failed to load apps: %w", err)
	}

	next := make(map[string]*App, len(apps))
	for name, app := range apps {
		next[name] = app
	}

	c.mu.Lock()
	c.apps = next
	c.mu.Unlock()
	return nil
}

// GetAll returns all apps from the cache, sorted by name
func (c *MemoryCache) GetAll() ([]*App, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	apps := make([]*App, 0, len(c.apps))
	for _, app := range c.apps {
		apps = append(apps, app)
	}
	sort.Slice(apps, func(i, j int) bool {
		return apps[i].CatalogID < apps[j].CatalogID
	})
	return apps, nil
}

// Get returns a single app from the cache by name
func (c *MemoryCache) Get(name string) (*App, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	app, ok := c.apps[name]
	if !ok {
		return nil, fmt.Errorf("app not found: %s", name)
	}
	return app, nil
}

// GetAllAsJSON returns all apps from the cache as JSON (for API responses)
func (c *MemoryCache) GetAllAsJSON() ([]byte, error) {
	apps, err := c.GetAll()
	if err != nil {
		return nil, err
	}

	return json.Marshal(map[string]interface{}{
		"apps": apps,
	})
}

// SystemCategories defines categories that are hidden from users
var SystemCategories = map[string]bool{
	"infrastructure": true,
}

// IsSystemApp returns true if the app is a system/infrastructure app
func IsSystemApp(app *App) bool {
	return SystemCategories[app.Category]
}

// GetUserApps returns only user-facing apps (excluding infrastructure)
func (c *MemoryCache) GetUserApps() ([]*App, error) {
	allApps, err := c.GetAll()
	if err != nil {
		return nil, err
	}

	var userApps []*App
	for _, app := range allApps {
		if !IsSystemApp(app) {
			userApps = append(userApps, app)
		}
	}

	return userApps, nil
}

// IsSystemAppByName checks if an app name corresponds to a system app
func (c *MemoryCache) IsSystemAppByName(name string) bool {
	app, err := c.Get(name)
	if err != nil {
		return false
	}
	return IsSystemApp(app)
}
