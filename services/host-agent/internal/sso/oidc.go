// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package sso

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
)

// OIDCInputs holds the deterministic inputs for an app's native OIDC
// integration: client credentials, the registered redirect URIs, the issuer
// (discovery) URL, and the launch URL.
type OIDCInputs struct {
	ClientID     string
	ClientSecret string
	IssuerURL    string
	RedirectURIs []string
	LaunchURL    string
}

// OIDCInputsForApp computes the deterministic OIDC inputs for a native-oidc app.
// The client secret is derived from the host secret so every Bloud host derives
// the same value without a shared store. A redirect URI is registered for every
// base URL (host + detected IPs) so login works from any of them, plus a
// direct-port URI on the primary base URL for debugging.
// Returns nil when the app does not use the native-oidc strategy.
func (g *BlueprintGenerator) OIDCInputsForApp(app *catalog.App) *OIDCInputs {
	if app == nil || app.SSO.Strategy != "native-oidc" {
		return nil
	}

	clientID := g.generateClientID(app.CatalogID)
	clientSecret := g.generateClientSecret(app.CatalogID)

	var redirectURIs []string
	for _, baseURL := range g.baseURLs {
		redirectURIs = append(redirectURIs, AppSubdomainURL(baseURL, app.CatalogID)+app.SSO.CallbackPath)
	}
	if app.Port > 0 {
		parsed, err := url.Parse(g.primaryBaseURL())
		if err == nil {
			debugURL := &url.URL{
				Scheme: parsed.Scheme,
				Host:   net.JoinHostPort(parsed.Hostname(), fmt.Sprintf("%d", app.Port)),
			}
			redirectURIs = append(redirectURIs, debugURL.String()+app.SSO.CallbackPath)
		}
	}

	return &OIDCInputs{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		IssuerURL:    strings.TrimSuffix(g.issuerBaseURL(), "/") + "/application/o/" + app.CatalogID + "/",
		RedirectURIs: redirectURIs,
		LaunchURL:    AppSubdomainURL(g.primaryBaseURL(), app.CatalogID),
	}
}
