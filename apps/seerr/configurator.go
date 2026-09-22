// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/servarr"
)

const (
	// appName is the node/secrets/log identity for Seerr.
	appName = "seerr"

	// jellyfinAppName is the catalog id of the media-server provider Seerr
	// onboards against. Its address, port and bootstrap admin password all come
	// from the resolved `mediaServer` binding; the password is the credential
	// apps/jellyfin publishes, and that account is never deleted.
	jellyfinAppName = "jellyfin"

	// jellyfinAdminUsername is the Jellyfin account apps/jellyfin creates for
	// Bloud (apps/jellyfin/configurator.go: bootstrapUsername).
	jellyfinAdminUsername = "bloud-bootstrap-admin"

	// configDirName holds Seerr's settings.json, SQLite database, logs and
	// cache; it is mounted at /app/config.
	configDirName    = "config"
	settingsFileName = "settings.json"

	// The image runs as uid 1000 (Dockerfile: USER node:node) and Bloud cannot
	// set a container user, so the mounted config dir must be world-writable or
	// the first boot cannot write settings.json, the database or logs/. Same
	// constraint and treatment as apps/authentik/server_configurator.go.
	configDirPerm = 0o777

	// adminEmail is only the login identity Seerr stores on the admin user it
	// creates from our Jellyfin login during onboarding.
	adminEmail = "bloud-admin@localhost"

	// PVR discovery. Seerr fulfils requests by handing them to a PVR, which it
	// stores as a DVR entry in one of two lists (settings.radarr,
	// settings.sonarr). Which PVRs to wire, where each one is and what its API
	// key is come from the resolved `pvr` bindings; the two catalog ids below
	// only select Seerr's own DVR vocabulary for each.
	radarrAppName = "radarr"
	sonarrAppName = "sonarr"

	// qualityProfilePath lists a PVR's quality profiles. A DVR entry names its
	// profile by id *and* name, and the ids are not a stable contract (the
	// images ship 1-6, but an admin can add profiles), so the entry is built
	// from the PVR's own list instead of a hardcoded id.
	qualityProfilePath = "/api/v3/qualityprofile"

	// preferredQualityProfile is the profile Bloud picks when the PVR has it:
	// both images ship HD-1080p (Radarr and Sonarr id 4), which is the
	// sensible default for a request-fulfilling setup. Any other set of
	// profiles falls back to the first one.
	preferredQualityProfile = "HD-1080p"

	// servarrAPIKeyHeader authenticates a Servarr API call; the key itself
	// comes from the PVR's binding (pkg/servarr: X-Api-Key, camelCase JSON,
	// string enums).
	servarrAPIKeyHeader = "X-Api-Key"

	// showsDirectory and moviesDirectory are the root folders of the shares
	// the PVRs and Seerr see at the same path. Every PVR's own configurator
	// creates its root folder in PreStart/PostStart before Seerr is
	// reconciled, which is what lets the DVR entry point at a fixed path.
	showsDirectory  = "/shows"
	moviesDirectory = "/movies"

	// minimumAvailabilityReleased is Radarr's "Released" availability: the
	// value Seerr's own Radarr form defaults to
	// (src/components/Settings/RadarrModal/index.tsx). RadarrSettings lists the
	// field as required (seerr-api.yml); omitting it makes the route reject
	// the whole body. SonarrSettings has no such property.
	minimumAvailabilityReleased = "released"
)

// pvrTarget is one PVR Seerr can fulfil requests through: the resolved binding
// that says where it is and what its API key is, plus Seerr's own vocabulary
// for it. Seerr keeps DVR entries in two lists (one per Servarr app type), so a
// target maps 1:1 onto one entry in service's list.
type pvrTarget struct {
	// binding is the resolved provider: the catalog knows from
	// metadata.yaml which PVRs integrate with Seerr, where they live and what
	// their API key is, so nothing about a provider is restated here.
	binding configurator.PVRBinding

	// service is the Seerr list this PVR's entry belongs to.
	service dvrService

	// name is the display name Seerr's own UI gives the entry.
	name string

	// activeDirectory is the PVR's root folder: the path of Bloud's shared
	// media mount for that kind of media, created by the PVR itself before
	// Seerr's PostStart runs (see reconcilePVRs).
	activeDirectory string

	// minimumAvailability is Radarr-only: RadarrSettings requires it
	// (seerr-api.yml) and Seerr's own Radarr form defaults it to "released"
	// (src/components/Settings/RadarrModal/index.tsx). Empty for Sonarr, whose
	// schema does not accept the field.
	minimumAvailability string

	// client reads the PVR's quality profiles from the host.
	client *appclient.Client
}

