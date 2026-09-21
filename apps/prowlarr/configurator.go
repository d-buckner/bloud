// SPDX-License-Identifier: AGPL-3.0-only

// Package prowlarr wires the Prowlarr indexer manager into Bloud: it
// pre-seeds the instance's config.xml so the app never offers its own login
// form, verifies through the app's API that external authentication is still
// in effect, and pushes Prowlarr's indexers into the PVR siblings that are
// installed.
package prowlarr

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
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/servarr"
)

const (
	// appName is the catalog id and the identity the API client logs under.
	appName = "prowlarr"

	// nodeName is the graph node / container name the host-agent reconciles.
	nodeName = "apps-prowlarr"

	// apiPath is Prowlarr's versioned API root. Prowlarr still answers on v1,
	// unlike Sonarr and Radarr which moved to v3.
	apiPath = "api/v1"

	// defaultPort is Prowlarr's web port, published on the host by Traefik's
	// route target.
	defaultPort = 9696

	// configFileName is the Servarr settings document inside the app's data
	// directory, mounted at /config in the container.
	configFileName = "config.xml"
)

const (
	// The PVRs Prowlarr syncs its indexers into. Each catalog id is also the
	// sibling's data directory under BloudDataPath and its container-name
	// suffix on apps-net; each port is the one the sibling publishes:
	// apps/sonarr/metadata.yaml (`port: 8989`) and apps/radarr/metadata.yaml
	// (`port: 7878`). The duplication is the accepted cost of wiring two
	// catalog entries from an app: the framework does not hand a consumer its
	// providers' addresses, so a provider port change is a greppable edit in
	// both the provider's metadata.yaml and here (see INTEGRATION.md).
	sonarrAppID = "sonarr"
	sonarrPort  = 8989
	radarrAppID = "radarr"
	radarrPort  = 7878

	// sonarrImplementation/radarrImplementation are Prowlarr's own names for
	// the two PVR contracts; the config contracts are what its applications
	// resource validates an application document against.
	sonarrImplementation = "Sonarr"
	sonarrConfigContract = "SonarrSettings"
	radarrImplementation = "Radarr"
	radarrConfigContract = "RadarrSettings"

	// syncLevelFullSync makes Prowlarr push every indexer to the PVR and keep
	// it in step afterwards (ApplicationSyncLevel.FullSync), the one level
	// that also removes an indexer from the PVR again.
	syncLevelFullSync = "fullSync"

	// siblingURLTemplate is how one container on apps-net addresses another:
	// every node is named "apps-<catalog id>". "localhost" resolves to the
	// Prowlarr container itself, which is exactly the trap Prowlarr's own
	// schema defaults fall into.
	siblingURLTemplate = "http://apps-%s:%d"

	// fieldProwlarrURL/fieldBaseURL/fieldAPIKey are the PVR settings fields
	// Bloud owns in an application document. The rest of the document keeps
	// whatever Prowlarr's schema defaults are.
	fieldProwlarrURL = "prowlarrUrl"
	fieldBaseURL     = "baseUrl"
	fieldAPIKey      = "apiKey"

	// pvrProbePath is Servarr's anonymous readiness route, the same one the
	// container healthchecks use: 200 once the web host is up, 503 while it
	// boots.
	pvrProbePath = "/ping"

	// probeTimeout bounds a sibling probe: a PVR that is not installed must
	// delay the reconciliation by seconds, not by the request default.
	probeTimeout = 5 * time.Second
)

// Configurator handles Prowlarr configuration.
type Configurator struct {
	port   int
	logger *slog.Logger
	// api is the shared Servarr client for the instance's own API.
	api *servarr.Client
	// clients builds the application-sync client, which needs the instance key
	// read from config.xml and so is built per reconciliation.
	clients configurator.ClientFactory
	// ownURL resolves the instance's own base URL (localhost:<port>, or the
	// test seam).
	ownURL func() string
	// targets are the PVR siblings, each with a probe pointed at the sibling's
	// published host port.
	targets []pvrTarget

	// baseURL is a test seam: when set, the own-API clients resolve to it
	// instead of localhost:port.
	baseURL string
	// sonarrBaseURL/radarrBaseURL are the matching seams for the sibling
	// probes.
	sonarrBaseURL string
	radarrBaseURL string
}

