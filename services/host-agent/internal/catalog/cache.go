package catalog

import (
	"encoding/json"
	"fmt"
	"sort"
)

// MemoryCache handles caching app catalog in memory
type MemoryCache struct {
	apps map[string]*App
}

// NewMemoryCache creates a new in-memory catalog cache
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{apps: make(map[string]*App)}
}

// Refresh loads all apps from the catalog and updates the in-memory cache
func (c *MemoryCache) Refresh(loader *Loader) error {
	apps, err := loader.LoadAll()
	if err != nil {
		return fmt.Errorf("failed to load apps: %w", err)
	}

	c.apps = make(map[string]*App, len(apps))
	for name, app := range apps {
		c.apps[name] = app
	}

	return nil
}

// GetAll returns all apps from the cache, sorted by name
func (c *MemoryCache) GetAll() ([]*App, error) {
	apps := make([]*App, 0, len(c.apps))
	for _, app := range c.apps {
		apps = append(apps, app)
	}
	sort.Slice(apps, func(i, j int) bool {
		return apps[i].Name < apps[j].Name
	})
	return apps, nil
}

// Get returns a single app from the cache by name
func (c *MemoryCache) Get(name string) (*App, error) {
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
