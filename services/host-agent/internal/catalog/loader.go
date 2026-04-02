package catalog

import (
	"fmt"
	"io/fs"
	"os"
	"path"

	"gopkg.in/yaml.v3"
)

// Loader handles loading app definitions from YAML files
type Loader struct {
	fsys fs.FS
}

// NewLoader creates a catalog loader that reads from the given directory path.
func NewLoader(appsDir string) *Loader {
	return &Loader{fsys: os.DirFS(appsDir)}
}

// NewLoaderFromFS creates a catalog loader that reads from an fs.FS.
// Use this with the embedded app registry for self-contained binaries.
func NewLoaderFromFS(fsys fs.FS) *Loader {
	return &Loader{fsys: fsys}
}

// LoadAll loads all app definitions from the apps directory
// Each app has its own subdirectory with a metadata.yaml file
func (l *Loader) LoadAll() (map[string]*App, error) {
	apps := make(map[string]*App)

	entries, err := fs.ReadDir(l.fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to read apps directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		metadataPath := path.Join(entry.Name(), "metadata.yaml")
		if _, err := fs.Stat(l.fsys, metadataPath); err != nil {
			continue
		}

		app, err := l.loadAppFromFile(metadataPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load %s: %w", entry.Name(), err)
		}

		apps[app.Name] = app
	}

	return apps, nil
}

// loadAppFromFile loads a single app definition from a YAML file
func (l *Loader) loadAppFromFile(filePath string) (*App, error) {
	data, err := fs.ReadFile(l.fsys, filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var app App
	if err := yaml.Unmarshal(data, &app); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	if err := l.validateApp(&app); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	return &app, nil
}

// validateApp validates that an app definition has all required fields
func (l *Loader) validateApp(app *App) error {
	if app.Name == "" {
		return fmt.Errorf("app name is required")
	}
	if app.DisplayName == "" {
		return fmt.Errorf("displayName is required")
	}
	if app.Description == "" {
		return fmt.Errorf("description is required")
	}
	if app.Category == "" {
		return fmt.Errorf("category is required")
	}
	return nil
}

// LoadGraph loads app definitions and builds an AppGraph
func (l *Loader) LoadGraph() (*AppGraph, error) {
	entries, err := fs.ReadDir(l.fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to read apps directory: %w", err)
	}

	var apps []*AppDefinition

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		metadataPath := path.Join(entry.Name(), "metadata.yaml")
		if _, err := fs.Stat(l.fsys, metadataPath); err != nil {
			continue
		}

		app, err := l.loadAppDefinition(metadataPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load %s: %w", entry.Name(), err)
		}

		apps = append(apps, app)
	}

	return NewGraph(apps), nil
}

// loadAppDefinition loads a single AppDefinition from a YAML file
func (l *Loader) loadAppDefinition(filePath string) (*AppDefinition, error) {
	data, err := fs.ReadFile(l.fsys, filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var app AppDefinition
	if err := yaml.Unmarshal(data, &app); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	if app.Name == "" {
		return nil, fmt.Errorf("app name is required")
	}

	return &app, nil
}
