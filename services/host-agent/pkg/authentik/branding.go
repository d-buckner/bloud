// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// EnsureLoginConfiguration applies Bloud-specific login page settings:
// - Sets the authentication flow title to "Sign in to Bloud"
// - Configures the identification stage to only accept username (not email)
// This is idempotent: safe to call on every PostStart.
//
// Authentik creates default flows asynchronously via blueprints after the health endpoint
// returns ready, so we retry until our changes stick. The blueprint for the default
// authentication flow runs during startup and can overwrite a patch applied just before it
// completes. We detect this by re-reading the flow title 3 seconds after patching: if a
// blueprint reset it, the outer loop retries, eventually patching after all blueprints finish.
//
// Refs:
//   - PATCH /api/v3/flows/instances/:slug/ (slug path param, title body field)
//   - GET  /api/v3/flows/instances/:slug/ (verify title)
//   - PATCH /api/v3/stages/identification/:stage_uuid/ (UUID path param, user_fields body field)
func (c *Client) EnsureLoginConfiguration(ctx context.Context) error {
	const (
		timeout  = 2 * time.Minute
		interval = 10 * time.Second
	)
	deadline := time.Now().Add(timeout)

	for {
		err := c.applyAndVerifyLoginConfiguration(ctx)
		if err == nil {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for login configuration to apply: %w", err)
		}

		if err := sleepCtx(ctx, interval); err != nil {
			return fmt.Errorf("timed out waiting for login configuration to apply: %w", err)
		}
	}
}

// applyAndVerifyLoginConfiguration patches the flow title and identification stage, then
// waits 3 seconds and re-reads both to confirm a blueprint didn't overwrite them.
func (c *Client) applyAndVerifyLoginConfiguration(ctx context.Context) error {
	if err := c.ensureFlowTitle(ctx, "default-authentication-flow", "Sign in to Bloud"); err != nil {
		return fmt.Errorf("ensuring flow title: %w", err)
	}

	if err := c.ensureIdentificationStageUsernameOnly(ctx, "default-authentication-identification"); err != nil {
		return fmt.Errorf("ensuring identification stage: %w", err)
	}

	// Wait briefly, then re-read both the flow title and identification stage user_fields
	// to confirm no blueprint overwrote our patches.
	if err := sleepCtx(ctx, 3*time.Second); err != nil {
		return err
	}

	title, err := c.getFlowTitle(ctx, "default-authentication-flow")
	if err != nil {
		return fmt.Errorf("verifying flow title: %w", err)
	}
	if title != "Sign in to Bloud" {
		return fmt.Errorf("flow title was reset to %q by a blueprint, will retry", title)
	}

	userFields, err := c.getIdentificationStageUserFields(ctx, "default-authentication-identification")
	if err != nil {
		return fmt.Errorf("verifying identification stage: %w", err)
	}
	if len(userFields) != 1 || userFields[0] != "username" {
		return fmt.Errorf("identification stage user_fields was reset to %v by a blueprint, will retry", userFields)
	}

	return nil
}

// getFlowTitle fetches the current title of a flow by slug.
func (c *Client) getFlowTitle(ctx context.Context, slug string) (string, error) {
	var result struct {
		Title string `json:"title"`
	}
	if err := c.cl.GET("/api/v3/flows/instances/"+url.PathEscape(slug)+"/").
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return "", fmt.Errorf("fetching flow: %w", err)
	}
	return result.Title, nil
}

// getIdentificationStageUserFields fetches the current user_fields of an identification stage by name.
func (c *Client) getIdentificationStageUserFields(ctx context.Context, stageName string) ([]string, error) {
	var result struct {
		Results []struct {
			Name       string   `json:"name"`
			UserFields []string `json:"user_fields"`
		} `json:"results"`
	}
	if err := c.cl.GET("/api/v3/stages/identification/").
		Query("search", stageName).
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return nil, fmt.Errorf("fetching identification stage: %w", err)
	}

	for _, stage := range result.Results {
		if stage.Name == stageName {
			return stage.UserFields, nil
		}
	}
	return nil, fmt.Errorf("identification stage %q not found", stageName)
}

