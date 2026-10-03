// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// getScopePropertyMappings retrieves the UUIDs of scope property mappings by scope name
func (c *Client) getScopePropertyMappings(ctx context.Context, scopes []string) ([]string, error) {
	all, err := c.listScopeMappings(ctx)
	if err != nil {
		return nil, err
	}

	// Build a set of requested scopes for quick lookup
	scopeSet := make(map[string]bool)
	for _, s := range scopes {
		scopeSet[s] = true
	}

	// Find matching mappings
	var mappings []string
	for _, mapping := range all {
		if scopeSet[mapping.ScopeName] {
			mappings = append(mappings, mapping.PK)
		}
	}

	return mappings, nil
}

// scopeMapping is one of Authentik's OAuth2 scope property mappings.
type scopeMapping struct {
	PK        string `json:"pk"`
	ScopeName string `json:"scope_name"`
}

// listScopeMappings returns every OAuth2 scope property mapping.
func (c *Client) listScopeMappings(ctx context.Context) ([]scopeMapping, error) {
	var result struct {
		Results []scopeMapping `json:"results"`
	}
	if err := c.cl.GET("/api/v3/propertymappings/provider/scope/").
		Query("page_size", "50").
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return nil, fmt.Errorf("listing scope mappings: %w", err)
	}
	return result.Results, nil
}

// extraScopeMappings resolves the given scope names to their mapping UUIDs, in
// the order requested. Unlike getScopePropertyMappings it fails when a scope has
// no mapping: an app that declares a scope (sso.scopes) needs it, and silently
// dropping it would surface later as a confusing sign-in failure in the app.
func (c *Client) extraScopeMappings(ctx context.Context, scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, nil
	}
	all, err := c.listScopeMappings(ctx)
	if err != nil {
		return nil, err
	}
	byScope := make(map[string]string, len(all))
	for _, m := range all {
		byScope[m.ScopeName] = m.PK
	}
	var pks, missing []string
	for _, scope := range scopes {
		pk, ok := byScope[scope]
		if !ok {
			missing = append(missing, scope)
			continue
		}
		pks = append(pks, pk)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("authentik has no scope mapping for %s", strings.Join(missing, ", "))
	}
	return pks, nil
}

// bloudEmailScopeMappingName identifies the custom scope mapping Bloud
// creates for the OIDC "email" scope.
const bloudEmailScopeMappingName = "Bloud OIDC: OpenID 'email' (verified)"

// bloudEmailScopeMappingExpression reports the user email as verified.
// Bloud is the identity provider and user identities are operator-managed,
// so the email is verified from the OP's point of view.
const bloudEmailScopeMappingExpression = `return {
    "email": request.user.email,
    "email_verified": True
}`

// ensureBloudEmailScopeMapping returns the UUID of a non-managed scope
// mapping for the OIDC "email" scope that reports email_verified: True.
// Authentik's managed mapping hardcodes email_verified to False, which
// breaks apps whose OIDC provider rejects unverified emails (e.g. AFFiNE).
func (c *Client) ensureBloudEmailScopeMapping(ctx context.Context) (string, error) {
	var result struct {
		Results []struct {
			PK      string `json:"pk"`
			Name    string `json:"name"`
			Managed string `json:"managed"`
		} `json:"results"`
	}
	if err := c.cl.GET("/api/v3/propertymappings/provider/scope/").
		Query("page_size", "100").
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return "", fmt.Errorf("listing scope mappings: %w", err)
	}

	for _, m := range result.Results {
		if m.Name == bloudEmailScopeMappingName && m.Managed == "" {
			return m.PK, nil
		}
	}

	payload := map[string]interface{}{
		"name":       bloudEmailScopeMappingName,
		"scope_name": "email",
		"expression": bloudEmailScopeMappingExpression,
	}
	var created struct {
		PK string `json:"pk"`
	}
	if err := c.cl.POST("/api/v3/propertymappings/provider/scope/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &created); err != nil {
		return "", fmt.Errorf("creating scope mapping: %w", err)
	}
	return created.PK, nil
}

// managedEmailScopeMappingUUID returns the UUID of Authentik's managed
// "email" scope mapping (or "" when not found).
func (c *Client) managedEmailScopeMappingUUID(ctx context.Context) (string, error) {
	var result struct {
		Results []struct {
			PK      string `json:"pk"`
			Managed string `json:"managed"`
		} `json:"results"`
	}
	if err := c.cl.GET("/api/v3/propertymappings/provider/scope/").
		Query("page_size", "100").
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return "", fmt.Errorf("listing scope mappings: %w", err)
	}
	for _, m := range result.Results {
		if m.Managed == "goauthentik.io/providers/oauth2/scope-email" {
			return m.PK, nil
		}
	}
	return "", nil
}

// ensureProviderEmailScopeMapping makes sure an OAuth2 provider's property
// mappings use Bloud's verified-email scope mapping instead of Authentik's
// managed one (which reports email_verified: False).
func (c *Client) ensureProviderEmailScopeMapping(ctx context.Context, providerID int) error {
	bloudEmail, err := c.ensureBloudEmailScopeMapping(ctx)
	if err != nil {
		return fmt.Errorf("ensuring email scope mapping: %w", err)
	}

	reqPath := fmt.Sprintf("/api/v3/providers/oauth2/%d/", providerID)
	var provider struct {
		PropertyMappings []string `json:"property_mappings"`
	}
	if err := c.cl.GET(reqPath).OK(http.StatusOK).DoInto(ctx, &provider); err != nil {
		return fmt.Errorf("fetching provider: %w", err)
	}

	managedEmail, _ := c.managedEmailScopeMappingUUID(ctx)

	var mappings []string
	for _, m := range provider.PropertyMappings {
		// Swap the managed email mapping for Bloud's verified one, keeping
		// every other mapping (including Bloud's when already present).
		if managedEmail != "" && m == managedEmail {
			mappings = append(mappings, bloudEmail)
			continue
		}
		mappings = append(mappings, m)
	}
	found := false
	for _, m := range mappings {
		if m == bloudEmail {
			found = true
			break
		}
	}
	if !found {
		mappings = append(mappings, bloudEmail)
	}

	// No drift: avoid churning the provider on every reconciliation pass.
	if len(mappings) == len(provider.PropertyMappings) {
		same := true
		for i := range mappings {
			if mappings[i] != provider.PropertyMappings[i] {
				same = false
				break
			}
		}
		if same {
			return nil
		}
	}

	if err := c.cl.PATCH(reqPath).JSON(map[string]interface{}{"property_mappings": mappings}).OK(http.StatusOK).Exec(ctx); err != nil {
		return fmt.Errorf("patching provider property mappings: %w", err)
	}
	return nil
}
