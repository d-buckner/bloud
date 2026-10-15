// SPDX-License-Identifier: AGPL-3.0-only

package penpot

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// envFileName is the dotenv file the backend node's configurator writes at
// <appDataDir>/config/penpot.env. Every Penpot container in metadata.yaml
// declares it as its envFile: the runtime reads the file on the host and merges
// it into the container environment, which is the same channel Penpot's own
// compose file uses (its services share one env block for exactly these keys).
const envFileName = "penpot.env"

// adminEnvFileName is the second file, and it exists because one file is not
// enough for this image set. The admin console reads PENPOT_ADMIN_CONSOLE_URI
// as *its own public base path*, while the backend and frontend read the same
// variable as *where to dial the console from*. Both readings are correct for
// their own process and mutually exclusive: Penpot's compose gives the variable
// to the backend and frontend services only, and the admin console service
// never sees it. Handing the console the shared file makes it exit(1) at
// startup with "Admin Console must be served under \"/admin-console/\", but
// PENPOT_ADMIN_CONSOLE_URI puts it at \"/\"", and because the backend's profile
// RPC calls the console, a dead console takes the whole app down: every
// request, including the login page's get-profile, answers
// 503 {:type :nitrate-unavailable}.
const adminEnvFileName = "penpot-admin-console.env"

// adminEnvKeys is the admin console's share of the settings, which is exactly
// what Penpot's own compose passes to that service: the public URI, the signing
// key it derives the backend's shared secret from, and the internal address it
// proxies the app RPC through. The database credentials are not here; metadata
// declares them alongside every other container that talks to Postgres.
var adminEnvKeys = []string{"PENPOT_PUBLIC_URI", "PENPOT_SECRET_KEY", "PENPOT_INTERNAL_URI"}

// containerURIs are the addresses Penpot's images use to find each other. They
// are the image defaults with Penpot's compose service names replaced by Bloud's
// container names, and they are written here rather than in metadata.yaml
// because the frontend's entrypoint only substitutes them when unset, so a
// missing one silently points the app at a container named "penpot-backend".
var containerURIs = [][2]string{
	{"PENPOT_BACKEND_URI", "http://" + backendNode + ":6060"},
	{"PENPOT_EXPORTER_URI", "http://" + exporterNode + ":6061"},
	{"PENPOT_MCP_URI", "http://" + mcpNode + ":4401"},
	{"PENPOT_MCP_URI_WS", "http://" + mcpNode + ":4402"},
	{"PENPOT_ADMIN_CONSOLE_URI", "http://" + adminConsoleNode + ":3000"},
	// The exporter drives a headless browser against the frontend, and the
	// frontend is what proxies /api: the internal address is the routed app
	// origin's container-side equivalent.
	{"PENPOT_INTERNAL_URI", "http://" + frontendNode + ":8080"},
	{"PENPOT_REDIS_URI", "redis://" + valkeyNode + "/0"},
}

// oidcScopes is what Penpot asks the provider for. "email" is not optional
// here: Penpot reads the identity out of the email claim and refuses to link an
// OIDC sign-in to a profile whose address it cannot see.
const oidcScopes = "openid email profile"

// publicSettings is everything the renderer needs that metadata.yaml cannot
// state: the address the browser uses, the install's signing key, and the OIDC
// client Bloud registered for this install.
type publicSettings struct {
	publicURL string
	secretKey string
	oidc      *configurator.OIDCOutput
}

// renderEnv renders penpot.env.
//
// The flags are the part worth reading. Penpot's defaults are a public
// registration form and email verification through SMTP, and a home install has
// neither an open-signup policy nor a mail server:
//
//   - disable-registration closes the password form, so the identity provider
//     is the only way in.
//   - enable-oidc-registration is what lets a first SSO sign-in create the
//     Penpot profile from the provider's claims. Penpot checks the two flags
//     independently (auth/oidc: registration OR oidc-registration), so closing
//     the password form does not close the SSO one.
//   - disable-email-verification stands in for the SMTP server Bloud does not
//     run; the provider already asserted the address is real, which is the
//     claim verification would have re-made.
//   - enable-mcp and enable-admin-console are what make the MCP server and the
//     admin console reachable through the frontend at all: the frontend's
//     entrypoint installs or removes those nginx location blocks by grepping
//     this string, so the containers existing is not enough.
//   - disable-onboarding drops the modals Penpot shows right after a profile is
//     created. :onboarding is in Penpot's default flag set, and what comes with
//     it is a first-run survey and a prompt to create a team (the profile props
//     onboarding-questions and onboarding-team-id exist for them). On Bloud the
//     first sign-in already came from an identity provider the operator wired up
//     in Settings, so those modals narrate a first run nobody is having.
//
// Secure session cookies are deliberately not set here. Penpot's own config
// adds disable-secure-session-cookies whenever the public URI is plain http on a
// non-localhost host, which is exactly Bloud's dev/default shape, and a second
// copy of that rule here would be a second place to be wrong.
func renderEnv(s publicSettings) (string, error) {
	return renderEnvFile(envSettings(s))
}

