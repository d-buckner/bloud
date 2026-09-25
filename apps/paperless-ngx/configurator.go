// SPDX-License-Identifier: AGPL-3.0-only

// Package paperless configures Paperless-ngx: it generates the app's
// configuration file before the container starts (public URL, secret key,
// internal admin, OIDC client) and verifies after start that the running app
// actually picked it up.
package paperlessngx

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const appName = "paperless-ngx"

// providerID names the OpenID Connect provider inside Paperless-ngx. It is the
// middle segment of both the callback path registered with the identity
// provider and the sign-in URL the login page advertises, so all three must
// agree: this constant, callbackPath, and metadata.yaml's sso.callbackPath.
const providerID = "bloud"

// providerName labels the sign-in button on the Paperless-ngx login page.
const providerName = "Bloud SSO"

// callbackPath is the OIDC redirect URI path. Paperless-ngx derives it from
// the provider id (django-allauth's openid_connect URL layout), so it cannot
// be chosen freely: it must equal metadata.yaml's sso.callbackPath.
const callbackPath = "/accounts/oidc/" + providerID + "/login/callback/"

// signInPath is the app's sign-in page: the page users land on and the page
// the SSO button is rendered on.
const signInPath = "/accounts/login/"

// providerLoginPath is the allauth view that starts the authorization-code
// flow; PostStart probes it to prove the provider resolves end to end.
const providerLoginPath = "/accounts/oidc/" + providerID + "/login/"

// signupPath is the allauth signup form. Paperless-ngx exposes it only while
// no user exists, and the account it creates is promoted to superuser: it is
// the app's own first-run flow, and the only way to obtain the instance's
// first account (there is no admin API).
const signupPath = "/accounts/signup/"

// baselineGroup is the Paperless-ngx group every SSO account is created into.
// Paperless-ngx grants new users no permissions of their own ("By default, new
// users are not granted any permissions"), while its REST API is guarded by
// model permissions, so an account with none cannot load the web app at all:
// the dashboard's first calls, GET /api/ui_settings/ and GET /api/saved_views/,
// answer 403 and the app is unusable. PostStart declares the group (creating it
// or restoring its declared permission set) and
// PAPERLESS_SOCIAL_ACCOUNT_DEFAULT_GROUPS adds every social signup to it.
const baselineGroup = "bloud-users"

// adminGroupClaim is the Authentik group whose members become Paperless-ngx
// superusers, via PAPERLESS_SOCIAL_ACCOUNT_SYNC_SUPERUSER_GROUP: Authentik's
// profile scope carries the `groups` claim, which allauth exposes to the app as
// the social account's extra data, and the app syncs superuser status from it on
// every login. It is the same membership that grants admin in Jellyfin
// (apps/jellyfin's LDAP admin filter) and Bloud's own admin role, so app admin
// rights follow the instance's role boundary instead of being granted per app.
const adminGroupClaim = "authentik Admins"

// adminUser is the internal-only Django superuser the container creates on
// first start from PAPERLESS_ADMIN_USER/PAPERLESS_ADMIN_PASSWORD. It gives the
// Django admin and the REST API a way in that does not depend on the identity
// provider; end users sign in through SSO and never use it. The email must
// carry a TLD (paperless-ngx.localhost is the app's own unroutable domain) so it
// is a valid address for password resets and notification mail.
const (
	adminUser  = "bloud-admin"
	adminEmail = "bloud-admin@paperless-ngx.localhost"
)

// confFileName is the configuration file written in PreStart, mounted
// read-only into the webserver at /usr/src/paperless/conf/paperless.conf and
// selected by the PAPERLESS_CONFIGURATION_PATH environment variable declared
// in metadata.yaml.
const confFileName = "paperless.conf"

// oidcApps is the Django app that must be in INSTALLED_APPS for the OIDC
// provider to exist at all (PAPERLESS_APPS is appended to the built-in list).
const oidcApps = "allauth.socialaccount.providers.openid_connect"

