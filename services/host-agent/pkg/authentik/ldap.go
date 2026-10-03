// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// LDAP Infrastructure constants
const (
	ldapProviderName    = "Bloud LDAP Provider"
	ldapApplicationSlug = "ldap"
	ldapApplicationName = "LDAP Authentication"
	ldapOutpostName     = "Bloud LDAP Outpost"
	ldapServiceUsername = "ldap-service"
	ldapServiceTokenID  = "ldap-service-bind-token"
)

// Proxy outpost constants for tailnet forward_domain auth
const (
	proxyOutpostName = "Bloud Tailnet Proxy Outpost"
)

// EnsureLDAPInfrastructure creates the LDAP provider, application, outpost, and service account
// if they don't already exist. This is idempotent - safe to call multiple times.
func (c *Client) EnsureLDAPInfrastructure(ctx context.Context, ldapBindPassword string) error {
	// 1. Create LDAP provider (if not exists)
	providerID, err := c.ensureLDAPProvider(ctx)
	if err != nil {
		return fmt.Errorf("ensuring LDAP provider: %w", err)
	}

	// 2. Create LDAP application (if not exists)
	if err := c.ensureLDAPApplication(ctx, providerID); err != nil {
		return fmt.Errorf("ensuring LDAP application: %w", err)
	}

	// 3. Create service account (if not exists)
	serviceAccountID, err := c.ensureLDAPServiceAccount(ctx)
	if err != nil {
		return fmt.Errorf("ensuring LDAP service account: %w", err)
	}

	// 4. Add service account to authentik Admins group (for LDAP search permissions)
	if err := c.addUserToGroup(ctx, serviceAccountID, "authentik Admins"); err != nil {
		return fmt.Errorf("adding service account to group: %w", err)
	}

	// 5. Create service account token (if not exists)
	if err := c.ensureLDAPServiceToken(ctx, serviceAccountID, ldapBindPassword); err != nil {
		return fmt.Errorf("ensuring LDAP service token: %w", err)
	}

	// 6. Set the service account's password for LDAP direct bind.
	// The app_password token alone is not sufficient: Authentik's LDAP outpost
	// in direct bind mode requires the user's actual password.
	if err := c.setUserPassword(ctx, serviceAccountID, ldapBindPassword); err != nil {
		return fmt.Errorf("setting service account password: %w", err)
	}

	// 7. Create LDAP outpost (if not exists)
	if err := c.ensureLDAPOutpost(ctx, providerID); err != nil {
		return fmt.Errorf("ensuring LDAP outpost: %w", err)
	}

	return nil
}

// ensureLDAPProvider creates the LDAP provider if it doesn't exist
func (c *Client) ensureLDAPProvider(ctx context.Context) (int, error) {
	// Check if provider exists
	providerID, err := c.findProviderID(ctx, "ldap", ldapProviderName)
	if err != nil {
		return 0, err
	}
	if providerID != 0 {
		return providerID, nil // Already exists
	}

	// Find required flows
	authFlowID, err := c.findFlowID(ctx, "default-authentication-flow")
	if err != nil {
		return 0, fmt.Errorf("finding auth flow: %w", err)
	}
	invalidFlowID, err := c.findFlowID(ctx, "default-provider-invalidation-flow")
	if err != nil {
		return 0, fmt.Errorf("finding invalidation flow: %w", err)
	}

	// Find search group (authentik Admins)
	searchGroupID, err := c.findGroupID(ctx, "authentik Admins")
	if err != nil {
		return 0, fmt.Errorf("finding search group: %w", err)
	}

	// Create the provider
	payload := map[string]interface{}{
		"name":               ldapProviderName,
		"authorization_flow": authFlowID,
		"invalidation_flow":  invalidFlowID,
		"search_group":       searchGroupID,
		"bind_mode":          "direct",
		"search_mode":        "direct",
	}
	var result struct {
		PK int `json:"pk"`
	}
	if err := c.cl.POST("/api/v3/providers/ldap/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &result); err != nil {
		return 0, fmt.Errorf("creating LDAP provider: %w", err)
	}

	return result.PK, nil
}