// renderAdminEnv renders the admin console's file: the same signing key and
// addresses, minus every PENPOT_*_URI that means "dial this from outside".
func renderAdminEnv(s publicSettings) (string, error) {
	all := envSettings(s)
	picked := make([][2]string, 0, len(adminEnvKeys))
	for _, want := range adminEnvKeys {
		for _, kv := range all {
			if kv[0] == want {
				picked = append(picked, kv)
			}
		}
	}
	return renderEnvFile(picked)
}

// envSettings is the whole rendered option list, before it becomes text. Both
// files are cut from this one slice so a value they share (the signing key most
// of all, since the console derives the backend's shared secret from it) cannot
// drift between them.
func envSettings(s publicSettings) [][2]string {
	settings := [][2]string{
		{"PENPOT_FLAGS", "disable-email-verification disable-registration " +
			"enable-oidc-registration enable-login-with-oidc enable-mcp enable-admin-console " +
			"disable-onboarding"},
		{"PENPOT_PUBLIC_URI", s.publicURL},
		// The signing key for sessions and for the tokens Penpot mints. It is
		// read back out of the previous file rather than regenerated (see
		// secretKey), so every container in the install shares one.
		{"PENPOT_SECRET_KEY", s.secretKey},
		{"PENPOT_OBJECTS_STORAGE_BACKEND", "fs"},
		{"PENPOT_OBJECTS_STORAGE_FS_DIRECTORY", "/opt/data/assets"},
		// Bloud does not phone home from an app it installed.
		{"PENPOT_TELEMETRY_ENABLED", "false"},
		// The image's nginx listens on [::]:8080 by default. A podman network
		// without an IPv6 subnet can refuse that bind, and the failure is an
		// nginx that never starts rather than a degraded feature.
		{"PENPOT_DISABLE_IPV6_LISTEN", "true"},
	}
	settings = append(settings, containerURIs...)

	if s.oidc != nil {
		settings = append(settings,
			[2]string{"PENPOT_OIDC_NAME", "Bloud SSO"},
			// Penpot joins ".well-known/openid-configuration" onto this, so it
			// takes the provider's issuer, not its origin.
			[2]string{"PENPOT_OIDC_BASE_URI", s.oidc.IssuerURL},
			[2]string{"PENPOT_OIDC_CLIENT_ID", s.oidc.ClientID},
			[2]string{"PENPOT_OIDC_CLIENT_SECRET", s.oidc.ClientSecret},
			[2]string{"PENPOT_OIDC_SCOPES", oidcScopes},
			[2]string{"PENPOT_OIDC_EMAIL_ATTR", "email"},
			[2]string{"PENPOT_OIDC_NAME_ATTR", "name"},
		)
		// Penpot applies its SSRF guard to the discovery, token, and userinfo
		// fetches it makes server-side, and that guard refuses private and
		// loopback addresses. Bloud's issuer is loopback by design on a
		// localhost install (sso.localhost resolves to the host), so without
		// this the provider is configured and unreachable at the same time.
		// Naming exactly the issuer's host keeps the bypass as narrow as the
		// dependency: every other address Penpot may be asked to fetch is
		// still checked.
		if host := hostOf(s.oidc.IssuerURL); host != "" {
			settings = append(settings, [2]string{"PENPOT_SSRF_ALLOWED_HOSTS", host})
		}
	}
	return settings
}

func renderEnvFile(settings [][2]string) (string, error) {
	var b strings.Builder
	b.WriteString("# Generated by Bloud; the host-agent rewrites this file on every\n")
	b.WriteString("# reconciliation and recreates the containers when its content changes.\n")
	for _, kv := range settings {
		if strings.ContainsAny(kv[1], "'\"\n") {
			return "", fmt.Errorf("env value for %s contains a quote or newline", kv[0])
		}
		fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
	}
	return b.String(), nil
}

// secretKey returns the install's PENPOT_SECRET_KEY: the one already in the
// file, or a new one when the file does not exist yet.
//
// It is read back from the file rather than from the secrets store because the
// key is not Bloud's to hand to anyone: it signs Penpot's own sessions and the
// tokens Penpot mints, and every container that must agree about it already
// reads this file. Rotating it invalidates every Penpot session, so it changes
// only when the app is reinstalled.
func secretKey(existing []byte) (string, error) {
	if key := envValue(existing, "PENPOT_SECRET_KEY"); key != "" {
		return key, nil
	}
	buf := make([]byte, 48)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate penpot secret key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// envValue reads one key out of an already-rendered env file. Missing file and
// missing key are the same answer: the caller decides whether that is a first
// boot or a fault.
func envValue(content []byte, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		return strings.Trim(strings.TrimPrefix(line, prefix), `"'`)
	}
	return ""
}

// hostOf returns the hostname of a URL, or "" when it cannot be parsed.
func hostOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}