// Configurator handles Paperless-ngx configuration.
type Configurator struct {
	port       int
	ssoBaseURL func() string // current Bloud base URL (host-set aware; read on every PreStart)
	secrets    configurator.AppSecretsProvider
	logger     *slog.Logger
	api        *paperlessNgxAPI

	// baseURL is a test seam: when set, the API client resolves to it instead
	// of localhost:port. Never used to build request URLs by hand.
	baseURL string
}

// NewConfigurator creates a Paperless-ngx configurator from the host Deps.
// deps.PrimaryBaseURL supplies the current Bloud base URL (e.g.
// "http://localhost:8080"); the app's public URL is derived from it the same
// way routes and OIDC redirect URIs are (paperless-ngx.<host>). It is a function
// so host changes made in the UI take effect without re-registering.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = 8000
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:       port,
		ssoBaseURL: deps.PrimaryBaseURL,
		secrets:    deps.Secrets,
		logger:     logger.With("app", appName),
	}
	c.api = newAPI(deps.HTTP, func() string {
		if c.baseURL != "" {
			return c.baseURL
		}
		return fmt.Sprintf("http://localhost:%d", c.port)
	})
	return c
}

func (c *Configurator) Name() string {
	return "apps-paperless-ngx"
}

// appExternalURL returns the public URL the browser uses to reach
// Paperless-ngx, e.g. "http://paperless-ngx.localhost:8080". It must match the
// OIDC redirect URI base registered by the host-agent (app subdomain +
// callbackPath) or the browser round-trip fails.
func (c *Configurator) appExternalURL() string {
	return configurator.AppExternalURL(c.ssoBaseURL, appName)
}

// PreStart writes the Paperless-ngx configuration file so the container comes
// up with the right public URL, secret key, admin account, and OIDC provider
// on its very first boot. Returns configChanged=true when the file content
// changed so the orchestrator recreates the container.
//
// The secret key is generated once and read back from the file on later runs:
// Paperless-ngx signs sessions and API tokens with it, so a fresh value on
// every reconciliation would sign every user out.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	dir := filepath.Join(state.DataPath, "config")
	path := filepath.Join(dir, confFileName)

	existing, _ := os.ReadFile(path)
	secretKey := confValue(string(existing), "PAPERLESS_SECRET_KEY")
	if secretKey == "" {
		generated, err := newSecretKey()
		if err != nil {
			return configurator.NoRestart(), fmt.Errorf("generating secret key: %w", err)
		}
		secretKey = generated
	}

	content, err := renderConf(publicSettings{
		publicURL: c.appExternalURL(),
		secretKey: secretKey,
		oidc:      state.OIDC,
	})
	if err != nil {
		return configurator.NoRestart(), err
	}

	// The file is read by the webserver process, which runs as the image's
	// unprivileged paperless user. Under rootless podman that user is a
	// subuid, not the host user that writes this file, so 0600 would be
	// unreadable (see INTEGRATION.md).
	changed, err := managedfile.Write(path, []byte(content), 0644)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing config file: %w", err)
	}
	if changed {
		c.logger.Info("wrote Paperless-ngx config file", "path", path, "sso", state.OIDC != nil)
	}
	return configurator.RestartIf(changed, "Paperless-ngx config rewritten"), nil
}

// PostStart verifies against the running app that the configuration took
// effect and that the instance has its internal admin account. Called on every
// reconciliation, so every check is a read or an idempotent probe.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if err := c.api.waitServer(ctx); err != nil {
		return fmt.Errorf("waiting for paperless webserver: %w", err)
	}

	adminToken, err := c.ensureBootstrapAdmin(ctx)
	if err != nil {
		return err
	}

	if state.OIDC == nil {
		return nil
	}
	if err := c.ensureBaselineGroup(ctx, adminToken); err != nil {
		return fmt.Errorf("declaring the SSO baseline group: %w", err)
	}
	if err := c.api.waitProviderAdvertised(ctx); err != nil {
		return fmt.Errorf("verifying OIDC provider on the sign-in page: %w", err)
	}
	if err := c.api.probeProviderLogin(ctx); err != nil {
		return fmt.Errorf("verifying OIDC authorization redirect: %w", err)
	}
	c.logger.Info("OIDC sign-in verified", "issuer", state.OIDC.IssuerURL)
	return nil
}

