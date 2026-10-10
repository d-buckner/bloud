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

// findGroupID resolves a group by its exact name. `name` is Authentik's exact
// filter for groups; `search` is the full-text one, which is both slower and
// willing to answer with a group whose name merely resembles the one asked for.
func (c *Client) findGroupID(ctx context.Context, name string) (string, error) {
	var result struct {
		Results []struct {
			PK   string `json:"pk"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := c.cl.GET("/api/v3/core/groups/").
		Query("name", name).
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return "", fmt.Errorf("looking up group %s: %w", name, err)
	}

	for _, group := range result.Results {
		if group.Name == name {
			return group.PK, nil
		}
	}

	return "", fmt.Errorf("group %s not found", name)
}

// GroupRef is a group as far as membership management needs to know it: the
// primary key that addresses it (a UUID, since Group's real PK is `group_uuid`)
// and which users it already holds.
type GroupRef struct {
	PK    string `json:"pk"`
	Name  string `json:"name"`
	Users []int  `json:"users"`
}

// HasUser reports whether the group already holds that user.
func (g *GroupRef) HasUser(pk int) bool {
	for _, u := range g.Users {
		if u == pk {
			return true
		}
	}
	return false
}

// lookupAdminsGroup resolves Authentik's superuser group together with its
// membership, in one request: the list serializer carries `users` next to `pk`.
//
// Reading membership from the call that resolves the group is what lets an
// add_user be skipped instead of issued every pass. Authentik's add_user is
// idempotent, so re-issuing it was never wrong, only a write per pass per service
// account for a fact one GET already answers.
func (c *Client) lookupAdminsGroup(ctx context.Context) (*GroupRef, error) {
	var result struct {
		Results []GroupRef `json:"results"`
	}
	if err := c.cl.GET("/api/v3/core/groups/").
		Query("name", AdminsGroup).
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return nil, fmt.Errorf("looking up group %s: %w", AdminsGroup, err)
	}

	for i := range result.Results {
		if result.Results[i].Name == AdminsGroup {
			return &result.Results[i], nil
		}
	}

	return nil, fmt.Errorf("group %s not found", AdminsGroup)
}

// findUserID resolves a user by exact username, returning 0 when there is no
// such account. See lookupUser for why the filter is exact.
func (c *Client) findUserID(ctx context.Context, username string) (int, error) {
	user, err := c.lookupUser(ctx, username)
	if err != nil {
		return 0, err
	}
	if user == nil {
		return 0, nil // Not found
	}
	return user.PK, nil
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
