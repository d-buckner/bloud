// SPDX-License-Identifier: AGPL-3.0-only

// Package arrmcp wires arr-mcp (bardesss/arr-mcp) into Bloud as an MCP tool
// server over the request and arr stack.
//
// The image serves MCP, a liveness endpoint and a config UI all from one port,
// and reads its configuration from a mounted /config/config.yaml. Bloud writes
// that file in PreStart from the resolved contract bindings: the request
// manager (Seerr), whichever Servarrs are installed, and the media server
// (Jellyfin) it reaches with a dedicated API key minted through the bootstrap
// admin login the `mediaServer` contract publishes.
//
// The inbound side is authenticated. The listener requires a bearer named in
// the config as a sha256 hash, so Bloud generates one and publishes the
// plaintext as the `mcp` contract's httpToken. The config UI is claimed the
// same way: Bloud mints a password and writes its scrypt hash, then publishes
// the plaintext under the `clientPassword` contract for the operator to
// reveal.
package arrmcp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"codeberg.org/d-buckner/bloud/apps/jellyfin"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const (
	// appName is the catalog ID (and secrets/registry key) for this app.
	appName = "arr-mcp"
	// nodeName is the graph node and container name; see NodeLifecycle.Name.
	nodeName = "apps-arr-mcp"
	// defaultPort is the server's bind port and the host-published port. It
	// must match metadata.yaml's `port`. 6060 is the image's own default.
	defaultPort = 6060

	// configDir is the subdirectory of the app data tree mounted at /config in
	// the container, and configFileName the file the image reads there.
	configDir      = "config"
	configFileName = "config.yaml"

	// The image runs as a uid Bloud cannot set, so the mounted config dir must
	// be world-writable or the config UI's save cannot replace the file. Same
	// constraint and treatment as apps/seerr.
	configDirPerm = 0o777

	// The provider app ids this app's contracts name. The request manager is
	// what this app exists to serve; the Servarrs and the media server are
	// optional and wired only when installed.
	seerrAppName    = "seerr"
	jellyfinAppName = "jellyfin"
	radarrAppName   = "radarr"
	sonarrAppName   = "sonarr"

	// jellyfinKeyName is the name the API key this app mints carries in
	// Jellyfin's own Security screen, so an operator can revoke the agent
	// without rotating the admin password.
	jellyfinKeyName = "arr-mcp"

	// configUIUsername is the account the config UI is claimed under. The
	// password is a published client credential; the username is a constant
	// because it is a label the operator must type, not a credential.
	configUIUsername = "bloud"

	// tokenName is the label arr-mcp's config UI shows for the bearer a
	// harness presents.
	tokenName = "bloud-hermes"
)

// Configurator handles the arr-mcp node lifecycle: mint the credentials this
// app owns, write the config.yaml the container reads (PreStart), and verify
// the server is serving MCP to an authenticated client with healthy services
// behind it (PostStart).
type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	clients configurator.ClientFactory
	logger  *slog.Logger
	api     *mcpAPI

	// baseURL is a test seam: when set, the MCP probe resolves to it instead
	// of localhost:port.
	baseURL string
}

// NewConfigurator creates a new arr-mcp configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:    port,
		secrets: deps.Secrets,
		clients: deps.HTTP,
		logger:  logger.With("app", appName),
	}
	c.api = newAPI(deps.HTTP, func() string {
		if c.baseURL != "" {
			return c.baseURL
		}
		return fmt.Sprintf("http://localhost:%d", c.port)
	})
	return c
}

func (c *Configurator) Name() string {
	return nodeName
}

