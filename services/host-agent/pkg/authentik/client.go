// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// Client provides access to the Authentik API. It wraps a single
// *appclient.Client: one appclient spec carries the base URL, the
// management-token bearer, the Accept header, and the (single-shot)
// retry/timeout policy, so every method below reads as declared intent
// over a shared transport instead of hand-rolling http.NewRequest.
type Client struct {
	baseURL     string
	cl          *appclient.Client
	emailDomain string
	spec        appclient.Spec
}

// UserEmailDomain returns the domain used for managed users' identity
// emails. SSO apps validate identity emails with an RFC-style validator
// that requires a TLD, so the bare "localhost" base domain used in dev
// environments is mapped to "localhost.local".
func UserEmailDomain(baseDomain string) string {
	if baseDomain == "" || baseDomain == "localhost" {
		return "localhost.local"
	}
	return baseDomain
}

// NewClient creates a new Authentik API client.
//
// The underlying appclient is single-shot (one attempt, no backoff) and
// 30s per-request, matching the client's historical behavior: these calls
// run inside an idempotent reconciliation pass, so a transient blip is
// retried on the next cycle rather than backed off here.
func NewClient(baseURL, token string) *Client {
	c := &Client{
		baseURL:     baseURL,
		emailDomain: UserEmailDomain(""),
	}
	c.spec = appclient.Spec{
		Name:    "authentik",
		BaseURL: baseURL,
		Headers: map[string]string{"Accept": "application/json"},
		StaticAuth: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
		},
		Timeout: 30 * time.Second,
		Retry:   appclient.RetryPolicy{MaxAttempts: 1},
	}
	c.cl = appclient.New(c.spec)
	return c
}

// WithClientFactory stamps the process-shared transport and logger into the
// client's appclient spec, so the Authentik client reuses the host's connection
// pool instead of owning one. Fields already set on the spec (retry, timeout,
// auth) win over the factory defaults.
func (c *Client) WithClientFactory(f configurator.ClientFactory) *Client {
	c.cl = f.New(c.spec)
	return c
}

// WithUserEmailDomain sets the domain used for managed users' identity
// emails (see UserEmailDomain). Defaults to "localhost.local".
func (c *Client) WithUserEmailDomain(domain string) *Client {
	c.emailDomain = UserEmailDomain(domain)
	return c
}

// ManagedUserEmail returns the identity email for a managed user.
func (c *Client) ManagedUserEmail(username string) string {
	return username + "@" + c.emailDomain
}

// ProviderResponse represents an Authentik API response
type ProviderResponse struct {
	PK   int    `json:"pk"`
	Name string `json:"name"`
}

// PaginatedResponse represents a paginated Authentik API response
type PaginatedResponse struct {
	Pagination struct {
		Count int `json:"count"`
	} `json:"pagination"`
	Results []ProviderResponse `json:"results"`
}

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

// IsAvailable checks if Authentik is available and the token is valid.
//
// A nil client reports unavailable. Callers hold this type behind an interface
// (api.AuthentikUserManagerInterface), where a nil *Client is not a nil
// interface, so the guard must live here rather than only at call sites.
func (c *Client) IsAvailable(ctx context.Context) bool {
	if c == nil || c.cl == nil {
		return false
	}
	_, err := c.cl.GET("/api/v3/core/applications/").Do(ctx)
	return err == nil
}

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

	var full map[string]interface{}
	if err := json.Unmarshal(body, &full); err != nil {
		return fmt.Errorf("parsing outpost: %w", err)
	}

	config, _ := full["config"].(map[string]interface{})
	if config == nil {
		config = make(map[string]interface{})
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
		JSON(map[string]interface{}{"providers": providers}).
		OK(http.StatusOK).
		Exec(ctx)
}

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

