package sso

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
)

// BlueprintGenerator generates Authentik OAuth2 blueprints from catalog SSO config
type BlueprintGenerator struct {
	hostSecret    string
	baseURL       string
	authentikURL  string
	blueprintsDir string
}

// NewBlueprintGenerator creates a new blueprint generator
func NewBlueprintGenerator(hostSecret, baseURL, authentikURL, blueprintsDir string) *BlueprintGenerator {
	return &BlueprintGenerator{
		hostSecret:    hostSecret,
		baseURL:       baseURL,
		authentikURL:  authentikURL,
		blueprintsDir: blueprintsDir,
	}
}

// GenerateForApp generates an Authentik blueprint for an app with SSO
func (g *BlueprintGenerator) GenerateForApp(app *catalog.App) error {
	switch app.SSO.Strategy {
	case "native-oidc":
		return g.generateOIDCBlueprint(app)
	case "forward-auth":
		return g.generateForwardAuthBlueprint(app)
	default:
		return nil // No blueprint needed for apps without SSO
	}
}

// generateOIDCBlueprint creates an OAuth2 Provider blueprint for native OIDC apps
func (g *BlueprintGenerator) generateOIDCBlueprint(app *catalog.App) error {
	clientID := g.generateClientID(app.Name)
	clientSecret := g.generateClientSecret(app.Name)

	// Build redirect URIs
	redirectURIs := []string{
		// Primary: embed path (for apps that use ACTUAL_OPENID_SERVER_HOSTNAME correctly)
		fmt.Sprintf("%s/embed/%s%s", g.baseURL, app.Name, app.SSO.CallbackPath),
		// Root-level callback: some apps (Actual Budget) build redirect_uri from window.location.origin
		// which is the Traefik host, not the embed path. This requires routing.absolutePaths in metadata.
		fmt.Sprintf("%s%s", g.baseURL, app.SSO.CallbackPath),
	}
	if app.Port > 0 {
		// Add direct port access for debugging
		// Extract host from baseURL (remove port if present)
		host := g.baseURL
		if idx := len(host) - 1; idx > 0 {
			for i := len(host) - 1; i >= 0; i-- {
				if host[i] == ':' {
					host = host[:i]
					break
				}
			}
		}
		redirectURIs = append(redirectURIs, fmt.Sprintf("%s:%d%s", host, app.Port, app.SSO.CallbackPath))
	}

	launchURL := fmt.Sprintf("%s/embed/%s", g.baseURL, app.Name)

	blueprint, err := g.renderOIDCBlueprint(app, clientID, clientSecret, redirectURIs, launchURL)
	if err != nil {
		return fmt.Errorf("rendering OIDC blueprint: %w", err)
	}

	return g.writeBlueprint(app.Name, blueprint)
}

// generateForwardAuthBlueprint creates a Proxy Provider blueprint for forward auth apps
func (g *BlueprintGenerator) generateForwardAuthBlueprint(app *catalog.App) error {
	// external_host should be the root URL, not the app-specific path.
	// The callback URL (/outpost.goauthentik.io/callback) is handled at root level by Traefik.
	externalHost := g.baseURL
	launchURL := fmt.Sprintf("%s/embed/%s", g.baseURL, app.Name)

	blueprint, err := g.renderForwardAuthBlueprint(app, externalHost, launchURL)
	if err != nil {
		return fmt.Errorf("rendering forward-auth blueprint: %w", err)
	}

	return g.writeBlueprint(app.Name, blueprint)
}

// writeBlueprint writes a blueprint file to the blueprints directory
func (g *BlueprintGenerator) writeBlueprint(appName, blueprint string) error {
	if err := os.MkdirAll(g.blueprintsDir, 0755); err != nil {
		return fmt.Errorf("creating blueprints directory: %w", err)
	}

	path := filepath.Join(g.blueprintsDir, fmt.Sprintf("%s.yaml", appName))
	if err := os.WriteFile(path, []byte(blueprint), 0644); err != nil {
		return fmt.Errorf("writing blueprint file: %w", err)
	}

	return nil
}

// DeleteBlueprint removes the blueprint file for an app
func (g *BlueprintGenerator) DeleteBlueprint(appName string) error {
	path := filepath.Join(g.blueprintsDir, fmt.Sprintf("%s.yaml", appName))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing blueprint file: %w", err)
	}
	return nil
}

// ForwardAuthProvider represents a forward-auth provider to add to the outpost
type ForwardAuthProvider struct {
	DisplayName string // e.g., "qBittorrent"
}

