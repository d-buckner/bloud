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