func (c *Client) addUserToGroup(ctx context.Context, userID int, groupName string) error {
	// Find the group
	groupID, err := c.findGroupID(ctx, groupName)
	if err != nil {
		return err
	}

	// Add user to group using the group's add_user endpoint.
	// 204 = success, 200 = already in group (idempotent)
	if err := c.cl.POST("/api/v3/core/groups/"+groupID+"/add_user/").
		JSON(map[string]int{"pk": userID}).
		OK(http.StatusNoContent, http.StatusOK).
		Exec(ctx); err != nil {
		return fmt.Errorf("adding user to group: %w", err)
	}

	return nil
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

// CreateUser creates a new user in Authentik and sets their password.
// The user gets a derived identity email (username@<domain>): SSO apps
// (e.g. AFFiNE) require a valid RFC-style email to create app accounts.
func (c *Client) CreateUser(ctx context.Context, username, password string) (int, error) {
	// Create the user
	payload := map[string]interface{}{
		"username":  username,
		"name":      username,
		"email":     c.ManagedUserEmail(username),
		"path":      "users",
		"is_active": true,
	}
	var result struct {
		PK int `json:"pk"`
	}
	if err := c.cl.POST("/api/v3/core/users/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &result); err != nil {
		return 0, fmt.Errorf("creating user: %w", err)
	}

	// Set the user's password
	if err := c.setUserPassword(ctx, result.PK, password); err != nil {
		return 0, fmt.Errorf("setting password: %w", err)
	}

	return result.PK, nil
}

// setUserPassword sets a user's password via the Authentik API
func (c *Client) setUserPassword(ctx context.Context, userID int, password string) error {
	// 204 No Content = success
	if err := c.cl.POST(fmt.Sprintf("/api/v3/core/users/%d/set_password/", userID)).
		JSON(map[string]string{"password": password}).
		OK(http.StatusNoContent, http.StatusOK).
		Exec(ctx); err != nil {
		return fmt.Errorf("setting password: %w", err)
	}

	return nil
}

// SetUserEmail sets the user's identity email via the Authentik API.
func (c *Client) SetUserEmail(ctx context.Context, userID int, email string) error {
	return c.cl.PATCH(fmt.Sprintf("/api/v3/core/users/%d/", userID)).
		JSON(map[string]string{"email": email}).
		OK(http.StatusOK).
		Exec(ctx)
}

// SetUserPassword sets a user's password via the Authentik API (public wrapper)
func (c *Client) SetUserPassword(ctx context.Context, userID int, password string) error {
	return c.setUserPassword(ctx, userID, password)
}

// AddUserToGroup adds a user to a group by name (public wrapper around internal method)
func (c *Client) AddUserToGroup(ctx context.Context, userID int, groupName string) error {
	return c.addUserToGroup(ctx, userID, groupName)
}

// RemoveUserFromGroup removes a user from a group by name
func (c *Client) RemoveUserFromGroup(ctx context.Context, userID int, groupName string) error {
	groupID, err := c.findGroupID(ctx, groupName)
	if err != nil {
		return err
	}

	if err := c.cl.POST("/api/v3/core/groups/"+groupID+"/remove_user/").
		JSON(map[string]int{"pk": userID}).
		OK(http.StatusNoContent, http.StatusOK).
		Exec(ctx); err != nil {
		return fmt.Errorf("removing user from group: %w", err)
	}

	return nil
}

// FindUserID finds a user ID by username (public wrapper)
func (c *Client) FindUserID(ctx context.Context, username string) (int, error) {
	return c.findUserID(ctx, username)
}

// ManagedUserInfo represents a user returned by ListUsers
type ManagedUserInfo struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	IsAdmin  bool   `json:"is_admin"`
	IsActive bool   `json:"is_active"`
}

// ListUsers fetches internal (non-service) users from Authentik and determines their roles
func (c *Client) ListUsers(ctx context.Context) ([]ManagedUserInfo, error) {
	// Fetch users of type "internal"
	var rawResult struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := c.cl.GET("/api/v3/core/users/").
		Query("type", "internal").
		Query("page_size", "200").
		OK(http.StatusOK).
		DoInto(ctx, &rawResult); err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}

	// Get the admin group members to cross-reference
	adminGroupMembers, err := c.getAdminGroupMembers(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting admin group members: %w", err)
	}

	var users []ManagedUserInfo
	for _, raw := range rawResult.Results {
		var user struct {
			PK       int    `json:"pk"`
			Username string `json:"username"`
			Name     string `json:"name"`
			IsActive bool   `json:"is_active"`
			Type     string `json:"type"`
		}
		if err := json.Unmarshal(raw, &user); err != nil {
			continue
		}

		// Skip service accounts
		if user.Type == "service_account" {
			continue
		}

		// Skip the akadmin user (Authentik's built-in admin)
		if user.Username == "akadmin" {
			continue
		}

		users = append(users, ManagedUserInfo{
			ID:       user.PK,
			Username: user.Username,
			Name:     user.Name,
			IsAdmin:  adminGroupMembers[user.PK],
			IsActive: user.IsActive,
		})
	}

	return users, nil
}

