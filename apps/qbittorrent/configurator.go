// SPDX-License-Identifier: AGPL-3.0-only

package qbittorrent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const appName = "qbittorrent"

// webUIPort is the port the WebUI listens on inside the container, published
// 1:1 on the host. It appears in three places that must agree:
//
//   - metadata.yaml's WEBUI_PORT env: LSIO's init runs
//     `qbittorrent-nox --webui-port=${WEBUI_PORT}`, and --webui-port overwrites
//     WebUI\Port in the conf file;
//   - the conf key WebUI\Port below, which is what the app itself persists;
//   - metadata.yaml's published host port, which Traefik routes to.
const webUIPort = 8081

// confRelPath is qBittorrent's conf file relative to the app's data directory
// (mounted at /config).
var confRelPath = filepath.Join("config", "qBittorrent", "qBittorrent.conf")

// The conf file's INI sections.
const (
	legalNoticeSection = "LegalNotice"
	preferencesSection = "Preferences"
)

// legalNoticeKeys accepts qBittorrent's first-run notice so no interactive
// prompt can stall a container the operator cannot attach to.
var legalNoticeKeys = map[string]string{
	"Accepted": "true",
}

// managedPreferences are the [Preferences] keys Bloud owns. qBittorrent owns
// this file (it rewrites it on a dirty timer and preserves the keys it does not
// know), so only these keys are ever merged in; every other key the user or
// the app wrote keeps its value.
//
// These particular keys are what make the WebUI usable behind forward-auth:
// qBittorrent has no external/header auth mode, so its own login is disabled
// for the proxy subnet and Traefik's forward-auth becomes the only gate.
var managedPreferences = map[string]string{
	// Bloud's downloads layout (mounted at /downloads).
	"Downloads\\SavePath": "/downloads/",
	"Downloads\\TempPath": "/downloads/incomplete/",
	// UPnP would punch its own port mappings through the VM's NAT; the peer
	// port is published explicitly instead.
	"Connection\\UPnP": "false",
	// Listen on every interface inside the container; the host port is what
	// decides reachability.
	"WebUI\\Address": "*",
	"WebUI\\Port":    fmt.Sprintf("%d", webUIPort),
	// Accept any Host header: Traefik preserves the browser's
	// `Host: qbittorrent.localhost:8080`, which is never the container's own
	// listen port, and HostHeaderValidation would 401 every such request.
	// Bloud routes by host in Traefik anyway.
	"WebUI\\ServerDomains":        "*",
	"WebUI\\HostHeaderValidation": "false",
	// Auth bypass for the proxy: the subnet whitelist is evaluated against the
	// connecting address, and every request arrives from the docker network /
	// host gateway. A single CIDR keeps the serialised value stable, so an
	// exact string comparison never flaps (which would recreate the container
	// every reconciliation).
	"WebUI\\AuthSubnetWhitelistEnabled": "true",
	"WebUI\\AuthSubnetWhitelist":        "0.0.0.0/0",
	// Loopback is inside the same trust boundary as the proxy subnet: nothing
	// but Bloud's own routing can reach this WebUI, so localhost stays
	// authentication-free too rather than leaving a second gate that behaves
	// differently from the one the proxy sees.
	"WebUI\\LocalHostAuth": "false",
}

// Configurator handles qBittorrent configuration.
type Configurator struct {
	port int
	api  *appclient.Client

	logger *slog.Logger

	// baseURL is a test seam: when set, the WebUI client resolves to it
	// instead of localhost:port.
	baseURL string

	// pollInterval is the cadence of PostStart's verification wait: 2s in
	// production, a few milliseconds in tests.
	pollInterval time.Duration
}

// NewConfigurator creates a new qBittorrent configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = webUIPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:         port,
		logger:       logger.With("app", appName),
		pollInterval: 2 * time.Second,
	}
	c.api = deps.HTTP.New(appclient.Spec{
		Name: appName,
		BaseURLFn: func() string {
			if c.baseURL != "" {
				return c.baseURL
			}
			return fmt.Sprintf("http://localhost:%d", c.port)
		},
	})
	return c
}

func (c *Configurator) Name() string {
	return "apps-qbittorrent"
}

