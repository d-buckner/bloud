// SPDX-License-Identifier: AGPL-3.0-only

package homeassistant

import (
	"context"
	"encoding/json"
	"fmt"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// The companion token: a Home Assistant long-lived access token that Bloud
// mints so a machine consumer (the ha-mcp wrapper today) can authenticate to
// Home Assistant without holding the break-glass owner password.
//
// Home Assistant has no REST endpoint for long-lived tokens. The documented
// paths are the profile page and the WebSocket command
// auth/long_lived_access_token, so minting means a WebSocket round trip
// (wsclient.go). INTEGRATION.md records the protocol source and the revocation
// path, which is also WebSocket-only: /auth/revoke matches refresh tokens, not
// long-lived access tokens.
//
// The token is minted once and reused. Every pass that found it usable is a
// read; a mint per pass would leave a new row on the owner's profile every
// reconciliation cycle.
const (
	// companionTokenKey is the secret name published to consumers. It is the
	// `appApi` contract's secret name, not a Home Assistant term: the contract
	// carries one credential and calls it `password`.
	companionTokenKey = "password"

	// companionClientName is what the token is labelled as on the Home
	// Assistant profile page, and what revocation matches on. An operator
	// looking at Home Assistant should be able to tell which integration
	// holds the token and delete it by hand.
	companionClientName = "Bloud ha-mcp"

	// companionTokenDays is the token's lifespan: Home Assistant's own
	// long-lived tokens are documented as valid for 10 years, so this is the
	// ceiling rather than an arbitrary long number. A shorter lifespan would
	// expire while the integration is still installed, and nothing here is
	// awake to renew it.
	companionTokenDays = 3650

	// longLivedTokenType is Home Assistant's token_type string for the tokens
	// this file mints, as reported by auth/refresh_tokens.
	longLivedTokenType = "long_lived_access_token"
)

// ensureCompanionToken returns the long-lived access token published to
// companion consumers, minting one only when none is stored or the stored one
// has stopped working.
//
// It is called from PostStart, where the network is allowed and Home Assistant
// is already answering. A failure is the caller's to weigh: the token is only
// useful to a consumer, so a Home Assistant install with no ha-mcp must not fail
// its install over one.
func (c *Configurator) ensureCompanionToken(ctx context.Context) (string, error) {
	if c.secrets == nil {
		return "", fmt.Errorf("no secrets provider available to store the Home Assistant companion token")
	}
	if stored := c.secrets.GetAppSecret(appName, companionTokenKey); stored != "" {
		valid, err := c.api.tokenValid(ctx, stored)
		if err != nil {
			// Inconclusive: keep the token rather than minting a replacement
			// for every pass while Home Assistant is briefly unreachable.
			c.logger.Warn("could not verify the stored Home Assistant companion token; keeping it", "error", err)
			return stored, nil
		}
		if valid {
			return stored, nil
		}
		c.logger.Info("stored Home Assistant companion token is no longer accepted; minting a new one")
	}

	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		return "", fmt.Errorf("failed to obtain the bootstrap password: %w", err)
	}
	accessToken, err := c.api.loginForAccessToken(ctx, bootstrapUsername, password)
	if err != nil {
		return "", err
	}

	token, err := c.mintLongLivedToken(ctx, accessToken)
	if err != nil {
		return "", err
	}
	if err := c.secrets.SetAppSecret(appName, companionTokenKey, token); err != nil {
		return "", fmt.Errorf("failed to publish the Home Assistant companion token: %w", err)
	}
	c.logger.Info("minted a Home Assistant long-lived companion token", "client_name", companionClientName)
	return token, nil
}

// mintLongLivedToken runs auth/long_lived_access_token over the WebSocket API
// using accessToken as the connection credential.
func (c *Configurator) mintLongLivedToken(ctx context.Context, accessToken string) (string, error) {
	wsURL, err := wsURLFor(c.baseURL())
	if err != nil {
		return "", err
	}
	conn, err := haDial(ctx, wsURL, accessToken)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.close() }()

	result, err := conn.call(ctx, map[string]any{
		"type":        "auth/long_lived_access_token",
		"client_name": companionClientName,
		"lifespan":    companionTokenDays,
	})
	if err != nil {
		return "", err
	}
	var token string
	if err := json.Unmarshal(result, &token); err != nil {
		return "", fmt.Errorf("the long-lived token payload from Home Assistant was unreadable: %w", err)
	}
	if token == "" {
		return "", fmt.Errorf("the long-lived token payload from Home Assistant carried no token")
	}
	return token, nil
}

// revokeCompanionToken deletes every long-lived token labelled as this
// integration's. It is teardown, so it is written to be safe to call when
// nothing was ever minted, and it reports the reason it could not act rather
// than pretending the tokens are gone.
//
// Revocation is by label, not by the stored string, so a token that was rotated
// by hand (or a mint that published twice) is still cleaned up.
func (c *Configurator) revokeCompanionToken(ctx context.Context) error {
	if c.secrets == nil {
		return nil
	}
	token := c.secrets.GetAppSecret(appName, companionTokenKey)
	if token == "" {
		return nil
	}
	wsURL, err := wsURLFor(c.baseURL())
	if err != nil {
		return err
	}
	conn, err := haDial(ctx, wsURL, token)
	if err != nil {
		// A refused connection is also the ordinary case for an instance that
		// is already stopped: the token dies with the data it was minted from.
		return fmt.Errorf("cannot reach Home Assistant to revoke its companion token: %w", err)
	}
	defer func() { _ = conn.close() }()

	result, err := conn.call(ctx, map[string]any{"type": "auth/refresh_tokens"})
	if err != nil {
		return err
	}
	var tokens []struct {
		ID         string `json:"id"`
		ClientName string `json:"client_name"`
		Type       string `json:"type"`
	}
	if err := json.Unmarshal(result, &tokens); err != nil {
		return fmt.Errorf("the Home Assistant token list was unreadable: %w", err)
	}
	deleted := 0
	for _, t := range tokens {
		if t.ClientName != companionClientName || t.Type != longLivedTokenType || t.ID == "" {
			continue
		}
		if _, err := conn.call(ctx, map[string]any{
			"type":             "auth/delete_refresh_token",
			"refresh_token_id": t.ID,
		}); err != nil {
			return err
		}
		deleted++
	}
	if deleted > 0 {
		c.logger.Info("revoked the Home Assistant companion tokens", "count", deleted)
	}
	return nil
}

// Remove revokes the companion token while Home Assistant can still be reached.
//
// The orchestrator owns containers and data; this is the one thing outside the
// container that this app leaves behind, because a long-lived admin token is
// stored in Bloud's secrets and is not part of Home Assistant's data directory.
// A failure is returned rather than swallowed: the uninstall should say that a
// token is still live somewhere instead of reporting a clean removal.
func (c *Configurator) Remove(ctx context.Context, _ *configurator.AppState, _ bool) error {
	return c.revokeCompanionToken(ctx)
}