// ensureBootstrapAdmin makes sure the instance has its internal admin account
// and returns that account's API token, which later steps use to declare
// instance state (the SSO baseline group). An empty token with a nil error
// means the credential could not be proven (no secrets provider, or the token
// endpoint rejected or throttled the check), which callers report rather than
// fail on: the next reconciliation re-checks.
//
// Paperless-ngx has no admin API: the account that owns the instance is the one
// its signup form creates, which the account adapter promotes to superuser
// while no user exists. Until such an account exists the sign-in page forwards
// visitors to the (closed) signup page, so the SSO entry point would be
// unreachable. Bloud therefore drives that form once per fresh install with the
// password from the secrets provider; every later pass finds signup closed and
// only verifies that the account still authenticates.
func (c *Configurator) ensureBootstrapAdmin(ctx context.Context) (string, error) {
	password, err := c.adminPassword()
	if err != nil {
		return "", err
	}
	if password == "" {
		c.logger.Warn("admin bootstrap skipped: no secrets provider")
		return "", nil
	}

	open, err := c.api.signupOpen(ctx)
	if err != nil {
		return "", fmt.Errorf("checking the signup state: %w", err)
	}
	if open {
		if err := c.api.signup(ctx, adminUser, adminEmail, password); err != nil {
			return "", fmt.Errorf("creating the internal admin account: %w", err)
		}
		c.logger.Info("internal admin account created", "user", adminUser)
	}

	token, err := c.api.login(ctx, adminUser, password)
	if err != nil {
		return "", fmt.Errorf("verifying internal admin account: %w", err)
	}
	if token == "" {
		// The credentials were rejected (another account owns the instance) or
		// the token endpoint throttled the check (5/min). Neither makes the app
		// unusable for SSO users, so it is reported rather than failed, and the
		// next reconciliation re-checks.
		c.logger.Warn("internal admin account not verified", "user", adminUser)
		return "", nil
	}
	c.logger.Info("internal admin account verified", "user", adminUser)
	return token, nil
}

// ensureBaselineGroup declares the group every SSO account is created into, so
// that a signed-in user can actually use the app. Paperless-ngx grants new
// users nothing and gates every REST endpoint on model permissions, so without
// this the dashboard's own requests answer 403 (see baselineGroup).
//
// An empty adminToken means the internal admin could not authenticate; the
// group is then reported as undeclared rather than failed, matching how an
// unverified admin is treated, and the next reconciliation retries.
func (c *Configurator) ensureBaselineGroup(ctx context.Context, adminToken string) error {
	if adminToken == "" {
		c.logger.Warn("baseline group not declared: no internal admin API token")
		return nil
	}
	permissions := baselinePermissions()
	changed, err := c.api.ensureGroup(ctx, adminToken, baselineGroup, permissions)
	if err != nil {
		return err
	}
	if changed {
		c.logger.Info("declared the SSO baseline group", "group", baselineGroup, "permissions", len(permissions))
	}
	return nil
}

