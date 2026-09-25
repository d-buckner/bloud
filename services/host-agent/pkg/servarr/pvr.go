// SPDX-License-Identifier: AGPL-3.0-only

package servarr

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

// The download-client provider every Servarr instance wires.
const (
	// QBittorrentAppID is the provider this consumer wires, as named in
	// apps/qbittorrent/metadata.yaml. The provider's address, port and
	// category all come from the resolved downloadClient binding: nothing
	// about the provider is duplicated here.
	QBittorrentAppID = "qbittorrent"

	// QBittorrentCategoryResource creates the category the download client
	// stores: qBittorrent never creates one by itself, and
	// TorrentImpl::setCategory returns false for a category it does not have
	// (src/base/bittorrent/torrentimpl.cpp), so a torrent added with an
	// unknown category silently ends up uncategorised.
	QBittorrentCategoryResource = "/api/v2/torrents/createCategory"
)

// PVRApp is everything that differs between the Servarr instances Bloud
// ships. Sonarr and Radarr are the same program with a different library:
// the same config.xml shape, the same external-auth mode, the same
// root-folder and download-client wiring. Only the values below differ, so
// they are the only thing the app packages carry.
type PVRApp struct {
	// Name is the catalog id, the identity the API client logs under, and
	// the suffix of the graph node name.
	Name string

	// APIPath is the instance's versioned API root. Sonarr and Radarr share
	// v3; Prowlarr still answers on v1.
	APIPath string

	// DefaultPort is the web port published on the host by Traefik's route
	// target, used when registration passes 0.
	DefaultPort int

	// ConfigFileName is the Servarr settings document inside the app's data
	// directory, mounted at /config in the container.
	ConfigFileName string

	// MediaSubdir is the shared library directory under the Bloud data root
	// (media/shows), and RootFolderMount is where metadata.yaml mounts it in
	// the container (/shows). A root folder is what makes the instance
	// usable, and what a request manager resolves a library against, and the
	// app never creates one for itself.
	MediaSubdir     string
	RootFolderMount string

	// DownloadCategoryField is this instance's slot in the QBittorrentSettings
	// contract; DownloadCategory is the value it stores there.
	DownloadCategoryField string
	DownloadCategory      string
}

// NodeName is the graph node / container name the host-agent reconciles.
func (a PVRApp) NodeName() string { return "apps-" + a.Name }

// RootFolderResource is the root-folder collection under this instance's API
// root.
func (a PVRApp) RootFolderResource() string { return "/" + a.APIPath + "/rootfolder" }

// PVRConfigurator is the shared lifecycle behind every Servarr app. It
// pre-seeds the instance's config.xml so the app never offers its own login
// form, verifies through the app's API that external authentication is still
// in effect, and connects the instance to the rest of the media stack: its
// library root folder, always, and the qBittorrent download client whenever
// that provider is installed.
type PVRConfigurator struct {
	app    PVRApp
	port   int
	logger *slog.Logger
	api    *Client

	// own is the same instance reached as a plain HTTP client: PostStart uses
	// it for the resources this package does not model (root folders, the
	// download-client list).
	own *appclient.Client

	// clients builds the provider client. The provider's address is only known
	// once the binding is resolved, so the client is built per reconciliation
	// rather than in the constructor.
	clients configurator.ClientFactory

	// secrets is where this instance publishes its own ApiKey for its
	// consumers. Nil in degraded contexts (no secret store), which only skips
	// the publication.
	secrets configurator.AppSecretsProvider

	// baseURL is a test seam: when set, the own-API clients resolve to it
	// instead of localhost:port.
	baseURL string
}

// NewPVRConfigurator builds the shared configurator for one Servarr app.
func NewPVRConfigurator(app PVRApp, port int, deps configurator.Deps) *PVRConfigurator {
	if port == 0 {
		port = app.DefaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &PVRConfigurator{
		app:     app,
		port:    port,
		logger:  logger.With("app", app.Name),
		clients: deps.HTTP,
		secrets: deps.Secrets,
	}
	c.api = NewClient(deps.HTTP, app.Name, app.APIPath, c.ownBaseURL)
	c.own = deps.HTTP.New(appclient.Spec{Name: app.Name, BaseURLFn: c.ownBaseURL})
	return c
}

// SetBaseURL points the own-API clients at a test server instead of
// localhost:port.
func (c *PVRConfigurator) SetBaseURL(u string) { c.baseURL = u }

// Port is the port this instance's API is reached on.
func (c *PVRConfigurator) Port() int { return c.port }

// ownBaseURL is where this instance's API answers: the test seam when set,
// otherwise its published host port.
func (c *PVRConfigurator) ownBaseURL() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return fmt.Sprintf("http://localhost:%d", c.port)
}

// Name returns the graph node this configurator owns.
func (c *PVRConfigurator) Name() string { return c.app.NodeName() }

// configPath returns the host path of the instance's settings document.
func (c *PVRConfigurator) configPath(state *configurator.AppState) string {
	return filepath.Join(state.DataPath, "config", c.app.ConfigFileName)
}

