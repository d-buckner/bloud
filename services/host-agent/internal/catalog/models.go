// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import "strings"

// App represents an application in the catalog
type App struct {
	CatalogID   string   `yaml:"name" json:"catalogId"`
	DisplayName string   `yaml:"displayName" json:"displayName"`
	Description string   `yaml:"description" json:"description"`
	Category    string   `yaml:"category" json:"category"`
	Icon        string   `yaml:"icon" json:"icon"`
	Screenshots []string `yaml:"screenshots" json:"screenshots"`
	Version     string   `yaml:"version" json:"version"`
	Port        int      `yaml:"port" json:"port"`
	// EstimatedSizeMB is the approximate total image download size, so the
	// catalog can set expectations before a long pull. Zero when unknown; the
	// API falls back to local `podman image inspect` sizes when available.
	EstimatedSizeMB int                    `yaml:"estimatedSizeMB,omitempty" json:"estimatedSizeMB,omitempty"`
	IsSystem        bool                   `yaml:"isSystem" json:"isSystem"`
	Dependencies    []string               `yaml:"dependencies" json:"dependencies"`
	Resources       Resources              `yaml:"resources" json:"resources"`
	SSO             SSO                    `yaml:"sso" json:"sso"`
	DefaultConfig   map[string]any         `yaml:"defaultConfig" json:"defaultConfig"`
	Docs            Docs                   `yaml:"docs" json:"docs"`
	Tags            []string               `yaml:"tags" json:"tags"`
	Routing         *Routing               `yaml:"routing,omitempty" json:"routing,omitempty"`
	Integrations    map[string]Integration `yaml:"integrations" json:"integrations"`
	Provides        Provides               `yaml:"provides,omitempty" json:"provides,omitempty"`
	Containers      []ContainerDef         `yaml:"containers,omitempty" json:"containers,omitempty"`
	// ExtraPorts are the non-UI ports an app exposes for other apps to
	// connect to. `port` is the UI: the dashboard opens it, Traefik routes
	// the app's root to it, and it is the thing a person clicks. That
	// single-port model stops being enough once an app serves a second thing
	// worth wiring, which is the case for Hermes: a browser dashboard on one
	// port and an OpenAI-compatible agent API on another, and a consumer
	// wants the second one. See ExtraPort.
	ExtraPorts []ExtraPort `yaml:"extraPorts,omitempty" json:"extraPorts,omitempty"`
	// Headless marks an app with no browser UI of its own: there is nothing
	// to open, so the dashboard draws no tile for it once installed. The app
	// is otherwise ordinary: it stays in the catalog, in
	// GET /api/apps/installed, and in the developer graph, and it installs,
	// reconciles, and routes like every other app. Wrapper and service-shaped
	// apps (affine-mcp) set it. Absent means the app has a UI worth a tile.
	Headless bool `yaml:"headless,omitempty" json:"headless,omitempty"`
}