// PreStart writes the config.yaml the container reads. It carries the auth
// block (the bearer hash, the claimed UI account and its password hash) and
// one service block per resolved binding.
//
// The credentials are generated on the first pass and reused afterwards, so a
// restart does not invalidate the namespace a harness registered. The scrypt
// hash of the UI password is derived deterministically, so a steady-state
// resync writes the same bytes and reports no change rather than minting a
// fresh salt every pass.
//
// A binding that has not resolved yet is written as absent, never as an empty
// service block: the schema rejects a keyed service with no api_key, and an
// absent service is how the app tells "not wired" from "wired with nothing".
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	dir := filepath.Join(state.DataPath, configDir)
	if err := os.MkdirAll(dir, configDirPerm); err != nil {
		return configurator.NoRestart(), fmt.Errorf("creating %s config directory: %w", appName, err)
	}
	if err := managedfile.EnsureWritable(dir, configDirPerm); err != nil {
		return configurator.NoRestart(), fmt.Errorf("making %s config directory writable: %w", appName, err)
	}

	bearer, err := c.ensureInboundToken()
	if err != nil {
		return configurator.NoRestart(), err
	}
	password, err := c.ensureUIPassword()
	if err != nil {
		return configurator.NoRestart(), err
	}

	cfg, err := c.buildConfig(ctx, state, bearer, password)
	if err != nil {
		return configurator.NoRestart(), err
	}

	content, err := yaml.Marshal(cfg)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("rendering %s config: %w", appName, err)
	}
	path := filepath.Join(state.DataPath, configDir, configFileName)
	// ModeSharedConfig, not ModeHostOnly: the container reads this file, so it
	// must be world-readable. The two live credentials it carries are hashes,
	// never the plaintext values Bloud publishes to consumers.
	changed, err := managedfile.Write(path, content, managedfile.ModeSharedConfig)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing %s config: %w", appName, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}
	c.logger.Info("wrote arr-mcp config", "path", path,
		"seerr", cfg.Services.Seerr != nil, "jellyfin", cfg.Services.Jellyfin != nil,
		"pvr", len(pvrBindings(state)))
	return configurator.MustRestart("arr-mcp config rewritten"), nil
}

// buildConfig assembles the config.yaml content from the resolved bindings
// plus the two credentials already minted. It is separate from PreStart so the
// per-binding wiring does not inflate PreStart's branch count.
func (c *Configurator) buildConfig(ctx context.Context, state *configurator.AppState, bearer, password string) (configFile, error) {
	cfg := configFile{
		Auth: configAuth{
			Username:        configUIUsername,
			AllowedHosts:    []string{},
			AllowTokenInURL: false,
			Tokens:          []configToken{},
		},
		Services: configServices{},
	}
	if bearer != "" {
		cfg.Auth.Tokens = append(cfg.Auth.Tokens, configToken{Name: tokenName, Tier: "read", Hash: sha256TokenHash(bearer)})
	}
	if password != "" {
		hash, err := scryptHash(password)
		if err != nil {
			return configFile{}, fmt.Errorf("hashing the %s config UI password: %w", appName, err)
		}
		cfg.Auth.PasswordHash = hash
	}
	if rm, ok := requestManagerBinding(state); ok {
		cfg.Services.Seerr = &serviceConfig{URL: rm.BaseURL, APIKey: rm.APIKey, DefaultUser: rm.DefaultUser}
	}
	for _, pvr := range pvrBindings(state) {
		svc := &serviceConfig{URL: pvr.BaseURL, APIKey: pvr.APIKey}
		switch pvr.App {
		case radarrAppName:
			cfg.Services.Radarr = svc
		case sonarrAppName:
			cfg.Services.Sonarr = svc
		}
	}
	if server, ok := mediaServerBinding(state); ok {
		key, err := c.ensureJellyfinAPIKey(ctx, server)
		if err != nil {
			return configFile{}, fmt.Errorf("provisioning the %s Jellyfin API key: %w", appName, err)
		}
		// default_user is the account the agent acts as, and the only account
		// arr-mcp will answer for while allow_other_users is false. The
		// bootstrap admin the key was minted as is that account: it exists on
		// every provider (Bloud-booted or operator-registered), and it is the
		// identity the full-privilege key already carries.
		cfg.Services.Jellyfin = &serviceConfig{URL: server.BaseURL, APIKey: key, DefaultUser: server.AdminUsername}
	}
	return cfg, nil
}

// PostStart verifies the server is serving MCP to an authenticated client and
// that the services it was configured with are healthy, in that order because
// the second is only meaningful once the first holds.
//
// The handshake alone proves the config parsed and the bearer is enforced (a
// broken config answers 503 on /mcp in repair mode, and a missing or wrong
// bearer answers 401). It does not prove a service credential is correct: a
// wrong Seerr key parses fine and only shows up when a tool call reaches the
// service. stack_health is the tool that reports that, so the gate ends on it.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	bearer := c.currentBearer()
	if err := c.api.waitServing(ctx, bearer); err != nil {
		return fmt.Errorf("waiting for the arr-mcp server: %w", err)
	}
	if err := c.api.waitStackHealthy(ctx, bearer); err != nil {
		return fmt.Errorf("asking arr-mcp to reach its services: %w", err)
	}
	c.logger.Info("arr-mcp is serving MCP with its services healthy")
	return nil
}

