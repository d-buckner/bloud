// SPDX-License-Identifier: AGPL-3.0-only

// Package sonarr wires the Sonarr PVR into Bloud: it pre-seeds the
// instance's config.xml so the app never offers its own login form, verifies
// through the app's API that external authentication is still in effect, and
// connects the instance to the rest of the media stack: its library root
// folder, always, and the qBittorrent download client whenever that provider
// is installed.
package sonarr

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/servarr"
)

const (
	// appName is the catalog id and the identity the API client logs under.
	appName = "sonarr"

	// nodeName is the graph node / container name the host-agent reconciles.
	nodeName = "apps-sonarr"

	// apiPath is Sonarr's versioned API root. Sonarr and Radarr share v3;
	// Prowlarr still answers on v1.
	apiPath = "api/v3"

	// defaultPort is Sonarr's web port, published on the host by Traefik's
	// route target.
	defaultPort = 8989

	// configFileName is the Servarr settings document inside the app's data
	// directory, mounted at /config in the container.
	configFileName = "config.xml"

	// rootFolderResource is the root-folder collection under Sonarr's API
	// root; rootFolderMount is the library mount metadata.yaml declares for it
	// ({{dataDir}}/media/shows → /shows). A root folder is what makes the
	// instance usable (and what a request manager resolves a library against),
	// and the app never creates one for itself.
	rootFolderResource = "/" + apiPath + "/rootfolder"
	rootFolderMount    = "/shows"

	// qbittorrentAppID and qbittorrentPort mirror the provider entry in
	// apps/qbittorrent/metadata.yaml. The download-client link is wired
	// app-side (see INTEGRATION.md), so this consumer holds the provider's
	// catalog id and published port as documented constants: a port change in
	// the qBittorrent catalog entry must be mirrored here.
	qbittorrentAppID = "qbittorrent"
	qbittorrentPort  = 8081

	// qbittorrentProbePath is qBittorrent's version route, the one the WebUI
	// answers without credentials, so a 200 means the provider is installed
	// and up. qbittorrentCategoryResource creates the category the download
	// client below stores: qBittorrent never creates one by itself, and
	// TorrentImpl::setCategory returns false for a category it does not have
	// (src/base/bittorrent/torrentimpl.cpp), so a torrent added with an
	// unknown category silently ends up uncategorised.
	qbittorrentProbePath        = "/api/v2/app/version"
	qbittorrentCategoryResource = "/api/v2/torrents/createCategory"

	// downloadCategoryField and downloadCategory are Sonarr's slot in the
	// QBittorrentSettings contract and the value it stores there.
	downloadCategoryField = "tvCategory"
	downloadCategory      = "tv-sonarr"

	// probeTimeout bounds the qBittorrent availability probe: an absent
	// sibling must cost seconds, not the request default.
	probeTimeout = 5 * time.Second
)

// Configurator handles Sonarr configuration.
type Configurator struct {
	port   int
	logger *slog.Logger
	api    *servarr.Client

	// own is the same instance reached as a plain HTTP client: PostStart uses
	// it for the resources pkg/servarr does not model (root folders, the
	// download-client list).
	own *appclient.Client

	// qbittorrent probes the download-client provider on its host-published
	// port and writes the download category into it. Those are the consumer's
	// business: the host never learns that a sibling exists.
	qbittorrent *appclient.Client

	// baseURL is a test seam: when set, the own-API clients resolve to it
	// instead of localhost:port.
	baseURL string
	// qbittorrentBaseURL is the matching seam for the qBittorrent client.
	qbittorrentBaseURL string
}

// NewConfigurator creates a new Sonarr configurator from the host Deps.
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
	c.api = servarr.NewClient(deps.HTTP, appName, apiPath, c.ownBaseURL)
	c.own = deps.HTTP.New(appclient.Spec{Name: appName, BaseURLFn: c.ownBaseURL})
	// The probe leaves the Sonarr container: it asks the host's published
	// qBittorrent port whether a download client provider exists yet.
	c.qbittorrent = deps.HTTP.New(appclient.Spec{Name: qbittorrentAppID, BaseURLFn: func() string {
		if c.qbittorrentBaseURL != "" {
			return c.qbittorrentBaseURL
		}
		return fmt.Sprintf("http://localhost:%d", qbittorrentPort)
	}})
	return c
}

// ownBaseURL is where this instance's API answers: the test seam when set,
// otherwise its published host port.
func (c *Configurator) ownBaseURL() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return fmt.Sprintf("http://localhost:%d", c.port)
}