// ContainerDef describes one container in a multi-container app.
type ContainerDef struct {
	Name  string `yaml:"name" json:"name"`
	Image string `yaml:"image" json:"image"`
	// Entrypoint replaces the image's own entrypoint. It is a different thing
	// from Command, which only replaces the argument list the entrypoint
	// receives, and the two are not interchangeable: an image whose entrypoint
	// is a supervision script cannot be redirected by passing it arguments.
	// Unset means keep whatever the image declares.
	Entrypoint    []string `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`
	Command       []string `yaml:"command,omitempty" json:"command,omitempty"`
	Network       string   `yaml:"network,omitempty" json:"network,omitempty"`
	Networks      []string `yaml:"networks,omitempty" json:"networks,omitempty"`
	RestartPolicy string   `yaml:"restartPolicy,omitempty" json:"restartPolicy,omitempty"`
	// ShmSize raises the container's /dev/shm tmpfs above podman's 64MB
	// default. That default is a correctness limit, not a comfort one: a
	// process that mmaps shared memory and finds the tmpfs full is sent SIGBUS
	// on the next write to a mapped page. The worker dies and the container
	// does not, so `restartPolicy: always` never fires. Authentik's gunicorn
	// workers fail that way in a self-reinforcing loop that takes SSO down for
	// every app (issue #267).
	//
	// Written as a size string: "256m", "1g", "268435456". ParseByteSize
	// defines what is accepted, and Loader validation rejects what is not, so
	// a typo fails catalog load instead of turning into a container silently
	// left on the default. Unset means leave the runtime default alone.
	ShmSize     string            `yaml:"shmSize,omitempty" json:"shmSize,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	// EnvFile names a host path holding additional `KEY=value` lines for the
	// container's environment. It exists for an image that takes its whole
	// configuration through process environment variables and nothing else,
	// where some of those values are resolved contract bindings rather than
	// static metadata: `environment:` can only render {{dataDir}},
	// {{appDataDir}} and the process-start TemplateVars, so a resolved binding
	// has no way in. The configurator writes the file and reports a recreate
	// when it changes.
	//
	// The spec revision hashes the *path*, never the contents, so rotating a
	// credential in the file does not read as catalog spec drift. The recreate
	// signal is the configurator's, not the renderer's, which is what keeps the
	// catalog-update diff comparing like for like.
	//
	// Values from the file override same-named entries in `environment:`: the
	// file carries resolved truth and must not be shadowed by a static default.
	// A declared file that does not exist is an error, not a silent no-op.
	EnvFile     string                `yaml:"envFile,omitempty" json:"envFile,omitempty"`
	ExtraHosts  []string              `yaml:"extraHosts,omitempty" json:"extraHosts,omitempty"` // host:ip entries (e.g. "sso.localhost:host-gateway")
	Ports       []ContainerPort       `yaml:"ports,omitempty" json:"ports,omitempty"`
	Volumes     []ContainerVolume     `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	DependsOn   []string              `yaml:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	HealthCheck *ContainerHealthCheck `yaml:"healthCheck,omitempty" json:"healthCheck,omitempty"`
}

// ContainerHealthCheck defines a container-level health check command.
type ContainerHealthCheck struct {
	Test     []string `yaml:"test" json:"test"`
	Interval int      `yaml:"interval" json:"interval"` // seconds between checks
	Timeout  int      `yaml:"timeout" json:"timeout"`   // seconds before check is considered failed
	Retries  int      `yaml:"retries" json:"retries"`   // consecutive failures before marking unhealthy
}

// ShmSizeBytes renders the declared shmSize as the byte count the runtime
// takes. An undeclared size is 0, which means "leave the runtime default", not
// "ask for zero bytes".
func (c ContainerDef) ShmSizeBytes() (int64, error) {
	if strings.TrimSpace(c.ShmSize) == "" {
		return 0, nil
	}
	return ParseByteSize(c.ShmSize)
}

// ContainerDefs returns the app's container definitions.
// Returns nil if no containers are defined.
func (a *App) ContainerDefs() []ContainerDef {
	return a.Containers
}

// HasHostNetworkedContainer reports whether any container shares the host
// network namespace. Only there does the loopback issuer
// (http://localhost:<Traefik port>) resolve to Traefik, which is what
// sso.loopbackIssuer depends on (enforced by Loader.validateApp).
func (a *App) HasHostNetworkedContainer() bool {
	for _, c := range a.Containers {
		if c.Network == "host" {
			return true
		}
		for _, n := range c.Networks {
			if n == "host" {
				return true
			}
		}
	}
	return false
}

// HasClientAccess reports whether any contract this app provides carries a
// clientAccess block, i.e. a credential meant to reach a client a human holds.
// The dashboard uses it to decide whether the right-click menu offers the
// reveal surface, so an app that never declared the pattern does not grow a
// dead menu item.
func (a *App) HasClientAccess() bool {
	for _, offer := range a.Provides {
		if offer.ClientAccess != nil {
			return true
		}
	}
	return false
}

// ExtraPort is one non-UI port an app exposes for other apps to connect to.
//
// It is declared at the app level rather than as a container `ports:` entry
// because those are different facts with different blast radii. A `ports:`
// entry publishes a port to the host, which is precisely what an agent API
// with terminal access must not do: it would sit on the LAN guarded by one
// bearer token. An ExtraPort is published to Bloud's consumers instead.
// Traefik reaches it on the loopback the app already binds, and a consumer
// dials it through the app's own public origin, so the port never gains a
// reach it was not declared to have.
//
// The name is the handle a provider binds a contract to (`provides:
// <contract>: port: gateway`), which is what lets the contract say *which*
// surface it means without either side hardcoding a number. An app with one
// port needs no ExtraPort at all: the contract resolves against `port`, the
// same way it always has.
type ExtraPort struct {
	// Name is how a contract offer refers to this port. A single lowercase
	// word, unique within the app.
	Name string `yaml:"name" json:"name"`
	// Port is the TCP port the app listens on.
	Port int `yaml:"port" json:"port"`
	// PathPrefix is the URL prefix Traefik routes to this port on the app's
	// own subdomain, e.g. `/v1` for an OpenAI-compatible API served beside
	// a dashboard. It is stated once here rather than also on the contract
	// because the route Traefik installs and the path a consumer appends are
	// the same fact: declaring it twice is how they drift.
	PathPrefix string `yaml:"pathPrefix,omitempty" json:"pathPrefix,omitempty"`
}

// ExtraPort returns the named extra port, or nil when the app declares no
// such port. A nil return is the caller's signal that the name does not
// resolve, which the loader rejects up front; it is not a condition a
// runtime path has to guess at.
func (a *App) ExtraPort(name string) *ExtraPort {
	for i := range a.ExtraPorts {
		if a.ExtraPorts[i].Name == name {
			return &a.ExtraPorts[i]
		}
	}
	return nil
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

type SSO struct {
	Strategy     string   `yaml:"strategy" json:"strategy"`                           // native-oidc, forward-auth, none
	BypassPaths  []string `yaml:"bypassPaths,omitempty" json:"bypassPaths,omitempty"` // Paths exempt from forward-auth (forward-auth only)
	CallbackPath string   `yaml:"callbackPath" json:"callbackPath"`                   // e.g. /oauth2/oidc/callback
	ProviderName string   `yaml:"providerName" json:"providerName"`                   // e.g. "Bloud SSO"
	UserCreation bool     `yaml:"userCreation" json:"userCreation"`                   // Auto-create users on first login
	LaunchPath   string   `yaml:"launchPath" json:"launchPath,omitempty"`             // Initial path to open when launching the app (overrides root)
	// Scopes lists OIDC scopes the app needs beyond the openid, profile and email
	// every native-oidc provider carries (e.g. offline_access for apps that
	// refresh tokens). native-oidc only.
	Scopes []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
	// AccessTokenMinutes overrides the provider's access token lifetime (the
	// default is 5 minutes). native-oidc only; 0 keeps the default.
	AccessTokenMinutes int `yaml:"accessTokenMinutes,omitempty" json:"accessTokenMinutes,omitempty"`
	// ClientType is the OAuth2 client type for native-oidc apps: "public"
	// (authorization-code + PKCE, no client secret) or "confidential"
	// (the default). Public clients are required by apps whose OIDC
	// integration rejects a client_secret (e.g. the Hermes dashboard).
	// Empty is treated as "confidential".
	ClientType string `yaml:"clientType,omitempty" json:"clientType,omitempty"`
	// LoopbackIssuer serves this app's OIDC issuer from the host loopback
	// (http://localhost:<Traefik port>) instead of the shared issuer host
	// (sso.localhost, or the primary host). An app sets it when its OIDC
	// client accepts https anywhere but http only on a literal loopback
	// hostname (the Hermes dashboard). Such an app runs with the host
	// network namespace so localhost:<Traefik port> is Traefik, and browser
	// access therefore works only from the machine running Bloud: the same
	// reach the *.localhost issuer host has.
	LoopbackIssuer bool `yaml:"loopbackIssuer,omitempty" json:"loopbackIssuer,omitempty"`
}

// PublicClient reports whether this app's native-oidc client is a public
// PKCE client (no client secret). Anything other than "public", including
// the empty default, is confidential.
func (s SSO) PublicClient() bool {
	return strings.EqualFold(strings.TrimSpace(s.ClientType), "public")
}

type Docs struct {
	Homepage string `yaml:"homepage" json:"homepage"`
	Source   string `yaml:"source" json:"source"`
}

// Routing defines custom routing configuration for Traefik
type Routing struct {
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"` // Custom response headers
}
