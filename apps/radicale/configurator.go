// SPDX-License-Identifier: AGPL-3.0-only

// Package radicale configures Radicale: it writes the app's INI configuration
// and its LDAP reader secret before the container starts, and checks after
// start that the running server actually demands credentials.
//
// Radicale takes its whole configuration from an INI file and reads no
// environment variables, so unlike most of the catalog this configurator owns
// the file the container boots from rather than a set of env overrides.
package radicale

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const appName = "radicale"

// nodeName is this app's graph node and container name; see NodeLifecycle.Name.
const nodeName = "apps-radicale"

// defaultPort is the app's own port, the value the constructor uses when
// registration passes 0. It matches metadata.yaml's `port`.
const defaultPort = 5232

const (
	// configFileName is the INI file the container boots from. metadata.yaml
	// mounts the generated config directory at containerConfigDir and passes
	// `--config /config/config`.
	configFileName = "config"

	// ldapSecretFileName holds the LDAP reader password. It is its own file
	// because Radicale offers `ldap_secret_file` for exactly this: the
	// credential stays out of the file that gets read, quoted, and diffed as
	// configuration.
	ldapSecretFileName = "ldap-secret"

	// containerConfigDir is where metadata.yaml mounts the generated files.
	containerConfigDir = "/config"

	// containerStorageDir is the `[storage] filesystem_folder` inside the
	// container. The host side is <appDataDir>/collections.
	containerStorageDir = "/var/lib/radicale/collections"

	// sharesDirName is the sharing database directory under the storage tree,
	// where Radicale's csv sharing backend defaults its database to.
	sharesDirName = "collection-db"

	// sharesFileName is the csv sharing database Radicale reads at startup. It
	// lives in the writable storage tree (not the read-only config dir).
	sharesFileName = "sharing.csv"
)

type Configurator struct {
	port   int
	logger *slog.Logger
	api    *radicaleAPI

	// secrets reads this app's own secret scope. It is how the shared-calendar
	// owner credential, published by the Authentik configurator during its own
	// convergence, reaches the call that creates the family calendar. Nil in
	// CLI and test contexts; the sharing paths treat that as "not yet".
	secrets configurator.AppSecretsProvider

	// restartContainerFn stops and starts the running container through the
	// host runtime, forcing Radicale to re-read its sharing database. Nil in
	// CLI/tests; PostStart treats nil as "cannot apply now".
	restartContainerFn func(ctx context.Context, name string) error

	// baseURL is a test seam: when set, the client resolves to it instead of
	// localhost:port. Never used to build request URLs by hand.
	baseURL string
}

// NewConfigurator creates a Radicale configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:               port,
		logger:             logger.With("app", appName),
		secrets:            deps.Secrets,
		restartContainerFn: deps.RestartContainer,
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
	return nodeName
}

// PreStart lays out the app's data tree and writes the config the container
// boots from. It runs on every reconciliation, so a pass that finds the files
// already correct changes nothing and asks for no recreate.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	configDir := filepath.Join(state.DataPath, "config")
	storageDir := filepath.Join(state.DataPath, "collections")
	for _, dir := range []string{configDir, storageDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return configurator.NoRestart(), fmt.Errorf("create %s: %w", dir, err)
		}
	}

	// The container writes its storage as the image's own `radicale` uid,
	// which under rootless podman is a subordinate uid the host agent is not
	// and cannot become. The host agent therefore cannot hand the directory
	// over by chowning it, so it opens it instead -- the same device used for
	// qbittorrent, prowlarr, and Authentik, whose containers are also not the
	// host user. Nothing is stranded by the ownership the container takes on:
	// teardown goes back through the namespace, not through this user.
	if err := managedfile.EnsureWritable(storageDir, 0o777); err != nil {
		return configurator.NoRestart(), fmt.Errorf("open %s for the container user: %w", storageDir, err)
	}

	secretChanged, err := c.syncLdapSecret(filepath.Join(configDir, ldapSecretFileName), state.LDAP)
	if err != nil {
		return configurator.NoRestart(), err
	}

	cfgPath := filepath.Join(configDir, configFileName)
	desired := renderConfig(c.port, state.LDAP)
	// ModeSharedConfig, not ModeHostOnly: the reader is the container process
	// running as the image's `radicale` uid, which is not the host uid that
	// wrote the file. 0600 would leave the app unable to read the config it
	// boots from. The secret that sits beside it is bounded by this directory,
	// which is the app's own data tree.
	cfgChanged, err := managedfile.Write(cfgPath, []byte(desired), managedfile.ModeSharedConfig)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("write %s: %w", cfgPath, err)
	}

	plan := c.planFor(ctx, state)
	sharesChanged, err := c.syncShares(state.DataPath, plan)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("write %s: %w", sharesFileName, err)
	}

	if !cfgChanged && !secretChanged && !sharesChanged {
		return configurator.NoRestart(), nil
	}

	c.logger.Info("wrote Radicale config", "path", cfgPath,
		"auth", authType(state.LDAP), "secretChanged", secretChanged,
		"sharesChanged", sharesChanged)
	return configurator.MustRestart("Radicale config rewritten"), nil
}

