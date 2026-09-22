// SPDX-License-Identifier: AGPL-3.0-only

// Package servarr holds the config.xml and HTTP-API pieces that Sonarr,
// Radarr and Prowlarr share.
//
// The three apps are forks of one .NET server: each keeps a single <Config>
// XML document (config.xml) next to its database and serves a versioned REST
// API that authenticates with an X-Api-Key header. They differ only in their
// API root ("api/v3" for Sonarr and Radarr, "api/v1" for Prowlarr) and in
// their default port, so both are parameters here and everything else lives
// in this package.
//
// Two upstream behaviours shape the code:
//
//   - config.xml keys are read with exactly-one-occurrence semantics: a key
//     that appears twice is treated as absent, the app then appends its own
//     default, and a file that does not parse makes the app refuse to boot.
//     This package therefore writes single, well-formed keys through
//     pkg/xmlutil and never rewrites the file when the keys it owns already
//     hold the desired values. The app re-serialises config.xml itself when
//     its settings are saved, so a raw byte comparison would report a change
//     on every reconciliation and restart the container in a loop.
//   - AuthenticationMethod=External installs the app's
//     NoAuthenticationHandler: it reads no username header at all and treats
//     every request as already authenticated. AuthenticationRequired must be
//     Enabled alongside it, because AllowedHosts is only validated as
//     non-empty when AuthenticationRequired != Enabled; leaving AllowedHosts
//     empty keeps ASP.NET host filtering off, so the instance never bakes in
//     a public hostname. Traefik's forward-auth middleware is therefore the
//     only gate in front of the app.
package servarr

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/xmlutil"
)

const (
	// rootElement is the single root element of every Servarr config.xml.
	rootElement = "Config"

	// SecretAPIKey is the name under which a Servarr instance publishes its own
	// ApiKey for its consumers (its `provides.secrets` metadata), and the key a
	// consumer reads it back under from an integration binding. It lives here
	// so the provider and its consumers cannot drift apart.
	SecretAPIKey = "apiKey"

	// apiKeyElement holds the instance API key. Servarr generates 32
	// lowercase hex characters and honours whatever is present exactly once
	// verbatim, so Bloud generates the same shape.
	apiKeyElement = "ApiKey"

	// authMethodElement selects the authentication handler. The value
	// External maps to NoAuthenticationHandler: no username header is read
	// and every request is treated as authenticated.
	authMethodElement = "AuthenticationMethod"

	// authRequiredElement must be Enabled alongside External, otherwise the
	// app validates AllowedHosts as non-empty (and we intentionally keep
	// AllowedHosts empty so ASP.NET host filtering stays off).
	authRequiredElement = "AuthenticationRequired"

	// externalAuthMode is the config.xml spelling of the External handler.
	externalAuthMode = "External"

	// authRequiredEnabled is the config.xml spelling of the enabled toggle.
	authRequiredEnabled = "Enabled"

	// apiKeyBytes is the entropy behind a generated key: 16 bytes render as
	// the 32 hex characters Servarr itself generates and accepts.
	apiKeyBytes = 16
)

// APIKey returns the ApiKey element from a Servarr config.xml, or "" when
// unset. A missing file is not an error: it simply has no key yet, which is
// the state before the first PreStart.
func APIKey(configPath string) (string, error) {
	cfg, err := xmlutil.Open(configPath, rootElement)
	if err != nil {
		return "", fmt.Errorf("servarr: read %s: %w", configPath, err)
	}
	return cfg.GetElement(apiKeyElement), nil
}

// EnsureExternalAuth applies Bloud's desired auth state to an instance's
// config.xml: AuthenticationMethod=External, AuthenticationRequired=Enabled,
// and a generated 32-hex ApiKey when the file has none. It preserves every
// other key. Returns whether the file content changed.
func EnsureExternalAuth(configPath string) (bool, error) {
	cfg, err := xmlutil.Open(configPath, rootElement)
	if err != nil {
		return false, fmt.Errorf("servarr: read %s: %w", configPath, err)
	}

	desired := xmlutil.ConfigValues{
		authMethodElement:   externalAuthMode,
		authRequiredElement: authRequiredEnabled,
	}
	if cfg.GetElement(apiKeyElement) == "" {
		key, err := generateAPIKey()
		if err != nil {
			return false, fmt.Errorf("servarr: generate API key: %w", err)
		}
		desired[apiKeyElement] = key
	}

	// HasConfig compares values, not bytes, so an app-side re-serialisation
	// of an already-correct file is not mistaken for drift. Reporting a
	// change here would recreate the container on every reconciliation.
	if cfg.HasConfig(desired) {
		return false, nil
	}

	cfg.ApplyConfig(desired)
	changed, err := cfg.Save()
	if err != nil {
		return false, fmt.Errorf("servarr: write %s: %w", configPath, err)
	}
	return changed, nil
}

// generateAPIKey returns a fresh 32-hex-character API key. Servarr does not
// validate the format, but matching its own generator keeps the value
// indistinguishable from one created in the UI, and the length is what the
// app's own key-display code assumes.
func generateAPIKey() (string, error) {
	buf := make([]byte, apiKeyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