// getAdminGroupMembers returns a set of user IDs that are in the "authentik Admins" group
func (c *Client) getAdminGroupMembers(ctx context.Context) (map[int]bool, error) {
	groupID, err := c.findGroupID(ctx, "authentik Admins")
	if err != nil {
		return nil, err
	}

	var group struct {
		Users []int `json:"users"`
	}
	if err := c.cl.GET("/api/v3/core/groups/"+groupID+"/").
		OK(http.StatusOK).
		DoInto(ctx, &group); err != nil {
		return nil, fmt.Errorf("fetching group: %w", err)
	}

	members := make(map[int]bool)
	for _, uid := range group.Users {
		members[uid] = true
	}
	return members, nil
}

// DeleteUser deletes a user by username
func (c *Client) DeleteUser(ctx context.Context, username string) error {
	// Find the user ID first
	userID, err := c.findUserID(ctx, username)
	if err != nil {
		return fmt.Errorf("finding user: %w", err)
	}
	if userID == 0 {
		return nil // User doesn't exist, nothing to delete
	}

	// 204 No Content = success, 404 = already deleted
	return c.cl.DELETE(fmt.Sprintf("/api/v3/core/users/%d/", userID)).
		OK(http.StatusNoContent, http.StatusNotFound).
		Exec(ctx)
}

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
		JSON(map[string]interface{}{"user_fields": []string{"username"}}).
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

// OIDC constants for Bloud's own OAuth2 application
const (
	bloudAppSlug      = "bloud"
	bloudAppName      = "Bloud"
	bloudProviderName = "Bloud OAuth2 Provider"
	bloudRedirectURI  = "/auth/callback"
)

// OIDCConfig holds the OAuth2/OIDC configuration for Bloud
type OIDCConfig struct {
	ProviderID   int // Authentik provider PK, used for lazy redirect URI registration
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
	Issuer       string
}

// TokenResponse represents the OAuth2 token response
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
}

// UserInfo represents the OIDC userinfo response
type UserInfo struct {
	Sub               string   `json:"sub"`
	PreferredUsername string   `json:"preferred_username"`
	Email             string   `json:"email,omitempty"`
	EmailVerified     bool     `json:"email_verified,omitempty"`
	Name              string   `json:"name,omitempty"`
	Groups            []string `json:"groups,omitempty"`
}

// EnsureBloudOAuthApp creates the OAuth2 provider and application for Bloud if they don't exist.
// Returns the OIDC configuration needed for the login flow.
// baseURLs contains the external host URLs (e.g., ["http://bloud.local", "http://192.168.1.50:8080"]).
// A redirect URI is registered for each base URL so OAuth works regardless of which host the user accesses.
// The returned OIDCConfig contains path templates (no host); callers derive full URLs from the request Host.
func (c *Client) EnsureBloudOAuthApp(ctx context.Context, baseURLs []string, clientSecret string) (*OIDCConfig, error) {
	// Build redirect URIs for all base URLs
	var redirectURIs []string
	for _, baseURL := range baseURLs {
		redirectURIs = append(redirectURIs, baseURL+bloudRedirectURI)
	}

	// Check if provider already exists
	providerID, err := c.findProviderID(ctx, "oauth2", bloudProviderName)
	if err != nil {
		return nil, fmt.Errorf("checking existing provider: %w", err)
	}

	if providerID == 0 {
		// Create the OAuth2 provider with all redirect URIs
		providerID, err = c.createBloudOAuth2Provider(ctx, redirectURIs, clientSecret)
		if err != nil {
			return nil, fmt.Errorf("creating OAuth2 provider: %w", err)
		}
	} else {
		// Provider exists: update redirect URIs to include any new IPs
		if err := c.updateBloudOAuth2ProviderRedirectURIs(ctx, providerID, redirectURIs); err != nil {
			return nil, fmt.Errorf("updating redirect URIs: %w", err)
		}
	}

	// Check if application already exists
	exists, err := c.applicationExists(ctx, bloudAppSlug)
	if err != nil {
		return nil, fmt.Errorf("checking existing application: %w", err)
	}

	if !exists {
		// Create the application
		if err := c.createBloudApplication(ctx, providerID); err != nil {
			return nil, fmt.Errorf("creating application: %w", err)
		}
	}

	// Return OIDC configuration with path templates only (no host baked in).
	// The auth handlers derive full URLs from the incoming request's Host header.
	// ProviderID is included so callers can lazily add redirect URIs for new hosts.
	return &OIDCConfig{
		ProviderID:   providerID,
		ClientID:     bloudAppSlug,
		ClientSecret: clientSecret,
		AuthURL:      "/application/o/authorize/",
		TokenURL:     "/application/o/token/",
		UserInfoURL:  "/application/o/userinfo/",
		Issuer:       "/application/o/" + bloudAppSlug + "/",
	}, nil
}