// feedsOf reads the resolved icsFeed bindings off the app state, tolerating
// the nil state a bare test pass hands over.
func feedsOf(state *configurator.AppState) []configurator.ICSFeedBinding {
	if state == nil {
		return nil
	}
	return state.Integrations.ICSFeeds
}

// planFor reads the live directory and feed bindings and builds the set of
// calendars the instance should have right now.
//
// Both halves of the reconcile read this one value: the share renderer walks
// its grants, the creator walks its segments. That is the point of the plan --
// the two halves cannot drift apart into disagreeing sets the way a constant and
// a contract can.
func (c *Configurator) planFor(ctx context.Context, state *configurator.AppState) Plan {
	users, enumerated := c.calendarRecipients(ctx, state)
	var dataPath string
	if state != nil {
		dataPath = state.DataPath
	}
	return planCalendars(users, feedsOf(state), enumerated, c.occupiedMounts(dataPath, users))
}

// occupiedMounts reports which of the plan's own-tree mounts are already taken
// by a collection the recipient made themselves.
//
// It reads the storage tree on disk rather than asking the server, because
// nobody can ask the server. Bloud cannot authenticate as a user over DAV --
// LDAP verifies credentials and never reveals them -- and the `owner_only`
// rights backend keeps every principal's tree sealed to that principal. The
// storage tree is the one place Bloud can see what is actually there.
//
// The mapping is direct: `pathutils.path_to_filesystem` joins the sane path
// onto the storage root with no encoding, so `<storage>/bob/Personal/` is bob's
// own collection at `/bob/Personal/`. A directory only counts when it carries
// `.Radicale.props`, the marker the server writes for a real collection, since
// a stray directory with the same name shadows nothing.
func (c *Configurator) occupiedMounts(dataPath string, users []DirectoryUser) map[string]bool {
	occupied := make(map[string]bool, len(users))
	if dataPath == "" {
		return occupied
	}
	root := filepath.Join(dataPath, "collections")
	for _, u := range users {
		marker := filepath.Join(root, u.Username, personalMount, ".Radicale.props")
		if _, err := os.Stat(marker); err == nil {
			occupied[occupiedKey(u.Username, personalMount)] = true
		}
	}
	return occupied
}

// syncShares renders the csv sharing database and writes it into the writable
// storage tree. Bloud is the single writer: Radicale reads it at startup and
// never writes it back unless its own sharing API is used, which Bloud does not
// call. A change needs a restart, exactly like the feed jobs and the rights.
//
// When the plan is not Complete -- the user list could not be read -- the file
// already on disk is left exactly as it is and the call reports no change.
// Rendering an empty list because the identity provider was briefly unreachable
// would take every shared calendar away from every user, and a transport error
// says nothing about who should hold a share. A file that does not exist yet is
// still written, header-only, so the sharing backend always has a database to
// read rather than a missing one.
func (c *Configurator) syncShares(dataPath string, plan Plan) (bool, error) {
	path := filepath.Join(dataPath, "collections", sharesDirName, sharesFileName)
	if !plan.Complete {
		if _, err := os.Stat(path); err == nil {
			c.logger.Warn("radicale sharing: user list unavailable, keeping the shares already on disk")
			return false, nil
		}
	}
	return managedfile.Write(path, []byte(renderShares(plan)), managedfile.ModeSharedConfig)
}

