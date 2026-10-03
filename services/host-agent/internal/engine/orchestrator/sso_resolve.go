// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"fmt"
	"net/url"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sso"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
)

// ssoURLs is the resolved set of SSO URLs for one provisioning pass.
type ssoURLs struct {
	hostSet      hostset.HostSet
	baseURLs     []string // every base URL for redirect-URI registration (primary first, then other hosts, then IPs)
	hostSecret   string
	authentikURL string // browser-accessible Authentik URL for OIDC issuer/discovery
	issuerURL    string // OIDC issuer base URL reachable from app containers (empty = authentikURL)
}

// resolveSSOURLs computes the SSO URLs from the live host state when one is
// configured, falling back to the legacy single-URL config fields. IP-based
// base URLs (detected local addresses) are appended so login keeps working
// when the host is reached by IP.
func (o *Orchestrator) resolveSSOURLs() ssoURLs {
	if o.hosts != nil {
		hs := o.hosts.Get()
		return ssoURLs{
			hostSet:      hs,
			baseURLs:     hs.AllBaseURLs(),
			hostSecret:   o.ssoHostSecret,
			authentikURL: hs.PrimaryBaseURL(),
			issuerURL:    hs.IssuerBaseURL(),
		}
	}
	// Legacy path: no live address state, so build the set from the SSO base
	// URL string. An unparseable value falls back to the default address rather
	// than a half-built set, because every URL derived from this point is a
	// redirect URI that has to be well-formed.
	legacy, err := hostset.ParsePublicURL(o.ssoBaseURL)
	if err != nil {
		o.logger.Warn("unparseable sso base url, using the default address",
			"ssoBaseURL", o.ssoBaseURL, "error", err)
		legacy, _ = hostset.ParsePublicURL(hostset.DefaultPublicURL)
	}
	return ssoURLs{
		hostSet:      hostset.New(legacy).WithServedPort(o.config.TraefikPort),
		baseURLs:     append([]string{o.ssoBaseURL}, netutil.LANBaseURLs(o.config.TraefikPort)...),
		hostSecret:   o.ssoHostSecret,
		authentikURL: o.ssoAuthentikURL,
		issuerURL:    o.ssoIssuerURL,
	}
}

// ensureSSO provisions the per-app SSO provider in the identity provider for
// apps that use forward-auth or native-oidc SSO. It is a no-op when SSO is not
// configured or the app's strategy is not provisioned in the identity provider
// (e.g. "ldap", which is provisioned by the LDAP outpost). Safe to call on
// every lifecycle pass (idempotent).
func (o *Orchestrator) ensureSSO(ctx context.Context, id string) error {
	if o.sso == nil || o.catalog == nil {
		return nil
	}
	u := o.resolveSSOURLs()
	if len(u.baseURLs) == 0 {
		return nil
	}
	// Use the owning app's catalog ID for subdomain and provider name. Graph
	// nodes are container names (e.g. "apps-navidrome") for apps defined with a
	// containers list, while routing and Authentik must use the catalog ID
	// ("navidrome") so forward-auth matches the app's real subdomain.
	appID := o.ownerApp(id)
	catalogApp, err := o.catalog.Get(appID)
	if err != nil || catalogApp == nil {
		return nil
	}
	// SSO is an app-level concern: provision it exactly once, on the app's
	// primary container node. The inter-app dependency edges are attached to
	// the same node, so it runs after SSO dependencies (e.g. Authentik) are
	// ready. Non-primary nodes (postgres, redis, ...) skip it.
	if o.primaryContainerNode(appID) != id {
		return nil
	}

	switch catalogApp.SSO.Strategy {
	case "forward-auth":
		o.logger.Info("provisioning forward-auth SSO", "app", appID)
		externalURL := buildAppSubdomainURL(u.hostSet.PrimaryBaseURL(), appID)
		return o.sso.EnsureForwardAuth(ctx, appID, catalogApp.DisplayName, externalURL)

	case "native-oidc":
		if u.hostSecret == "" || u.authentikURL == "" {
			o.logger.Warn("native-oidc SSO skipped: missing SSO host secret or Authentik URL", "app", appID)
			return nil
		}
		inputs := o.oidcInputsForApp(catalogApp, u)
		if inputs == nil || len(inputs.RedirectURIs) == 0 {
			return fmt.Errorf("building OIDC inputs for %q", appID)
		}
		o.logger.Info("provisioning native-oidc SSO", "app", appID)
		return o.sso.EnsureNativeOIDC(ctx, appID, catalogApp.DisplayName, inputs.ClientID, inputs.ClientSecret, inputs.RedirectURIs, inputs.LaunchURL,
			authentik.OIDCTuning{ExtraScopes: inputs.ExtraScopes, AccessTokenMinutes: inputs.AccessTokenMinutes})
	}

	return nil
}

// oidcInputsForApp computes the deterministic OIDC inputs for a native-oidc
// app from the resolved SSO URLs and host secret. Returns nil when the SSO
// base URL or host secret is not configured.
func (o *Orchestrator) oidcInputsForApp(catalogApp *catalog.App, u ssoURLs) *sso.OIDCInputs {
	if len(u.baseURLs) == 0 || u.hostSecret == "" {
		return nil
	}
	issuerURL := u.issuerURL
	if catalogApp.SSO.LoopbackIssuer {
		// The app's OIDC client refuses a non-loopback http issuer, so on a
		// plain-http deployment its issuer routes through the host loopback
		// rather than the shared issuer host. The app container shares the host
		// network namespace, which makes localhost:<compat port> reach Traefik.
		//
		// Under a https public URL the override is skipped (empty return): the
		// client accepts the public issuer, and the loopback string would send
		// the browser to the visitor's own machine instead of this instance.
		if loopback := u.hostSet.LoopbackIssuerBaseURL(); loopback != "" {
			issuerURL = loopback
		}
	}
	gen := sso.NewBlueprintGenerator(
		u.hostSecret,
		"",
		u.baseURLs,
		u.authentikURL,
		issuerURL,
		"", // no blueprints dir: provisioning goes through the identity provider API
		nil,
	)
	return gen.OIDCInputsForApp(catalogApp)
}

// buildAppSubdomainURL constructs the app's subdomain URL from a base URL.
// e.g., "http://localhost:8080" + "navidrome" → "http://navidrome.localhost:8080"
// appPublicURL is the origin a browser dials for an installed app: the
// instance's public address with the app's own subdomain prepended, which is
// what Traefik's `HostRegexp(^<app>\.)` route serves. An integration binding
// hands this to a consumer that reaches its provider through the browser rather
// than through the container network, so the consumer never has to guess what
// address the operator's users actually type.
func (o *Orchestrator) appPublicURL(appID string) string {
	return buildAppSubdomainURL(o.resolveSSOURLs().hostSet.PrimaryBaseURL(), appID)
}

func buildAppSubdomainURL(baseURL, appName string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	parsed.Host = appName + "." + parsed.Host
	return parsed.String()
}