// GenerateOutpostBlueprint creates or updates the outpost blueprint with all forward-auth providers.
// This is needed because providers must be explicitly added to the embedded outpost for forward-auth to work.
// The blueprint uses !Find to reference providers by name, so they can be created by separate app blueprints.
// It also configures the outpost with the correct browser-accessible URL for OAuth redirects.
func (g *BlueprintGenerator) GenerateOutpostBlueprint(providers []ForwardAuthProvider) error {
	if len(providers) == 0 {
		// No forward-auth providers - remove the outpost blueprint if it exists
		path := filepath.Join(g.blueprintsDir, "bloud-outpost.yaml")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing outpost blueprint: %w", err)
		}
		return nil
	}

	blueprint, err := g.renderOutpostBlueprint(providers)
	if err != nil {
		return fmt.Errorf("rendering outpost blueprint: %w", err)
	}

	if err := os.MkdirAll(g.blueprintsDir, 0755); err != nil {
		return fmt.Errorf("creating blueprints directory: %w", err)
	}

	path := filepath.Join(g.blueprintsDir, "bloud-outpost.yaml")
	if err := os.WriteFile(path, []byte(blueprint), 0644); err != nil {
		return fmt.Errorf("writing outpost blueprint: %w", err)
	}

	return nil
}