// pvrTargets resolves the PVRs Bloud wires into Seerr from the bindings the
// orchestrator handed this app. The order is stable (the order metadata.yaml
// declares the compatible apps), so a reconciliation always makes the same
// calls in the same order; what is per-PVR here is Seerr's own vocabulary for
// that PVR (its DVR list, its display name, its root folder), not its address.
func pvrTargets(state *configurator.AppState) ([]pvrTarget, []configurator.PVRBinding) {
	var (
		targets   []pvrTarget
		unhandled []configurator.PVRBinding
	)
	for _, binding := range state.Integrations.PVRs {
		var target pvrTarget
		switch binding.App {
		case sonarrAppName:
			target = pvrTarget{
				service:         dvrSonarr,
				name:            "Sonarr",
				activeDirectory: showsDirectory,
			}
		case radarrAppName:
			target = pvrTarget{
				service:             dvrRadarr,
				name:                "Radarr",
				activeDirectory:     moviesDirectory,
				minimumAvailability: minimumAvailabilityReleased,
			}
		default:
			// A PVR the catalog declares compatible but Seerr has no DVR
			// settings for cannot be wired: the settings are Seerr's, not the
			// provider's, so this is where they have to exist.
			unhandled = append(unhandled, binding)
			continue
		}
		target.binding = binding
		targets = append(targets, target)
	}
	return targets, unhandled
}

// Configurator handles Seerr's Bloud integration. PreStart makes the config
// directory usable by the non-root container and seeds the API key the
// admin-only onboarding calls need; PostStart drives Seerr's first-run wizard
// through its own API and wires whichever PVRs are installed, so users land on
// a usable request UI instead of a setup form. Every method is idempotent and
// runs on every reconciliation.
type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
	api     *seerrAPI
	// clients builds the host-side clients for the resolved providers. A
	// provider's address is only known once its binding is resolved, so they
	// are built per reconciliation rather than in the constructor.
	clients configurator.ClientFactory

	// baseURL is a test seam: when set, the API client resolves to it instead
	// of localhost:port. Never used to build request URLs by hand.
	baseURL string
}

// NewConfigurator creates a new Seerr configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = 5055
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:    port,
		secrets: deps.Secrets,
		logger:  logger.With("app", appName),
		clients: deps.HTTP,
	}
	c.api = newAPI(deps.HTTP, func() string {
		if c.baseURL != "" {
			return c.baseURL
		}
		return fmt.Sprintf("http://localhost:%d", c.port)
	})
	return c
}

// providerClient builds the client a configurator uses to reach a provider
// *from the host* (its own API calls): the binding's LocalURL, never the
// address stored for the app's containers.
func (c *Configurator) providerClient(ref configurator.ProviderRef) *appclient.Client {
	return c.clients.New(appclient.Spec{Name: ref.App, BaseURLFn: func() string {
		return ref.LocalURL
	}})
}

// mediaServerBinding returns the installed media-server provider, or false when
// the catalog declares none or it is not installed yet, which is the state that
// defers onboarding.
func mediaServerBinding(state *configurator.AppState) (configurator.MediaServerBinding, bool) {
	for _, binding := range state.Integrations.MediaServers {
		if binding.App == jellyfinAppName && binding.Installed {
			return binding, true
		}
	}
	return configurator.MediaServerBinding{}, false
}

// Name returns the node name this configurator manages.
func (c *Configurator) Name() string {
	return "apps-seerr"
}

