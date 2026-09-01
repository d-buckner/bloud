// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package authentik

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client provides access to the Authentik API
type Client struct {
	baseURL    string
	token      string
	emailDomain string
	httpClient *http.Client
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

// NewClient creates a new Authentik API client
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL:     baseURL,
		token:       token,
		emailDomain: UserEmailDomain(""),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
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

// ProviderResponse represents an Authentik provider in API responses
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
func (c *Client) DeleteApplication(slug string) error {
	reqURL := fmt.Sprintf("%s/api/v3/core/applications/%s/", c.baseURL, url.PathEscape(slug))

	req, err := http.NewRequest(http.MethodDelete, reqURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	// 204 No Content = success, 404 = already deleted (acceptable)
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
}

// DeleteOAuth2Provider deletes an OAuth2 provider by name
func (c *Client) DeleteOAuth2Provider(providerName string) error {
	providerID, err := c.findProviderID("oauth2", providerName)
	if err != nil {
		return err
	}
	if providerID == 0 {
		return nil // Provider doesn't exist
	}

	return c.deleteProviderByID("oauth2", providerID)
}

// DeleteProxyProvider deletes a proxy provider by name
func (c *Client) DeleteProxyProvider(providerName string) error {
	providerID, err := c.findProviderID("proxy", providerName)
	if err != nil {
		return err
	}
	if providerID == 0 {
		return nil // Provider doesn't exist
	}

	return c.deleteProviderByID("proxy", providerID)
}

// findProviderID finds a provider ID by type and name
func (c *Client) findProviderID(providerType, name string) (int, error) {
	reqURL := fmt.Sprintf("%s/api/v3/providers/%s/?search=%s", c.baseURL, providerType, url.QueryEscape(name))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var result PaginatedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decoding response: %w", err)
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
func (c *Client) deleteProviderByID(providerType string, id int) error {
	reqURL := fmt.Sprintf("%s/api/v3/providers/%s/%d/", c.baseURL, providerType, id)

	req, err := http.NewRequest(http.MethodDelete, reqURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
}

// DeleteAppSSO deletes both the application and provider for an app.
// This is the main cleanup function to call during app uninstall.
func (c *Client) DeleteAppSSO(appName, displayName, ssoStrategy string) error {
	// Delete the application first (by slug)
	if err := c.DeleteApplication(appName); err != nil {
		return fmt.Errorf("deleting application: %w", err)
	}

	// Delete the provider based on strategy
	switch ssoStrategy {
	case "native-oidc":
		providerName := fmt.Sprintf("%s OAuth2 Provider", displayName)
		if err := c.DeleteOAuth2Provider(providerName); err != nil {
			return fmt.Errorf("deleting OAuth2 provider: %w", err)
		}
	case "forward-auth":
		providerName := fmt.Sprintf("%s Proxy Provider", displayName)
		if err := c.DeleteProxyProvider(providerName); err != nil {
			return fmt.Errorf("deleting proxy provider: %w", err)
		}
	}

	return nil
}

// IsAvailable checks if Authentik is available and the token is valid
func (c *Client) IsAvailable() bool {
	reqURL := fmt.Sprintf("%s/api/v3/core/applications/", c.baseURL)

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return false
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
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
func (c *Client) AddProviderToEmbeddedOutpost(providerName string) error {
	// Find the proxy provider ID
	providerID, err := c.findProviderID("proxy", providerName)
	if err != nil {
		return fmt.Errorf("finding provider: %w", err)
	}
	if providerID == 0 {
		return fmt.Errorf("provider %s not found", providerName)
	}

	// Find the embedded outpost
	outpost, err := c.findEmbeddedOutpost()
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
	return c.updateOutpostProviders(outpost.PK, outpost.Providers)
}

// findEmbeddedOutpost finds the authentik Embedded Outpost
func (c *Client) findEmbeddedOutpost() (*OutpostResponse, error) {
	reqURL := fmt.Sprintf("%s/api/v3/outposts/instances/?search=Embedded", c.baseURL)

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var result OutpostPaginatedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
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
// rather than the server's internal bind address. Safe to call repeatedly — only patches
// when the value differs.
func (c *Client) EnsureEmbeddedOutpostHost(baseURL string) error {
	outpost, err := c.findEmbeddedOutpost()
	if err != nil {
		return fmt.Errorf("finding embedded outpost: %w", err)
	}
	if outpost == nil {
		return nil // Not set up yet; will be called again after setup
	}

	// Fetch full outpost object to get current config
	reqURL := fmt.Sprintf("%s/api/v3/outposts/instances/%s/", c.baseURL, outpost.PK)
	req, _ := http.NewRequest(http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching outpost: status %d: %s", resp.StatusCode, string(body))
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

	patchBytes, _ := json.Marshal(full)
	req2, _ := http.NewRequest(http.MethodPut, reqURL, bytes.NewReader(patchBytes))
	req2.Header.Set("Authorization", "Bearer "+c.token)
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json")

	resp2, err := c.httpClient.Do(req2)
	if err != nil {
		return err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		body2, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("updating outpost host: status %d: %s", resp2.StatusCode, string(body2))
	}
	return nil
}

// updateOutpostProviders updates the providers list for an outpost
func (c *Client) updateOutpostProviders(outpostPK string, providers []int) error {
	reqURL := fmt.Sprintf("%s/api/v3/outposts/instances/%s/", c.baseURL, outpostPK)

	// Create the patch payload
	payload := map[string]interface{}{
		"providers": providers,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPatch, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return nil
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
func (c *Client) EnsureLDAPInfrastructure(ldapBindPassword string) error {
	// 1. Create LDAP provider (if not exists)
	providerID, err := c.ensureLDAPProvider()
	if err != nil {
		return fmt.Errorf("ensuring LDAP provider: %w", err)
	}

	// 2. Create LDAP application (if not exists)
	if err := c.ensureLDAPApplication(providerID); err != nil {
		return fmt.Errorf("ensuring LDAP application: %w", err)
	}

	// 3. Create service account (if not exists)
	serviceAccountID, err := c.ensureLDAPServiceAccount()
	if err != nil {
		return fmt.Errorf("ensuring LDAP service account: %w", err)
	}

	// 4. Add service account to authentik Admins group (for LDAP search permissions)
	if err := c.addUserToGroup(serviceAccountID, "authentik Admins"); err != nil {
		return fmt.Errorf("adding service account to group: %w", err)
	}

	// 5. Create service account token (if not exists)
	if err := c.ensureLDAPServiceToken(serviceAccountID, ldapBindPassword); err != nil {
		return fmt.Errorf("ensuring LDAP service token: %w", err)
	}

	// 6. Set the service account's password for LDAP direct bind.
	// The app_password token alone is not sufficient — Authentik's LDAP outpost
	// in direct bind mode requires the user's actual password.
	if err := c.setUserPassword(serviceAccountID, ldapBindPassword); err != nil {
		return fmt.Errorf("setting service account password: %w", err)
	}

	// 7. Create LDAP outpost (if not exists)
	if err := c.ensureLDAPOutpost(providerID); err != nil {
		return fmt.Errorf("ensuring LDAP outpost: %w", err)
	}

	return nil
}

// ensureLDAPProvider creates the LDAP provider if it doesn't exist
func (c *Client) ensureLDAPProvider() (int, error) {
	// Check if provider exists
	providerID, err := c.findProviderID("ldap", ldapProviderName)
	if err != nil {
		return 0, err
	}
	if providerID != 0 {
		return providerID, nil // Already exists
	}

	// Find required flows
	authFlowID, err := c.findFlowID("default-authentication-flow")
	if err != nil {
		return 0, fmt.Errorf("finding auth flow: %w", err)
	}
	invalidFlowID, err := c.findFlowID("default-provider-invalidation-flow")
	if err != nil {
		return 0, fmt.Errorf("finding invalidation flow: %w", err)
	}

	// Find search group (authentik Admins)
	searchGroupID, err := c.findGroupID("authentik Admins")
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
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/providers/ldap/", bytes.NewReader(payloadBytes))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("creating LDAP provider: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		PK int `json:"pk"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	return result.PK, nil
}

// ensureLDAPApplication creates the LDAP application if it doesn't exist
func (c *Client) ensureLDAPApplication(providerID int) error {
	// Check if application exists
	reqURL := fmt.Sprintf("%s/api/v3/core/applications/%s/", c.baseURL, ldapApplicationSlug)
	req, _ := http.NewRequest(http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil // Already exists
	}

	// Create the application
	payload := map[string]interface{}{
		"name":               ldapApplicationName,
		"slug":               ldapApplicationSlug,
		"provider":           providerID,
		"policy_engine_mode": "any",
	}
	payloadBytes, _ := json.Marshal(payload)

	req, err = http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/core/applications/", bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err = c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("creating LDAP application: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ensureLDAPServiceAccount creates the service account if it doesn't exist
func (c *Client) ensureLDAPServiceAccount() (int, error) {
	// Check if user exists
	userID, err := c.findUserID(ldapServiceUsername)
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
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/core/users/", bytes.NewReader(payloadBytes))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("creating service account: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		PK int `json:"pk"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	return result.PK, nil
}

// ensureLDAPServiceToken creates the service account token if it doesn't exist
func (c *Client) ensureLDAPServiceToken(userID int, password string) error {
	// Check if token exists
	tokenExists, err := c.tokenExists(ldapServiceTokenID)
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
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/core/tokens/", bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("creating service token: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ensureLDAPOutpost creates the LDAP outpost if it doesn't exist
func (c *Client) ensureLDAPOutpost(providerID int) error {
	// Check if outpost exists
	outpost, err := c.findOutpostByName(ldapOutpostName)
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
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/outposts/instances/", bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("creating LDAP outpost: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetLDAPServiceTokenKey returns the LDAP service account token key for bind operations
func (c *Client) GetLDAPServiceTokenKey() (string, error) {
	reqURL := fmt.Sprintf("%s/api/v3/core/tokens/%s/view_key/", c.baseURL, url.PathEscape(ldapServiceTokenID))
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("getting LDAP service token key: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.Key, nil
}

// GetLDAPOutpostToken returns the auto-generated token for the LDAP outpost
func (c *Client) GetLDAPOutpostToken() (string, error) {
	// Find the LDAP outpost
	outpost, err := c.findOutpostByName(ldapOutpostName)
	if err != nil {
		return "", fmt.Errorf("finding outpost: %w", err)
	}
	if outpost == nil {
		return "", fmt.Errorf("LDAP outpost not found")
	}

	// The token identifier follows the pattern ak-outpost-{uuid}-api
	tokenIdentifier := fmt.Sprintf("ak-outpost-%s-api", outpost.PK)

	// Query for the token key using the view_key endpoint
	reqURL := fmt.Sprintf("%s/api/v3/core/tokens/%s/view_key/", c.baseURL, url.PathEscape(tokenIdentifier))
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("getting token key: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.Key, nil
}

// Helper methods for LDAP infrastructure

func (c *Client) findFlowID(slug string) (string, error) {
	reqURL := fmt.Sprintf("%s/api/v3/flows/instances/%s/", c.baseURL, url.PathEscape(slug))
	req, _ := http.NewRequest(http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("flow %s not found: status %d: %s", slug, resp.StatusCode, string(body))
	}

	var result struct {
		PK string `json:"pk"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.PK, nil
}

func (c *Client) findGroupID(name string) (string, error) {
	reqURL := fmt.Sprintf("%s/api/v3/core/groups/?search=%s", c.baseURL, url.QueryEscape(name))
	req, _ := http.NewRequest(http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("searching groups: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []struct {
			PK   string `json:"pk"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	for _, group := range result.Results {
		if group.Name == name {
			return group.PK, nil
		}
	}

	return "", fmt.Errorf("group %s not found", name)
}

func (c *Client) findUserID(username string) (int, error) {
	reqURL := fmt.Sprintf("%s/api/v3/core/users/?search=%s", c.baseURL, url.QueryEscape(username))
	req, _ := http.NewRequest(http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("searching users: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []struct {
			PK       int    `json:"pk"`
			Username string `json:"username"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	for _, user := range result.Results {
		if user.Username == username {
			return user.PK, nil
		}
	}

	return 0, nil // Not found
}

func (c *Client) addUserToGroup(userID int, groupName string) error {
	// Find the group
	groupID, err := c.findGroupID(groupName)
	if err != nil {
		return err
	}

	// Add user to group using the group's add_user endpoint
	reqURL := fmt.Sprintf("%s/api/v3/core/groups/%s/add_user/", c.baseURL, groupID)
	payload := map[string]int{"pk": userID}
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 204 = success, 200 = already in group (idempotent)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("adding user to group: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) tokenExists(identifier string) (bool, error) {
	reqURL := fmt.Sprintf("%s/api/v3/core/tokens/?identifier=%s", c.baseURL, url.QueryEscape(identifier))
	req, _ := http.NewRequest(http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("searching tokens: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []struct {
			Identifier string `json:"identifier"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}

	for _, token := range result.Results {
		if token.Identifier == identifier {
			return true, nil
		}
	}

	return false, nil
}

func (c *Client) findOutpostByName(name string) (*OutpostResponse, error) {
	reqURL := fmt.Sprintf("%s/api/v3/outposts/instances/?search=%s", c.baseURL, url.QueryEscape(name))
	req, _ := http.NewRequest(http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("searching outposts: status %d: %s", resp.StatusCode, string(body))
	}

	var result OutpostPaginatedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
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
func (c *Client) CreateUser(username, password string) (int, error) {
	// Create the user
	payload := map[string]interface{}{
		"username":  username,
		"name":      username,
		"email":     c.ManagedUserEmail(username),
		"path":      "users",
		"is_active": true,
	}
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/core/users/", bytes.NewReader(payloadBytes))
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("creating user: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		PK int `json:"pk"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decoding response: %w", err)
	}

	// Set the user's password
	if err := c.setUserPassword(result.PK, password); err != nil {
		return 0, fmt.Errorf("setting password: %w", err)
	}

	return result.PK, nil
}

// setUserPassword sets a user's password via the Authentik API
func (c *Client) setUserPassword(userID int, password string) error {
	reqURL := fmt.Sprintf("%s/api/v3/core/users/%d/set_password/", c.baseURL, userID)
	payload := map[string]string{"password": password}
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 204 No Content = success
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("setting password: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// SetUserEmail sets the user's identity email via the Authentik API.
func (c *Client) SetUserEmail(userID int, email string) error {
	reqURL := fmt.Sprintf("%s/api/v3/core/users/%d/", c.baseURL, userID)
	payload := map[string]string{"email": email}
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPatch, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("setting user email: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// SetUserPassword sets a user's password via the Authentik API (public wrapper)
func (c *Client) SetUserPassword(userID int, password string) error {
	return c.setUserPassword(userID, password)
}

// AddUserToGroup adds a user to a group by name (public wrapper around internal method)
func (c *Client) AddUserToGroup(userID int, groupName string) error {
	return c.addUserToGroup(userID, groupName)
}

// RemoveUserFromGroup removes a user from a group by name
func (c *Client) RemoveUserFromGroup(userID int, groupName string) error {
	groupID, err := c.findGroupID(groupName)
	if err != nil {
		return err
	}

	reqURL := fmt.Sprintf("%s/api/v3/core/groups/%s/remove_user/", c.baseURL, groupID)
	payload := map[string]int{"pk": userID}
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("removing user from group: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// FindUserID finds a user ID by username (public wrapper)
func (c *Client) FindUserID(username string) (int, error) {
	return c.findUserID(username)
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
func (c *Client) ListUsers() ([]ManagedUserInfo, error) {
	// Fetch users of type "internal"
	reqURL := fmt.Sprintf("%s/api/v3/core/users/?type=internal&page_size=200", c.baseURL)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("listing users: status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var rawResult struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(body, &rawResult); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	// Get the admin group members to cross-reference
	adminGroupMembers, err := c.getAdminGroupMembers()
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
func (c *Client) getAdminGroupMembers() (map[int]bool, error) {
	groupID, err := c.findGroupID("authentik Admins")
	if err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s/api/v3/core/groups/%s/", c.baseURL, groupID)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetching group: status %d: %s", resp.StatusCode, string(body))
	}

	var group struct {
		Users []int `json:"users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&group); err != nil {
		return nil, fmt.Errorf("decoding group: %w", err)
	}

	members := make(map[int]bool)
	for _, uid := range group.Users {
		members[uid] = true
	}
	return members, nil
}

// DeleteUser deletes a user by username
func (c *Client) DeleteUser(username string) error {
	// Find the user ID first
	userID, err := c.findUserID(username)
	if err != nil {
		return fmt.Errorf("finding user: %w", err)
	}
	if userID == 0 {
		return nil // User doesn't exist, nothing to delete
	}

	// Delete the user
	reqURL := fmt.Sprintf("%s/api/v3/core/users/%d/", c.baseURL, userID)
	req, err := http.NewRequest(http.MethodDelete, reqURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	// 204 No Content = success, 404 = already deleted
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("deleting user: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// EnsureLoginConfiguration applies Bloud-specific login page settings:
// - Sets the authentication flow title to "Sign in to Bloud"
// - Configures the identification stage to only accept username (not email)
// This is idempotent — safe to call on every PostStart.
//
// Authentik creates default flows asynchronously via blueprints after the health endpoint
// returns ready, so we retry until our changes stick. The blueprint for the default
// authentication flow runs during startup and can overwrite a patch applied just before it
// completes. We detect this by re-reading the flow title 3 seconds after patching — if a
// blueprint reset it, the outer loop retries, eventually patching after all blueprints finish.
//
// Refs:
//   - PATCH /api/v3/flows/instances/:slug/ (slug path param, title body field)
//   - GET  /api/v3/flows/instances/:slug/ (verify title)
//   - PATCH /api/v3/stages/identification/:stage_uuid/ (UUID path param, user_fields body field)
func (c *Client) EnsureLoginConfiguration() error {
	const (
		timeout  = 2 * time.Minute
		interval = 10 * time.Second
	)
	deadline := time.Now().Add(timeout)

	for {
		err := c.applyAndVerifyLoginConfiguration()
		if err == nil {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for login configuration to apply: %w", err)
		}

		time.Sleep(interval)
	}
}

// applyAndVerifyLoginConfiguration patches the flow title and identification stage, then
// waits 3 seconds and re-reads both to confirm a blueprint didn't overwrite them.
func (c *Client) applyAndVerifyLoginConfiguration() error {
	if err := c.ensureFlowTitle("default-authentication-flow", "Sign in to Bloud"); err != nil {
		return fmt.Errorf("ensuring flow title: %w", err)
	}

	if err := c.ensureIdentificationStageUsernameOnly("default-authentication-identification"); err != nil {
		return fmt.Errorf("ensuring identification stage: %w", err)
	}

	// Wait briefly, then re-read both the flow title and identification stage user_fields
	// to confirm no blueprint overwrote our patches.
	time.Sleep(3 * time.Second)

	title, err := c.getFlowTitle("default-authentication-flow")
	if err != nil {
		return fmt.Errorf("verifying flow title: %w", err)
	}
	if title != "Sign in to Bloud" {
		return fmt.Errorf("flow title was reset to %q by a blueprint, will retry", title)
	}

	userFields, err := c.getIdentificationStageUserFields("default-authentication-identification")
	if err != nil {
		return fmt.Errorf("verifying identification stage: %w", err)
	}
	if len(userFields) != 1 || userFields[0] != "username" {
		return fmt.Errorf("identification stage user_fields was reset to %v by a blueprint, will retry", userFields)
	}

	return nil
}

// getFlowTitle fetches the current title of a flow by slug.
func (c *Client) getFlowTitle(slug string) (string, error) {
	reqURL := fmt.Sprintf("%s/api/v3/flows/instances/%s/", c.baseURL, url.PathEscape(slug))
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("fetching flow: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding flow: %w", err)
	}
	return result.Title, nil
}

// getIdentificationStageUserFields fetches the current user_fields of an identification stage by name.
func (c *Client) getIdentificationStageUserFields(stageName string) ([]string, error) {
	reqURL := fmt.Sprintf("%s/api/v3/stages/identification/?search=%s", c.baseURL, url.QueryEscape(stageName))
	req, _ := http.NewRequest(http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching identification stage: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetching identification stage: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []struct {
			Name       string   `json:"name"`
			UserFields []string `json:"user_fields"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding identification stage: %w", err)
	}

	for _, stage := range result.Results {
		if stage.Name == stageName {
			return stage.UserFields, nil
		}
	}
	return nil, fmt.Errorf("identification stage %q not found", stageName)
}

// ensureFlowTitle PATCHes the title of a flow by slug.
// API: PATCH /api/v3/flows/instances/:slug/ — slug is the URL path parameter.
func (c *Client) ensureFlowTitle(slug, title string) error {
	reqURL := fmt.Sprintf("%s/api/v3/flows/instances/%s/", c.baseURL, url.PathEscape(slug))
	payload := map[string]string{"title": title}
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPatch, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("patching flow title: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ensureIdentificationStageUsernameOnly sets user_fields to ["username"] on an identification stage.
// API: GET /api/v3/stages/identification/?search=name to find the stage UUID,
// then PATCH /api/v3/stages/identification/:stage_uuid/ with user_fields.
// Valid user_fields values: email, username, upn.
func (c *Client) ensureIdentificationStageUsernameOnly(stageName string) error {
	reqURL := fmt.Sprintf("%s/api/v3/stages/identification/?search=%s", c.baseURL, url.QueryEscape(stageName))
	req, _ := http.NewRequest(http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching identification stages: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("fetching identification stages: status %d: %s", resp.StatusCode, string(body))
	}

	// pk is a UUID string (stage_uuid), used as the path parameter for PATCH
	var result struct {
		Results []struct {
			PK   string `json:"pk"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding identification stages: %w", err)
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

	patchURL := fmt.Sprintf("%s/api/v3/stages/identification/%s/", c.baseURL, stageUUID)
	payload := map[string]interface{}{
		"user_fields": []string{"username"},
	}
	payloadBytes, _ := json.Marshal(payload)

	patchReq, err := http.NewRequest(http.MethodPatch, patchURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	patchReq.Header.Set("Authorization", "Bearer "+c.token)
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("Accept", "application/json")

	patchResp, err := c.httpClient.Do(patchReq)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer patchResp.Body.Close()

	if patchResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(patchResp.Body)
		return fmt.Errorf("patching identification stage: status %d: %s", patchResp.StatusCode, string(body))
	}

	return nil
}

// EnsureBranding updates the default Authentik brand with the provided CSS.
// The CSS is pushed inline because Authentik uses Constructable Stylesheets
// which forbid @import rules in branding_custom_css.
// This is idempotent — safe to call on every PostStart.
//
// The default brand is created by an Authentik migration, which can lag
// behind the server readiness probe on slow hosts (cold CI runners). A
// PostStart error is terminal in the reconciler (ERROR status is never
// retried), so a single transient "brand not found" would brick the SSO
// stack. Retry while the brand is absent; other API errors fail fast.
func (c *Client) EnsureBranding(css string) error {
	const (
		maxAttempts = 15
		retryDelay  = 4 * time.Second
	)

	brandPK := ""
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		brandPK, lastErr = c.defaultBrandPK()
		if lastErr == nil {
			break
		}
		if !errors.Is(lastErr, errBrandNotFound) {
			return lastErr
		}
		if attempt < maxAttempts {
			time.Sleep(retryDelay)
		}
	}
	if brandPK == "" {
		return lastErr
	}

	patchURL := fmt.Sprintf("%s/api/v3/core/brands/%s/", c.baseURL, brandPK)

	payload := map[string]string{"branding_custom_css": css}
	payloadBytes, _ := json.Marshal(payload)

	patchReq, err := http.NewRequest(http.MethodPatch, patchURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("creating patch request: %w", err)
	}
	patchReq.Header.Set("Authorization", "Bearer "+c.token)
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("Accept", "application/json")

	patchResp, err := c.httpClient.Do(patchReq)
	if err != nil {
		return fmt.Errorf("patching brand: %w", err)
	}
	defer patchResp.Body.Close()

	if patchResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(patchResp.Body)
		return fmt.Errorf("patching brand CSS: status %d: %s", patchResp.StatusCode, string(body))
	}

	return nil
}

// errBrandNotFound indicates the default brand does not exist yet (migration
// still running).
var errBrandNotFound = fmt.Errorf("default brand not found")

// defaultBrandPK returns the UUID of the default Authentik brand
// (domain = "authentik-default"), or errBrandNotFound while it does not exist.
func (c *Client) defaultBrandPK() (string, error) {
	reqURL := fmt.Sprintf("%s/api/v3/core/brands/?domain=authentik-default", c.baseURL)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching brands: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("fetching brands: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []struct {
			PK string `json:"brand_uuid"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding brands: %w", err)
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
// The returned OIDCConfig contains path templates (no host) — callers derive full URLs from the request Host.
func (c *Client) EnsureBloudOAuthApp(baseURLs []string, clientSecret string) (*OIDCConfig, error) {
	// Build redirect URIs for all base URLs
	var redirectURIs []string
	for _, baseURL := range baseURLs {
		redirectURIs = append(redirectURIs, baseURL+bloudRedirectURI)
	}

	// Check if provider already exists
	providerID, err := c.findProviderID("oauth2", bloudProviderName)
	if err != nil {
		return nil, fmt.Errorf("checking existing provider: %w", err)
	}

	if providerID == 0 {
		// Create the OAuth2 provider with all redirect URIs
		providerID, err = c.createBloudOAuth2Provider(redirectURIs, clientSecret)
		if err != nil {
			return nil, fmt.Errorf("creating OAuth2 provider: %w", err)
		}
	} else {
		// Provider exists — update redirect URIs to include any new IPs
		if err := c.updateBloudOAuth2ProviderRedirectURIs(providerID, redirectURIs); err != nil {
			return nil, fmt.Errorf("updating redirect URIs: %w", err)
		}
	}

	// Check if application already exists
	exists, err := c.applicationExists(bloudAppSlug)
	if err != nil {
		return nil, fmt.Errorf("checking existing application: %w", err)
	}

	if !exists {
		// Create the application
		if err := c.createBloudApplication(providerID); err != nil {
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
func (c *Client) createBloudOAuth2Provider(redirectURIs []string, clientSecret string) (int, error) {
	// Find required flows
	authFlowID, err := c.findFlowID("default-provider-authorization-implicit-consent")
	if err != nil {
		// Fall back to explicit consent flow
		authFlowID, err = c.findFlowID("default-provider-authorization-explicit-consent")
		if err != nil {
			return 0, fmt.Errorf("finding authorization flow: %w", err)
		}
	}

	invalidFlowID, err := c.findFlowID("default-provider-invalidation-flow")
	if err != nil {
		return 0, fmt.Errorf("finding invalidation flow: %w", err)
	}

	// Get certificate UUID for signing (Authentik API requires UUID, not name)
	certUUID, err := c.getFirstCertificateUUID()
	if err != nil {
		return 0, fmt.Errorf("getting signing certificate: %w", err)
	}

	// Get scope property mappings for openid, profile, and email
	scopeMappings, err := c.getScopePropertyMappings([]string{"openid", "profile", "email"})
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
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/providers/oauth2/", bytes.NewReader(payloadBytes))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("creating OAuth2 provider: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		PK int `json:"pk"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	return result.PK, nil
}

// AddRedirectURI adds a redirect URI to an OAuth2 provider if it's not already registered.
// This is called lazily on first request from an unknown host.
func (c *Client) AddRedirectURI(providerID int, redirectURI string) error {
	// Fetch current provider to get existing redirect URIs
	reqURL := fmt.Sprintf("%s/api/v3/providers/oauth2/%d/", c.baseURL, providerID)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching provider: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("fetching provider: status %d: %s", resp.StatusCode, string(body))
	}

	var provider struct {
		RedirectURIs []struct {
			MatchingMode string `json:"matching_mode"`
			URL          string `json:"url"`
		} `json:"redirect_uris"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		return fmt.Errorf("decoding provider: %w", err)
	}

	// Check if already registered
	for _, uri := range provider.RedirectURIs {
		if uri.URL == redirectURI {
			return nil // Already registered
		}
	}

	// Build updated list with new URI appended
	var uriEntries []map[string]string
	for _, uri := range provider.RedirectURIs {
		uriEntries = append(uriEntries, map[string]string{
			"matching_mode": uri.MatchingMode,
			"url":           uri.URL,
		})
	}
	uriEntries = append(uriEntries, map[string]string{
		"matching_mode": "strict",
		"url":           redirectURI,
	})

	payload := map[string]interface{}{
		"redirect_uris": uriEntries,
	}
	payloadBytes, _ := json.Marshal(payload)

	patchReq, err := http.NewRequest(http.MethodPatch, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("creating patch request: %w", err)
	}
	patchReq.Header.Set("Authorization", "Bearer "+c.token)
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("Accept", "application/json")

	patchResp, err := c.httpClient.Do(patchReq)
	if err != nil {
		return fmt.Errorf("patching provider: %w", err)
	}
	defer patchResp.Body.Close()

	if patchResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(patchResp.Body)
		return fmt.Errorf("patching redirect URIs: status %d: %s", patchResp.StatusCode, string(body))
	}

	return nil
}

// updateBloudOAuth2ProviderRedirectURIs patches the redirect URIs on an existing provider
func (c *Client) updateBloudOAuth2ProviderRedirectURIs(providerID int, redirectURIs []string) error {
	var uriEntries []map[string]string
	for _, uri := range redirectURIs {
		uriEntries = append(uriEntries, map[string]string{
			"matching_mode": "strict",
			"url":           uri,
		})
	}

	payload := map[string]interface{}{
		"redirect_uris": uriEntries,
	}
	payloadBytes, _ := json.Marshal(payload)

	reqURL := fmt.Sprintf("%s/api/v3/providers/oauth2/%d/", c.baseURL, providerID)
	req, err := http.NewRequest(http.MethodPatch, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("updating redirect URIs: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// getFirstCertificateUUID retrieves the UUID of the first available certificate keypair
// This is needed because the Authentik API requires a UUID for signing_key, not a name
func (c *Client) getFirstCertificateUUID() (string, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/api/v3/crypto/certificatekeypairs/", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("listing certificates: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []struct {
			PK string `json:"pk"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if len(result.Results) == 0 {
		return "", fmt.Errorf("no certificates found in Authentik")
	}

	return result.Results[0].PK, nil
}

// getScopePropertyMappings retrieves the UUIDs of scope property mappings by scope name
func (c *Client) getScopePropertyMappings(scopes []string) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/api/v3/propertymappings/provider/scope/?page_size=50", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("listing scope mappings: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []struct {
			PK        string `json:"pk"`
			ScopeName string `json:"scope_name"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	// Build a set of requested scopes for quick lookup
	scopeSet := make(map[string]bool)
	for _, s := range scopes {
		scopeSet[s] = true
	}

	// Find matching mappings
	var mappings []string
	for _, mapping := range result.Results {
		if scopeSet[mapping.ScopeName] {
			mappings = append(mappings, mapping.PK)
		}
	}

	return mappings, nil
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
func (c *Client) ensureBloudEmailScopeMapping() (string, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/api/v3/propertymappings/provider/scope/?page_size=100", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("listing scope mappings: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results []struct {
			PK      string `json:"pk"`
			Name    string `json:"name"`
			Managed string `json:"managed"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
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
	payloadBytes, _ := json.Marshal(payload)
	req, err = http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/propertymappings/provider/scope/", bytes.NewReader(payloadBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err = c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("creating scope mapping: status %d: %s", resp.StatusCode, string(body))
	}

	var created struct {
		PK string `json:"pk"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", err
	}
	return created.PK, nil
}

// managedEmailScopeMappingUUID returns the UUID of Authentik's managed
// "email" scope mapping (or "" when not found).
func (c *Client) managedEmailScopeMappingUUID() (string, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/api/v3/propertymappings/provider/scope/?page_size=100", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("listing scope mappings: status %d", resp.StatusCode)
	}

	var result struct {
		Results []struct {
			PK      string `json:"pk"`
			Managed string `json:"managed"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
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
func (c *Client) ensureProviderEmailScopeMapping(providerID int) error {
	bloudEmail, err := c.ensureBloudEmailScopeMapping()
	if err != nil {
		return fmt.Errorf("ensuring email scope mapping: %w", err)
	}

	reqURL := fmt.Sprintf("%s/api/v3/providers/oauth2/%d/", c.baseURL, providerID)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("fetching provider: status %d: %s", resp.StatusCode, string(body))
	}

	var provider struct {
		PropertyMappings []string `json:"property_mappings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		return fmt.Errorf("decoding provider: %w", err)
	}

	managedEmail, _ := c.managedEmailScopeMappingUUID()

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

	// No drift — avoid churning the provider on every reconciliation pass.
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

	payload := map[string]interface{}{"property_mappings": mappings}
	payloadBytes, _ := json.Marshal(payload)
	patchReq, err := http.NewRequest(http.MethodPatch, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	patchReq.Header.Set("Authorization", "Bearer "+c.token)
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("Accept", "application/json")

	patchResp, err := c.httpClient.Do(patchReq)
	if err != nil {
		return err
	}
	defer patchResp.Body.Close()

	if patchResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(patchResp.Body)
		return fmt.Errorf("patching provider property mappings: status %d: %s", patchResp.StatusCode, string(body))
	}
	return nil
}

// applicationExists checks if an application with the given slug exists
func (c *Client) applicationExists(slug string) (bool, error) {
	reqURL := fmt.Sprintf("%s/api/v3/core/applications/%s/", c.baseURL, url.PathEscape(slug))
	req, _ := http.NewRequest(http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK, nil
}

// createBloudApplication creates the Authentik application for Bloud
func (c *Client) createBloudApplication(providerID int) error {
	payload := map[string]interface{}{
		"name":               bloudAppName,
		"slug":               bloudAppSlug,
		"provider":           providerID,
		"policy_engine_mode": "any",
	}
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/core/applications/", bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("creating application: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ExchangeCode exchanges an authorization code for tokens
func (c *Client) ExchangeCode(code, redirectURI, clientID, clientSecret string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/application/o/token/", bytes.NewReader([]byte(data.Encode())))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed: status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &tokenResp, nil
}

// GetUserInfo retrieves user information using an access token
func (c *Client) GetUserInfo(accessToken string) (*UserInfo, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/application/o/userinfo/", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("userinfo request failed: status %d: %s", resp.StatusCode, string(body))
	}

	var userInfo UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &userInfo, nil
}

// EnsureForwardAuth implements orchestrator.SSOProvisioner.
func (c *Client) EnsureForwardAuth(appName, displayName, externalURL string) error {
	return c.EnsureForwardAuthApplication(appName, displayName, externalURL)
}

// EnsureForwardDomainAuth creates a forward_domain proxy provider, application, and
// standalone proxy outpost for the tailnet MagicDNS domain. In forward_domain mode,
// a single cookie on the domain (e.g. ".tail12756a.ts.net") authenticates all *.domain
// subdomains. A standalone outpost is used (instead of the embedded outpost) so that
// AUTHENTIK_HOST_BROWSER can point to the tailnet URL while the embedded outpost
// continues using localhost for local access.
// Returns the outpost API token needed to start the standalone outpost container.
// cookieDomain is the MagicDNS suffix (e.g. "tail12756a.ts.net").
func (c *Client) EnsureForwardDomainAuth(cookieDomain string) (string, error) {
	const (
		providerName = "Tailnet Forward Domain Provider"
		appSlug      = "tailnet-domain"
		appName      = "Tailnet Domain Auth"
	)

	externalHost := "https://bloud." + cookieDomain

	// Check if provider already exists.
	existingID, err := c.findProviderID("proxy", providerName)
	if err != nil {
		return "", fmt.Errorf("checking proxy provider: %w", err)
	}

	var providerID int
	if existingID != 0 {
		providerID = existingID
	} else {
		authFlowID, err := c.findFlowID("default-authentication-flow")
		if err != nil {
			return "", fmt.Errorf("finding auth flow: %w", err)
		}
		invalidationFlowID, err := c.findFlowID("default-provider-invalidation-flow")
		if err != nil {
			return "", fmt.Errorf("finding invalidation flow: %w", err)
		}

		providerID, err = c.createForwardDomainProvider(providerName, externalHost, cookieDomain, authFlowID, invalidationFlowID)
		if err != nil {
			return "", fmt.Errorf("creating forward_domain provider: %w", err)
		}
	}

	if err := c.ensureProxyApplication(appSlug, appName, providerID); err != nil {
		return "", fmt.Errorf("ensuring proxy application: %w", err)
	}

	// Use a standalone proxy outpost (not the embedded outpost) so the browser-facing
	// URL can be the tailnet domain while local auth stays on localhost.
	if err := c.ensureProxyOutpost(providerID); err != nil {
		return "", fmt.Errorf("ensuring proxy outpost: %w", err)
	}

	token, err := c.GetProxyOutpostToken()
	if err != nil {
		return "", fmt.Errorf("getting proxy outpost token: %w", err)
	}

	return token, nil
}

// ensureProxyOutpost creates the standalone proxy outpost if it doesn't exist.
// This outpost runs as a separate container with AUTHENTIK_HOST_BROWSER set to the
// tailnet URL, allowing remote users to authenticate via tailnet while the embedded
// outpost continues serving local auth on localhost.
func (c *Client) ensureProxyOutpost(providerID int) error {
	outpost, err := c.findOutpostByName(proxyOutpostName)
	if err != nil {
		return err
	}
	if outpost != nil {
		// Outpost exists — ensure the provider is attached.
		for _, pid := range outpost.Providers {
			if pid == providerID {
				return nil
			}
		}
		outpost.Providers = append(outpost.Providers, providerID)
		return c.updateOutpostProviders(outpost.PK, outpost.Providers)
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
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/outposts/instances/", bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("creating proxy outpost: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetProxyOutpostToken returns the auto-generated token for the standalone proxy outpost.
// Authentik creates a token with identifier "ak-outpost-{uuid}-api" when an outpost is created.
func (c *Client) GetProxyOutpostToken() (string, error) {
	outpost, err := c.findOutpostByName(proxyOutpostName)
	if err != nil {
		return "", fmt.Errorf("finding outpost: %w", err)
	}
	if outpost == nil {
		return "", fmt.Errorf("proxy outpost not found")
	}

	tokenIdentifier := fmt.Sprintf("ak-outpost-%s-api", outpost.PK)

	reqURL := fmt.Sprintf("%s/api/v3/core/tokens/%s/view_key/", c.baseURL, url.PathEscape(tokenIdentifier))
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("getting token key: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.Key, nil
}

// createForwardDomainProvider creates a proxy provider in forward_domain mode.
func (c *Client) createForwardDomainProvider(name, externalHost, cookieDomain, authFlowID, invalidationFlowID string) (int, error) {
	payload := map[string]interface{}{
		"name":               name,
		"authorization_flow": authFlowID,
		"invalidation_flow":  invalidationFlowID,
		"external_host":      externalHost,
		"mode":               "forward_domain",
		"cookie_domain":      cookieDomain,
	}
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/providers/proxy/", bytes.NewReader(payloadBytes))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		PK int `json:"pk"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	return result.PK, nil
}

// EnsureForwardAuthApplication creates or verifies the Authentik proxy provider and
// application for an app using the forward-auth SSO strategy. It also adds the
// provider to the embedded outpost so Traefik's forwardAuth middleware can reach it.
// externalURL is the full URL users access the app on, e.g. "http://navidrome.localhost:8080".
func (c *Client) EnsureForwardAuthApplication(appName, displayName, externalURL string) error {
	providerName := fmt.Sprintf("%s Proxy Provider", displayName)

	// Check if provider already exists
	existingID, err := c.findProviderID("proxy", providerName)
	if err != nil {
		return fmt.Errorf("checking proxy provider: %w", err)
	}

	var providerID int
	if existingID != 0 {
		providerID = existingID
	} else {
		// Find required flows
		authFlowID, err := c.findFlowID("default-authentication-flow")
		if err != nil {
			return fmt.Errorf("finding auth flow: %w", err)
		}
		invalidationFlowID, err := c.findFlowID("default-provider-invalidation-flow")
		if err != nil {
			return fmt.Errorf("finding invalidation flow: %w", err)
		}

		providerID, err = c.createProxyProvider(providerName, externalURL, authFlowID, invalidationFlowID)
		if err != nil {
			return fmt.Errorf("creating proxy provider: %w", err)
		}
	}

	// Ensure application exists
	if err := c.ensureProxyApplication(appName, displayName, providerID); err != nil {
		return fmt.Errorf("ensuring proxy application: %w", err)
	}

	// Add provider to embedded outpost
	if err := c.AddProviderToEmbeddedOutpost(providerName); err != nil {
		return fmt.Errorf("adding to embedded outpost: %w", err)
	}

	return nil
}

// createProxyProvider creates a new Authentik proxy provider in forward_single mode.
func (c *Client) createProxyProvider(name, externalHost, authFlowID, invalidationFlowID string) (int, error) {
	payload := map[string]interface{}{
		"name":               name,
		"authorization_flow": authFlowID,
		"invalidation_flow":  invalidationFlowID,
		"external_host":      externalHost,
		"mode":               "forward_single",
	}
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/providers/proxy/", bytes.NewReader(payloadBytes))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		PK int `json:"pk"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	return result.PK, nil
}

// ensureProxyApplication creates the Authentik application for a proxy provider if it doesn't exist.
func (c *Client) ensureProxyApplication(slug, displayName string, providerID int) error {
	reqURL := fmt.Sprintf("%s/api/v3/core/applications/%s/", c.baseURL, url.PathEscape(slug))
	req, _ := http.NewRequest(http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil // Already exists
	}

	// Create application
	payload := map[string]interface{}{
		"name":               displayName,
		"slug":               slug,
		"provider":           providerID,
		"policy_engine_mode": "any",
	}
	payloadBytes, _ := json.Marshal(payload)

	req, err = http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/core/applications/", bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err = c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// EnsureNativeOIDC creates or verifies the Authentik OAuth2 provider and
// application for an app using the native-oidc SSO strategy. The provider uses
// a confidential client with the exact client ID/secret derived by the
// host-agent (so the app and the identity provider agree without a shared
// store). redirectURIs must cover every URL the app may use as its callback.
// launchURL, when non-empty, is set as the application's meta launch URL.
func (c *Client) EnsureNativeOIDC(appName, displayName, clientID, clientSecret string, redirectURIs []string, launchURL string) error {
	providerName := fmt.Sprintf("%s OAuth2 Provider", displayName)

	// Check if provider already exists
	existingID, err := c.findProviderID("oauth2", providerName)
	if err != nil {
		return fmt.Errorf("checking OAuth2 provider: %w", err)
	}

	var providerID int
	if existingID != 0 {
		providerID = existingID
		// Refresh redirect URIs so newly detected hosts/IPs work
		if err := c.updateBloudOAuth2ProviderRedirectURIs(providerID, redirectURIs); err != nil {
			return fmt.Errorf("updating redirect URIs: %w", err)
		}
		// Swap in the verified-email scope mapping (Authentik's managed one
		// reports email_verified: False, which apps like AFFiNE reject).
		if err := c.ensureProviderEmailScopeMapping(providerID); err != nil {
			return fmt.Errorf("updating email scope mapping: %w", err)
		}
	} else {
		// Find required flows
		authFlowID, err := c.findFlowID("default-provider-authorization-implicit-consent")
		if err != nil {
			authFlowID, err = c.findFlowID("default-provider-authorization-explicit-consent")
			if err != nil {
				return fmt.Errorf("finding authorization flow: %w", err)
			}
		}
		invalidationFlowID, err := c.findFlowID("default-provider-invalidation-flow")
		if err != nil {
			return fmt.Errorf("finding invalidation flow: %w", err)
		}

		certUUID, err := c.getFirstCertificateUUID()
		if err != nil {
			return fmt.Errorf("getting signing certificate: %w", err)
		}
		// Use Bloud's verified-email scope mapping for the "email" scope:
		// Authentik's managed mapping hardcodes email_verified: False, which
		// breaks apps whose OIDC provider rejects unverified emails (AFFiNE).
		scopeMappings, err := c.getScopePropertyMappings([]string{"openid", "profile"})
		if err != nil {
			return fmt.Errorf("getting scope mappings: %w", err)
		}
		bloudEmail, err := c.ensureBloudEmailScopeMapping()
		if err != nil {
			return fmt.Errorf("ensuring email scope mapping: %w", err)
		}
		scopeMappings = append(scopeMappings, bloudEmail)

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
			"access_token_validity":      "minutes=5",
			"refresh_token_validity":     "days=30",
		}
		payloadBytes, _ := json.Marshal(payload)

		req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/providers/oauth2/", bytes.NewReader(payloadBytes))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("creating OAuth2 provider: status %d: %s", resp.StatusCode, string(body))
		}

		var result struct {
			PK int `json:"pk"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return err
		}
		providerID = result.PK
	}

	// Ensure the application exists (and points at this provider)
	if err := c.ensureOIDCApplication(appName, displayName, providerID, launchURL); err != nil {
		return fmt.Errorf("ensuring OAuth2 application: %w", err)
	}

	return nil
}

// ensureOIDCApplication creates the Authentik application for an OIDC provider
// if it doesn't exist. An existing application is left untouched (provider
// drift is not reconciled — the provider is the source of auth behavior).
func (c *Client) ensureOIDCApplication(slug, displayName string, providerID int, launchURL string) error {
	reqURL := fmt.Sprintf("%s/api/v3/core/applications/%s/", c.baseURL, url.PathEscape(slug))
	req, _ := http.NewRequest(http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil // Already exists
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
	payloadBytes, _ := json.Marshal(payload)

	req, err = http.NewRequest(http.MethodPost, c.baseURL+"/api/v3/core/applications/", bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err = c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