func (g *BlueprintGenerator) renderOutpostBlueprint(providers []ForwardAuthProvider) (string, error) {
	data := struct {
		Providers    []ForwardAuthProvider
		AuthentikURL string // Browser-accessible URL for OAuth redirects
	}{
		Providers:    providers,
		AuthentikURL: g.baseURL,
	}

	tmpl, err := template.New("outpost").Parse(outpostBlueprintTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// GetSSOEnvVars returns the environment variables needed for an app's SSO config
func (g *BlueprintGenerator) GetSSOEnvVars(app *catalog.App) map[string]string {
	if app.SSO.Strategy != "native-oidc" {
		return nil
	}

	clientID := g.generateClientID(app.Name)
	clientSecret := g.generateClientSecret(app.Name)
	discoveryURL := fmt.Sprintf("%s/application/o/%s/", g.authentikURL, app.Name)
	redirectURL := fmt.Sprintf("%s/embed/%s%s", g.baseURL, app.Name, app.SSO.CallbackPath)

	env := make(map[string]string)

	if app.SSO.Env.ClientID != "" {
		env[app.SSO.Env.ClientID] = clientID
	}
	if app.SSO.Env.ClientSecret != "" {
		env[app.SSO.Env.ClientSecret] = clientSecret
	}
	if app.SSO.Env.DiscoveryURL != "" {
		env[app.SSO.Env.DiscoveryURL] = discoveryURL
	}
	if app.SSO.Env.RedirectURL != "" {
		env[app.SSO.Env.RedirectURL] = redirectURL
	}
	if app.SSO.Env.Provider != "" {
		env[app.SSO.Env.Provider] = "oidc"
	}
	if app.SSO.Env.ProviderName != "" {
		env[app.SSO.Env.ProviderName] = app.SSO.ProviderName
	}
	if app.SSO.Env.UserCreation != "" {
		if app.SSO.UserCreation {
			env[app.SSO.Env.UserCreation] = "1"
		} else {
			env[app.SSO.Env.UserCreation] = "0"
		}
	}

	return env
}

func (g *BlueprintGenerator) generateClientID(appName string) string {
	return fmt.Sprintf("%s-client", appName)
}

func (g *BlueprintGenerator) generateClientSecret(appName string) string {
	// TODO: For production, derive secrets properly and sync with NixOS modules.
	// For now, use static pattern matching NixOS module defaults to ensure consistency.
	// The NixOS modules use "{appName}-secret-change-in-production" as default.
	return fmt.Sprintf("%s-secret-change-in-production", appName)
}

func (g *BlueprintGenerator) renderOIDCBlueprint(app *catalog.App, clientID, clientSecret string, redirectURIs []string, launchURL string) (string, error) {
	data := struct {
		AppName      string
		DisplayName  string
		ClientID     string
		ClientSecret string
		RedirectURIs []string
		LaunchURL    string
	}{
		AppName:      app.Name,
		DisplayName:  app.DisplayName,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURIs: redirectURIs,
		LaunchURL:    launchURL,
	}

	tmpl, err := template.New("blueprint").Parse(oidcBlueprintTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func (g *BlueprintGenerator) renderForwardAuthBlueprint(app *catalog.App, externalHost, launchURL string) (string, error) {
	data := struct {
		AppName      string
		DisplayName  string
		ExternalHost string
		LaunchURL    string
	}{
		AppName:      app.Name,
		DisplayName:  app.DisplayName,
		ExternalHost: externalHost,
		LaunchURL:    launchURL,
	}

	tmpl, err := template.New("blueprint").Parse(forwardAuthBlueprintTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

const oidcBlueprintTemplate = `# Authentik Blueprint for {{.DisplayName}}
# Auto-generated by Bloud host-agent from catalog SSO config
version: 1
metadata:
  name: {{.AppName}}-sso-blueprint
  labels:
    managed-by: bloud

entries:
  # OAuth2 Provider
  - model: authentik_providers_oauth2.oauth2provider
    id: {{.AppName}}-oauth2-provider
    identifiers:
      name: {{.DisplayName}} OAuth2 Provider
    attrs:
      authorization_flow: !Find [authentik_flows.flow, [slug, default-provider-authorization-implicit-consent]]
      invalidation_flow: !Find [authentik_flows.flow, [slug, default-provider-invalidation-flow]]
      client_type: confidential
      client_id: {{.ClientID}}
      client_secret: {{.ClientSecret}}
      redirect_uris:
{{- range .RedirectURIs}}
        - url: "{{.}}"
          matching_mode: strict
{{- end}}
      signing_key: !Find [authentik_crypto.certificatekeypair, [name, "authentik Self-signed Certificate"]]
      sub_mode: hashed_user_id
      include_claims_in_id_token: true
      access_code_validity: minutes=1
      access_token_validity: minutes=5
      refresh_token_validity: days=30
      property_mappings:
        - !Find [authentik_providers_oauth2.scopemapping, [managed, goauthentik.io/providers/oauth2/scope-openid]]
        - !Find [authentik_providers_oauth2.scopemapping, [managed, goauthentik.io/providers/oauth2/scope-email]]
        - !Find [authentik_providers_oauth2.scopemapping, [managed, goauthentik.io/providers/oauth2/scope-profile]]

  # Application
  - model: authentik_core.application
    id: {{.AppName}}-application
    identifiers:
      slug: {{.AppName}}
    attrs:
      name: {{.DisplayName}}
      provider: !KeyOf {{.AppName}}-oauth2-provider
      policy_engine_mode: any
      group: ""
{{- if .LaunchURL}}
      meta_launch_url: "{{.LaunchURL}}"
{{- end}}
`

const forwardAuthBlueprintTemplate = `# Authentik Forward Auth Blueprint for {{.DisplayName}}
# Auto-generated by Bloud host-agent from catalog SSO config
version: 1
metadata:
  name: {{.AppName}}-sso-blueprint
  labels:
    managed-by: bloud

entries:
  # Proxy Provider for forward auth
  - model: authentik_providers_proxy.proxyprovider
    id: {{.AppName}}-proxy-provider
    identifiers:
      name: {{.DisplayName}} Proxy Provider
    attrs:
      authorization_flow: !Find [authentik_flows.flow, [slug, default-provider-authorization-implicit-consent]]
      invalidation_flow: !Find [authentik_flows.flow, [slug, default-provider-invalidation-flow]]
      mode: forward_single
      external_host: "{{.ExternalHost}}"
      access_token_validity: minutes=5

  # Application
  - model: authentik_core.application
    id: {{.AppName}}-application
    identifiers:
      slug: {{.AppName}}
    attrs:
      name: {{.DisplayName}}
      provider: !KeyOf {{.AppName}}-proxy-provider
      policy_engine_mode: any
      group: ""
{{- if .LaunchURL}}
      meta_launch_url: "{{.LaunchURL}}"
{{- end}}
`

const outpostBlueprintTemplate = `# Authentik Embedded Outpost Configuration
# Auto-generated by Bloud host-agent
# This blueprint adds all Bloud forward-auth providers to the embedded outpost.
# It also configures authentik_host and authentik_host_browser for correct OAuth redirects.
# IMPORTANT: When using state: present with config, all config values must be specified
# as the entire config dict is replaced (not merged).
version: 1
metadata:
  name: bloud-outpost-providers
  labels:
    managed-by: bloud

entries:
  # Update the embedded outpost to include all Bloud forward-auth providers
  - model: authentik_outposts.outpost
    state: present
    identifiers:
      name: authentik Embedded Outpost
    attrs:
      providers:
{{- range .Providers}}
        - !Find [authentik_providers_proxy.proxyprovider, [name, "{{.DisplayName}} Proxy Provider"]]
{{- end}}
      config:
        # URL for outpost to communicate with authentik (internal)
        authentik_host: "{{.AuthentikURL}}"
        # Browser-accessible URL for OAuth redirects (critical for forward-auth)
        authentik_host_browser: "{{.AuthentikURL}}"
        log_level: info
`