// ensureLDAPApplication creates the LDAP application if it doesn't exist
func (c *Client) ensureLDAPApplication(ctx context.Context, providerID int) error {
	// Check if application exists
	appPath := "/api/v3/core/applications/" + ldapApplicationSlug + "/"
	_, err := c.cl.GET(appPath).Do(ctx)
	if err == nil {
		return nil // Already exists
	}
	if appclient.StatusOf(err) == 0 {
		return err // transport error: don't attempt create on an unreachable server
	}

	// Create the application
	payload := map[string]interface{}{
		"name":               ldapApplicationName,
		"slug":               ldapApplicationSlug,
		"provider":           providerID,
		"policy_engine_mode": "any",
	}
	if err := c.cl.POST("/api/v3/core/applications/").JSON(payload).OK(http.StatusCreated).Exec(ctx); err != nil {
		return fmt.Errorf("creating LDAP application: %w", err)
	}

	return nil
}

// ensureLDAPServiceAccount creates the service account if it doesn't exist
func (c *Client) ensureLDAPServiceAccount(ctx context.Context) (int, error) {
	// Check if user exists
	userID, err := c.findUserID(ctx, ldapServiceUsername)
	if err != nil {
		return 0, err
	}
	if userID != 0 {
		return userID, nil // Already exists
	}

	// Create the service account
	payload := map[string]interface{}{
		"username":  ldapServiceUsername,
		"name":      "LDAP Service Account",
		"path":      "users",
		"type":      "service_account",
		"is_active": true,
	}
	var result struct {
		PK int `json:"pk"`
	}
	if err := c.cl.POST("/api/v3/core/users/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &result); err != nil {
		return 0, fmt.Errorf("creating service account: %w", err)
	}

	return result.PK, nil
}

// ensureLDAPServiceToken creates the service account token if it doesn't exist
func (c *Client) ensureLDAPServiceToken(ctx context.Context, userID int, password string) error {
	// Check if token exists
	tokenExists, err := c.tokenExists(ctx, ldapServiceTokenID)
	if err != nil {
		return err
	}
	if tokenExists {
		return nil // Already exists
	}

	// Create the token
	payload := map[string]interface{}{
		"identifier": ldapServiceTokenID,
		"user":       userID,
		"intent":     "app_password",
		"expiring":   false,
		"key":        password,
	}
	if err := c.cl.POST("/api/v3/core/tokens/").JSON(payload).OK(http.StatusCreated).Exec(ctx); err != nil {
		return fmt.Errorf("creating service token: %w", err)
	}

	return nil
}

// ensureLDAPOutpost creates the LDAP outpost if it doesn't exist
func (c *Client) ensureLDAPOutpost(ctx context.Context, providerID int) error {
	// Check if outpost exists
	outpost, err := c.findOutpostByName(ctx, ldapOutpostName)
	if err != nil {
		return err
	}
	if outpost != nil {
		return nil // Already exists
	}

	// Create the outpost
	payload := map[string]interface{}{
		"name":      ldapOutpostName,
		"type":      "ldap",
		"providers": []int{providerID},
		"config": map[string]interface{}{
			"authentik_host": c.baseURL,
			"log_level":      "info",
		},
	}
	if err := c.cl.POST("/api/v3/outposts/instances/").JSON(payload).OK(http.StatusCreated).Exec(ctx); err != nil {
		return fmt.Errorf("creating LDAP outpost: %w", err)
	}

	return nil
}

// GetLDAPServiceTokenKey returns the LDAP service account token key for bind operations
func (c *Client) GetLDAPServiceTokenKey(ctx context.Context) (string, error) {
	var result struct {
		Key string `json:"key"`
	}
	if err := c.cl.GET("/api/v3/core/tokens/"+url.PathEscape(ldapServiceTokenID)+"/view_key/").
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return "", fmt.Errorf("getting LDAP service token key: %w", err)
	}
	return result.Key, nil
}

// GetLDAPOutpostToken returns the auto-generated token for the LDAP outpost
func (c *Client) GetLDAPOutpostToken(ctx context.Context) (string, error) {
	// Find the LDAP outpost
	outpost, err := c.findOutpostByName(ctx, ldapOutpostName)
	if err != nil {
		return "", fmt.Errorf("finding outpost: %w", err)
	}
	if outpost == nil {
		return "", fmt.Errorf("LDAP outpost not found")
	}

	// The token identifier follows the pattern ak-outpost-{uuid}-api
	tokenIdentifier := fmt.Sprintf("ak-outpost-%s-api", outpost.PK)

	// Query for the token key using the view_key endpoint
	var result struct {
		Key string `json:"key"`
	}
	if err := c.cl.GET("/api/v3/core/tokens/"+url.PathEscape(tokenIdentifier)+"/view_key/").
		OK(http.StatusOK).
		DoInto(ctx, &result); err != nil {
		return "", fmt.Errorf("getting token key: %w", err)
	}
	return result.Key, nil
}

// Helper methods for LDAP infrastructure