// sharesCSVHeader is the semicolon-delimited header Radicale's csv sharing
// backend writes and reads. The field order and names are pinned by
// radicale/sharing/__init__.py DB_FIELDS_V1.
const sharesCSVHeader = "ShareType;PathOrToken;PathMapped;Conversion;Owner;User;Permissions;EnabledByOwner;EnabledByUser;HiddenByOwner;HiddenByUser;TimestampCreated;TimestampUpdated;Properties;Actions"

// feedURL composes the URL the sync dials: the provider's container address
// plus the declared path, with the key as a query parameter. The Servarr feed
// endpoint accepts no header auth, which is why the key travels in the URL.
func feedURL(feed configurator.ICSFeedBinding) string {
	u := strings.TrimSuffix(feed.BaseURL, "/") + feed.Path
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "apikey=" + url.QueryEscape(feed.APIKey)
}

// resyncGeneratedConfig re-renders the sharing database against the live
// bindings and restarts the container when it changed. The shares depend on
// which users exist and which feeds are installed, and Radicale reads them at
// process start, so a change needs a restart. Returns true when the container
// was restarted, in which case the caller should skip probing it this pass.
func (c *Configurator) resyncGeneratedConfig(ctx context.Context, dataPath string, plan Plan) (bool, error) {
	if dataPath == "" {
		return false, nil
	}
	sharesChanged, err := c.syncShares(dataPath, plan)
	if err != nil {
		return false, fmt.Errorf("write %s: %w", sharesFileName, err)
	}
	if !sharesChanged {
		return false, nil
	}
	c.logger.Info("generated config changed; restarting Radicale to apply",
		"sharesChanged", sharesChanged)
	if err := c.restartContainer(ctx); err != nil {
		return false, fmt.Errorf("config rewritten but the container could not be restarted: %w", err)
	}
	return true, nil
}

// PostStart does two jobs. First it re-renders the feed sync jobs against the
// live bindings and restarts the container when they changed: the jobs depend
// on which feed providers (Radarr, Sonarr) are installed, the vendored plugin
// reads its config only at process start, and the orchestrator's PostStart-only
// resync is the only hook a RUNNING node gets after a provider is installed or
// removed. Then it checks the running server for the one property that matters:
// an unauthenticated request for a collection is refused. That is a behavioral
// assertion about the process, not a re-read of the file that was written, so
// it catches a container that came up on a stale or ignored config.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	plan := c.planFor(ctx, state)

	// A bare test pass hands over a nil state; the resync is the only part that
	// needs a data path, and the calendar pass needs neither.
	var dataPath string
	if state != nil {
		dataPath = state.DataPath
	}

	restarted, err := c.resyncGeneratedConfig(ctx, dataPath, plan)
	if err != nil {
		return err
	}
	if restarted {
		// The server is restarting; probing it now would race. The next
		// reconciliation pass re-probes once it is serving again.
		return nil
	}

	// Every collection the plan calls for is created here, the family calendar
	// and the feed collections alike. They have to be created over DAV by the
	// account that owns them, against the running server, which makes this the
	// one point in the lifecycle where they can be. It is idempotent, it warns
	// rather than fails, and one calendar failing does not stop the others.
	if err := c.ensureCalendars(ctx, plan); err != nil {
		return err
	}

	status, err := c.api.probeUnauthenticated(ctx)
	if err != nil {
		// Liveness is the health check's job, and PostStart runs after it
		// passes; a failure here is a race, not a fault. Say so and let the
		// next pass re-probe rather than parking the node in ERROR.
		c.logger.Warn("could not probe Radicale for credential enforcement", "error", err)
		return nil
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		c.logger.Info("credential enforcement verified", "status", status)
		return nil
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		// The server is up and asked for credentials, but its auth backend
		// errored. With `type = ldap` that is the Authentik LDAP outpost being
		// unreachable, which is the provider's outage and not this app's
		// configuration. Parking the calendar in ERROR on every identity
		// provider restart would bury the real signal, so warn and let the
		// next reconciliation re-probe.
		c.logger.Warn("Radicale is serving but its LDAP backend errored", "status", status)
		return nil
	default:
		return fmt.Errorf("radicale answered an unauthenticated collection request with %d; "+
			"the generated [auth] section is not in effect and the server is open", status)
	}
}

