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
	Routing       *Routing               `yaml:"routing,omitempty" json:"routing,omitempty"`
	Bootstrap     *BootstrapConfig       `yaml:"bootstrap,omitempty" json:"bootstrap,omitempty"`
}

// Resources defines resource requirements for an app
type Resources struct {
	MinRam  int  `yaml:"minRam" json:"minRam"`    // MB
	MinDisk int  `yaml:"minDisk" json:"minDisk"`  // GB
	GPU     bool `yaml:"gpu" json:"gpu"`
}

// SSO defines SSO integration configuration
type SSO struct {
	Strategy     string `yaml:"strategy" json:"strategy"`         // native-oidc, forward-auth, none
	CallbackPath string `yaml:"callbackPath" json:"callbackPath"` // e.g. /oauth2/oidc/callback
	ProviderName string `yaml:"providerName" json:"providerName"` // e.g. "Bloud SSO"
	UserCreation bool   `yaml:"userCreation" json:"userCreation"` // Auto-create users on first login
	Env          SSOEnv `yaml:"env" json:"env"`                   // Environment variable mappings
}

// SSOEnv maps SSO config values to app-specific environment variable names
type SSOEnv struct {
	ClientID     string `yaml:"clientId" json:"clientId"`
	ClientSecret string `yaml:"clientSecret" json:"clientSecret"`
	DiscoveryURL string `yaml:"discoveryUrl" json:"discoveryUrl"`
	RedirectURL  string `yaml:"redirectUrl" json:"redirectUrl"`
	Provider     string `yaml:"provider" json:"provider"`
	ProviderName string `yaml:"providerName" json:"providerName"`
	UserCreation string `yaml:"userCreation" json:"userCreation"`
}

// HealthCheck defines health check configuration
type HealthCheck struct {
	Path     string `yaml:"path" json:"path"`
	Interval int    `yaml:"interval" json:"interval"`  // seconds
	Timeout  int    `yaml:"timeout" json:"timeout"`    // seconds
}

// Docs contains documentation links
type Docs struct {
	Homepage string `yaml:"homepage" json:"homepage"`
	Source   string `yaml:"source" json:"source"`
}

// AbsolutePath defines a root-level route for apps that use absolute paths
// (e.g., AdGuard Home redirects to /install.html, /login.html)
type AbsolutePath struct {
	Rule     string            `yaml:"rule" json:"rule"`                         // Traefik rule syntax (e.g., "Path(`/install.html`)")
	Priority int               `yaml:"priority" json:"priority"`                 // Route priority (higher = matched first)
	Headers  map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"` // Custom headers for this route (overrides app headers)
}

// Routing defines custom routing configuration for Traefik
type Routing struct {
	Headers       map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`             // Custom response headers
	StripPrefix   *bool             `yaml:"stripPrefix,omitempty" json:"stripPrefix,omitempty"`     // Strip /embed/<app> prefix (default: true)
	AbsolutePaths []AbsolutePath    `yaml:"absolutePaths,omitempty" json:"absolutePaths,omitempty"` // Root-level routes for apps using absolute paths
}

// BootstrapConfig defines client-side pre-configuration for an app
type BootstrapConfig struct {
	IndexedDB *IndexedDBConfig `yaml:"indexedDB,omitempty" json:"indexedDB,omitempty"`
}

// IndexedDBConfig defines IndexedDB setup requirements
type IndexedDBConfig struct {
	Database   string           `yaml:"database" json:"database"`
	Intercepts []IndexedDBEntry `yaml:"intercepts,omitempty" json:"intercepts,omitempty"` // Values returned on read, injected via service worker
	Writes     []IndexedDBEntry `yaml:"writes,omitempty" json:"writes,omitempty"`         // Values written from main page before iframe loads
	Entries    []IndexedDBEntry `yaml:"entries,omitempty" json:"entries,omitempty"`       // Legacy: deprecated, use intercepts/writes instead
}

// IndexedDBEntry defines a key-value entry to write
type IndexedDBEntry struct {
	Store string `yaml:"store" json:"store"`
	Key   string `yaml:"key" json:"key"`
	Value string `yaml:"value" json:"value"` // Supports {{field}} templates for any App field plus {{origin}}, {{embedUrl}}
}
