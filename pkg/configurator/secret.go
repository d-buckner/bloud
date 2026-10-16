// SPDX-License-Identifier: AGPL-3.0-only

package configurator

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// RandomToken returns n bytes of entropy as URL-safe base64. The alphabet
// excludes characters that would break a line-based config file, which is
// where these tokens usually land.
func RandomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// EnsureSecret returns the stored secret for app/key, or mints one of size
// bytes and stores it. A nil provider returns ("", false, nil), matching the
// CLI/test contract where there is no store. minted reports whether this call
// created the secret, so the caller can log the first mint without logging a
// read. label names the credential in the error text.
func EnsureSecret(secrets AppSecretsProvider, app, key string, size int, label string) (secret string, minted bool, err error) {
	if secrets == nil {
		return "", false, nil
	}
	if existing := secrets.GetAppSecret(app, key); existing != "" {
		return existing, false, nil
	}
	token, err := RandomToken(size)
	if err != nil {
		return "", false, fmt.Errorf("generating the %s %s: %w", app, label, err)
	}
	if err := secrets.SetAppSecret(app, key, token); err != nil {
		return "", false, fmt.Errorf("publishing the %s %s: %w", app, label, err)
	}
	return token, true, nil
}
