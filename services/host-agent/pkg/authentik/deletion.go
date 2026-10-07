// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// DeleteApplication deletes an Authentik application by slug
func (c *Client) DeleteApplication(ctx context.Context, slug string) error {
	// 204 No Content = success, 404 = already deleted (acceptable)
	return c.cl.DELETE("/api/v3/core/applications/"+url.PathEscape(slug)+"/").
		OK(http.StatusNoContent, http.StatusNotFound).
		Exec(ctx)
}

// DeleteOAuth2Provider deletes an OAuth2 provider by name
func (c *Client) DeleteOAuth2Provider(ctx context.Context, providerName string) error {
	providerID, err := c.findProviderID(ctx, "oauth2", providerName)
	if err != nil {
		return err
	}
	if providerID == 0 {
		return nil // Provider doesn't exist
	}

	return c.deleteProviderByID(ctx, "oauth2", providerID)
}

// DeleteProxyProvider deletes a proxy provider by name
func (c *Client) DeleteProxyProvider(ctx context.Context, providerName string) error {
	providerID, err := c.findProviderID(ctx, "proxy", providerName)
	if err != nil {
		return err
	}
	if providerID == 0 {
		return nil // Provider doesn't exist
	}

	return c.deleteProviderByID(ctx, "proxy", providerID)
}

// findProviderID finds a provider ID by type and name
func (c *Client) findProviderID(ctx context.Context, providerType, name string) (int, error) {
	var result PaginatedResponse
	if err := c.cl.GET("/api/v3/providers/"+providerType+"/").
		Query("search", name).
		DoInto(ctx, &result); err != nil {
		return 0, fmt.Errorf("searching %s providers: %w", providerType, err)
	}

	// Find exact match
	for _, provider := range result.Results {
		if provider.Name == name {
			return provider.PK, nil
		}
	}

	return 0, nil // Not found
}

// deleteProviderByID deletes a provider by type and ID
func (c *Client) deleteProviderByID(ctx context.Context, providerType string, id int) error {
	// 204 No Content = success, 404 = already deleted (acceptable)
	return c.cl.DELETE(fmt.Sprintf("/api/v3/providers/%s/%d/", providerType, id)).
		OK(http.StatusNoContent, http.StatusNotFound).
		Exec(ctx)
}

// providerTypeForStrategy maps an SSO strategy to the Authentik provider type
// that strategy creates, and reports whether the strategy has a per-app
// provider at all. "ldap" authenticates through the shared LDAP outpost and
// "none" joins the provider not at all, so neither owns a provider to delete.
func providerTypeForStrategy(strategy string) (string, bool) {
	switch strategy {
	case "native-oidc":
		return "oauth2", true
	case "forward-auth":
		return "proxy", true
	default:
		return "", false
	}
}

// providerNameForStrategy builds the provider name the provisioning path uses
// for a strategy: "<DisplayName> OAuth2 Provider" / "<DisplayName> Proxy
// Provider", matching the blueprint templates in internal/sso.
func providerNameForStrategy(strategy, displayName string) string {
	switch strategy {
	case "native-oidc":
		return fmt.Sprintf("%s OAuth2 Provider", displayName)
	case "forward-auth":
		return fmt.Sprintf("%s Proxy Provider", displayName)
	default:
		return ""
	}
}

// findProvidersForApplication returns the PKs of every provider of
// providerType that is attached to the application named appSlug.
//
// This is the lookup that makes uninstall cleanup immune to display-name
// drift. A provider's name embeds the app's display name, so a catalog update
// that renames an app strands the provider created under the old name: the
// name-based delete searches for a string nothing produces any more, finds
// nothing, and reports success. The application link is keyed on the catalog
// ID, which never changes, and Authentik reports it on every provider row as
// `assigned_application_slug`.
//
// The filter is client-side because Authentik's `application` query parameter
// takes the object's UUID, not its slug, and Bloud does not store UUIDs.
func (c *Client) findProvidersForApplication(ctx context.Context, providerType, appSlug string) ([]int, error) {
	var result PaginatedResponse
	if err := c.cl.GET("/api/v3/providers/"+providerType+"/").DoInto(ctx, &result); err != nil {
		return nil, fmt.Errorf("listing %s providers: %w", providerType, err)
	}
	var ids []int
	for _, p := range result.Results {
		if p.AssignedApplicationSlug == appSlug {
			ids = append(ids, p.PK)
		}
	}
	return ids, nil
}

// deleteProvidersForApplication deletes every provider attached to appSlug.
// It reports how many it deleted so the caller can tell a real cleanup from a
// no-op.
func (c *Client) deleteProvidersForApplication(ctx context.Context, providerType, appSlug string) (int, error) {
	ids, err := c.findProvidersForApplication(ctx, providerType, appSlug)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := c.deleteProviderByID(ctx, providerType, id); err != nil {
			return len(ids), fmt.Errorf("deleting %s provider %d for %q: %w", providerType, id, appSlug, err)
		}
	}
	return len(ids), nil
}

// DeleteAppSSO deletes both the application and provider for an app.
// This is the main cleanup function to call during app uninstall.
//
// Provider first, application second, and the order is load-bearing: deleting
// the application clears the `assigned_application_slug` link on the provider
// it owned, which is the field the find-by-application lookup reads. Delete
// the application first and the provider stops being findable by the one key
// that cannot drift.
func (c *Client) DeleteAppSSO(ctx context.Context, appName, displayName, ssoStrategy string) error {
	if err := c.deleteAppProvider(ctx, appName, displayName, ssoStrategy); err != nil {
		return err
	}

	// Delete the application by slug.
	if err := c.DeleteApplication(ctx, appName); err != nil {
		return fmt.Errorf("deleting application: %w", err)
	}

	return nil
}

// deleteAppProvider removes the per-app provider a strategy created. A
// strategy with no per-app provider ("ldap", "none") is a no-op.
func (c *Client) deleteAppProvider(ctx context.Context, appName, displayName, ssoStrategy string) error {
	providerType, ok := providerTypeForStrategy(ssoStrategy)
	if !ok {
		return nil
	}

	// Primary key: the application link, which is the catalog ID and so is
	// immune to a display-name change.
	if _, err := c.deleteProvidersForApplication(ctx, providerType, appName); err != nil {
		return err
	}

	// Fallback for a provider that exists but carries no application link,
	// which is how a provider created outside Bloud's own provisioning path
	// would look. Idempotent: a name that matches nothing deletes nothing.
	name := providerNameForStrategy(ssoStrategy, displayName)
	if name == "" {
		return nil
	}
	var err error
	switch providerType {
	case "oauth2":
		err = c.DeleteOAuth2Provider(ctx, name)
	case "proxy":
		err = c.DeleteProxyProvider(ctx, name)
	}
	if err != nil {
		return fmt.Errorf("deleting %s provider: %w", providerType, err)
	}
	return nil
}

// Deprovision implements orchestrator.SSOProvisioner.Deprovision: it deletes
// the application and provider an app's previous SSO strategy created. It is
// idempotent by way of DeleteAppSSO, which treats an already-deleted provider
// as success. Strategies with no per-app provider ("none", "ldap") delete
// nothing beyond a possibly-absent application slug.
func (c *Client) Deprovision(ctx context.Context, appName, displayName, ssoStrategy string) error {
	return c.DeleteAppSSO(ctx, appName, displayName, ssoStrategy)
}
