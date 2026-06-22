package catalog

// App represents an application in the catalog
type App struct {
	Name          string                 `yaml:"name" json:"name"`
	DisplayName   string                 `yaml:"displayName" json:"displayName"`
	Description   string                 `yaml:"description" json:"description"`
	Category      string                 `yaml:"category" json:"category"`
	Icon          string                 `yaml:"icon" json:"icon"`
	Screenshots   []string               `yaml:"screenshots" json:"screenshots"`
	Version       string                 `yaml:"version" json:"version"`
	Port          int                    `yaml:"port" json:"port"`
	IsSystem      bool                   `yaml:"isSystem" json:"isSystem"`
	Dependencies  []string               `yaml:"dependencies" json:"dependencies"`
	Resources     Resources              `yaml:"resources" json:"resources"`
	SSO           SSO                    `yaml:"sso" json:"sso"`
	DefaultConfig map[string]interface{} `yaml:"defaultConfig" json:"defaultConfig"`
	HealthCheck   HealthCheck            `yaml:"healthCheck" json:"healthCheck"`
	Docs          Docs                   `yaml:"docs" json:"docs"`
	Tags          []string               `yaml:"tags" json:"tags"`
	Routing      *Routing               `yaml:"routing,omitempty" json:"routing,omitempty"`
	Integrations map[string]Integration `yaml:"integrations" json:"integrations"`
	Container     *ContainerSpec         `yaml:"container,omitempty" json:"container,omitempty"`
}

// ContainerSpec describes the portable container topology for an app.
type ContainerSpec struct {
	Name          string            `yaml:"name,omitempty" json:"name,omitempty"`
	Image         string            `yaml:"image" json:"image"`
	Network       string            `yaml:"network,omitempty" json:"network,omitempty"`
	RestartPolicy string            `yaml:"restartPolicy,omitempty" json:"restartPolicy,omitempty"`
	Command       []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Environment   map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	Ports         []ContainerPort   `yaml:"ports,omitempty" json:"ports,omitempty"`
	Volumes       []ContainerVolume `yaml:"volumes,omitempty" json:"volumes,omitempty"`
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
	Strategy     string   `yaml:"strategy" json:"strategy"`                         // native-oidc, forward-auth, none
	BypassPaths  []string `yaml:"bypassPaths,omitempty" json:"bypassPaths,omitempty"` // Paths exempt from forward-auth (forward-auth only)
	CallbackPath string `yaml:"callbackPath" json:"callbackPath"`       // e.g. /oauth2/oidc/callback
	ProviderName string `yaml:"providerName" json:"providerName"`       // e.g. "Bloud SSO"
	UserCreation bool   `yaml:"userCreation" json:"userCreation"`       // Auto-create users on first login
	LaunchPath   string `yaml:"launchPath" json:"launchPath,omitempty"` // Initial path to open when launching the app (overrides root)
	Env          SSOEnv `yaml:"env" json:"env"`                         // Environment variable mappings
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

// HealthCheck defines health check configuration
type HealthCheck struct {
	Path     string `yaml:"path" json:"path"`
	Interval int    `yaml:"interval" json:"interval"` // seconds
	Timeout  int    `yaml:"timeout" json:"timeout"`   // seconds
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

