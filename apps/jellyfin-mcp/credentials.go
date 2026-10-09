// SPDX-License-Identifier: AGPL-3.0-only

package jellyfinmcp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// --- inbound bearer ---

// ensureVerificationSecret returns the HMAC key this app's listener verifies
// inbound bearers against, generating and persisting one on the first pass and
// reusing it afterwards.
//
// It is stored as a private app secret rather than passed straight into the
// token: the key must outlive the token it signs, because regenerating the key
// invalidates every token already handed out, and a harness holds the token it
// registered with for as long as the namespace exists.
func (c *Configurator) ensureVerificationSecret() (string, error) {
	if c.secrets == nil {
		return "", nil
	}
	if existing := c.secrets.GetAppSecret(appName, verificationSecretKey); existing != "" {
		return existing, nil
	}
	secret, err := randomSecret()
	if err != nil {
		return "", fmt.Errorf("generating the %s verification secret: %w", appName, err)
	}
	if err := c.secrets.SetAppSecret(appName, verificationSecretKey, secret); err != nil {
		return "", fmt.Errorf("persisting the %s verification secret: %w", appName, err)
	}
	c.logger.Info("generated the jellyfin-mcp inbound verification secret")
	return secret, nil
}

// ensureInboundToken returns the bearer a harness presents, minting and
// publishing one on the first pass and reusing it afterwards.
//
// The token is a JWT because that is the only shape this image's `jwt` auth
// mode accepts, and it carries no expiry. A short-lived token would need a
// minter next to it: the harness caches the bearer it registered with, so an
// expiring credential means a namespace that works until the day it silently
// 401s. What Bloud has instead of expiry is rotation: clear `httpToken` and
// `jwtVerificationSecret` from the secrets store and the next pass mints a
// fresh pair, which invalidates the old bearer at the listener.
func (c *Configurator) ensureInboundToken(secret string) (string, error) {
	if c.secrets == nil {
		return "", nil
	}
	if existing := c.secrets.GetAppSecret(appName, httpTokenKey); existing != "" {
		return existing, nil
	}
	if secret == "" {
		return "", fmt.Errorf("cannot mint the %s bearer without a verification secret", appName)
	}
	token, err := mintToken(secret, jwtIssuer, jwtAudience, "bloud:"+appName)
	if err != nil {
		return "", fmt.Errorf("minting the %s bearer: %w", appName, err)
	}
	if err := c.secrets.SetAppSecret(appName, httpTokenKey, token); err != nil {
		return "", fmt.Errorf("publishing the %s bearer: %w", appName, err)
	}
	c.logger.Info("minted the jellyfin-mcp MCP bearer")
	return token, nil
}

// mintToken builds an HS256 JWT: the standard base64url header, payload, and
// signature. Written against the encoding rather than a JWT library because
// the whole requirement is one HMAC over two strings, and a dependency that
// does it would still have to be told not to set an expiry.
func mintToken(secret, issuer, audience, subject string) (string, error) {
	header, err := encodeJSONSegment(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := encodeJSONSegment(map[string]any{
		"iss": issuer,
		"aud": audience,
		"sub": subject,
		"iat": time.Now().UTC().Unix(),
	})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + payload
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte(unsigned)); err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// encodeJSONSegment renders a JSON object as a base64url segment with the
// padding stripped, which is what a JWT segment is.
func encodeJSONSegment(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encoding JWT segment: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
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
// A key rather than the bootstrap admin password, because the image cannot
// use a password at all: its client sets `X-Emby-Token` from JELLYFIN_API_KEY
// and probes /System/Info, and JELLYFIN_USERNAME/JELLYFIN_PASSWORD are read
// into fields nothing logs in with. Measured against the pinned image: a
// credentials-only config fails every tool call with "Jellyfin authentication
// failed". So the key is not a preference, it is the only credential shape
// this wrapper accepts.
//
// Minting one is also the better boundary even if the password worked. The key
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
	jf := newJellyfinClient(c.providerClient(server.ProviderRef))
	key, err := jf.ensureAPIKey(ctx, server.AdminUsername, server.AdminPassword, jellyfinKeyName)
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
// Every value here is generated (a base64url secret, a JWT) or resolved from
// catalog metadata (a container URL), so none should ever contain one;
// failing loudly beats writing a file whose quoting no longer means what it
// says.
func renderEnvFile(server configurator.MediaServerBinding, wired bool, apiKey, secret string) string {
	var b strings.Builder
	b.WriteString("# Generated by Bloud; rewritten on every reconciliation.\n")

	settings := make([][2]string, 0, 3)
	if wired {
		settings = append(settings, [2]string{envJellyfinURL, server.BaseURL})
	}
	if apiKey != "" {
		settings = append(settings, [2]string{envJellyfinAPIKey, apiKey})
	}
	if secret != "" {
		settings = append(settings, [2]string{envVerificationSecret, secret})
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
