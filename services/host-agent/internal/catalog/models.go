// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package catalog

// App represents an application in the catalog
type App struct {
	CatalogID     string                 `yaml:"name" json:"catalogId"`
	DisplayName   string                 `yaml:"displayName" json:"displayName"`
	Description   string                 `yaml:"description" json:"description"`
	Category      string                 `yaml:"category" json:"category"`
	Icon          string                 `yaml:"icon" json:"icon"`
	Screenshots   []string               `yaml:"screenshots" json:"screenshots"`
	Version       string                 `yaml:"version" json:"version"`
	Port          int                    `yaml:"port" json:"port"`
	// EstimatedSizeMB is the approximate total image download size, so the
	// catalog can set expectations before a long pull. Zero when unknown; the
	// API falls back to local `podman image inspect` sizes when available.
	EstimatedSizeMB int                  `yaml:"estimatedSizeMB,omitempty" json:"estimatedSizeMB,omitempty"`
	IsSystem      bool                   `yaml:"isSystem" json:"isSystem"`
	Dependencies  []string               `yaml:"dependencies" json:"dependencies"`
	Resources     Resources              `yaml:"resources" json:"resources"`
	SSO           SSO                    `yaml:"sso" json:"sso"`
	DefaultConfig map[string]interface{} `yaml:"defaultConfig" json:"defaultConfig"`
	Docs         Docs                   `yaml:"docs" json:"docs"`
	Tags         []string               `yaml:"tags" json:"tags"`
	Routing      *Routing               `yaml:"routing,omitempty" json:"routing,omitempty"`
	Integrations map[string]Integration `yaml:"integrations" json:"integrations"`
	Containers   []ContainerDef         `yaml:"containers,omitempty" json:"containers,omitempty"`
}

// ContainerDef describes one container in a multi-container app.
type ContainerDef struct {
	Name          string                `yaml:"name" json:"name"`
	Image         string                `yaml:"image" json:"image"`
	Command       []string              `yaml:"command,omitempty" json:"command,omitempty"`
	Network       string                `yaml:"network,omitempty" json:"network,omitempty"`
	Networks      []string              `yaml:"networks,omitempty" json:"networks,omitempty"`
	RestartPolicy string                `yaml:"restartPolicy,omitempty" json:"restartPolicy,omitempty"`
	Environment   map[string]string     `yaml:"environment,omitempty" json:"environment,omitempty"`
	ExtraHosts    []string              `yaml:"extraHosts,omitempty" json:"extraHosts,omitempty"` // host:ip entries (e.g. "sso.localhost:host-gateway")
	Ports         []ContainerPort       `yaml:"ports,omitempty" json:"ports,omitempty"`
	Volumes       []ContainerVolume     `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	DependsOn     []string              `yaml:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	HealthCheck   *ContainerHealthCheck `yaml:"healthCheck,omitempty" json:"healthCheck,omitempty"`
}

// ContainerHealthCheck defines a container-level health check command.
type ContainerHealthCheck struct {
	Test     []string `yaml:"test" json:"test"`
	Interval int      `yaml:"interval" json:"interval"` // seconds between checks
	Timeout  int      `yaml:"timeout" json:"timeout"`   // seconds before check is considered failed
	Retries  int      `yaml:"retries" json:"retries"`   // consecutive failures before marking unhealthy
}

// ContainerDefs returns the app's container definitions.
// Returns nil if no containers are defined.
func (a *App) ContainerDefs() []ContainerDef {
	return a.Containers
}

// ContainerPort maps a host port to a container port.
type ContainerPort struct {
	Host      int    `yaml:"host" json:"host"`
	Container int    `yaml:"container" json:"container"`
	Protocol  string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
}

// ContainerVolume mounts a host path into a container.
type ContainerVolume struct {
	Source      string   `yaml:"source" json:"source"`
	Destination string   `yaml:"destination" json:"destination"`
	Options     []string `yaml:"options,omitempty" json:"options,omitempty"`
}

// Resources defines resource requirements for an app
type Resources struct {
	MinRam  int  `yaml:"minRam" json:"minRam"`   // MB
	MinDisk int  `yaml:"minDisk" json:"minDisk"` // GB
	GPU     bool `yaml:"gpu" json:"gpu"`
}

// SSO defines SSO integration configuration
type SSO struct {
	Strategy     string   `yaml:"strategy" json:"strategy"`                           // native-oidc, forward-auth, none
	BypassPaths  []string `yaml:"bypassPaths,omitempty" json:"bypassPaths,omitempty"` // Paths exempt from forward-auth (forward-auth only)
	CallbackPath string   `yaml:"callbackPath" json:"callbackPath"`                   // e.g. /oauth2/oidc/callback
	ProviderName string   `yaml:"providerName" json:"providerName"`                   // e.g. "Bloud SSO"
	UserCreation bool     `yaml:"userCreation" json:"userCreation"`                   // Auto-create users on first login
	LaunchPath   string   `yaml:"launchPath" json:"launchPath,omitempty"`             // Initial path to open when launching the app (overrides root)
	Env          SSOEnv   `yaml:"env" json:"env"`                                     // Environment variable mappings
}

// SSOEnv maps SSO config values to app-specific environment variable names
type SSOEnv struct {
	ClientID       string `yaml:"clientId" json:"clientId"`
	ClientSecret   string `yaml:"clientSecret" json:"clientSecret"`
	DiscoveryURL   string `yaml:"discoveryUrl" json:"discoveryUrl"`
	RedirectURL    string `yaml:"redirectUrl" json:"redirectUrl"`
	ServerHostname string `yaml:"serverHostname" json:"serverHostname"` // Base URL for app server (e.g., ACTUAL_OPENID_SERVER_HOSTNAME)
	Issuer         string `yaml:"issuer" json:"issuer"`                 // OIDC issuer URL (e.g., OAUTH_OIDC_ISSUER)
	Provider       string `yaml:"provider" json:"provider"`
	ProviderName   string `yaml:"providerName" json:"providerName"`
	UserCreation   string `yaml:"userCreation" json:"userCreation"`
}

// Docs contains documentation links
type Docs struct {
	Homepage string `yaml:"homepage" json:"homepage"`
	Source   string `yaml:"source" json:"source"`
}

// Routing defines custom routing configuration for Traefik
type Routing struct {
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"` // Custom response headers
}
