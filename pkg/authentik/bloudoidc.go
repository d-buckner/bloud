// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

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

	payload := map[string]any{
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
		JSON(map[string]any{"redirect_uris": uriEntries}).
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