// PreStart prepares the mounted config directory so the non-root container can
// boot. It deliberately writes nothing into it: Seerr owns settings.json and
// the file must not be pre-seeded (see INTEGRATION.md: a partial settings.json
// merges shallowly per top-level key and erases the defaults that gate Seerr's
// own startup). Nothing the container reads at boot is Bloud's to write, so
// changed is always false.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (bool, error) {
	dir := filepath.Join(state.DataPath, configDirName)
	if err := os.MkdirAll(dir, configDirPerm); err != nil {
		return false, fmt.Errorf("creating Seerr config directory: %w", err)
	}
	if err := managedfile.EnsureWritable(dir, configDirPerm); err != nil {
		return false, fmt.Errorf("making Seerr config directory writable: %w", err)
	}
	return false, nil
}

// PostStart drives Seerr's first-run wizard through its own API and keeps the
// PVR wiring in step with the installed stack. It is idempotent: an initialized
// instance skips the wizard (and the Jellyfin guard defers it rather than
// failing the node when the media server is not installed yet), but both paths
// end in the same PVR reconciliation, so installing a PVR after onboarding
// re-runs PostStart through the integration staleness path and the DVR entry
// appears then.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// The API key comes from the file Seerr wrote for itself at boot; Bloud
	// deliberately writes nothing into the config dir (see PreStart).
	settingsPath := filepath.Join(state.DataPath, configDirName, settingsFileName)

	public, err := c.api.settingsPublic(ctx)
	if err != nil {
		return fmt.Errorf("reading Seerr initialization state: %w", err)
	}
	if public.Initialized {
		// Nothing left to onboard: the PVRs are what can have changed since, and
		// the Jellyfin coupling is what can have *broken* since; nothing else
		// notices when the media server is replaced (see
		// reconcileJellyfinCoupling).
		if jellyfin, ok := mediaServerBinding(state); ok {
			c.reconcileJellyfinCoupling(ctx, settingsPath, c.providerClient(jellyfin.ProviderRef), jellyfin)
		}
		return c.reconcilePVRsWithStoredKey(ctx, state)
	}

	// Seerr's only non-interactive onboarding path starts with a Jellyfin
	// administrator login, so without Jellyfin there is nothing to do. A
	// missing provider is never an error: a later reconciliation re-runs
	// PostStart once Jellyfin is installed. (PVR wiring is deferred with it:
	// it needs the admin user the login below creates.)
	//
	// It is not silent, though: an instance that has never completed onboarding
	// serves its first-run wizard to anyone who can reach it, and whoever
	// completes it (pointing Seerr at a Jellyfin they control) becomes its
	// admin. With `sso.strategy: none` there is no gate in front of that, so the
	// operator has to know. The node is deliberately left RUNNING rather than
	// ERROR: ERROR is terminal, so a Seerr installed before its media server
	// would never converge once Jellyfin appeared.
	jellyfin, ok := mediaServerBinding(state)
	if !ok {
		c.logger.Warn("No media server is installed, so Seerr's onboarding is deferred; until one is, the instance is unconfigured and its setup wizard is reachable by anyone who can reach it",
			"mediaServer", jellyfinAppName)
		return nil
	}
	password := jellyfin.AdminPassword
	if password == "" {
		// The provider is installed but has not published its bootstrap
		// password yet (its PreStart has not run, or it is still converging):
		// onboarding is deferred rather than failed, and the next
		// reconciliation finds it.
		c.logger.Warn("the media server has not published its bootstrap admin password yet; Seerr onboarding is deferred",
			"mediaServer", jellyfin.App)
		return nil
	}

	apiKey, err := readAPIKey(settingsPath)
	if err != nil {
		return err
	}

	if err := c.api.loginWithJellyfin(ctx, jellyfinLogin{
		Username:   jellyfinAdminUsername,
		Password:   password,
		Hostname:   jellyfin.Node,
		Port:       jellyfin.Port,
		UseSSL:     false,
		URLBase:    "",
		Email:      adminEmail,
		ServerType: mediaServerTypeJellyfin,
	}); err != nil {
		return fmt.Errorf("creating the Seerr admin from the Jellyfin bootstrap admin: %w", err)
	}
	c.logger.Info("created the Seerr admin from the Jellyfin bootstrap admin")

	c.syncJellyfinLibraries(ctx, apiKey)

	if err := c.api.initialize(ctx, apiKey); err != nil {
		return fmt.Errorf("completing Seerr setup (settings/initialize): %w", err)
	}

	after, err := c.api.settingsPublic(ctx)
	if err != nil {
		return fmt.Errorf("confirming Seerr initialization: %w", err)
	}
	if !after.Initialized {
		return fmt.Errorf("seerr: settings/initialize did not mark the instance initialized (observed initialized=%t)", after.Initialized)
	}

	// The PVR step is the last one: it needs the admin user the Jellyfin login
	// created and the instance to be initialized, so a deferred onboarding
	// defers PVR wiring with it.
	if err := c.reconcilePVRs(ctx, state, apiKey); err != nil {
		return err
	}

	c.logger.Info("Seerr onboarding complete")
	return nil
}