func (c *Configurator) Name() string {
	return nodeName
}

// configPath returns the host path of the instance's config.xml.
func (c *Configurator) configPath(state *configurator.AppState) string {
	return filepath.Join(state.DataPath, "config", configFileName)
}

// PreStart creates the instance's config, library and download directories,
// then puts the instance into Servarr's External authentication mode before
// the container starts. It returns changed=true only when config.xml content
// actually changed, because that flag recreates the container: directory
// creation alone must never report a change.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (bool, error) {
	dirs := []string{
		filepath.Join(state.DataPath, "config"),
		filepath.Join(state.BloudDataPath, "media", "shows"),
		filepath.Join(state.BloudDataPath, "downloads"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return false, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// The container runs as LSIO's `abc` (PUID=1000), which under rootless
	// podman maps to a host subuid the host agent is neither owner nor group
	// member of. Two directories therefore have to be opened up, because the
	// container's own init chowns only its /config:
	//
	//   - The media library Sonarr imports into. Its init script runs
	//     `lsiown -R abc:abc /config /run/sonarr-temp` and never touches
	//     /downloads, so a 0755 directory stays unwritable for abc and Sonarr
	//     rejects /shows as a root folder ("Folder '/shows' is not writable by
	//     user 'abc'"). 0777 rather than the sticky 1777: abc has to be able to
	//     move and delete files the operator copied into the library by hand,
	//     which the sticky bit would forbid.
	//   - The config dir. pkg/managedfile writes config.xml through a temp file
	//     created *inside* the target directory, and after the first boot that
	//     directory belongs to abc's subuid, so without world-write the next
	//     auth repair (a UI settings save rewrites config.xml) fails with
	//     EACCES, and a PreStart failure is terminal for the node.
	//
	// The container's own /config and /downloads are chowned by the LSIO init
	// script (for /downloads that is the qBittorrent container's init, which
	// mounts it), so only these dirs need the mode.
	configDir := filepath.Join(state.DataPath, "config")
	if err := os.Chmod(configDir, 0o777); err != nil {
		return false, fmt.Errorf("making the config directory writable: %w", err)
	}
	mediaDir := filepath.Join(state.BloudDataPath, "media", "shows")
	if err := os.Chmod(mediaDir, 0o777); err != nil {
		return false, fmt.Errorf("making the media library writable: %w", err)
	}
	downloadsDir := filepath.Join(state.BloudDataPath, "downloads")
	if err := os.Chmod(downloadsDir, 0o777); err != nil {
		return false, fmt.Errorf("making the downloads directory writable: %w", err)
	}

	changed, err := servarr.EnsureExternalAuth(c.configPath(state))
	if err != nil {
		return false, fmt.Errorf("failed to configure %s: %w", appName, err)
	}
	return changed, nil
}

// Remove is a no-op for the Sonarr configurator; container and data removal
// are handled at a higher level by the orchestrator.
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}

// PostStart verifies through Sonarr's own API that external authentication is
// in effect, repairing it when a settings save (from the UI or an API client)
// rewrote config.xml. It then registers the instance's library root folder and,
// when qBittorrent is installed, the download client that feeds it. Idempotent
// on every reconciliation.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	configPath := c.configPath(state)
	key, err := servarr.APIKey(configPath)
	if err != nil {
		return fmt.Errorf("failed to read %s API key: %w", appName, err)
	}
	if key == "" {
		return fmt.Errorf("%s: no ApiKey in %s; PreStart must run before PostStart", appName, configPath)
	}

	changed, err := c.api.EnsureExternalAuth(ctx, key)
	if err != nil {
		return err
	}
	if changed {
		c.logger.Info("repaired external authentication", "config", configPath)
	}

	if err := c.ensureRootFolder(ctx, key); err != nil {
		return err
	}
	return c.ensureDownloadClient(ctx, key)
}

// ensureRootFolder registers the instance's library mount (/shows) as a root
// folder. It needs no sibling, so it runs on every reconciliation whatever
// else is installed, and it is idempotent: an instance that already has the
// path is left alone.
func (c *Configurator) ensureRootFolder(ctx context.Context, apiKey string) error {
	body, err := c.own.GET(rootFolderResource).
		Header(servarr.APIKeyHeader, apiKey).
		OK(http.StatusOK).
		Do(ctx)
	if err != nil {
		return fmt.Errorf("%s: reading root folders: %w", appName, err)
	}

	var folders []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(body, &folders); err != nil {
		return fmt.Errorf("%s: reading root folders → %d: decode JSON: %w", appName, http.StatusOK, err)
	}
	for _, folder := range folders {
		if folder.Path == rootFolderMount {
			return nil
		}
	}

	if err := c.own.POST(rootFolderResource).
		Header(servarr.APIKeyHeader, apiKey).
		JSON(map[string]string{"path": rootFolderMount}).
		OK(http.StatusOK, http.StatusCreated).
		Exec(ctx); err != nil {
		return fmt.Errorf("%s: adding the root folder %s: %w", appName, rootFolderMount, err)
	}
	c.logger.Info("added root folder", "path", rootFolderMount)
	return nil
}