// ensureFlowTitle PATCHes the title of a flow by slug.
// API: PATCH /api/v3/flows/instances/:slug/ (slug is the URL path parameter).
func (c *Client) ensureFlowTitle(ctx context.Context, slug, title string) error {
	return c.cl.PATCH("/api/v3/flows/instances/" + url.PathEscape(slug) + "/").
		JSON(map[string]string{"title": title}).
		OK(http.StatusOK).
		Exec(ctx)
}

// ensureIdentificationStageUsernameOnly sets user_fields to ["username"] on an identification stage.
// API: GET /api/v3/stages/identification/?search=name to find the stage UUID,
// then PATCH /api/v3/stages/identification/:stage_uuid/ with user_fields.
// Valid user_fields values: email, username, upn.
func (c *Client) ensureIdentificationStageUsernameOnly(ctx context.Context, stageName string) error {
	// pk is a UUID string (stage_uuid), used as the path parameter for PATCH
	var result struct {
		Results []struct {
			PK   string `json:"pk"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := c.cl.GET("/api/v3/stages/identification/").
		Query("search", stageName).
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return fmt.Errorf("fetching identification stages: %w", err)
	}

	var stageUUID string
	for _, stage := range result.Results {
		if stage.Name == stageName {
			stageUUID = stage.PK
			break
		}
	}

	if stageUUID == "" {
		return fmt.Errorf("identification stage %q not found", stageName)
	}

	return c.cl.PATCH("/api/v3/stages/identification/" + stageUUID + "/").
		JSON(map[string]any{"user_fields": []string{"username"}}).
		OK(http.StatusOK).
		Exec(ctx)
}

// EnsureBranding updates the default Authentik brand with the provided CSS.
// The CSS is pushed inline because Authentik uses Constructable Stylesheets
// which forbid @import rules in branding_custom_css.
// This is idempotent: safe to call on every PostStart.
//
// The default brand is created by an Authentik migration, which can lag
// behind the server readiness probe on slow hosts (cold CI runners). A
// PostStart error is terminal in the reconciler (ERROR status is never
// retried), so a single transient "brand not found" would brick the SSO
// stack. Retry while the brand is absent; other API errors fail fast.
func (c *Client) EnsureBranding(ctx context.Context, css string) error {
	const (
		maxAttempts = 15
		retryDelay  = 4 * time.Second
	)

	brandPK := ""
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		brandPK, lastErr = c.defaultBrandPK(ctx)
		if lastErr == nil {
			break
		}
		if !errors.Is(lastErr, errBrandNotFound) {
			return lastErr
		}
		if attempt < maxAttempts {
			if err := sleepCtx(ctx, retryDelay); err != nil {
				return err
			}
		}
	}
	if brandPK == "" {
		return lastErr
	}

	return c.cl.PATCH("/api/v3/core/brands/" + brandPK + "/").
		JSON(map[string]string{"branding_custom_css": css}).
		OK(http.StatusOK).
		Exec(ctx)
}

// errBrandNotFound indicates the default brand does not exist yet (migration
// still running).
var errBrandNotFound = fmt.Errorf("default brand not found")

// defaultBrandPK returns the UUID of the default Authentik brand
// (domain = "authentik-default"), or errBrandNotFound while it does not exist.
func (c *Client) defaultBrandPK(ctx context.Context) (string, error) {
	var result struct {
		Results []struct {
			PK string `json:"brand_uuid"`
		} `json:"results"`
	}
	if err := c.cl.GET("/api/v3/core/brands/").
		Query("domain", "authentik-default").
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return "", fmt.Errorf("fetching brands: %w", err)
	}

	if len(result.Results) == 0 {
		return "", errBrandNotFound
	}
	return result.Results[0].PK, nil
}