// syncJellyfinLibraries runs the library step best-effort: a Jellyfin with no
// libraries yet, or a scan that fails, leaves the instance usable (an admin can
// toggle libraries in the UI), so it must never fail the node, but it is the
// difference between a working request catalogue and an empty one, so a failure
// is surfaced as a warning.
func (c *Configurator) syncJellyfinLibraries(ctx context.Context, apiKey string) {
	if err := c.api.syncJellyfinLibraries(ctx, apiKey); err != nil {
		c.logger.Warn("Jellyfin library sync failed; libraries can be enabled in Seerr's settings", "error", err)
	}
}

// pvrQualityProfile is one entry of a PVR's /api/v3/qualityprofile list.
type pvrQualityProfile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// reconcilePVRs makes Seerr able to fulfil requests by giving it a DVR entry
// for every PVR that is installed, repairing one that drifted and pruning the
// entry of a PVR that is gone.
//
// Ordering is the framework's, not this function's: the optional `pvr`
// integration in metadata.yaml makes the engine bring a PVR up before Seerr,
// and the PVR's own configurator created its root folder (/shows, /movies)
// before its container was declared healthy. That is why activeDirectory can be
// a fixed path here: the folder exists by the time this runs, and the same
// shared mount is what both apps see.
//
// Every step is best-effort in the same way the Jellyfin library sync is: a PVR
// that is not installed, a PVR whose key is not published yet, or a PVR that is
// still booting leaves the instance usable (requests simply cannot be fulfilled
// yet) and is retried on the next reconciliation. The one case that fails the
// node is a PVR that is up and rejects us: that is a misconfiguration an
// operator has to see.
func (c *Configurator) reconcilePVRs(ctx context.Context, state *configurator.AppState, seerrKey string) error {
	targets, unhandled := pvrTargets(state)
	for _, binding := range unhandled {
		c.logger.Warn("no Seerr DVR settings for this PVR; skipping it", "pvr", binding.App)
	}
	for _, pvr := range targets {
		pvr.client = c.providerClient(pvr.binding.ProviderRef)
		if err := c.reconcilePVR(ctx, pvr, seerrKey); err != nil {
			return err
		}
	}
	return nil
}

// reconcilePVRsWithStoredKey is reconcilePVRs for a PostStart that did not just
// onboard the instance, so no API key is in hand. An unreadable key is a
// warning and no wiring: without it no admin call is possible, and there is no
// wizard here that could not run: the onboarding path is where the file is
// required.
func (c *Configurator) reconcilePVRsWithStoredKey(ctx context.Context, state *configurator.AppState) error {
	settingsPath := filepath.Join(state.DataPath, configDirName, settingsFileName)
	apiKey, err := readAPIKey(settingsPath)
	if err != nil {
		c.logger.Warn("cannot read Seerr's API key; skipping PVR wiring", "error", err)
		return nil
	}
	return c.reconcilePVRs(ctx, state, apiKey)
}

