// SPDX-License-Identifier: AGPL-3.0-only

package arrmcp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/scrypt"
)

const (
	// The secrets-store keys this app owns. httpToken and the UI password are
	// published (under provides.mcp.secrets and provides.clientPassword.secrets);
	// the Jellyfin API key is private, which keeps it out of any consumer's
	// binding.
	httpTokenKey      = "httpToken"
	clientPasswordKey = "password"
	jellyfinAPIKeyKey = "jellyfinApiKey"

	// scrypt parameters arr-mcp's own password verifier expects. They match the
	// image's hashPassword exactly: Node's crypto.scrypt with N=16384 (the
	// image's SCRYPT_N), r=8, p=1, a 64-byte key, and the result rendered as
	// `scrypt$<saltHex>$<hashHex>`.
	scryptN      = 1 << 14
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 64
)

// ensureInboundToken returns the bearer a harness presents, generating and
// publishing one on the first pass and reusing it afterwards. The same shape as
// apps/jellyfin-mcp: an opaque string the listener checks by exact match.
func (c *Configurator) ensureInboundToken() (string, error) {
	if c.secrets == nil {
		return "", nil
	}
	if existing := c.secrets.GetAppSecret(appName, httpTokenKey); existing != "" {
		return existing, nil
	}
	token, err := randomSecret(32)
	if err != nil {
		return "", fmt.Errorf("generating the %s MCP bearer: %w", appName, err)
	}
	if err := c.secrets.SetAppSecret(appName, httpTokenKey, token); err != nil {
		return "", fmt.Errorf("publishing the %s bearer: %w", appName, err)
	}
	c.logger.Info("generated the arr-mcp MCP bearer")
	return token, nil
}

// ensureUIPassword returns the config UI password, minting one on the first
// pass and publishing it under the clientPassword contract so the operator can
// reveal it. The config file carries only the scrypt hash of it, never the
// plaintext.
func (c *Configurator) ensureUIPassword() (string, error) {
	if c.secrets == nil {
		return "", nil
	}
	if existing := c.secrets.GetAppSecret(appName, clientPasswordKey); existing != "" {
		return existing, nil
	}
	password, err := randomSecret(24)
	if err != nil {
		return "", fmt.Errorf("generating the %s config UI password: %w", appName, err)
	}
	if err := c.secrets.SetAppSecret(appName, clientPasswordKey, password); err != nil {
		return "", fmt.Errorf("publishing the %s config UI password: %w", appName, err)
	}
	c.logger.Info("minted the arr-mcp config UI password")
	return password, nil
}

// randomSecret returns n bytes of entropy as URL-safe base64.
func randomSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// scryptHash renders a password in the exact format arr-mcp's verifier reads:
// `scrypt$<saltHex>$<hashHex>`. The salt is derived from the password rather
// than random, so the same password yields the same hash: a random salt would
// rewrite the config file on every reconciliation, which breaks the steady-state
// rule that a resync is a read-only diff. The passwords here are minted
// high-entropy secrets, so the usual precomputation cost of a deterministic
// salt does not apply.
func scryptHash(password string) (string, error) {
	digest := sha256.Sum256([]byte(password))
	salt := digest[:16]
	key, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("scrypt$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

// sha256TokenHash renders a bearer in the hash form arr-mcp's config takes:
// `sha256:<64 hex>` of the plaintext. The server hashes the bearer it is given
// and compares, so the plaintext never lands in the config file.
func sha256TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])
}