// restartContainer stops and starts the running container through the host
// runtime so Radicale re-reads its config, which it loads once at process
// start. It mirrors the Home Assistant pattern: the configurator owns the
// restart decision, the runtime performs the side effect.
func (c *Configurator) restartContainer(ctx context.Context) error {
	if c.restartContainerFn == nil {
		return errors.New("no container restart callback")
	}
	return c.restartContainerFn(ctx, c.Name())
}

// syncLdapSecret keeps the secret file equal to the LDAP reader password the
// identity provider published, and removes the file when there is no provider
// to read it with. A credential nothing uses is still a credential on disk.
func (c *Configurator) syncLdapSecret(path string, ldap *configurator.LDAPOutput) (bool, error) {
	if !hasLDAP(ldap) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("remove stale LDAP secret: %w", err)
		}
		return false, nil
	}
	changed, err := managedfile.Write(path, []byte(ldap.BindPassword+"\n"), managedfile.ModeSharedConfig)
	if err != nil {
		return false, fmt.Errorf("write LDAP reader secret: %w", err)
	}
	return changed, nil
}

// hasLDAP reports whether the bound provider carries a usable reader credential.
func hasLDAP(ldap *configurator.LDAPOutput) bool {
	return ldap != nil && ldap.BindPassword != ""
}

// authType is the value the [auth] section gets for this provider, for logs.
func authType(ldap *configurator.LDAPOutput) string {
	if hasLDAP(ldap) {
		return "ldap"
	}
	return "denyall"
}

// renderConfig renders the Radicale INI file.
//
// Three settings carry the Authentik specifics: the users-OU-scoped search
// base and the `sAMAccountName` filter (see the two constants below), and
// `ldap_ignore_attribute_create_modify_timestamp`, a workaround Radicale
// carries for Authentik's LDAP outpost, which serves createTimestamp and
// modifyTimestamp in a shape ldap3 rejects.
//
// `rights = owner_only` is the isolation model: each user owns their own tree.
// Cross-user access goes through Radicale's native sharing (the map shares the
// configurator writes), not through the rights model.
//
// With no LDAP provider bound the config denies everything rather than
// falling back to Radicale's default of no authentication. An installed app
// that is locked until its identity provider arrives converges when the
// provider shows up; an installed app that is open until then is just open.

// ldapUserFilter matches a Bloud login name against Authentik's LDAP outpost.
//
// Authentik serves the username in `sAMAccountName` and `cn`, but NOT in
// `uid`: there `uid` is a 64-char content hash. A `(uid={0})` filter, the
// one most CalDAV setup guides use, matches nothing against this directory.
// Jellyfin hit the same wall; see the LdapUidAttribute note in apps/jellyfin.
const ldapUserFilter = "(sAMAccountName={0})"

// ldapUserSearchPrefix scopes the login search to the user tree.
//
// Authentik's LDAP also serves `ou=virtual-groups`, and a group named "admin"
// carries the same `cn` and `sAMAccountName` as the admin user. Searched from
// the directory base, every login name matches two entries, and Radicale
// rejects an ambiguous filter outright, so nobody could sign in. Scoping to
// the users OU makes the match unique.
const ldapUserSearchPrefix = "ou=users,"

// ldapSearchBase returns the users-OU-scoped search base for a Bloud LDAP
// provider. It is idempotent: a base that already carries the prefix is
// returned unchanged rather than doubled.
func ldapSearchBase(baseDN string) string {
	if strings.HasPrefix(baseDN, ldapUserSearchPrefix) {
		return baseDN
	}
	return ldapUserSearchPrefix + baseDN
}