// createBloudOAuth2Provider creates the OAuth2 provider for Bloud
func (c *Client) createBloudOAuth2Provider(ctx context.Context, redirectURIs []string, clientSecret string) (int, error) {
	// Find required flows
	authFlowID, err := c.findFlowID(ctx, "default-provider-authorization-implicit-consent")
	if err != nil {
		// Fall back to explicit consent flow
		authFlowID, err = c.findFlowID(ctx, "default-provider-authorization-explicit-consent")
		if err != nil {
			return 0, fmt.Errorf("finding authorization flow: %w", err)
		}
	}

	invalidFlowID, err := c.findFlowID(ctx, "default-provider-invalidation-flow")
	if err != nil {
		return 0, fmt.Errorf("finding invalidation flow: %w", err)
	}

	// Get certificate UUID for signing (Authentik API requires UUID, not name)
	certUUID, err := c.getFirstCertificateUUID(ctx)
	if err != nil {
		return 0, fmt.Errorf("getting signing certificate: %w", err)
	}

	// Get scope property mappings for openid, profile, and email
	scopeMappings, err := c.getScopePropertyMappings(ctx, []string{"openid", "profile", "email"})
	if err != nil {
		return 0, fmt.Errorf("getting scope mappings: %w", err)
	}

	// Build redirect URI entries for all base URLs
	var uriEntries []map[string]string
	for _, uri := range redirectURIs {
		uriEntries = append(uriEntries, map[string]string{
			"matching_mode": "strict",
			"url":           uri,
		})
	}

	payload := map[string]interface{}{
		"name":                       bloudProviderName,
		"authorization_flow":         authFlowID,
		"invalidation_flow":          invalidFlowID,
		"client_type":                "confidential",
		"client_id":                  bloudAppSlug,
		"client_secret":              clientSecret,
		"redirect_uris":              uriEntries,
		"signing_key":                certUUID,
		"property_mappings":          scopeMappings,
		"sub_mode":                   "user_username",
		"include_claims_in_id_token": true,
	}

	var result struct {
		PK int `json:"pk"`
	}
	if err := c.cl.POST("/api/v3/providers/oauth2/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &result); err != nil {
		return 0, fmt.Errorf("creating OAuth2 provider: %w", err)
	}

	return result.PK, nil
}

// updateBloudOAuth2ProviderRedirectURIs patches the redirect URIs on an existing provider
func (c *Client) updateBloudOAuth2ProviderRedirectURIs(ctx context.Context, providerID int, redirectURIs []string) error {
	var uriEntries []map[string]string
	for _, uri := range redirectURIs {
		uriEntries = append(uriEntries, map[string]string{
			"matching_mode": "strict",
			"url":           uri,
		})
	}

	return c.cl.PATCH(fmt.Sprintf("/api/v3/providers/oauth2/%d/", providerID)).
		JSON(map[string]interface{}{"redirect_uris": uriEntries}).
		OK(http.StatusOK).
		Exec(ctx)
}

// getFirstCertificateUUID retrieves the UUID of the first available certificate keypair
// This is needed because the Authentik API requires a UUID for signing_key, not a name
func (c *Client) getFirstCertificateUUID(ctx context.Context) (string, error) {
	var result struct {
		Results []struct {
			PK string `json:"pk"`
		} `json:"results"`
	}
	if err := c.cl.GET("/api/v3/crypto/certificatekeypairs/").OK(http.StatusOK).DoInto(ctx, &result); err != nil {
		return "", fmt.Errorf("listing certificates: %w", err)
	}

	if len(result.Results) == 0 {
		return "", fmt.Errorf("no certificates found in Authentik")
	}

	return result.Results[0].PK, nil
}

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

// createBloudApplication creates the Authentik application for Bloud
func (c *Client) createBloudApplication(ctx context.Context, providerID int) error {
	payload := map[string]interface{}{
		"name":               bloudAppName,
		"slug":               bloudAppSlug,
		"provider":           providerID,
		"policy_engine_mode": "any",
	}
	if err := c.cl.POST("/api/v3/core/applications/").JSON(payload).OK(http.StatusCreated).Exec(ctx); err != nil {
		return fmt.Errorf("creating application: %w", err)
	}
	return nil
}

