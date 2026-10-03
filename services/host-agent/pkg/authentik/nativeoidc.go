// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

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

	providerID := existingID
	if providerID == 0 {
		if providerID, err = c.createNativeProvider(ctx, providerName, clientID, clientSecret, redirectURIs, tuning); err != nil {
			return err
		}
	} else if err := c.reconcileNativeProvider(ctx, providerID, redirectURIs, tuning); err != nil {
		return err
	}

	// Ensure the application exists (and points at this provider)
	if err := c.ensureOIDCApplication(ctx, appName, displayName, providerID, launchURL); err != nil {
		return fmt.Errorf("ensuring OAuth2 application: %w", err)
	}

	return nil
}

// reconcileNativeProvider brings an existing provider up to date: the redirect
// URI list is refreshed so newly detected hosts and addresses work, the
// verified-email scope mapping is swapped in (Authentik's managed one reports
// email_verified: False, which apps like AFFiNE reject), and the app's declared
// tuning is applied.
func (c *Client) reconcileNativeProvider(ctx context.Context, providerID int, redirectURIs []string, tuning OIDCTuning) error {
	if err := c.updateBloudOAuth2ProviderRedirectURIs(ctx, providerID, redirectURIs); err != nil {
		return fmt.Errorf("updating redirect URIs: %w", err)
	}
	if err := c.ensureProviderEmailScopeMapping(ctx, providerID); err != nil {
		return fmt.Errorf("updating email scope mapping: %w", err)
	}
	if !tuning.isZero() {
		if err := c.ensureProviderTuning(ctx, providerID, tuning); err != nil {
			return fmt.Errorf("applying provider tuning: %w", err)
		}
	}
	return nil
}

// findAuthorizationFlow picks the provider's authorization flow, preferring the
// implicit-consent flow and falling back to explicit consent when the instance
// only ships one.
func (c *Client) findAuthorizationFlow(ctx context.Context) (string, error) {
	if id, err := c.findFlowID(ctx, "default-provider-authorization-implicit-consent"); err == nil {
		return id, nil
	}
	id, err := c.findFlowID(ctx, "default-provider-authorization-explicit-consent")
	if err != nil {
		return "", fmt.Errorf("finding authorization flow: %w", err)
	}
	return id, nil
}

// redirectURIEntries renders the redirect URIs as Authentik's strict-match
// rows: a URI that is not on the list is refused rather than prefix-matched.
func redirectURIEntries(redirectURIs []string) []map[string]string {
	var uriEntries []map[string]string
	for _, uri := range redirectURIs {
		uriEntries = append(uriEntries, map[string]string{
			"matching_mode": "strict",
			"url":           uri,
		})
	}
	return uriEntries
}

// createNativeProvider builds the OAuth2 provider from scratch: the flows it
// runs, the signing key it signs with, and the scope mapping set that carries
// Bloud's verified-email mapping plus whatever extra scopes the app declared.
func (c *Client) createNativeProvider(ctx context.Context, providerName, clientID, clientSecret string, redirectURIs []string, tuning OIDCTuning) (int, error) {
	// Find required flows
	authFlowID, err := c.findAuthorizationFlow(ctx)
	if err != nil {
		return 0, err
	}
	invalidationFlowID, err := c.findFlowID(ctx, "default-provider-invalidation-flow")
	if err != nil {
		return 0, fmt.Errorf("finding invalidation flow: %w", err)
	}

	certUUID, err := c.getFirstCertificateUUID(ctx)
	if err != nil {
		return 0, fmt.Errorf("getting signing certificate: %w", err)
	}
	// Use Bloud's verified-email scope mapping for the "email" scope:
	// Authentik's managed mapping hardcodes email_verified: False, which
	// breaks apps whose OIDC provider rejects unverified emails (AFFiNE).
	scopeMappings, err := c.getScopePropertyMappings(ctx, []string{"openid", "profile"})
	if err != nil {
		return 0, fmt.Errorf("getting scope mappings: %w", err)
	}
	bloudEmail, err := c.ensureBloudEmailScopeMapping(ctx)
	if err != nil {
		return 0, fmt.Errorf("ensuring email scope mapping: %w", err)
	}
	scopeMappings = append(scopeMappings, bloudEmail)
	extraMappings, err := c.extraScopeMappings(ctx, tuning.ExtraScopes)
	if err != nil {
		return 0, err
	}
	scopeMappings = append(scopeMappings, extraMappings...)

	payload := map[string]interface{}{
		"name":                       providerName,
		"authorization_flow":         authFlowID,
		"invalidation_flow":          invalidationFlowID,
		"client_type":                "confidential",
		"client_id":                  clientID,
		"client_secret":              clientSecret,
		"redirect_uris":              redirectURIEntries(redirectURIs),
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
		return 0, fmt.Errorf("creating OAuth2 provider: %w", err)
	}
	return result.PK, nil
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