// pvrTarget is one PVR sibling Bloud wires Prowlarr to.
type pvrTarget struct {
	// appID is the catalog id: the sibling's data directory under
	// BloudDataPath and its container-name suffix on apps-net.
	appID string
	// port is the port the sibling's metadata.yaml publishes.
	port int
	// implementation/configContract are Prowlarr's names for the sibling's
	// application contract.
	implementation string
	configContract string
	// probe asks the sibling's published host port whether it is installed.
	probe *appclient.Client
}

// NewConfigurator creates a new Prowlarr configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:    port,
		logger:  logger.With("app", appName),
		clients: deps.HTTP,
	}
	c.ownURL = func() string {
		if c.baseURL != "" {
			return c.baseURL
		}
		return fmt.Sprintf("http://localhost:%d", c.port)
	}
	c.api = servarr.NewClient(deps.HTTP, appName, apiPath, c.ownURL)

	// The probes leave the Prowlarr container: they ask the host's published
	// ports whether a PVR exists yet.
	c.targets = []pvrTarget{
		{
			appID:          sonarrAppID,
			port:           sonarrPort,
			implementation: sonarrImplementation,
			configContract: sonarrConfigContract,
			probe: deps.HTTP.New(appclient.Spec{Name: sonarrAppID, BaseURLFn: func() string {
				if c.sonarrBaseURL != "" {
					return c.sonarrBaseURL
				}
				return fmt.Sprintf("http://localhost:%d", sonarrPort)
			}}),
		},
		{
			appID:          radarrAppID,
			port:           radarrPort,
			implementation: radarrImplementation,
			configContract: radarrConfigContract,
			probe: deps.HTTP.New(appclient.Spec{Name: radarrAppID, BaseURLFn: func() string {
				if c.radarrBaseURL != "" {
					return c.radarrBaseURL
				}
				return fmt.Sprintf("http://localhost:%d", radarrPort)
			}}),
		},
	}
	return c
}

func (c *Configurator) Name() string {
	return nodeName
}

// configPath returns the host path of the instance's config.xml.
func (c *Configurator) configPath(state *configurator.AppState) string {
	return filepath.Join(state.DataPath, "config", configFileName)
}