// reconcilePVR brings one PVR's DVR entry in line with the running stack.
func (c *Configurator) reconcilePVR(ctx context.Context, pvr pvrTarget, seerrKey string) error {
	pvrID := pvr.binding.App
	if !pvr.binding.Installed {
		return c.prunePVR(ctx, pvr, seerrKey)
	}

	key := pvr.binding.APIKey
	if key == "" {
		// The PVR is installed but has not published its key yet (its PreStart
		// has not run, or it is still converging): wire nothing. Writing an
		// entry with an empty key would only have to be corrected later, and
		// the next reconciliation picks the key up.
		c.logger.Warn("PVR has not published its API key yet; skipping it", "pvr", pvrID)
		return nil
	}

	profile, err := c.pvrQualityProfile(ctx, pvr, key)
	if err != nil {
		// A key the PVR rejects is a real misconfiguration (the key the PVR
		// published is the source of truth for it) and is surfaced with the
		// provider and the status. Anything else (a profile list that is not
		// there yet while the instance finishes booting) is a retry.
		if status := appclient.StatusOf(err); status >= 400 && status < 500 {
			return fmt.Errorf("wiring Seerr to %s: %w", pvrID, err)
		}
		c.logger.Warn("PVR quality profiles are not listable yet; skipping it",
			"pvr", pvrID, "error", err)
		return nil
	}

	existing, err := c.api.listDVRs(ctx, pvr.service, seerrKey)
	if err != nil {
		// The PVR is up, so this is not the "not installed" case: a Seerr that
		// cannot be read or written here is a real problem, not a retry.
		return fmt.Errorf("wiring Seerr to %s: reading Seerr's %s settings: %w", pvrID, pvr.service, err)
	}

	desired := dvrSettingsFor(pvr, key, profile)
	for _, entry := range existing {
		if entry.Hostname != pvr.binding.Node {
			// Not ours: an entry an admin added by hand (Seerr's own UI
			// defaults to localhost) is theirs to keep, and matching it by
			// name would make two different targets look like one.
			continue
		}
		if entry.sameWiring(desired) {
			return nil
		}
		// POST always appends (api.go: createDVR), so a drifted entry is
		// corrected in place with its id.
		desired.ID = entry.ID
		if err := c.api.updateDVR(ctx, pvr.service, seerrKey, desired); err != nil {
			if servarr.TransientFailure(err) {
				c.logger.Warn("Seerr did not answer while repairing the PVR entry; retrying on the next reconciliation",
					"pvr", pvrID, "error", err)
				return nil
			}
			return fmt.Errorf("wiring Seerr to %s: repairing its %s entry: %w", pvrID, pvr.service, err)
		}
		c.logger.Info("repaired the Seerr PVR entry", "pvr", pvrID, "id", entry.ID)
		return nil
	}

	if err := c.api.createDVR(ctx, pvr.service, seerrKey, desired); err != nil {
		if servarr.TransientFailure(err) {
			c.logger.Warn("Seerr did not answer while adding the PVR entry; retrying on the next reconciliation",
				"pvr", pvrID, "error", err)
			return nil
		}
		return fmt.Errorf("wiring Seerr to %s: %w", pvrID, err)
	}
	c.logger.Info("added the PVR to Seerr", "pvr", pvrID, "hostname", pvr.binding.Node)
	return nil
}

