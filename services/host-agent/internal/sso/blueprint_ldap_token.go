// SPDX-License-Identifier: AGPL-3.0-only

package sso

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// GetLDAPOutpostToken queries Authentik API to get the auto-generated LDAP outpost token.
// Authentik auto-generates a token with identifier "ak-outpost-{uuid}-api" when an outpost is created.
func (g *BlueprintGenerator) GetLDAPOutpostToken(ctx context.Context, authentikURL, apiToken string) (string, error) {
	client := &http.Client{}

	// Step 1: Query for the LDAP outpost by name. Authentik names the token it
	// auto-generates after the outpost's PK, so the outpost has to be found
	// before its token can be.
	var outpostResp struct {
		Results []struct {
			PK string `json:"pk"`
		} `json:"results"`
	}
	outpostURL := fmt.Sprintf("%s/api/v3/outposts/instances/?name=%s", authentikURL, url.QueryEscape("Bloud LDAP Outpost"))
	if err := getAuthentikList(ctx, client, outpostURL, apiToken, "outpost", &outpostResp); err != nil {
		return "", err
	}
	if len(outpostResp.Results) == 0 {
		return "", fmt.Errorf("LDAP outpost not found")
	}

	// Step 2: Query for the token using the auto-generated identifier.
	tokenIdentifier := fmt.Sprintf("ak-outpost-%s-api", outpostResp.Results[0].PK)
	tokenURL := fmt.Sprintf("%s/api/v3/core/tokens/?identifier=%s", authentikURL, url.QueryEscape(tokenIdentifier))
	var tokenResp struct {
		Results []struct {
			Key string `json:"key"`
		} `json:"results"`
	}
	if err := getAuthentikList(ctx, client, tokenURL, apiToken, "token", &tokenResp); err != nil {
		return "", err
	}
	if len(tokenResp.Results) == 0 {
		return "", fmt.Errorf("LDAP outpost token not found (identifier: %s)", tokenIdentifier)
	}

	return tokenResp.Results[0].Key, nil
}

// getAuthentikList performs one authenticated GET against an Authentik list
// endpoint and decodes the page into out. `what` names the resource so the
// error says which of the two lookups failed.
func getAuthentikList(ctx context.Context, client *http.Client, endpoint, apiToken, what string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("creating %s request: %w", what, err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("querying %s: %w", what, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s query failed with status %d", what, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding %s response: %w", what, err)
	}
	return nil
}
