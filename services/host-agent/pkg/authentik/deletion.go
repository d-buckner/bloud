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

// DeleteAppSSO deletes both the application and provider for an app.
// This is the main cleanup function to call during app uninstall.
func (c *Client) DeleteAppSSO(ctx context.Context, appName, displayName, ssoStrategy string) error {
	// Delete the application first (by slug)
	if err := c.DeleteApplication(ctx, appName); err != nil {
		return fmt.Errorf("deleting application: %w", err)
	}

	// Delete the provider based on strategy
	switch ssoStrategy {
	case "native-oidc":
		providerName := fmt.Sprintf("%s OAuth2 Provider", displayName)
		if err := c.DeleteOAuth2Provider(ctx, providerName); err != nil {
			return fmt.Errorf("deleting OAuth2 provider: %w", err)
		}
	case "forward-auth":
		providerName := fmt.Sprintf("%s Proxy Provider", displayName)
		if err := c.DeleteProxyProvider(ctx, providerName); err != nil {
			return fmt.Errorf("deleting proxy provider: %w", err)
		}
	}

	return nil
}
