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
)

type Configurator struct {
	port   int
	logger *slog.Logger
	api    *radicaleAPI

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
		port:   port,
		logger: logger.With("app", appName),
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
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
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
	if !cfgChanged && !secretChanged {
		return configurator.NoRestart(), nil
	}

	c.logger.Info("wrote Radicale config", "path", cfgPath,
		"auth", authType(state.LDAP), "secretChanged", secretChanged)
	return configurator.MustRestart("Radicale config rewritten"), nil
}

// PostStart checks the running server for the one property that matters: an
// unauthenticated request for a collection is refused. That is a behavioral
// assertion about the process, not a re-read of the file that was written, so
// it catches a container that came up on a stale or ignored config.
func (c *Configurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
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
// `rights = owner_only` is the isolation model: a user's collections live
// under their own top-level path and nothing else is reachable, so one Bloud
// account cannot read another's calendar or contacts.
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
	fmt.Fprintf(&b, "filesystem_folder = %s\n\n", containerStorageDir)

	b.WriteString("[rights]\n")
	b.WriteString("type = owner_only\n\n")

	b.WriteString("[web]\n")
	b.WriteString("# The built-in web UI: create and manage calendars and address books\n")
	b.WriteString("# from a browser. It authenticates with the same credentials as the\n")
	b.WriteString("# DAV clients.\n")
	b.WriteString("type = internal\n\n")

	b.WriteString("[logging]\n")
	b.WriteString("level = info\n")
	return b.String()
}