// ExchangeCode exchanges an authorization code for tokens
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI, clientID, clientSecret string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)

	var tokenResp TokenResponse
	if err := c.cl.POST("/application/o/token/").Anonymous().Form(data).OK(http.StatusOK).DoInto(ctx, &tokenResp); err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}

	return &tokenResp, nil
}

// GetUserInfo retrieves user information using an access token
func (c *Client) GetUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	var userInfo UserInfo
	if err := c.cl.GET("/application/o/userinfo/").
		Anonymous().
		Header("Authorization", "Bearer "+accessToken).
		OK(http.StatusOK).
		DoInto(ctx, &userInfo); err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}

	return &userInfo, nil
}

// EnsureForwardAuth implements orchestrator.SSOProvisioner.
func (c *Client) EnsureForwardAuth(ctx context.Context, appName, displayName, externalURL string) error {
	return c.EnsureForwardAuthApplication(ctx, appName, displayName, externalURL)
}

// EnsureForwardDomainAuth creates a forward_domain proxy provider, application, and
// standalone proxy outpost for the tailnet MagicDNS domain. In forward_domain mode,
// a single cookie on the domain (e.g. ".tail12756a.ts.net") authenticates all *.domain
// subdomains. A standalone outpost is used (instead of the embedded outpost) so that
// AUTHENTIK_HOST_BROWSER can point to the tailnet URL while the embedded outpost
// continues using localhost for local access.
// Returns the outpost API token needed to start the standalone outpost container.
// cookieDomain is the MagicDNS suffix (e.g. "tail12756a.ts.net").
func (c *Client) EnsureForwardDomainAuth(ctx context.Context, cookieDomain string) (string, error) {
	const (
		providerName = "Tailnet Forward Domain Provider"
		appSlug      = "tailnet-domain"
		appName      = "Tailnet Domain Auth"
	)

	externalHost := "https://bloud." + cookieDomain

	// Check if provider already exists.
	existingID, err := c.findProviderID(ctx, "proxy", providerName)
	if err != nil {
		return "", fmt.Errorf("checking proxy provider: %w", err)
	}

	var providerID int
	if existingID != 0 {
		providerID = existingID
	} else {
		authFlowID, err := c.findFlowID(ctx, "default-authentication-flow")
		if err != nil {
			return "", fmt.Errorf("finding auth flow: %w", err)
		}
		invalidationFlowID, err := c.findFlowID(ctx, "default-provider-invalidation-flow")
		if err != nil {
			return "", fmt.Errorf("finding invalidation flow: %w", err)
		}

		providerID, err = c.createForwardDomainProvider(ctx, providerName, externalHost, cookieDomain, authFlowID, invalidationFlowID)
		if err != nil {
			return "", fmt.Errorf("creating forward_domain provider: %w", err)
		}
	}

	if err := c.ensureProxyApplication(ctx, appSlug, appName, providerID); err != nil {
		return "", fmt.Errorf("ensuring proxy application: %w", err)
	}

	// Use a standalone proxy outpost (not the embedded outpost) so the browser-facing
	// URL can be the tailnet domain while local auth stays on localhost.
	if err := c.ensureProxyOutpost(ctx, providerID); err != nil {
		return "", fmt.Errorf("ensuring proxy outpost: %w", err)
	}

	token, err := c.GetProxyOutpostToken(ctx)
	if err != nil {
		return "", fmt.Errorf("getting proxy outpost token: %w", err)
	}

	return token, nil
}

// ensureProxyOutpost creates the standalone proxy outpost if it doesn't exist.
// This outpost runs as a separate container with AUTHENTIK_HOST_BROWSER set to the
// tailnet URL, allowing remote users to authenticate via tailnet while the embedded
// outpost continues serving local auth on localhost.
func (c *Client) ensureProxyOutpost(ctx context.Context, providerID int) error {
	outpost, err := c.findOutpostByName(ctx, proxyOutpostName)
	if err != nil {
		return err
	}
	if outpost != nil {
		// Outpost exists: ensure the provider is attached.
		for _, pid := range outpost.Providers {
			if pid == providerID {
				return nil
			}
		}
		outpost.Providers = append(outpost.Providers, providerID)
		return c.updateOutpostProviders(ctx, outpost.PK, outpost.Providers)
	}

	payload := map[string]interface{}{
		"name":      proxyOutpostName,
		"type":      "proxy",
		"providers": []int{providerID},
		"config": map[string]interface{}{
			"authentik_host": c.baseURL,
			"log_level":      "info",
		},
	}
	if err := c.cl.POST("/api/v3/outposts/instances/").JSON(payload).OK(http.StatusCreated).Exec(ctx); err != nil {
		return fmt.Errorf("creating proxy outpost: %w", err)
	}

	return nil
}

