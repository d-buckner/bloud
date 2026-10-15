// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"fmt"
	"net/http"
)

// BlueprintApplied is what Authentik records about the last apply of one
// blueprint file.
type BlueprintApplied struct {
	// Hash is the hex sha512 of the file content the worker last applied, empty
	// when the worker has not discovered the file yet.
	Hash string `json:"last_applied_hash"`
	// Status is the instance's apply status ("successful", "error", ...).
	Status string `json:"status"`
}

// BlueprintApplied reports the last apply of the blueprint at path, relative to
// the worker's /blueprints directory. A file the worker has not discovered yet
// answers the zero value rather than an error.
func (c *Client) BlueprintApplied(ctx context.Context, path string) (BlueprintApplied, error) {
	var result struct {
		Results []BlueprintApplied `json:"results"`
	}
	if err := c.cl.GET("/api/v3/managed/blueprints/").
		Query("path", path).
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return BlueprintApplied{}, fmt.Errorf("reading blueprint %s: %w", path, err)
	}
	if len(result.Results) == 0 {
		return BlueprintApplied{}, nil
	}
	return result.Results[0], nil
}
