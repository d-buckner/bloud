// SPDX-License-Identifier: AGPL-3.0-only

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

	return json.Marshal(map[string]any{
		"apps": apps,
	})
}

// IsSystemApp reports whether the app is Bloud's own plumbing rather than
// something a person installs.
//
// The declared flag is the answer, and it is the only place the catalog says
// it. This function used to read `category` against a set that held
// `infrastructure`, and the two do not agree. Traefik declares `category:
// network` and Authentik declares `security`, so the category test let both
// system apps into the user catalog, which is what the dashboard then had to
// hide on its own. The same test hid every app that honestly called itself
// infrastructure: Hindsight declares that category and disappeared from
// GET /api/apps with no error raised anywhere.
func IsSystemApp(app *App) bool {
	return app.IsSystem
}

// GetUserApps returns only user-facing apps (excluding the apps Bloud runs for
// the whole instance)
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