// PreStart creates the instance's config and shared download directories,
// then puts the instance into Servarr's External authentication mode before
// the container starts. Prowlarr mounts no library (it moves no files; it
// only manages indexers), so no media directory is created for it. It returns
// changed=true only when config.xml content actually changed, because that
// flag recreates the container: directory creation alone must never report a
// change.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (bool, error) {
	dirs := []string{
		filepath.Join(state.DataPath, "config"),
		filepath.Join(state.BloudDataPath, "downloads"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return false, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// The container runs as LSIO's `abc` (PUID=1000), which under rootless
	// podman maps to a host subuid the host agent is neither owner nor group
	// member of, while the container's init only chowns its own /config. Two
	// directories need to be opened up:
	//
	//   - The config dir. pkg/managedfile writes config.xml through a temp file
	//     created *inside* the target directory, and after the first boot that
	//     directory belongs to abc's subuid, so without world-write the next
	//     auth repair (a UI settings save rewrites config.xml) fails with
	//     EACCES, and a PreStart failure is terminal for the node.
	//   - The shared downloads dir, which Prowlarr itself never mounts but the
	//     PVRs do: whoever mounts it last owns its mode, and the container that
	//     ends up running the import has to be able to write it.
	configDir := filepath.Join(state.DataPath, "config")
	if err := os.Chmod(configDir, 0o777); err != nil {
		return false, fmt.Errorf("making the config directory writable: %w", err)
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

// Remove is a no-op for the Prowlarr configurator; container and data removal
// are handled at a higher level by the orchestrator.
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}

// PostStart verifies through Prowlarr's own API that external authentication
// is in effect, repairing it when a settings save (from the UI or an API
// client) rewrote config.xml, then reconciles the application-sync list so
// Prowlarr's indexers reach the PVR siblings that are installed. Idempotent on
// every reconciliation.
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

	return c.syncPvrApplications(ctx, state, key)
}

// syncPvrApplications reconciles the instance's application-sync list with the
// PVR siblings that are installed, so Prowlarr's indexers reach whatever PVRs
// exist without the user configuring each one by hand. Every link is optional:
// a sibling that does not answer is skipped (and pruned when Bloud had wired it
// before), and the step is a read, a compare, and a write only on difference.
func (c *Configurator) syncPvrApplications(ctx context.Context, state *configurator.AppState, apiKey string) error {
	apps := newApplicationsAPI(c.clients, c.ownURL, apiKey)
	existing, err := apps.listApplications(ctx)
	if err != nil {
		return err
	}

	// One pass of the instance's own connection tests, fetched lazily: the
	// verdicts matter only where an entry already looks converged but its key
	// reads back masked (see applicationMatches), and asking costs a live
	// connection test per stored entry.
	var (
		verdicts     map[int]bool
		verdictsErr  error
		verdictsRead bool
	)
	testStored := func() (map[int]bool, error) {
		if !verdictsRead {
			verdictsRead = true
			verdicts, verdictsErr = apps.testStoredApplications(ctx)
		}
		return verdicts, verdictsErr
	}

	for _, target := range c.targets {
		if err := c.reconcilePvrApplication(ctx, apps, existing, testStored, target, state); err != nil {
			return err
		}
	}
	return nil
}

// reconcilePvrApplication wires one PVR into Prowlarr, or skips it when that
// PVR is not installed.
//
// The one failure policy worth stating: a *transient* failure of the sibling or
// of Prowlarr itself (a connection failure, a 5xx, a 429; see
// servarr.TransientFailure) leaves the link as it is and returns nil, because
// ERROR is terminal in the orchestrator: failing the node over a provider that
// is restarting would leave the app "failed" until an operator reinstalls it,
// while the next reconciliation would have wired the link by itself. A 4xx is a
// real fault (a rejected key, an invalid document) and is reported.
func (c *Configurator) reconcilePvrApplication(
	ctx context.Context,
	apps *applicationsAPI,
	existing []application,
	testStored func() (map[int]bool, error),
	target pvrTarget,
	state *configurator.AppState,
) error {
	current, found := findApplication(existing, target.implementation, target.baseURL())

	// Provider discovery. A PVR that does not answer on its published port is
	// not installed (or not up yet), so this link is skipped rather than
	// failed; a later reconciliation picks it up. An entry Bloud wired for a
	// PVR that is now gone is pruned, so an uninstall does not leave Prowlarr
	// pushing indexers at a hostname that no longer resolves.
	if !c.pvrAvailable(ctx, target) {
		if !found {
			return nil
		}
		if _, err := apps.deleteApplication(ctx, current.ID); err != nil {
			if err := c.transientOrError(err, target,
				"Prowlarr did not answer while pruning the application for an uninstalled PVR"); err == nil {
				return nil
			}
			return fmt.Errorf("pruning the %s application (id %d) after %s disappeared: %w",
				target.implementation, current.ID, target.appID, err)
		}
		c.logger.Info("pruned the Prowlarr application for an uninstalled PVR",
			"pvr", target.appID, "application", current.Name, "id", current.ID)
		return nil
	}

	siblingKey, err := c.siblingAPIKey(state, target)
	if err != nil {
		return err
	}
	desired := c.desiredApplication(target, siblingKey)

	if found && applicationMatches(current, desired) {
		// The stored key reads back masked, so matching fields cannot prove the
		// entry still works: a PVR whose data was purged and reinstalled keeps
		// its address but mints a fresh key, and Prowlarr would then push
		// indexers with a key that PVR rejects, silently, for as long as the
		// entry exists. The instance's own test of the entries it holds is the
		// only readable verdict, so a match counts as converged only when
		// Prowlarr either confirms it or cannot be asked at all.
		verdicts, err := testStored()
		if err != nil {
			return c.transientOrError(err, target,
				"Prowlarr did not answer its own application test; accepting the stored entry for this pass")
		}
		if valid, known := verdicts[current.ID]; !known || valid {
			return nil
		}
		c.logger.Info("Prowlarr reports its stored application as unreachable; re-pushing it",
			"pvr", target.appID, "application", current.Name, "id", current.ID)
	}

	if found {
		// Repair in place. The document is PUT to the entry the instance holds
		// (RestPutById), which runs the same validator and connection test as a
		// create: nothing the operator owns on it is lost the way a
		// delete-then-create would lose it, and re-testing the document under
		// its own name is not rejected as a duplicate.
		document := mergeApplication(current, desired)
		if err := apps.updateApplication(ctx, document); err != nil {
			return c.transientOrError(err, target,
				"Prowlarr or the PVR did not answer while updating the application")
		}
		c.logger.Info("updated the Prowlarr application",
			"pvr", target.appID, "application", document.Name, "id", document.ID)
		return nil
	}

	// Test before creating: /applications/test is the endpoint that validates
	// the document against the PVR and its own schema without saving anything,
	// so a PVR that rejects the key or is unreachable leaves no half-written
	// entry behind, and the failure names the sibling and the status. Prowlarr
	// runs the same test again inside the create, which is why a rejection can
	// surface there too.
	if err := apps.testApplication(ctx, desired); err != nil {
		if duplicateName(err) {
			// Another entry already holds the name Bloud wants. With the
			// reserved name that means an entry the operator created under it,
			// theirs to keep, and not a fault: Bloud's link is reported as
			// unwired instead of failing the node.
			c.logger.Warn("another Prowlarr application already holds this name; leaving it in place",
				"pvr", target.appID, "application", desired.Name)
			return nil
		}
		return c.transientOrError(err, target,
			"the PVR did not answer the Prowlarr application test")
	}
	created, err := apps.createApplication(ctx, desired)
	if err != nil {
		return c.transientOrError(err, target,
			"Prowlarr did not answer while creating the application")
	}
	if !created {
		// createApplication classifies the shared validator's "Should be
		// unique" as already done: another entry already holds the name Bloud
		// wants. That entry may belong to the operator (findApplication did not
		// match it, so it is not at Bloud's address), so it is left alone and
		// the miss is logged instead of claimed as a wiring.
		c.logger.Warn("another Prowlarr application already holds this name; leaving it in place",
			"pvr", target.appID, "application", desired.Name)
		return nil
	}
	c.logger.Info("wired the PVR into Prowlarr", "pvr", target.appID, "application", desired.Name)
	return nil
}

// desiredApplication is the application document Bloud wants Prowlarr to hold
// for one PVR: a full indexer sync into the sibling over apps-net, authenticated
// with the sibling's own key.
//
// prowlarrUrl and baseUrl are always written explicitly: their schema defaults
// are http://localhost:*, which inside the Prowlarr container is Prowlarr
// itself, so a synced indexer would point the PVR at a dead address.
// syncCategories/animeSyncCategories are omitted so the instance keeps its own
// schema defaults.
func (c *Configurator) desiredApplication(target pvrTarget, siblingAPIKey string) application {
	return application{
		Name:           target.implementation + managedNameSuffix,
		Implementation: target.implementation,
		ConfigContract: target.configContract,
		SyncLevel:      syncLevelFullSync,
		Tags:           []int{},
		Fields: []applicationField{
			{Name: fieldProwlarrURL, Value: fmt.Sprintf(siblingURLTemplate, appName, c.port)},
			{Name: fieldBaseURL, Value: target.baseURL()},
			{Name: fieldAPIKey, Value: siblingAPIKey},
		},
	}
}

// mergeApplication returns the document to write back for an entry the instance
// already holds: Bloud's fields on the operator's entry. The id selects it, and
// the name and tags survive the write: the name is the field Prowlarr's UI lets
// the operator change (which is why findApplication does not match on it) and
// the tags are not Bloud's to reset.
func mergeApplication(current, desired application) application {
	merged := desired
	merged.ID = current.ID
	merged.Name = current.Name
	merged.Tags = current.Tags
	return merged
}

// managedNameSuffix marks the application entries Bloud creates as its own.
// The name is unique per entry in Prowlarr, so taking the implementation's
// name ("Sonarr") would collide with an entry the operator added under that
// name and leave Bloud's own PVR unwired; a distinct name lets both exist.
// Entries Bloud wrote before, and entries the operator renamed, are recognized
// by their address instead (see findApplication) and keep their name.
const managedNameSuffix = " (Bloud)"

// baseURL is the address Bloud stores for a sibling: its container name on
// apps-net and the port its metadata publishes.
func (t pvrTarget) baseURL() string {
	return fmt.Sprintf(siblingURLTemplate, t.appID, t.port)
}

// findApplication returns the entry Bloud owns for one PVR: the one whose
// implementation *and* base address are the ones Bloud writes. The address has
// to be part of the identity: an operator may keep their own entry for the
// same implementation (a remote Sonarr, a second instance), and the prune
// deletes whatever this function returns. The name is not part of it, because
// the name is the one field the operator is free to change in Prowlarr's UI.
func findApplication(apps []application, implementation, baseURL string) (application, bool) {
	for _, app := range apps {
		if app.Implementation == implementation && app.field(fieldBaseURL) == baseURL {
			return app, true
		}
	}
	return application{}, false
}

// applicationMatches reports whether an entry already carries the state Bloud
// wants: same contract, same sync level, same container addresses, same sibling
// key.
//
// The name is deliberately not compared: it is the operator's field (see
// findApplication), so an entry they renamed is not drift to undo.
//
// The apiKey field is special. Prowlarr masks every non-empty
// PrivacyLevel.ApiKey field on read (see maskedSecret in api.go), so a stored
// key can never be compared verbatim. A masked value therefore counts as
// matching (treating it as drift would rewrite the entry on every
// reconciliation) and the caller confirms through Prowlarr's own test of its
// stored entries that the key still works before it accepts a masked match as
// converged (see testStoredApplications). A readable value (an empty or absent
// key) is compared as-is, so an entry that is missing the key is repaired.
func applicationMatches(current, desired application) bool {
	if current.Implementation != desired.Implementation ||
		current.ConfigContract != desired.ConfigContract ||
		current.SyncLevel != desired.SyncLevel {
		return false
	}
	for _, want := range desired.Fields {
		got := current.field(want.Name)
		if want.Name == fieldAPIKey && got == maskedSecret {
			continue
		}
		if got != want.Value {
			return false
		}
	}
	return true
}

// transientOrError is the reconcile's failure policy in one place: a
// *transient* failure of the PVR or of Prowlarr itself (a connection failure, a
// 5xx, a 429; see servarr.TransientFailure) is logged and returns nil, so the
// phase ends and the next reconciliation retries the link. Anything else (a
// rejected key, an invalid document) is returned, because ERROR is terminal in
// the orchestrator and a real fault has to surface.
func (c *Configurator) transientOrError(err error, target pvrTarget, message string) error {
	if !servarr.TransientFailure(err) {
		return err
	}
	c.logger.Warn(message, "pvr", target.appID, "error", err)
	return nil
}

// pvrAvailable reports whether a sibling PVR answers on the host. The probe
// leaves the Prowlarr container: it asks the host's published port whether the
// PVR exists yet. Any failure (connection refused, timeout, a non-200 status)
// means "not usable yet", including a PVR that is installed but still booting.
func (c *Configurator) pvrAvailable(ctx context.Context, target pvrTarget) bool {
	// The probe bounds itself with a deadline rather than a per-call client
	// timeout, so an absent sibling costs seconds, not the request default.
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	err := target.probe.GET(pvrProbePath).
		OK(http.StatusOK).
		NoRetry().
		Exec(probeCtx)
	if err != nil {
		c.logger.Info("PVR is not installed; skipping its Prowlarr application",
			"pvr", target.appID, "error", err)
		return false
	}
	return true
}

// siblingAPIKey reads a PVR's own API key from the config.xml its PreStart
// wrote (<BloudDataPath>/<app>/config/config.xml, the same file convention
// apps/navidrome uses to read Authentik's token). A sibling that answers on its
// port but has no key on disk cannot be wired, and that is a misconfiguration
// rather than a race, so it is an error instead of a skip.
func (c *Configurator) siblingAPIKey(state *configurator.AppState, target pvrTarget) (string, error) {
	path := servarr.SiblingConfigPath(state.BloudDataPath, target.appID)
	key, err := servarr.APIKey(path)
	if err != nil {
		return "", fmt.Errorf("reading the %s API key: %w", target.appID, err)
	}
	if key == "" {
		return "", fmt.Errorf("no ApiKey in %s; %s answers but its PreStart has not run", path, target.appID)
	}
	return key, nil
}