// ensureDownloadClient keeps the download-client link in line with the
// provider: when qBittorrent answers its published port it creates the category
// Sonarr's client stores and then adds the client, and when the provider is
// gone it prunes the client it left behind. An absent provider is never an
// error; Sonarr works without a download client, and a later reconciliation
// (or the provider's own staleness trigger) wires the link.
func (c *Configurator) ensureDownloadClient(ctx context.Context, apiKey string) error {
	spec := servarr.DownloadClientSpec{
		APIPath:       apiPath,
		APIKey:        apiKey,
		Host:          "apps-" + qbittorrentAppID,
		Port:          qbittorrentPort,
		CategoryField: downloadCategoryField,
		Category:      downloadCategory,
	}

	if !c.qbittorrentAvailable(ctx) {
		pruned, err := servarr.RemoveDownloadClient(ctx, c.own, spec)
		if err != nil {
			if servarr.TransientFailure(err) {
				c.logger.Warn("qBittorrent did not answer while pruning the stale download client; retrying on the next reconciliation", "error", err)
				return nil
			}
			return err
		}
		if pruned {
			c.logger.Info("pruned stale download client", "implementation", servarr.ImplementationQBittorrent)
		}
		return nil
	}

	if err := c.ensureDownloadCategory(ctx); err != nil {
		// A provider that is mid-restart, overloaded or briefly 5xx-ing must not
		// fail this node: ERROR is terminal, so the app would stay "failed" over
		// a condition the next reconciliation fixes by itself. Only a failure
		// the provider will not recover from on its own (a rejected key, a
		// 403 from a broken whitelist) is reported as an error.
		if servarr.TransientFailure(err) {
			c.logger.Warn("qBittorrent is not ready for the download-client category yet; retrying on the next reconciliation", "error", err)
			return nil
		}
		return err
	}

	changed, err := servarr.EnsureDownloadClient(ctx, c.own, spec)
	if err != nil {
		if servarr.TransientFailure(err) {
			c.logger.Warn("qBittorrent did not answer the download-client test; retrying on the next reconciliation", "error", err)
			return nil
		}
		return err
	}
	if changed {
		c.logger.Info("added download client", "implementation", servarr.ImplementationQBittorrent)
	}
	return nil
}

// ensureDownloadCategory creates the category the download client stores.
// qBittorrent never creates a category by itself and refuses to tag a torrent
// with one it does not know, so the category has to exist first; only this
// consumer knows which category it wants, which is why the write happens here
// and not in the provider. A category that already exists is the normal
// second-run outcome and not an error: qBittorrent answers 409 Conflict for it
// (TorrentsController::createCategoryAction → SessionImpl::addCategory returns
// false for a known name, src/base/bittorrent/sessionimpl.cpp).
func (c *Configurator) ensureDownloadCategory(ctx context.Context) error {
	created, err := c.qbittorrent.POST(qbittorrentCategoryResource).
		Form(url.Values{"category": {downloadCategory}}).
		OK(http.StatusOK).
		AlreadyDone(http.StatusConflict).
		Ensure(ctx)
	if err != nil {
		return fmt.Errorf("%s: creating the %s category in qBittorrent: %w", appName, downloadCategory, err)
	}
	if created {
		c.logger.Info("created qBittorrent category", "category", downloadCategory)
	}
	return nil
}

// qbittorrentAvailable reports whether Bloud's qBittorrent answers on its
// published host port. Any failure (connection refused, timeout, a non-200
// status) means "not usable yet", including an install that is still booting.
func (c *Configurator) qbittorrentAvailable(ctx context.Context) bool {
	// The probe bounds itself with a deadline rather than a per-call client
	// timeout, so an absent sibling costs seconds, not the request default.
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	err := c.qbittorrent.GET(qbittorrentProbePath).
		OK(http.StatusOK).
		NoRetry().
		Exec(probeCtx)
	if err != nil {
		c.logger.Info("qBittorrent is not installed yet; the download client link is skipped", "error", err)
		return false
	}
	return true
}