// ensureJellyfinAPIKey returns the API key this app calls Jellyfin with,
// minting it through apps/jellyfin's shared client on the first pass and
// reusing the stored key afterwards.
func (c *Configurator) ensureJellyfinAPIKey(ctx context.Context, server configurator.MediaServerBinding) (string, error) {
	if c.secrets == nil {
		return "", nil
	}
	if server.AdminUsername == "" {
		return "", fmt.Errorf("the %s media server published no admin username, so %s cannot log in to mint a key", server.App, appName)
	}
	if stored := c.secrets.GetAppSecret(appName, jellyfinAPIKeyKey); stored != "" {
		return stored, nil
	}
	key, err := jellyfin.EnsureAPIKey(ctx, c.clients, func() string { return server.LocalURL }, server.AdminUsername, server.AdminPassword, jellyfinKeyName)
	if err != nil {
		return "", err
	}
	if err := c.secrets.SetAppSecret(appName, jellyfinAPIKeyKey, key); err != nil {
		return "", fmt.Errorf("persisting the %s Jellyfin API key: %w", appName, err)
	}
	c.logger.Info("provisioned the arr-mcp Jellyfin API key", "keyName", jellyfinKeyName)
	return key, nil
}

// currentBearer reads the MCP bearer this app publishes without generating
// one. PreStart owns generation; PostStart only has to present whatever is
// current, so a rotation landing between the two phases is still picked up.
func (c *Configurator) currentBearer() string {
	if c.secrets == nil {
		return ""
	}
	return c.secrets.GetAppSecret(appName, httpTokenKey)
}

// requestManagerBinding returns the installed Seerr provider, or false when
// none is installed or it has not published its key yet.
func requestManagerBinding(state *configurator.AppState) (configurator.RequestManagerBinding, bool) {
	if state == nil {
		return configurator.RequestManagerBinding{}, false
	}
	for _, b := range state.Integrations.RequestManagers {
		if b.App == seerrAppName && b.Installed && b.APIKey != "" {
			return b, true
		}
	}
	return configurator.RequestManagerBinding{}, false
}

// pvrBindings returns every installed Servarr that has published its key.
func pvrBindings(state *configurator.AppState) []configurator.PVRBinding {
	if state == nil {
		return nil
	}
	var out []configurator.PVRBinding
	for _, b := range state.Integrations.PVRs {
		if b.Installed && b.APIKey != "" {
			out = append(out, b)
		}
	}
	return out
}

// mediaServerBinding returns the installed Jellyfin provider, or false when
// the catalog declares none or it is not installed yet.
func mediaServerBinding(state *configurator.AppState) (configurator.MediaServerBinding, bool) {
	if state == nil {
		return configurator.MediaServerBinding{}, false
	}
	for _, b := range state.Integrations.MediaServers {
		if b.App == jellyfinAppName && b.Installed && b.AdminPassword != "" {
			return b, true
		}
	}
	return configurator.MediaServerBinding{}, false
}

// configFile is the subset of arr-mcp's config.yaml Bloud owns. The schema is
// strict on unknown keys, so this shape must match the image's exactly; the
// tags are what yaml.v3 renders.
type configFile struct {
	Auth     configAuth     `yaml:"auth"`
	Services configServices `yaml:"services"`
}

type configAuth struct {
	Username        string        `yaml:"username"`
	AllowedHosts    []string      `yaml:"allowed_hosts"`
	AllowTokenInURL bool          `yaml:"allow_token_in_url"`
	PasswordHash    string        `yaml:"password_hash,omitempty"`
	Tokens          []configToken `yaml:"tokens"`
}

type configToken struct {
	Name string `yaml:"name"`
	Tier string `yaml:"tier"`
	Hash string `yaml:"hash"`
}

type configServices struct {
	Seerr    *serviceConfig `yaml:"seerr,omitempty"`
	Jellyfin *serviceConfig `yaml:"jellyfin,omitempty"`
	Radarr   *serviceConfig `yaml:"radarr,omitempty"`
	Sonarr   *serviceConfig `yaml:"sonarr,omitempty"`
}

type serviceConfig struct {
	URL         string `yaml:"url"`
	APIKey      string `yaml:"api_key"`
	DefaultUser string `yaml:"default_user,omitempty"`
}
