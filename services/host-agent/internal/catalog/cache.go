package catalog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Cache handles caching app catalog in the database
type Cache struct {
	db *sql.DB
}

// NewCache creates a new catalog cache
func NewCache(db *sql.DB) *Cache {
	return &Cache{db: db}
}

// Refresh loads all apps from the catalog and updates the database cache
func (c *Cache) Refresh(loader *Loader) error {
	// Load all apps from YAML files
	apps, err := loader.LoadAll()
	if err != nil {
		return fmt.Errorf("failed to load apps: %w", err)
	}

	// Start transaction
	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	// Clear existing cache
	if _, err := tx.Exec("DELETE FROM catalog_cache"); err != nil {
		return fmt.Errorf("failed to clear cache: %w", err)
	}

	// Insert each app into cache
	stmt, err := tx.Prepare(`
		INSERT INTO catalog_cache (name, yaml_content, updated_at)
		VALUES (?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for name, app := range apps {
		// Marshal app back to YAML for storage
		yamlData, err := yaml.Marshal(app)
		if err != nil {
			return fmt.Errorf("failed to marshal app %s: %w", name, err)
		}

		if _, err := stmt.Exec(name, string(yamlData), time.Now()); err != nil {
			return fmt.Errorf("failed to insert app %s: %w", name, err)
		}
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// GetAll returns all apps from the cache
func (c *Cache) GetAll() ([]*App, error) {
	rows, err := c.db.Query("SELECT yaml_content FROM catalog_cache ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("failed to query catalog: %w", err)
	}
	defer rows.Close()

	var apps []*App
	for rows.Next() {
		var yamlContent string
		if err := rows.Scan(&yamlContent); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		var app App
		if err := yaml.Unmarshal([]byte(yamlContent), &app); err != nil {
			return nil, fmt.Errorf("failed to unmarshal app: %w", err)
		}

		apps = append(apps, &app)
	}

	return apps, nil
}

// Get returns a single app from the cache by name
func (c *Cache) Get(name string) (*App, error) {
	var yamlContent string
	err := c.db.QueryRow("SELECT yaml_content FROM catalog_cache WHERE name = ?", name).Scan(&yamlContent)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("app not found: %s", name)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query app: %w", err)
	}

	var app App
	if err := yaml.Unmarshal([]byte(yamlContent), &app); err != nil {
		return nil, fmt.Errorf("failed to unmarshal app: %w", err)
	}

	return &app, nil
}

// GetAllAsJSON returns all apps from the cache as JSON (for API responses)
func (c *Cache) GetAllAsJSON() ([]byte, error) {
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
func (c *Cache) GetUserApps() ([]*App, error) {
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
func (c *Cache) IsSystemAppByName(name string) bool {
	app, err := c.Get(name)
	if err != nil {
		return false
	}
	return IsSystemApp(app)
}