// prunePVR removes the DVR entry Bloud added for a PVR that is no longer
// installed, so Seerr does not keep handing requests to a hostname that no
// longer resolves.
//
// The entry is identified by the address Bloud wrote (the provider's container
// name on apps-net), which is what distinguishes it from an entry an admin
// added by hand: Seerr's own UI defaults to localhost, so a foreign entry does
// not collide. The binding still carries that address when the provider is
// gone, because it comes from the provider's catalog metadata, which outlives
// its installation.
//
// Best effort by design: the PVR is gone, so a Seerr that cannot be read here is
// a warning for the next reconciliation, never a node failure.
func (c *Configurator) prunePVR(ctx context.Context, pvr pvrTarget, seerrKey string) error {
	existing, err := c.api.listDVRs(ctx, pvr.service, seerrKey)
	if err != nil {
		c.logger.Warn("could not read Seerr's PVR settings to prune a stale entry", "pvr", pvr.binding.App, "error", err)
		return nil
	}
	for _, entry := range existing {
		if entry.Hostname != pvr.binding.Node {
			continue
		}
		if err := c.api.deleteDVR(ctx, pvr.service, seerrKey, entry.ID); err != nil {
			return fmt.Errorf("wiring Seerr to %s: pruning its stale %s entry: %w", pvr.binding.App, pvr.service, err)
		}
		c.logger.Info("removed the stale Seerr PVR entry", "pvr", pvr.binding.App, "id", entry.ID)
	}
	return nil
}

// dvrSettingsFor renders the entry Bloud keeps for a PVR. Seerr stores the
// posted body verbatim (api.go), so this is the whole payload: the fields
// Seerr's own UI sends for a PVR it manages.
func dvrSettingsFor(pvr pvrTarget, apiKey string, profile pvrQualityProfile) dvrSettings {
	dvr := dvrSettings{
		Name:              pvr.name,
		Hostname:          pvr.binding.Node,
		Port:              pvr.binding.Port,
		APIKey:            apiKey,
		UseSSL:            false,
		BaseURL:           "",
		ActiveProfileID:   profile.ID,
		ActiveProfileName: profile.Name,
		ActiveDirectory:   pvr.activeDirectory,
		IsDefault:         true,
		Is4k:              false,
		SyncEnabled:       true,
		Tags:              []int{},
		// Empty for Sonarr, whose schema does not carry the field.
		MinimumAvailability: pvr.minimumAvailability,
	}
	if pvr.service == dvrSonarr {
		// SonarrSettings-only fields: the series types are the values Seerr's
		// own Sonarr setup form defaults to, and season folders match a
		// library laid out one folder per season.
		dvr.SeriesType = seriesTypeStandard
		dvr.AnimeSeriesType = seriesTypeStandard
		dvr.EnableSeasonFolders = true
	}
	return dvr
}

// pvrQualityProfile picks the profile a DVR entry should use: the one named
// preferredQualityProfile when the PVR has it, the first one otherwise. An
// empty list is an error (the entry cannot be built without an id), which the
// caller treats as "not ready yet" rather than a node failure.
func (c *Configurator) pvrQualityProfile(ctx context.Context, pvr pvrTarget, apiKey string) (pvrQualityProfile, error) {
	var profiles []pvrQualityProfile
	if err := pvr.client.GET(qualityProfilePath).
		Header(servarrAPIKeyHeader, apiKey).
		OK(http.StatusOK).
		NoRetry().
		DoInto(ctx, &profiles); err != nil {
		return pvrQualityProfile{}, err
	}
	if len(profiles) == 0 {
		return pvrQualityProfile{}, fmt.Errorf("%s: %s returned no quality profiles", pvr.binding.App, qualityProfilePath)
	}
	for _, profile := range profiles {
		if profile.Name == preferredQualityProfile {
			return profile, nil
		}
	}
	return profiles[0], nil
}

// Remove is a no-op for the Seerr configurator; container and data removal are
// handled at a higher level by the orchestrator.
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}

// readAPIKey reads the API key Seerr generated for itself into settings.json
// (server/lib/settings/index.ts: a missing key is minted during load and the
// file is saved before the HTTP listener opens). Seerr honours a non-empty
// main.apiKey verbatim, so an empty or unreadable file means the admin-only
// onboarding calls cannot be authenticated: an error naming the file rather
// than a silent unauthenticated attempt.
func readAPIKey(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	var doc struct {
		Main struct {
			APIKey string `json:"apiKey"`
		} `json:"main"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}
	if doc.Main.APIKey == "" {
		return "", fmt.Errorf("%s has no main.apiKey; Seerr onboarding cannot authenticate", path)
	}
	return doc.Main.APIKey, nil
}