// PreStart creates the directories the container mounts and merges Bloud's keys
// into qBittorrent's conf file before the container reads it at boot.
//
// It reports changed=true only when the conf file's content actually changed:
// qBittorrent rewrites the file itself, so a merge that adds nothing must not
// bounce the container. Directory creation never counts as a change.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (bool, error) {
	dirs := []string{
		filepath.Join(state.DataPath, "config", "qBittorrent"),
		filepath.Join(state.BloudDataPath, "downloads"),
		filepath.Join(state.BloudDataPath, "downloads", "incomplete"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return false, fmt.Errorf("qbittorrent: create directory %s: %w", dir, err)
		}
	}

	// The container runs as LSIO's `abc` (PUID=1000), a host subuid under
	// rootless podman that the host agent is neither owner nor group member of,
	// and its init chowns what it mounts. Two places therefore have to be opened
	// up. Neither matters on the first install, which is why the live
	// verification never saw them:
	//
	//   - The conf file. /config is chowned to abc at every start, so after the
	//     first boot the merge below (pkg/configurator.INIFile.Save rewrites the
	//     file in place via os.Create) fails with EACCES the moment a managed
	//     key drifts, and a PreStart failure is terminal for the node, which
	//     would leave the WebUI auth bypass un-repairable by Bloud. The
	//     directory needs write too, for a conf file that does not exist yet.
	//   - The downloads tree. This container's init chowns /downloads
	//     non-recursively and only when it is a mount point, so the
	//     subdirectory Bloud pre-creates for Downloads\TempPath
	//     (/downloads/incomplete) stays owned by the host agent at 0755 and
	//     qBittorrent cannot write it.
	//
	// 0777, not the sticky 1777: abc has to be able to replace and unlink files
	// Bloud or the operator owns.
	configDir := filepath.Join(state.DataPath, "config")
	if err := os.Chmod(configDir, 0o777); err != nil {
		return false, fmt.Errorf("qbittorrent: make %s writable: %w", configDir, err)
	}
	confPath := filepath.Join(state.DataPath, confRelPath)
	confDir := filepath.Dir(confPath)
	if err := os.Chmod(confDir, 0o777); err != nil {
		return false, fmt.Errorf("qbittorrent: make %s writable: %w", confDir, err)
	}
	if _, err := os.Stat(confPath); err == nil {
		if err := os.Chmod(confPath, 0o666); err != nil {
			return false, fmt.Errorf("qbittorrent: make %s writable: %w", confPath, err)
		}
	}
	for _, dir := range []string{
		filepath.Join(state.BloudDataPath, "downloads"),
		filepath.Join(state.BloudDataPath, "downloads", "incomplete"),
	} {
		if err := os.Chmod(dir, 0o777); err != nil {
			return false, fmt.Errorf("qbittorrent: make %s writable: %w", dir, err)
		}
	}

	conf, err := configurator.LoadINI(confPath)
	if err != nil {
		return false, fmt.Errorf("qbittorrent: load %s: %w", confPath, err)
	}

	noticesChanged := conf.EnsureKeys(legalNoticeSection, legalNoticeKeys)
	prefsChanged := conf.EnsureKeys(preferencesSection, managedPreferences)
	if !noticesChanged && !prefsChanged {
		return false, nil
	}

	if err := conf.Save(confPath); err != nil {
		return false, fmt.Errorf("qbittorrent: save %s: %w", confPath, err)
	}
	return true, nil
}

// PostStart verifies through qBittorrent's own API that the WebUI accepts
// unauthenticated requests from the proxy. Called on every reconciliation, so
// it converges rather than acting once.
func (c *Configurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if err := c.waitWebUIReachable(ctx); err != nil {
		return fmt.Errorf("qbittorrent: WebUI auth bypass is not in effect: %w", err)
	}
	return nil
}

// Remove is a no-op for the qBittorrent configurator; container and data
// removal are handled at a higher level by the orchestrator.
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}

// waitWebUIReachable polls GET /api/v2/app/version until it answers 200.
//
// That endpoint is the honest test of the auth bypass: it returns 403 while the
// requesting address is not whitelisted, and 200 only once PreStart's whitelist
// (and its LocalHostAuth counterpart) is what the running process actually
// loaded. Without it the node could go healthy while every proxied request is
// rejected with 403.
func (c *Configurator) waitWebUIReachable(ctx context.Context) error {
	return c.api.GET("/api/v2/app/version").
		Anonymous().
		WithRetry(versionWaitPolicy(c.pollInterval)).
		Ready(appclient.StatusIs(http.StatusOK)).
		Wait(ctx)
}

// versionWaitPolicy bounds the WebUI verification poll. The cadence is constant
// (MaxInterval pins the factor-clamped backoff) and the attempt cap keeps the
// wait inside the orchestrator's 150s PostStart budget: ~60s at the 2s
// production interval.
func versionWaitPolicy(iv time.Duration) appclient.RetryPolicy {
	if iv <= 0 {
		iv = 2 * time.Second
	}
	return appclient.RetryPolicy{MaxAttempts: 30, Initial: iv, MaxInterval: iv}
}