// ConfigPath is the exported form: the host path of the instance's settings
// document, which is where its ApiKey lives.
func (c *PVRConfigurator) ConfigPath(state *configurator.AppState) string {
	return c.configPath(state)
}

// PreStart creates the instance's config, library and download directories,
// then puts the instance into Servarr's External authentication mode before
// the container starts. It reports a restart only when the settings document's
// content actually changed, because that flag recreates the container:
// directory creation alone must never report a change.
func (c *PVRConfigurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	mediaDir := filepath.Join(state.BloudDataPath, "media", c.app.MediaSubdir)
	downloadsDir := filepath.Join(state.BloudDataPath, "downloads")
	configDir := filepath.Join(state.DataPath, "config")

	for _, dir := range []string{configDir, mediaDir, downloadsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return configurator.NoRestart(), fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// The container runs as LSIO's `abc` (PUID=1000), which under rootless
	// podman maps to a host subuid the host agent is neither owner nor group
	// member of. Three directories therefore have to be opened up, because the
	// container's own init chowns only its /config:
	//
	//   - The media library the instance imports into. Its init script runs
	//     `lsiown -R abc:abc /config /run/sonarr-temp` and never touches
	//     /downloads, so a 0755 directory stays unwritable for abc and the
	//     instance rejects the root folder ("Folder '/shows' is not writable
	//     by user 'abc'"). 0777 rather than the sticky 1777: abc has to be
	//     able to move and delete files the operator copied into the library
	//     by hand, which the sticky bit would forbid.
	//   - The config dir. pkg/managedfile writes the settings document through
	//     a temp file created *inside* the target directory, and after the
	//     first boot that directory belongs to abc's subuid, so without
	//     world-write the next auth repair (a UI settings save rewrites
	//     config.xml) fails with EACCES, and a PreStart failure is terminal
	//     for the node.
	//   - The shared downloads dir, for the same reason as the library.
	//
	// The container's own /config and /downloads are chowned by the LSIO init
	// script (for /downloads that is the qBittorrent container's init, which
	// mounts it), so only these dirs need the mode. A directory a container has
	// already taken over cannot be chmodded from the host at all (EPERM against
	// a subordinate uid), so the mode is applied only where it is still
	// missing: see pkg/managedfile.EnsureWritable.
	for _, dir := range []struct{ path, what string }{
		{configDir, "config directory"},
		{mediaDir, "media library"},
		{downloadsDir, "downloads directory"},
	} {
		if err := managedfile.EnsureWritable(dir.path, 0o777); err != nil {
			return configurator.NoRestart(), fmt.Errorf("making the %s writable: %w", dir.what, err)
		}
	}

	changed, err := EnsureExternalAuth(c.configPath(state))
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("failed to configure %s: %w", c.app.Name, err)
	}
	if err := c.publishAPIKey(state); err != nil {
		return configurator.NoRestart(), err
	}
	return configurator.RestartIf(changed, c.app.Name+" external-auth config rewritten"), nil
}

// publishAPIKey stores the instance's own ApiKey in the host secret store
// under the name this app declares in `provides.pvr.secrets`. Consumers
// (Prowlarr, Seerr) receive it through their integration binding, so no app
// ever reads this instance's config.xml.
//
// Publishing is idempotent, and the value is adopted from config.xml rather
// than generated here: config.xml is what the running instance authenticates
// with, and the app itself may rewrite the key (a settings save in the UI).
func (c *PVRConfigurator) publishAPIKey(state *configurator.AppState) error {
	if c.secrets == nil {
		return nil
	}
	key, err := APIKey(c.configPath(state))
	if err != nil {
		return fmt.Errorf("%s: reading the API key to publish: %w", c.app.Name, err)
	}
	if key == "" {
		// EnsureExternalAuth generates a key when the file has none, so an
		// empty one here means the file did not take the write.
		return fmt.Errorf("%s: no ApiKey in %s to publish", c.app.Name, c.configPath(state))
	}
	if err := c.secrets.SetAppSecret(c.app.Name, SecretAPIKey, key); err != nil {
		return fmt.Errorf("%s: publishing the API key: %w", c.app.Name, err)
	}
	return nil
}

