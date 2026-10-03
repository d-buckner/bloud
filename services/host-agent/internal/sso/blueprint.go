// SPDX-License-Identifier: AGPL-3.0-only

package sso

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/secrets"
	"golang.org/x/crypto/hkdf"
)

// BlueprintGenerator generates Authentik OAuth2 blueprints from catalog SSO config
type BlueprintGenerator struct {
	hostSecret       string
	ldapBindPassword string
	baseURLs         []string // All base URLs (configured host + detected IPs)
	authentikURL     string   // browser-facing Authentik base URL
	issuerURL        string   // OIDC issuer base URL reachable from app containers (falls back to authentikURL)
	blueprintsDir    string
	secrets          *secrets.Manager
}

// NewBlueprintGenerator creates a new blueprint generator.
// baseURLs should contain the configured host URL first, followed by IP-based URLs.
// issuerURL is the base URL app containers use to reach the OIDC provider
// (discovery/token endpoints); when empty, authentikURL is used.
func NewBlueprintGenerator(hostSecret, ldapBindPassword string, baseURLs []string, authentikURL, issuerURL, blueprintsDir string, secretsMgr *secrets.Manager) *BlueprintGenerator {
	return &BlueprintGenerator{
		hostSecret:       hostSecret,
		ldapBindPassword: ldapBindPassword,
		baseURLs:         baseURLs,
		authentikURL:     authentikURL,
		issuerURL:        issuerURL,
		blueprintsDir:    blueprintsDir,
		secrets:          secretsMgr,
	}
}

// issuerBaseURL returns the OIDC issuer base URL for app containers,
// falling back to the browser-facing Authentik URL when not configured.
func (g *BlueprintGenerator) issuerBaseURL() string {
	if g.issuerURL != "" {
		return g.issuerURL
	}
	return g.authentikURL
}

// primaryBaseURL returns the first (configured) base URL.
func (g *BlueprintGenerator) primaryBaseURL() string {
	if len(g.baseURLs) > 0 {
		return g.baseURLs[0]
	}
	return ""
}

// appSubdomainURL builds a subdomain URL for an app from a base URL.
// e.g., "http://localhost:8080" + "miniflux" → "http://miniflux.localhost:8080"
func appSubdomainURL(baseURL, appName string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL // fallback
	}
	parsed.Host = appName + "." + parsed.Host
	return parsed.String()
}

// GenerateForApp generates an Authentik blueprint for an app with SSO
func (g *BlueprintGenerator) GenerateForApp(app *catalog.App) error {
	switch app.SSO.Strategy {
	case "native-oidc":
		return g.generateOIDCBlueprint(app)
	case "forward-auth":
		return g.generateForwardAuthBlueprint(app)
	case "ldap":
		return g.generateLDAPBlueprint(app)
	default:
		return nil // No blueprint needed for apps without SSO
	}
}

// generateOIDCBlueprint creates an OAuth2 Provider blueprint for native OIDC apps.
// Registers redirect URIs for all base URLs so OAuth works from any host/IP.
func (g *BlueprintGenerator) generateOIDCBlueprint(app *catalog.App) error {
	inputs := g.OIDCInputsForApp(app)
	if inputs == nil {
		return fmt.Errorf("app %q does not use the native-oidc strategy", app.CatalogID)
	}

	blueprint, err := g.renderOIDCBlueprint(app, inputs.ClientID, inputs.ClientSecret, inputs.ClientType, inputs.RedirectURIs, inputs.LaunchURL)
	if err != nil {
		return fmt.Errorf("rendering OIDC blueprint: %w", err)
	}

	return g.writeBlueprint(app.CatalogID, blueprint)
}

// generateForwardAuthBlueprint creates a Proxy Provider blueprint for forward auth apps
func (g *BlueprintGenerator) generateForwardAuthBlueprint(app *catalog.App) error {
	// external_host should be the root URL, not the app-specific path.
	// The callback URL (/outpost.goauthentik.io/callback) is handled at root level by Traefik.
	externalHost := g.primaryBaseURL()
	launchURL := appSubdomainURL(g.primaryBaseURL(), app.CatalogID)

	blueprint, err := g.renderForwardAuthBlueprint(app, externalHost, launchURL)
	if err != nil {
		return fmt.Errorf("rendering forward-auth blueprint: %w", err)
	}

	return g.writeBlueprint(app.CatalogID, blueprint)
}

// generateLDAPBlueprint creates app-specific groups for LDAP authentication
// The LDAP provider and outpost are created separately via GenerateLDAPOutpostBlueprint
func (g *BlueprintGenerator) generateLDAPBlueprint(app *catalog.App) error {
	launchURL := appSubdomainURL(g.primaryBaseURL(), app.CatalogID)

	blueprint, err := g.renderLDAPBlueprint(app, launchURL)
	if err != nil {
		return fmt.Errorf("rendering LDAP blueprint: %w", err)
	}

	return g.writeBlueprint(app.CatalogID, blueprint)
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

func (g *BlueprintGenerator) generateClientID(appName string) string {
	return fmt.Sprintf("%s-client", appName)
}

func (g *BlueprintGenerator) generateClientSecret(appName string) string {
	// Use HKDF to derive a deterministic, unique secret for each app from the host secret.
	// This ensures:
	// 1. Same secret is generated for the same app + hostSecret
	// 2. Different apps get different secrets
	// 3. Secrets are cryptographically strong
	secret := DeriveSecret(g.hostSecret, "oauth-client-secret:"+appName, 32)

	// Persist the derived secret so it survives reconciliation
	if g.secrets != nil {
		_ = g.secrets.SetAppSecret(appName, "oauthClientSecret", secret)
	}

	return secret
}

// DeriveSecret uses HKDF-SHA256 to derive a deterministic secret from a master secret.
func DeriveSecret(masterSecret, context string, length int) string {
	if masterSecret == "" {
		// Fallback to old behavior if no master secret configured
		return context + "-fallback-secret"
	}

	hkdfReader := hkdf.New(sha256.New, []byte(masterSecret), nil, []byte(context))
	key := make([]byte, length)
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		// This should never happen with HKDF
		panic(fmt.Sprintf("HKDF read failed: %v", err))
	}
	// Use RawURLEncoding (no padding) to avoid the '=' character.
	// Some OAuth clients URL-encode credentials per RFC 6749 before base64 encoding,
	// but Authentik doesn't URL-decode them, causing authentication failures.
	return base64.RawURLEncoding.EncodeToString(key)
}

// GetLDAPBindPassword returns the LDAP bind password for apps to use
// This should match what's in the blueprint
func (g *BlueprintGenerator) GetLDAPBindPassword() string {
	return g.ldapBindPassword
}
