package authentik

import (
	"bytes"
	"encoding/json"
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
	httpClient *http.Client
}

// NewClient creates a new Authentik API client
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
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
	ldapProviderName     = "Bloud LDAP Provider"
	ldapApplicationSlug  = "ldap"
	ldapApplicationName  = "LDAP Authentication"
	ldapOutpostName      = "Bloud LDAP Outpost"
	ldapServiceUsername  = "ldap-service"
	ldapServiceTokenID   = "ldap-service-bind-token"
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

	// 6. Create LDAP outpost (if not exists)
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

// CreateUser creates a new user in Authentik and sets their password
func (c *Client) CreateUser(username, password string) (int, error) {
	// Create the user
	payload := map[string]interface{}{
		"username":  username,
		"name":      username,
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

// AddUserToGroup adds a user to a group by name (public wrapper around internal method)
func (c *Client) AddUserToGroup(userID int, groupName string) error {
	return c.addUserToGroup(userID, groupName)
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

// OIDC constants for Bloud's own OAuth2 application
const (
	bloudAppSlug         = "bloud"
	bloudAppName         = "Bloud"
	bloudProviderName    = "Bloud OAuth2 Provider"
	bloudRedirectURI     = "/auth/callback"
)

// OIDCConfig holds the OAuth2/OIDC configuration for Bloud
type OIDCConfig struct {
	ProviderID   int    // Authentik provider PK, used for lazy redirect URI registration
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
		"redirect_uris":             uriEntries,
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
