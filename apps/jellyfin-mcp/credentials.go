// SPDX-License-Identifier: AGPL-3.0-only

package jellyfinmcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"codeberg.org/d-buckner/bloud/apps/jellyfin"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// --- inbound bearer ---

// ensureInboundToken returns the bearer a harness presents, generating and
// publishing one on the first pass and reusing it afterwards.
//
// It is an opaque string rather than a structured token, because that is what
// the listener checks: the image takes HTTP_TOKEN and requires an exact match,
// with no signature, no audience, and no expiry to verify. There is nothing to
// mint and nothing to sign, and a JWT here would be a format the server ignores
// while looking like a credential it checks.
//
// It carries no expiry for the same reason it carries no signature. A harness
// caches the bearer it registered with, so a credential that expires is a
// namespace that works until the day it silently 401s, and fixing it needs a
// minter wired to the registration lifecycle. What Bloud has instead of expiry
// is rotation: clear `httpToken` from the secrets store and the next pass
// generates a fresh bearer, which invalidates the old one at the listener.
func (c *Configurator) ensureInboundToken() (string, error) {
	if c.secrets == nil {
		return "", nil
	}
	if existing := c.secrets.GetAppSecret(appName, httpTokenKey); existing != "" {
		return existing, nil
	}
	token, err := randomSecret()
	if err != nil {
		return "", fmt.Errorf("generating the %s MCP bearer: %w", appName, err)
	}
	if err := c.secrets.SetAppSecret(appName, httpTokenKey, token); err != nil {
		return "", fmt.Errorf("publishing the %s bearer: %w", appName, err)
	}
	c.logger.Info("generated the jellyfin-mcp MCP bearer")
	return token, nil
}

// randomSecret returns 32 bytes of entropy as URL-safe base64.
func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// --- outbound Jellyfin credential ---

// ensureJellyfinAPIKey returns the API key this app calls Jellyfin with,
// minting one through Jellyfin's own API on the first pass and reusing the
// stored key afterwards.
//
// A key rather than the bootstrap admin password, for two reasons that point
// the same way. The image has no password login at all: it reads JELLYFIN_URL,
// JELLYFIN_API_KEY, and an optional JELLYFIN_USER_ID, and there is no username
// and password for it to use. And the key has to travel in the `Authorization`
// header, because Jellyfin 12 ships with legacy `X-Emby-Token` authorization
// disabled by default, which is the exact thing the wrapper this app replaced
// got wrong.
//
// Minting one is the better boundary even if the password worked. The key
// shows up in Jellyfin's own Dashboard -> Security -> API Keys under this
// app's name, so an operator can revoke the agent without rotating the admin
// password, and the admin password stays out of this container's environment.
//
// The account logged into is the one the provider published, never a name this
// configurator assumes. A Jellyfin Bloud booted publishes the managed bootstrap
// account it made; a Jellyfin the operator registered from off-host publishes
// the account they typed into Settings, which is the only way anyone downstream
// can learn it. Hardcoding the local account name here made the remote case fail
// unconditionally: the login 401s against a server whose admin is called
// anything else, and the node never converges.
func (c *Configurator) ensureJellyfinAPIKey(ctx context.Context, server configurator.MediaServerBinding) (string, error) {
	if c.secrets == nil {
		return "", nil
	}
	if server.AdminUsername == "" {
		// Not a "not yet" state. `adminUsername` is a static value the Jellyfin
		// catalog entry declares, so a binding that carries an address and a
		// password and no username is a wiring bug, and naming that is worth
		// more than a 401 that names the wrong thing.
		return "", fmt.Errorf("the %s media server published no admin username, so %s cannot log in to mint a key", server.App, appName)
	}
	if stored := c.secrets.GetAppSecret(appName, jellyfinAPIKeyKey); stored != "" {
		return stored, nil
	}
	key, err := jellyfin.EnsureAPIKey(ctx, c.clients, func() string { return server.LocalURL }, server.AdminUsername, server.AdminPassword, jellyfinKeyName)
	if err != nil {
		return "", err
	}
	if err := c.secrets.SetAppSecret(appName, jellyfinAPIKeyKey, key); err != nil {
		return "", fmt.Errorf("persisting the %s Jellyfin API key: %w", appName, err)
	}
	c.logger.Info("provisioned the jellyfin-mcp Jellyfin API key", "keyName", jellyfinKeyName)
	return key, nil
}

// --- config rendering ---

// renderEnvFile renders the env file the container is created from: one
// KEY=value line per setting, single-quoted. Only values that resolved are
// written, so an unresolved binding is an absent variable rather than an
// empty one, which the server reads differently.
//
// A value containing a quote or a line break is rejected rather than escaped.
// Every value here is generated (a base64url secret) or resolved from catalog
// metadata or the provider (a container URL, a Jellyfin key), so none should
// ever contain one; failing loudly beats writing a file whose quoting no longer
// means what it says.
func renderEnvFile(server configurator.MediaServerBinding, wired bool, apiKey, bearer string) string {
	var b strings.Builder
	b.WriteString("# Generated by Bloud; rewritten on every reconciliation.\n")

	settings := make([][2]string, 0, 3)
	if wired {
		settings = append(settings, [2]string{envJellyfinURL, server.BaseURL})
	}
	if apiKey != "" {
		settings = append(settings, [2]string{envJellyfinAPIKey, apiKey})
	}
	if bearer != "" {
		settings = append(settings, [2]string{envBearerToken, bearer})
	}
	for _, kv := range settings {
		if strings.ContainsAny(kv[1], "'\r\n") {
			// Never echo the value: two of these keys are credentials.
			fmt.Fprintf(&b, "# %s omitted: contains a quote or line break\n", kv[0])
			continue
		}
		fmt.Fprintf(&b, "%s='%s'\n", kv[0], kv[1])
	}
	return b.String()
}
