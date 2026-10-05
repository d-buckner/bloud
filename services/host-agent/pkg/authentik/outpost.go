// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// OutpostResponse represents an Authentik outpost in API responses
type OutpostResponse struct {
	PK        string `json:"pk"`
	Name      string `json:"name"`
	Providers []int  `json:"providers"`
}

// OutpostPaginatedResponse represents a paginated outpost API response
type OutpostPaginatedResponse struct {
	Pagination struct {
		Count int `json:"count"`
	} `json:"pagination"`
	Results []OutpostResponse `json:"results"`
}

// AddProviderToEmbeddedOutpost adds a proxy provider to the embedded outpost
func (c *Client) AddProviderToEmbeddedOutpost(ctx context.Context, providerName string) error {
	// Find the proxy provider ID
	providerID, err := c.findProviderID(ctx, "proxy", providerName)
	if err != nil {
		return fmt.Errorf("finding provider: %w", err)
	}
	if providerID == 0 {
		return fmt.Errorf("provider %s not found", providerName)
	}

	// Find the embedded outpost
	outpost, err := c.findEmbeddedOutpost(ctx)
	if err != nil {
		return fmt.Errorf("finding embedded outpost: %w", err)
	}
	if outpost == nil {
		return fmt.Errorf("embedded outpost not found")
	}

	// Check if provider is already in outpost
	for _, pid := range outpost.Providers {
		if pid == providerID {
			return nil // Already added
		}
	}

	// Add the provider to the outpost
	outpost.Providers = append(outpost.Providers, providerID)
	return c.updateOutpostProviders(ctx, outpost.PK, outpost.Providers)
}

// findEmbeddedOutpost finds the authentik Embedded Outpost
func (c *Client) findEmbeddedOutpost(ctx context.Context) (*OutpostResponse, error) {
	var result OutpostPaginatedResponse
	if err := c.cl.GET("/api/v3/outposts/instances/").
		Query("search", "Embedded").
		DoInto(ctx, &result); err != nil {
		return nil, fmt.Errorf("searching outposts: %w", err)
	}

	// Find the embedded outpost
	for i, outpost := range result.Results {
		if outpost.Name == "authentik Embedded Outpost" {
			return &result.Results[i], nil
		}
	}

	return nil, nil
}

// EnsureEmbeddedOutpostHost sets the authentik_host config on the embedded outpost so the
// embedded outpost generates browser-accessible authorize redirect URLs (e.g. via Traefik)
// rather than the server's internal bind address. Safe to call repeatedly: only patches
// when the value differs.
func (c *Client) EnsureEmbeddedOutpostHost(ctx context.Context, baseURL string) error {
	outpost, err := c.findEmbeddedOutpost(ctx)
	if err != nil {
		return fmt.Errorf("finding embedded outpost: %w", err)
	}
	if outpost == nil {
		return nil // Not set up yet; will be called again after setup
	}

	// Fetch full outpost object to get current config
	path := "/api/v3/outposts/instances/" + outpost.PK + "/"
	body, err := c.cl.GET(path).Do(ctx)
	if err != nil {
		return fmt.Errorf("fetching outpost: %w", err)
	}

	var full map[string]any
	if err := json.Unmarshal(body, &full); err != nil {
		return fmt.Errorf("parsing outpost: %w", err)
	}

	config, _ := full["config"].(map[string]any)
	if config == nil {
		config = make(map[string]any)
	}
	if config["authentik_host"] == baseURL {
		return nil // Already set correctly
	}

	config["authentik_host"] = baseURL
	full["config"] = config

	if err := c.cl.PUT(path).JSON(full).OK(http.StatusOK).Exec(ctx); err != nil {
		return fmt.Errorf("updating outpost host: %w", err)
	}
	return nil
}

// updateOutpostProviders updates the providers list for an outpost
func (c *Client) updateOutpostProviders(ctx context.Context, outpostPK string, providers []int) error {
	return c.cl.PATCH("/api/v3/outposts/instances/" + outpostPK + "/").
		JSON(map[string]any{"providers": providers}).
		OK(http.StatusOK).
		Exec(ctx)
}

func (c *Client) findOutpostByName(ctx context.Context, name string) (*OutpostResponse, error) {
	var result OutpostPaginatedResponse
	if err := c.cl.GET("/api/v3/outposts/instances/").
		Query("search", name).
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return nil, fmt.Errorf("searching outposts: %w", err)
	}

	for i, outpost := range result.Results {
		if outpost.Name == name {
			return &result.Results[i], nil
		}
	}

	return nil, nil // Not found
}