// baselinePermissions is the declared permission set of baselineGroup, named by
// Django permission codename. It is the whole document domain: every verb on
// every model of the documents and paperless_mail apps, which is the set
// Paperless-ngx's own "Global permissions" table describes for a user who works
// with documents, including UISettings (which the web app requires at least
// view on). It adds read-only access to the instance configuration and
// statistics the app renders. Instance administration (changing Application
// Configuration, managing users and groups) is deliberately absent: that is
// what the adminGroupClaim superuser mapping is for.
//
// The image is pinned in metadata.yaml, so an image bump is where this list is
// reviewed: a model added upstream needs its codenames added here.
func baselinePermissions() []string {
	models := []string{
		"correspondent",
		"customfield",
		"customfieldinstance",
		"document",
		"documenttype",
		"mailaccount",
		"mailrule",
		"note",
		"paperlesstask",
		"processedmail",
		"savedview",
		"savedviewfilterrule",
		"sharelink",
		"sharelinkbundle",
		"storagepath",
		"tag",
		"uisettings",
		"workflow",
		"workflowaction",
		"workflowactionemail",
		"workflowactionwebhook",
		"workflowrun",
		"workflowtrigger",
	}
	permissions := make([]string, 0, len(models)*4+2)
	for _, model := range models {
		// Every model carries all four; the API rejects a codename that does
		// not exist, so a list that drifts from the app fails loudly here
		// rather than leaving users without access.
		for _, verb := range []string{"add", "change", "delete", "view"} {
			permissions = append(permissions, verb+"_"+model)
		}
	}
	return append(permissions,
		"view_applicationconfiguration",
		"view_global_statistics",
	)
}

// adminPassword returns the internal admin password, generating and persisting
// it on first use. Empty with a nil error means no secrets provider is wired
// (degraded CLI contexts), which callers report rather than treat as a value.
func (c *Configurator) adminPassword() (string, error) {
	if c.secrets == nil {
		return "", nil
	}
	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		return "", fmt.Errorf("generating admin password: %w", err)
	}
	return password, nil
}

// publicSettings are the per-install values the generated config file carries.
type publicSettings struct {
	publicURL string
	secretKey string
	oidc      *configurator.OIDCOutput
}

// renderConf renders paperless.conf in python-dotenv syntax: Paperless-ngx
// loads the file with load_dotenv before reading its settings, and real
// environment variables win over it, so the manifest keeps the static
// connection settings and this file carries only what depends on the install.
//
// Values are single-quoted, which python-dotenv reads verbatim (no escape
// processing), so JSON and URLs survive unmangled.
func renderConf(s publicSettings) (string, error) {
	settings := [][2]string{
		{"PAPERLESS_URL", s.publicURL},
		// allauth builds the OIDC redirect URI with this protocol, and it has
		// to equal the URL the browser uses, which is the one Bloud registers
		// with the identity provider. allauth defaults to https; Bloud serves
		// plain HTTP until the port-80/TLS story lands.
		{"PAPERLESS_ACCOUNT_DEFAULT_HTTP_PROTOCOL", schemeOf(s.publicURL)},
		{"PAPERLESS_SECRET_KEY", s.secretKey},
	}
	if s.oidc != nil {
		providers, err := socialAccountProviders(s.oidc)
		if err != nil {
			return "", err
		}
		settings = append(settings,
			[2]string{"PAPERLESS_APPS", oidcApps},
			[2]string{"PAPERLESS_SOCIALACCOUNT_PROVIDERS", providers},
			[2]string{"PAPERLESS_SOCIAL_AUTO_SIGNUP", "true"},
			[2]string{"PAPERLESS_SOCIALACCOUNT_ALLOW_SIGNUPS", "true"},
			// Every social signup joins the baseline group, so an SSO user
			// reaches the app with the permissions it needs (see
			// baselineGroup); adminGroupClaim's members additionally become
			// superusers.
			[2]string{"PAPERLESS_SOCIAL_ACCOUNT_DEFAULT_GROUPS", baselineGroup},
			[2]string{"PAPERLESS_SOCIAL_ACCOUNT_SYNC_SUPERUSER_GROUP", adminGroupClaim},
			// With the provider wired, the app's own password form is a second
			// credential path Bloud does not manage, and the sign-in is one
			// hop instead of two. Neither setting touches the Django admin
			// login or the API credential login, which is what keeps the
			// internal admin (and this configurator) working; that pair is the
			// break-glass path if the provider is ever unreachable. Signup
			// stays available, so a fresh install still bootstraps its admin
			// account through that form.
			[2]string{"PAPERLESS_DISABLE_REGULAR_LOGIN", "true"},
			[2]string{"PAPERLESS_REDIRECT_LOGIN_TO_SSO", "true"},
			[2]string{"PAPERLESS_LOGOUT_REDIRECT_URL", endpointURL(s.oidc.IssuerURL, "end-session/")},
		)
	}

	var b strings.Builder
	b.WriteString("# Generated by Bloud; the host-agent rewrites this file on every\n")
	b.WriteString("# reconciliation and restarts the container when its content changes.\n")
	for _, kv := range settings {
		if strings.ContainsRune(kv[1], '\'') {
			return "", fmt.Errorf("config value for %s contains a single quote", kv[0])
		}
		fmt.Fprintf(&b, "%s='%s'\n", kv[0], kv[1])
	}
	return b.String(), nil
}

