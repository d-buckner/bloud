// SPDX-License-Identifier: AGPL-3.0-only

package sso

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
)

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
		AuthentikURL: g.authentikURL,
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

func (g *BlueprintGenerator) renderOIDCBlueprint(app *catalog.App, clientID, clientSecret, clientType string, redirectURIs []string, launchURL string) (string, error) {
	if clientType == "" {
		clientType = "confidential"
	}
	data := struct {
		AppName      string
		DisplayName  string
		ClientID     string
		ClientSecret string
		ClientType   string
		RedirectURIs []string
		LaunchURL    string
	}{
		AppName:      app.CatalogID,
		DisplayName:  app.DisplayName,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		ClientType:   clientType,
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
		AppName:      app.CatalogID,
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

func (g *BlueprintGenerator) renderLDAPBlueprint(app *catalog.App, launchURL string) (string, error) {
	data := struct {
		AppName     string
		DisplayName string
		LaunchURL   string
	}{
		AppName:     app.CatalogID,
		DisplayName: app.DisplayName,
		LaunchURL:   launchURL,
	}

	tmpl, err := template.New("blueprint").Parse(ldapAppBlueprintTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// LDAPApp represents an app that uses LDAP authentication
type LDAPApp struct {
	Name        string
	DisplayName string
}

// GenerateLDAPOutpostBlueprint creates the LDAP provider, service account, and outpost
// This is called when any app with LDAP strategy is installed
func (g *BlueprintGenerator) GenerateLDAPOutpostBlueprint(apps []LDAPApp, ldapBindPassword string) error {
	if len(apps) == 0 {
		// No LDAP apps - remove the LDAP outpost blueprint if it exists
		path := filepath.Join(g.blueprintsDir, "bloud-ldap.yaml")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing LDAP outpost blueprint: %w", err)
		}
		return nil
	}

	blueprint, err := g.renderLDAPOutpostBlueprint(apps, ldapBindPassword)
	if err != nil {
		return fmt.Errorf("rendering LDAP outpost blueprint: %w", err)
	}

	if err := os.MkdirAll(g.blueprintsDir, 0755); err != nil {
		return fmt.Errorf("creating blueprints directory: %w", err)
	}

	path := filepath.Join(g.blueprintsDir, "bloud-ldap.yaml")
	if err := os.WriteFile(path, []byte(blueprint), 0644); err != nil {
		return fmt.Errorf("writing LDAP outpost blueprint: %w", err)
	}

	return nil
}

func (g *BlueprintGenerator) renderLDAPOutpostBlueprint(apps []LDAPApp, ldapBindPassword string) (string, error) {
	data := struct {
		Apps             []LDAPApp
		LDAPBindPassword string
		AuthentikURL     string
	}{
		Apps:             apps,
		LDAPBindPassword: ldapBindPassword,
		AuthentikURL:     g.authentikURL,
	}

	tmpl, err := template.New("ldap-outpost").Parse(ldapOutpostBlueprintTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}