// GetProxyOutpostToken returns the auto-generated token for the standalone proxy outpost.
// Authentik creates a token with identifier "ak-outpost-{uuid}-api" when an outpost is created.
func (c *Client) GetProxyOutpostToken(ctx context.Context) (string, error) {
	outpost, err := c.findOutpostByName(ctx, proxyOutpostName)
	if err != nil {
		return "", fmt.Errorf("finding outpost: %w", err)
	}
	if outpost == nil {
		return "", fmt.Errorf("proxy outpost not found")
	}

	tokenIdentifier := fmt.Sprintf("ak-outpost-%s-api", outpost.PK)

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

// createForwardDomainProvider creates a proxy provider in forward_domain mode.
func (c *Client) createForwardDomainProvider(ctx context.Context, name, externalHost, cookieDomain, authFlowID, invalidationFlowID string) (int, error) {
	payload := map[string]interface{}{
		"name":               name,
		"authorization_flow": authFlowID,
		"invalidation_flow":  invalidationFlowID,
		"external_host":      externalHost,
		"mode":               "forward_domain",
		"cookie_domain":      cookieDomain,
	}
	var result struct {
		PK int `json:"pk"`
	}
	if err := c.cl.POST("/api/v3/providers/proxy/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &result); err != nil {
		return 0, err
	}
	return result.PK, nil
}

// EnsureForwardAuthApplication creates or verifies the Authentik proxy provider and
// application for an app using the forward-auth SSO strategy. It also adds the
// provider to the embedded outpost so Traefik's forwardAuth middleware can reach it.
// externalURL is the full URL users access the app on, e.g. "http://navidrome.localhost:8080".
func (c *Client) EnsureForwardAuthApplication(ctx context.Context, appName, displayName, externalURL string) error {
	providerName := fmt.Sprintf("%s Proxy Provider", displayName)

	// Check if provider already exists
	existingID, err := c.findProviderID(ctx, "proxy", providerName)
	if err != nil {
		return fmt.Errorf("checking proxy provider: %w", err)
	}

	var providerID int
	if existingID != 0 {
		providerID = existingID
	} else {
		// Find required flows
		authFlowID, err := c.findFlowID(ctx, "default-authentication-flow")
		if err != nil {
			return fmt.Errorf("finding auth flow: %w", err)
		}
		invalidationFlowID, err := c.findFlowID(ctx, "default-provider-invalidation-flow")
		if err != nil {
			return fmt.Errorf("finding invalidation flow: %w", err)
		}

		providerID, err = c.createProxyProvider(ctx, providerName, externalURL, authFlowID, invalidationFlowID)
		if err != nil {
			return fmt.Errorf("creating proxy provider: %w", err)
		}
	}

	// Ensure application exists
	if err := c.ensureProxyApplication(ctx, appName, displayName, providerID); err != nil {
		return fmt.Errorf("ensuring proxy application: %w", err)
	}

	// Add provider to embedded outpost
	if err := c.AddProviderToEmbeddedOutpost(ctx, providerName); err != nil {
		return fmt.Errorf("adding to embedded outpost: %w", err)
	}

	return nil
}

// createProxyProvider creates a new Authentik proxy provider in forward_single mode.
func (c *Client) createProxyProvider(ctx context.Context, name, externalHost, authFlowID, invalidationFlowID string) (int, error) {
	payload := map[string]interface{}{
		"name":               name,
		"authorization_flow": authFlowID,
		"invalidation_flow":  invalidationFlowID,
		"external_host":      externalHost,
		"mode":               "forward_single",
	}
	var result struct {
		PK int `json:"pk"`
	}
	if err := c.cl.POST("/api/v3/providers/proxy/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &result); err != nil {
		return 0, err
	}
	return result.PK, nil
}

// ensureProxyApplication creates the Authentik application for a proxy provider if it doesn't exist.
func (c *Client) ensureProxyApplication(ctx context.Context, slug, displayName string, providerID int) error {
	appPath := "/api/v3/core/applications/" + url.PathEscape(slug) + "/"
	_, err := c.cl.GET(appPath).Do(ctx)
	if err == nil {
		return nil // Already exists
	}
	if appclient.StatusOf(err) == 0 {
		return err // transport error: don't attempt create on an unreachable server
	}

	// Create application
	payload := map[string]interface{}{
		"name":               displayName,
		"slug":               slug,
		"provider":           providerID,
		"policy_engine_mode": "any",
	}
	if err := c.cl.POST("/api/v3/core/applications/").JSON(payload).OK(http.StatusCreated).Exec(ctx); err != nil {
		return err
	}
	return nil
}

// defaultAccessTokenValidity is the access token lifetime of a native-oidc
// provider that does not declare sso.accessTokenMinutes.
const defaultAccessTokenValidity = "minutes=5"

// OIDCTuning holds the optional per-app OAuth2 provider settings an app declares
// under sso: in metadata.yaml. The zero value keeps every default, so apps that
// declare nothing are provisioned exactly as before.
type OIDCTuning struct {
	// ExtraScopes are scope names added to the provider on top of openid,
	// profile and email (e.g. "offline_access").
	ExtraScopes []string
	// AccessTokenMinutes overrides the access token lifetime; 0 keeps the default.
	AccessTokenMinutes int
}

func (t OIDCTuning) isZero() bool {
	return len(t.ExtraScopes) == 0 && t.AccessTokenMinutes == 0
}

// accessTokenValidity renders the lifetime in Authentik's duration syntax.
func (t OIDCTuning) accessTokenValidity() string {
	if t.AccessTokenMinutes > 0 {
		return fmt.Sprintf("minutes=%d", t.AccessTokenMinutes)
	}
	return defaultAccessTokenValidity
}

// EnsureNativeOIDC creates or verifies the Authentik OAuth2 provider and
// application for an app using the native-oidc SSO strategy. The provider uses
// a confidential client with the exact client ID/secret derived by the
// host-agent (so the app and the identity provider agree without a shared
// store). redirectURIs must cover every URL the app may use as its callback.
// launchURL, when non-empty, is set as the application's meta launch URL.
// tuning carries the app's optional extra scopes and token lifetime; it is
// applied on creation and reconciled on an existing provider.
func (c *Client) EnsureNativeOIDC(ctx context.Context, appName, displayName, clientID, clientSecret string, redirectURIs []string, launchURL string, tuning OIDCTuning) error {
	providerName := fmt.Sprintf("%s OAuth2 Provider", displayName)

	// Check if provider already exists
	existingID, err := c.findProviderID(ctx, "oauth2", providerName)
	if err != nil {
		return fmt.Errorf("checking OAuth2 provider: %w", err)
	}

	var providerID int
	if existingID != 0 {
		providerID = existingID
		// Refresh redirect URIs so newly detected hosts/IPs work
		if err := c.updateBloudOAuth2ProviderRedirectURIs(ctx, providerID, redirectURIs); err != nil {
			return fmt.Errorf("updating redirect URIs: %w", err)
		}
		// Swap in the verified-email scope mapping (Authentik's managed one
		// reports email_verified: False, which apps like AFFiNE reject).
		if err := c.ensureProviderEmailScopeMapping(ctx, providerID); err != nil {
			return fmt.Errorf("updating email scope mapping: %w", err)
		}
		if !tuning.isZero() {
			if err := c.ensureProviderTuning(ctx, providerID, tuning); err != nil {
				return fmt.Errorf("applying provider tuning: %w", err)
			}
		}
	} else {
		// Find required flows
		authFlowID, err := c.findFlowID(ctx, "default-provider-authorization-implicit-consent")
		if err != nil {
			authFlowID, err = c.findFlowID(ctx, "default-provider-authorization-explicit-consent")
			if err != nil {
				return fmt.Errorf("finding authorization flow: %w", err)
			}
		}
		invalidationFlowID, err := c.findFlowID(ctx, "default-provider-invalidation-flow")
		if err != nil {
			return fmt.Errorf("finding invalidation flow: %w", err)
		}

		certUUID, err := c.getFirstCertificateUUID(ctx)
		if err != nil {
			return fmt.Errorf("getting signing certificate: %w", err)
		}
		// Use Bloud's verified-email scope mapping for the "email" scope:
		// Authentik's managed mapping hardcodes email_verified: False, which
		// breaks apps whose OIDC provider rejects unverified emails (AFFiNE).
		scopeMappings, err := c.getScopePropertyMappings(ctx, []string{"openid", "profile"})
		if err != nil {
			return fmt.Errorf("getting scope mappings: %w", err)
		}
		bloudEmail, err := c.ensureBloudEmailScopeMapping(ctx)
		if err != nil {
			return fmt.Errorf("ensuring email scope mapping: %w", err)
		}
		scopeMappings = append(scopeMappings, bloudEmail)
		extraMappings, err := c.extraScopeMappings(ctx, tuning.ExtraScopes)
		if err != nil {
			return err
		}
		scopeMappings = append(scopeMappings, extraMappings...)

		var uriEntries []map[string]string
		for _, uri := range redirectURIs {
			uriEntries = append(uriEntries, map[string]string{
				"matching_mode": "strict",
				"url":           uri,
			})
		}

		payload := map[string]interface{}{
			"name":                       providerName,
			"authorization_flow":         authFlowID,
			"invalidation_flow":          invalidationFlowID,
			"client_type":                "confidential",
			"client_id":                  clientID,
			"client_secret":              clientSecret,
			"redirect_uris":              uriEntries,
			"signing_key":                certUUID,
			"property_mappings":          scopeMappings,
			"sub_mode":                   "hashed_user_id",
			"include_claims_in_id_token": true,
			"access_code_validity":       "minutes=1",
			"access_token_validity":      tuning.accessTokenValidity(),
			"refresh_token_validity":     "days=30",
		}

		var result struct {
			PK int `json:"pk"`
		}
		if err := c.cl.POST("/api/v3/providers/oauth2/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &result); err != nil {
			return fmt.Errorf("creating OAuth2 provider: %w", err)
		}
		providerID = result.PK
	}

	// Ensure the application exists (and points at this provider)
	if err := c.ensureOIDCApplication(ctx, appName, displayName, providerID, launchURL); err != nil {
		return fmt.Errorf("ensuring OAuth2 application: %w", err)
	}

	return nil
}

// ensureProviderTuning brings an existing OAuth2 provider in line with the
// app's declared extra scopes and access token lifetime. It only adds scope
// mappings (never removes one) and only writes when something drifted, so a
// steady-state reconciliation pass issues no PATCH.
func (c *Client) ensureProviderTuning(ctx context.Context, providerID int, tuning OIDCTuning) error {
	extra, err := c.extraScopeMappings(ctx, tuning.ExtraScopes)
	if err != nil {
		return err
	}

	reqPath := fmt.Sprintf("/api/v3/providers/oauth2/%d/", providerID)
	var provider struct {
		PropertyMappings    []string `json:"property_mappings"`
		AccessTokenValidity string   `json:"access_token_validity"`
	}
	if err := c.cl.GET(reqPath).OK(http.StatusOK).DoInto(ctx, &provider); err != nil {
		return fmt.Errorf("fetching provider: %w", err)
	}

	patch := map[string]interface{}{}

	mappings := append([]string(nil), provider.PropertyMappings...)
	have := make(map[string]bool, len(mappings))
	for _, m := range mappings {
		have[m] = true
	}
	for _, pk := range extra {
		if !have[pk] {
			mappings = append(mappings, pk)
			have[pk] = true
		}
	}
	if len(mappings) != len(provider.PropertyMappings) {
		patch["property_mappings"] = mappings
	}

	if tuning.AccessTokenMinutes > 0 && provider.AccessTokenValidity != tuning.accessTokenValidity() {
		patch["access_token_validity"] = tuning.accessTokenValidity()
	}

	if len(patch) == 0 {
		return nil
	}
	if err := c.cl.PATCH(reqPath).JSON(patch).OK(http.StatusOK).Exec(ctx); err != nil {
		return fmt.Errorf("patching provider: %w", err)
	}
	return nil
}

// ensureOIDCApplication creates the Authentik application for an OIDC provider
// if it doesn't exist. An existing application is left untouched (provider
// drift is not reconciled; the provider is the source of auth behavior).
func (c *Client) ensureOIDCApplication(ctx context.Context, slug, displayName string, providerID int, launchURL string) error {
	appPath := "/api/v3/core/applications/" + url.PathEscape(slug) + "/"
	_, err := c.cl.GET(appPath).Do(ctx)
	if err == nil {
		return nil // Already exists
	}
	if appclient.StatusOf(err) == 0 {
		return err // transport error: don't attempt create on an unreachable server
	}

	payload := map[string]interface{}{
		"name":               displayName,
		"slug":               slug,
		"provider":           providerID,
		"policy_engine_mode": "any",
	}
	if launchURL != "" {
		payload["meta_launch_url"] = launchURL
	}
	if err := c.cl.POST("/api/v3/core/applications/").JSON(payload).OK(http.StatusCreated).Exec(ctx); err != nil {
		return err
	}
	return nil
}

// sleepCtx sleeps for d unless ctx is done first; returns the ctx error when
// canceled. Used by the login/branding retry loops so a PostStart-budget
// cancel interrupts the wait instead of blocking for the full interval.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