func renderConfig(port int, ldap *configurator.LDAPOutput) string {
	var b strings.Builder
	b.WriteString("# Managed by Bloud. Edits here are overwritten on the next\n")
	b.WriteString("# reconciliation cycle; the settings live in apps/radicale/configurator.go.\n\n")

	b.WriteString("[server]\n")
	fmt.Fprintf(&b, "hosts = 0.0.0.0:%d\n", port)
	b.WriteString("max_connections = 100\n")
	b.WriteString("timeout = 30\n\n")

	b.WriteString("[auth]\n")
	if hasLDAP(ldap) {
		b.WriteString("# Users are verified against the Bloud identity provider's LDAP\n")
		b.WriteString("# outpost with the password their client sends.\n")
		b.WriteString("type = ldap\n")
		fmt.Fprintf(&b, "ldap_uri = ldap://%s\n", net.JoinHostPort(ldap.Host, strconv.Itoa(ldap.Port)))
		fmt.Fprintf(&b, "ldap_base = %s\n", ldapSearchBase(ldap.BaseDN))
		fmt.Fprintf(&b, "ldap_reader_dn = %s\n", ldap.BindUser)
		fmt.Fprintf(&b, "ldap_secret_file = %s/%s\n", containerConfigDir, ldapSecretFileName)
		fmt.Fprintf(&b, "ldap_filter = %s\n", ldapUserFilter)
		b.WriteString("ldap_ignore_attribute_create_modify_timestamp = true\n")
		b.WriteString("ldap_security = none\n")
		b.WriteString("realm = Bloud\n\n")
	} else {
		b.WriteString("# No identity provider is bound to this install. Deny every login\n")
		b.WriteString("# until one is; the next reconciliation rewrites this section.\n")
		b.WriteString("type = denyall\n\n")
	}

	b.WriteString("[storage]\n")
	// One raw string rather than a WriteString per line: funlen counts
	// statements, and a comment block should not be what pushes a renderer
	// over its budget.
	b.WriteString(`# Radicale's own backend. Feeds used to arrive through a vendored
# plugin that wrapped this backend and wrote into it from inside the server
# process; they now arrive over CalDAV from a sidecar, which is why the server
# is back to being plain Radicale. The type name is "multifilesystem", not
# "filesystem": those two are the only internal storage types Radicale
# registers, and an unrecognized one fails at startup rather than at first
# request.
`)
	b.WriteString("type = multifilesystem\n")
	fmt.Fprintf(&b, "filesystem_folder = %s\n\n", containerStorageDir)

	b.WriteString("[rights]\n")
	b.WriteString("type = owner_only\n\n")

	writeSharingConfig(&b)

	b.WriteString("[web]\n")
	b.WriteString("# The built-in web UI: create and manage calendars and address books\n")
	b.WriteString("# from a browser. It authenticates with the same credentials as the\n")
	b.WriteString("# DAV clients.\n")
	b.WriteString("type = internal\n\n")

	writeCORSHeaders(&b)

	b.WriteString("[logging]\n")
	b.WriteString("level = info\n")
	return b.String()
}

// writeSharingConfig writes the [sharing] section that enables Radicale's
// native map shares. The configurator is the single writer of sharing.csv;
// Radicale reads it at startup and never writes it back unless its own sharing
// API is used, which Bloud does not call.
func writeSharingConfig(b *strings.Builder) {
	b.WriteString("[sharing]\n")
	b.WriteString("# The csv backend with map shares: Bloud writes sharing.csv as the single\n")
	b.WriteString("# writer, and Radicale mounts each shared collection as a virtual\n")
	b.WriteString("# collection in the recipient's own tree, so discovery lists it.\n")
	b.WriteString("type = csv\n")
	b.WriteString("collection_by_map = true\n")
	b.WriteString("permit_create_map = true\n\n")
}

// writeCORSHeaders writes the [headers] section that lets a browser-based DAV
// client (Calino) call the server cross-origin. Every request is authenticated
// with Basic credentials the client sets explicitly, and the browser's
// preflight is an anonymous OPTIONS that Radicale answers without auth, so a
// wildcard origin exposes nothing a caller without the user's password can
// read. Without these headers the browser refuses the cross-origin DAV call and
// Calino shows an empty window.
func writeCORSHeaders(b *strings.Builder) {
	b.WriteString("[headers]\n")
	b.WriteString("# Calino's browser-to-Radicale DAV calls are cross-origin; these\n")
	b.WriteString("# headers are what make the browser expose the responses.\n")
	b.WriteString("Access-Control-Allow-Origin = *\n")
	b.WriteString("Access-Control-Allow-Methods = GET, HEAD, OPTIONS, PROPFIND, PROPPATCH, REPORT, PUT, DELETE, MKCALENDAR, MKCOL, MOVE, COPY\n")
	b.WriteString("Access-Control-Allow-Headers = authorization, content-type, depth, destination, if-match, if-none-match, overwrite, prefer, x-requested-with\n")
	b.WriteString("Access-Control-Expose-Headers = DAV, ETag, Sync-Token, Location, Content-Range, WWW-Authenticate, Allow\n")
	b.WriteString("Access-Control-Max-Age = 86400\n\n")
}
