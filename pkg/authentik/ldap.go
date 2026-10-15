// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"fmt"
	"net/http"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
)

// LDAP Infrastructure constants
const (
	ldapProviderName    = "Bloud LDAP Provider"
	ldapApplicationSlug = "ldap"
	ldapApplicationName = "LDAP Authentication"
	ldapOutpostName     = "Bloud LDAP Outpost"
	ldapServiceUsername = "ldap-service"
	ldapServiceTokenID  = "ldap-service-bind-token"

	// ldapBindFlowSlug is the flow the outpost runs for a bind, declared in
	// apps/authentik/auth.yaml. Its login stage sets the session lifetime, and
	// in cached bind mode that lifetime is how long a bind is remembered.
	ldapBindFlowSlug = "bloud-ldap-bind"
	// ldapBindMode caches a successful bind in the outpost for the life of the
	// session the bind flow issued, so a repeat bind costs no flow execution
	// (~0.1s instead of ~1.8s). The flip side: a password changed or a user
	// deactivated in Authentik keeps binding from the cache until that session
	// expires, which ldapBindFlowSlug's login stage bounds at one hour.
	ldapBindMode   = "cached"
	ldapSearchMode = "direct"
)

// EnsureLDAPInfrastructure creates the LDAP provider, application, outpost, and service account
// if they don't already exist. This is idempotent - safe to call multiple times.
func (c *Client) EnsureLDAPInfrastructure(ctx context.Context, ldapBindPassword string) error {
	// The Admins group is wanted twice below: as the provider's search_group, and
	// as the membership that lets the bind account read the directory. One read
	// answers both, and it carries the group's current members, so a service
	// account that is already in place costs no write.
	admins, err := c.lookupAdminsGroup(ctx)
	if err != nil {
		return fmt.Errorf("finding search group: %w", err)
	}

	// 1. Create LDAP provider (if not exists)
	providerID, err := c.ensureLDAPProvider(ctx, admins.PK)
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
	if !admins.HasUser(serviceAccountID) {
		if err := c.addUserToGroupID(ctx, serviceAccountID, admins.PK); err != nil {
			return fmt.Errorf("adding service account to group: %w", err)
		}
	}

	// 5. Create service account token (if not exists)
	if err := c.ensureLDAPServiceToken(ctx, serviceAccountID, ldapBindPassword); err != nil {
		return fmt.Errorf("ensuring LDAP service token: %w", err)
	}

	// 6. Set the service account's password for LDAP bind.
	// The app_password token alone is not sufficient: a bind the outpost has not
	// cached runs the bind flow, whose password stage checks the actual password.
	if err := c.setUserPassword(ctx, serviceAccountID, ldapBindPassword); err != nil {
		return fmt.Errorf("setting service account password: %w", err)
	}

	// 7. Create LDAP outpost (if not exists)
	if err := c.ensureLDAPOutpost(ctx, providerID); err != nil {
		return fmt.Errorf("ensuring LDAP outpost: %w", err)
	}

	return nil
}

// ensureLDAPProvider creates the LDAP provider, or brings an existing one onto
// the bind flow and modes above. searchGroupID is the already-resolved pk of the
// group the directory search is scoped to; the caller has it because it needs
// the same group for the bind account.
func (c *Client) ensureLDAPProvider(ctx context.Context, searchGroupID string) (int, error) {
	bindFlowID, err := c.findFlowID(ctx, ldapBindFlowSlug)
	if err != nil {
		return 0, fmt.Errorf("finding LDAP bind flow: %w", err)
	}

	providerID, err := c.findProviderID(ctx, "ldap", ldapProviderName)
	if err != nil {
		return 0, err
	}
	if providerID != 0 {
		return providerID, c.ensureLDAPProviderModes(ctx, providerID, bindFlowID)
	}

	invalidFlowID, err := c.findFlowID(ctx, "default-provider-invalidation-flow")
	if err != nil {
		return 0, fmt.Errorf("finding invalidation flow: %w", err)
	}

	// Create the provider
	payload := map[string]any{
		"name":               ldapProviderName,
		"authorization_flow": bindFlowID,
		"invalidation_flow":  invalidFlowID,
		"search_group":       searchGroupID,
		"bind_mode":          ldapBindMode,
		"search_mode":        ldapSearchMode,
	}
	var result struct {
		PK int `json:"pk"`
	}
	if err := c.cl.POST("/api/v3/providers/ldap/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &result); err != nil {
		return 0, fmt.Errorf("creating LDAP provider: %w", err)
	}

	return result.PK, nil
}

// ensureLDAPProviderModes moves an existing provider onto the bind flow and
// modes. Installs created before the bind cache bound through the 90-day
// default flow in direct mode; this is what carries them over. It only writes
// when something drifted, so a steady-state pass issues no PATCH. The bind flow
// and the cached mode are written together: cached mode on the default flow
// would remember a bind for 90 days.
func (c *Client) ensureLDAPProviderModes(ctx context.Context, providerID int, bindFlowID string) error {
	reqPath := fmt.Sprintf("/api/v3/providers/ldap/%d/", providerID)
	var provider struct {
		AuthorizationFlow string `json:"authorization_flow"`
		BindMode          string `json:"bind_mode"`
		SearchMode        string `json:"search_mode"`
	}
	if err := c.cl.GET(reqPath).OK(http.StatusOK).DoInto(ctx, &provider); err != nil {
		return fmt.Errorf("fetching LDAP provider: %w", err)
	}

	if provider.AuthorizationFlow == bindFlowID &&
		provider.BindMode == ldapBindMode &&
		provider.SearchMode == ldapSearchMode {
		return nil
	}
	patch := map[string]any{
		"authorization_flow": bindFlowID,
		"bind_mode":          ldapBindMode,
		"search_mode":        ldapSearchMode,
	}
	if err := c.cl.PATCH(reqPath).JSON(patch).OK(http.StatusOK).Exec(ctx); err != nil {
		return fmt.Errorf("patching LDAP provider: %w", err)
	}
	return nil
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
	payload := map[string]any{
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
	return c.createUserRecord(ctx, ldapServiceUsername, "LDAP Service Account", "", "service_account")
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
	payload := map[string]any{
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
	payload := map[string]any{
		"name":      ldapOutpostName,
		"type":      "ldap",
		"providers": []int{providerID},
		"config": map[string]any{
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
	return c.tokenKey(ctx, ldapServiceTokenID)
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
	return c.tokenKey(ctx, tokenIdentifier)
}

// Helper methods for LDAP infrastructure
