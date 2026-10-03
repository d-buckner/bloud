// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

func (c *Client) findFlowID(ctx context.Context, slug string) (string, error) {
	var result struct {
		PK string `json:"pk"`
	}
	if err := c.cl.GET("/api/v3/flows/instances/"+url.PathEscape(slug)+"/").
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return "", fmt.Errorf("flow %s not found: %w", slug, err)
	}
	return result.PK, nil
}

func (c *Client) findGroupID(ctx context.Context, name string) (string, error) {
	var result struct {
		Results []struct {
			PK   string `json:"pk"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := c.cl.GET("/api/v3/core/groups/").
		Query("search", name).
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return "", fmt.Errorf("searching groups: %w", err)
	}

	for _, group := range result.Results {
		if group.Name == name {
			return group.PK, nil
		}
	}

	return "", fmt.Errorf("group %s not found", name)
}

func (c *Client) findUserID(ctx context.Context, username string) (int, error) {
	var result struct {
		Results []struct {
			PK       int    `json:"pk"`
			Username string `json:"username"`
		} `json:"results"`
	}
	if err := c.cl.GET("/api/v3/core/users/").
		Query("search", username).
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return 0, fmt.Errorf("searching users: %w", err)
	}

	for _, user := range result.Results {
		if user.Username == username {
			return user.PK, nil
		}
	}

	return 0, nil // Not found
}

func (c *Client) tokenExists(ctx context.Context, identifier string) (bool, error) {
	var result struct {
		Results []struct {
			Identifier string `json:"identifier"`
		} `json:"results"`
	}
	if err := c.cl.GET("/api/v3/core/tokens/").
		Query("identifier", identifier).
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return false, fmt.Errorf("searching tokens: %w", err)
	}

	for _, token := range result.Results {
		if token.Identifier == identifier {
			return true, nil
		}
	}

	return false, nil
}

// applicationExists checks if an application with the given slug exists
func (c *Client) applicationExists(ctx context.Context, slug string) (bool, error) {
	_, err := c.cl.GET("/api/v3/core/applications/" + url.PathEscape(slug) + "/").Do(ctx)
	if err == nil {
		return true, nil
	}
	if appclient.StatusOf(err) == 0 {
		return false, err // transport error
	}
	return false, nil // 404 (or any definitive non-OK) → does not exist
}