// allauthProviders is the PAPERLESS_SOCIALACCOUNT_PROVIDERS document: the
// django-allauth provider configuration, keyed by provider class name. It is
// modelled as structs rather than a map so json.Marshal orders the fields
// deterministically (an unchanged config never churns the file).
type allauthProviders struct {
	OpenIDConnect allauthOpenIDConnect `json:"openid_connect"`
}

type allauthOpenIDConnect struct {
	PKCEEnabled bool         `json:"OAUTH_PKCE_ENABLED"`
	Apps        []allauthApp `json:"APPS"`
	Scope       []string     `json:"SCOPE"`
}

type allauthApp struct {
	ProviderID string             `json:"provider_id"`
	Name       string             `json:"name"`
	ClientID   string             `json:"client_id"`
	Secret     string             `json:"secret"`
	Settings   allauthAppSettings `json:"settings"`
}

type allauthAppSettings struct {
	ServerURL     string `json:"server_url"`
	FetchUserinfo bool   `json:"fetch_userinfo"`
}

// socialAccountProviders renders the allauth provider document for the
// host-agent's native-oidc provider output.
func socialAccountProviders(oidc *configurator.OIDCOutput) (string, error) {
	doc := allauthProviders{OpenIDConnect: allauthOpenIDConnect{
		PKCEEnabled: true,
		Apps: []allauthApp{{
			ProviderID: providerID,
			Name:       providerName,
			ClientID:   oidc.ClientID,
			Secret:     oidc.ClientSecret,
			Settings: allauthAppSettings{
				// allauth fetches this URL itself to discover the issuer's
				// endpoints, so it must be the discovery document, not the
				// bare issuer.
				ServerURL:     endpointURL(oidc.IssuerURL, ".well-known/openid-configuration"),
				FetchUserinfo: true,
			},
		}},
		Scope: []string{"openid", "profile", "email"},
	}}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("rendering social account providers: %w", err)
	}
	return string(out), nil
}

// schemeOf returns the scheme of a URL, defaulting to http when it cannot be
// parsed (the dev default, matching appExternalURL's fallback).
func schemeOf(rawURL string) string {
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Scheme != "" {
		return parsed.Scheme
	}
	return "http"
}

// endpointURL joins a path onto the issuer URL, tolerating its trailing slash.
func endpointURL(issuer, path string) string {
	return strings.TrimSuffix(issuer, "/") + "/" + path
}

// newSecretKey generates a Django SECRET_KEY: 64 bytes of entropy in URL-safe
// base64, the length upstream recommends.
func newSecretKey() (string, error) {
	buf := make([]byte, 64)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// confValue returns the value of key in a generated config file, unquoted.
// Empty when the key is absent (including when the file does not exist).
func confValue(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `'"`)
	}
	return ""
}