// PostStart verifies through the instance's own API that external
// authentication is in effect, repairing it when a settings save (from the
// UI or an API client) rewrote config.xml. It then registers the instance's
// library root folder and, when qBittorrent is installed, the download client
// that feeds it. Idempotent on every reconciliation.
func (c *PVRConfigurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	configPath := c.configPath(state)
	key, err := APIKey(configPath)
	if err != nil {
		return fmt.Errorf("failed to read %s API key: %w", c.app.Name, err)
	}
	if key == "" {
		return fmt.Errorf("%s: no ApiKey in %s; PreStart must run before PostStart", c.app.Name, configPath)
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
	return c.ensureDownloadClient(ctx, key, state)
}

// ensureRootFolder registers the instance's library mount as a root folder.
// It needs no provider, so it runs on every reconciliation whatever else is
// installed, and it is idempotent: an instance that already has the path is
// left alone.
func (c *PVRConfigurator) ensureRootFolder(ctx context.Context, apiKey string) error {
	resource := c.app.RootFolderResource()
	body, err := c.own.GET(resource).
		Header(APIKeyHeader, apiKey).
		OK(http.StatusOK).
		Do(ctx)
	if err != nil {
		return fmt.Errorf("%s: reading root folders: %w", c.app.Name, err)
	}

	var folders []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(body, &folders); err != nil {
		return fmt.Errorf("%s: reading root folders → %d: decode JSON: %w", c.app.Name, http.StatusOK, err)
	}
	for _, folder := range folders {
		if folder.Path == c.app.RootFolderMount {
			return nil
		}
	}

	if err := c.own.POST(resource).
		Header(APIKeyHeader, apiKey).
		JSON(map[string]string{"path": c.app.RootFolderMount}).
		OK(http.StatusOK, http.StatusCreated).
		Exec(ctx); err != nil {
		return fmt.Errorf("%s: adding the root folder %s: %w", c.app.Name, c.app.RootFolderMount, err)
	}
	c.logger.Info("added root folder", "path", c.app.RootFolderMount)
	return nil
}

// ensureDownloadClient keeps the download-client link in line with the
// resolved provider: while qBittorrent is installed it makes the category
// this instance's client stores and adds the client, and once the provider is
// gone it prunes the client Bloud left behind. An absent provider is never an
// error; the instance works without a download client, and a later
// reconciliation (or the provider's own staleness trigger) wires the link.
func (c *PVRConfigurator) ensureDownloadClient(ctx context.Context, apiKey string, state *configurator.AppState) error {
	binding, ok := downloadClientBinding(state)
	if !ok {
		// The catalog does not declare this provider: nothing to wire and
		// nothing Bloud could have written.
		return nil
	}

	spec := DownloadClientSpec{
		APIPath:       c.app.APIPath,
		APIKey:        apiKey,
		Host:          binding.Node,
		Port:          binding.Port,
		CategoryField: c.app.DownloadCategoryField,
		Category:      c.app.DownloadCategory,
	}

	if !binding.Installed {
		pruned, err := RemoveDownloadClient(ctx, c.own, spec)
		if err != nil {
			if TransientFailure(err) {
				c.logger.Warn("qBittorrent did not answer while pruning the stale download client; retrying on the next reconciliation", "error", err)
				return nil
			}
			return err
		}
		if pruned {
			c.logger.Info("pruned stale download client", "implementation", ImplementationQBittorrent)
		}
		return nil
	}

	qbittorrent := c.clients.New(appclient.Spec{Name: QBittorrentAppID, BaseURLFn: func() string {
		return binding.LocalURL
	}})
	if err := c.ensureDownloadCategory(ctx, qbittorrent); err != nil {
		// A provider that is mid-restart, overloaded or briefly 5xx-ing must
		// not fail this node: ERROR is terminal, so the app would stay "failed"
		// over a condition the next reconciliation fixes by itself. Only a
		// failure the provider will not recover from on its own (a rejected
		// key, a 403 from a broken whitelist) is reported as an error.
		if TransientFailure(err) {
			c.logger.Warn("qBittorrent is not ready for the download-client category yet; retrying on the next reconciliation", "error", err)
			return nil
		}
		return err
	}

	changed, err := EnsureDownloadClient(ctx, c.own, spec)
	if err != nil {
		if TransientFailure(err) {
			c.logger.Warn("qBittorrent did not answer the download-client test; retrying on the next reconciliation", "error", err)
			return nil
		}
		return err
	}
	if changed {
		c.logger.Info("added download client", "implementation", ImplementationQBittorrent)
	}
	return nil
}

// downloadClientBinding returns the downloadClient provider Bloud wires, or
// false when the catalog declares no such provider.
func downloadClientBinding(state *configurator.AppState) (configurator.DownloadClientBinding, bool) {
	for _, binding := range state.Integrations.DownloadClients {
		if binding.App == QBittorrentAppID {
			return binding, true
		}
	}
	return configurator.DownloadClientBinding{}, false
}

// ensureDownloadCategory creates the category the download client stores.
// qBittorrent never creates a category by itself and refuses to tag a torrent
// with one it does not know, so the category has to exist first; only this
// consumer knows which category it wants, which is why the write happens here
// and not in the provider. A category that already exists is the normal
// second-run outcome and not an error: qBittorrent answers 409 Conflict for it
// (TorrentsController::createCategoryAction → SessionImpl::addCategory returns
// false for a known name, src/base/bittorrent/sessionimpl.cpp).
func (c *PVRConfigurator) ensureDownloadCategory(ctx context.Context, qbittorrent *appclient.Client) error {
	created, err := qbittorrent.POST(QBittorrentCategoryResource).
		Form(url.Values{"category": {c.app.DownloadCategory}}).
		OK(http.StatusOK).
		AlreadyDone(http.StatusConflict).
		Ensure(ctx)
	if err != nil {
		return fmt.Errorf("%s: creating the %s category in qBittorrent: %w", c.app.Name, c.app.DownloadCategory, err)
	}
	if created {
		c.logger.Info("created qBittorrent category", "category", c.app.DownloadCategory)
	}
	return nil
}
