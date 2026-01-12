package catalog

// AppDefinition represents an application in the catalog
type AppDefinition struct {
	Name         string                 `yaml:"name" json:"name"`
	Image        string                 `yaml:"image" json:"image"`
	Integrations map[string]Integration `yaml:"integrations" json:"integrations"`
}

// Integration defines how an app connects to other apps
type Integration struct {
	Required   bool            `yaml:"required" json:"required"`
	Multi      bool            `yaml:"multi" json:"multi"`
	Compatible []CompatibleApp `yaml:"compatible" json:"compatible"`
}

// CompatibleApp defines a specific app that can fulfill an integration
type CompatibleApp struct {
	App      string `yaml:"app" json:"app"`
	Default  bool   `yaml:"default,omitempty" json:"default,omitempty"`
	Category string `yaml:"category,omitempty" json:"category,omitempty"`
}

// IntegrationRef is a back-pointer: which app needs this app, for what integration
type IntegrationRef struct {
	App         string `json:"app"`
	Integration string `json:"integration"`
}

// ConfigTask represents a configuration action to perform
type ConfigTask struct {
	Target      string `json:"target"`
	Source      string `json:"source"`
	Integration string `json:"integration"`
}
