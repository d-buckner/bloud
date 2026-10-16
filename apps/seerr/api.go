// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"bytes"
	"context"
	"net/http"
	"strconv"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// Everything Seerr-specific about Bloud's HTTP conversation with the app lives
// in this file: paths, payloads, field names. They are taken from
// seerr-team/seerr v3.5.0 (the pinned image tag), cited per method, so a future
// version bump can re-verify each one against the same files. The v3.4.1 to
// v3.5.0 bump was checked against this list: five of the eight files below
// changed, and two of those changed a route Bloud calls.
// server/routes/settings/index.ts moved the library endpoints, which is what
// syncJellyfinLibraries is written against. server/routes/auth.ts made
// POST /auth/jellyfin return a trimmed user and classify an unreachable media
// server; Bloud discards that body and matches only the already-configured 500.
//
//	server/routes/index.ts             route registration + GET /settings/public
//	server/routes/auth.ts              POST /auth/jellyfin
//	server/routes/settings/index.ts    /settings/jellyfin*, /settings/initialize
//	                                   and the mounts of the two DVR routers (44-51)
//	server/routes/settings/radarr.ts   GET/POST/PUT/DELETE /settings/radarr
//	server/routes/settings/sonarr.ts   GET/POST/PUT/DELETE /settings/sonarr
//	server/lib/settings/index.ts       PublicSettings / JellyfinSettings fields,
//	                                   DVRSettings and its Radarr/Sonarr variants (68-104)
//	src/components/Setup/JellyfinSetup.tsx        the login payload the wizard sends
//	src/components/Settings/SettingsJellyfin.tsx  the library sync/enable calls
const (
	// apiRoot prefixes every route below (server/routes/index.ts).
	apiRoot = "/api/v1"

	// apiKeyHeader is Seerr's credential for non-interactive admin calls:
	// server/middleware/auth.ts checkUser matches it verbatim against
	// settings.main.apiKey and treats a match as admin user id 1. That is how
	// the configurator performs admin-only onboarding steps without owning a
	// login session.
	apiKeyHeader = "X-API-Key"

	// mediaServerTypeJellyfin is MediaServerType.JELLYFIN
	// (server/constants/server.ts: PLEX = 1, JELLYFIN = 2, EMBY = 3).
	mediaServerTypeJellyfin = 2

	// alreadyConfiguredError is the rejection POST /auth/jellyfin answers with
	// when Jellyfin is configured and the caller sends a hostname
	// (server/routes/auth.ts). It doubles as the already-done signal for a
	// re-run whose earlier attempt created the admin.
	alreadyConfiguredError = "Jellyfin hostname already configured"

	// seriesTypeStandard is the "standard" value of SonarrSettings' seriesType
	// and animeSeriesType enums (server/lib/settings/index.ts:94-95; the other
	// values are daily and anime).
	seriesTypeStandard = "standard"
)

// dvrService names one of Seerr's two PVR lists. The value is the path segment
// under /api/v1/settings, which is also the name of the route file
// (server/routes/settings/index.ts:44-51).
type dvrService string

const (
	// dvrRadarr and dvrSonarr are the two lists a DVR entry can live in; a
	// Servarr app belongs to exactly one of them.
	dvrRadarr dvrService = "radarr"
	dvrSonarr dvrService = "sonarr"
)

// path returns the admin route holding this list, e.g. /api/v1/settings/radarr.
func (s dvrService) path() string {
	return apiRoot + "/settings/" + string(s)
}

// dvrSettings is one entry of a DVR list: what POST/PUT store and what GET
// returns (server/lib/settings/index.ts:68-87 DVRSettings, plus the Sonarr-only
// fields at 93-104). Bloud writes the subset below and reads the same shape
// back for the drift comparison; Seerr stores the body verbatim, so a field
// left out of the payload is a field Seerr leaves undefined.
//
// The Sonarr-only fields carry omitempty because the Radarr payload must not
// contain them: RadarrSettings has no seriesType.
type dvrSettings struct {
	// ID is assigned by Seerr on create (server/routes/settings/radarr.ts:15-37
	// appends with the next id). Zero means "not stored yet", so it is omitted
	// from a create payload; an update fills it from the entry it is replacing.
	// (Seerr's first entry legitimately has id 0: an update of it sends no id
	// in the body, which is fine because the route takes the id from the URL.)
	ID int `json:"id,omitempty"`

	Name              string `json:"name"`
	Hostname          string `json:"hostname"`
	Port              int    `json:"port"`
	APIKey            string `json:"apiKey"`
	UseSSL            bool   `json:"useSsl"`
	BaseURL           string `json:"baseUrl"`
	ActiveProfileID   int    `json:"activeProfileId"`
	ActiveProfileName string `json:"activeProfileName"`
	ActiveDirectory   string `json:"activeDirectory"`
	IsDefault         bool   `json:"isDefault"`
	Is4k              bool   `json:"is4k"`
	SyncEnabled       bool   `json:"syncEnabled"`
	Tags              []int  `json:"tags"`

	// Sonarr only (SonarrSettings; Radarr omits them entirely).
	SeriesType          string `json:"seriesType,omitempty"`
	AnimeSeriesType     string `json:"animeSeriesType,omitempty"`
	EnableSeasonFolders bool   `json:"enableSeasonFolders,omitempty"`

	// Radarr only (RadarrSettings lists it as required: seerr-api.yml,
	// RadarrSettings.required). Omitting it makes the route reject the whole
	// body: "request/body must have required property 'minimumAvailability'".
	MinimumAvailability string `json:"minimumAvailability,omitempty"`
}

// sameWiring reports whether other already holds the settings Bloud writes.
// Only the fields Bloud owns are compared: Seerr fills in ids and tags itself,
// and an admin may edit the rest in the UI without that being drift this
// configurator should undo. The comparison is split in three so each group can
// be read against the API doc; the ID and Tags fields are deliberately absent.
func (s dvrSettings) sameWiring(other dvrSettings) bool {
	return s.sameAddress(other) && s.sameSyncPolicy(other) && s.samePerAppToggles(other)
}

// sameAddress compares how Seerr reaches the DVR.
func (s dvrSettings) sameAddress(o dvrSettings) bool {
	return s.Name == o.Name &&
		s.Hostname == o.Hostname &&
		s.Port == o.Port &&
		s.APIKey == o.APIKey &&
		s.UseSSL == o.UseSSL &&
		s.BaseURL == o.BaseURL
}

// sameSyncPolicy compares the library and profile wiring.
func (s dvrSettings) sameSyncPolicy(o dvrSettings) bool {
	return s.ActiveProfileID == o.ActiveProfileID &&
		s.ActiveProfileName == o.ActiveProfileName &&
		s.ActiveDirectory == o.ActiveDirectory &&
		s.IsDefault == o.IsDefault &&
		s.Is4k == o.Is4k &&
		s.SyncEnabled == o.SyncEnabled
}

// samePerAppToggles compares the fields that exist for one sibling and not the
// other (Sonarr's series types, Radarr's minimum availability); the sibling
// that omits one leaves it zero on both sides.
func (s dvrSettings) samePerAppToggles(o dvrSettings) bool {
	return s.SeriesType == o.SeriesType &&
		s.AnimeSeriesType == o.AnimeSeriesType &&
		s.EnableSeasonFolders == o.EnableSeasonFolders &&
		s.MinimumAvailability == o.MinimumAvailability
}

// seerrAPI is the typed surface over Seerr's HTTP API for one instance.
type seerrAPI struct {
	cl *appclient.Client
}

// newAPI builds the typed client against a base-URL resolver.
func newAPI(f configurator.HTTPFactory, baseURLFn func() string) *seerrAPI {
	return &seerrAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// publicSettings is the subset of Seerr's public settings Bloud reads
// (server/lib/settings/index.ts: PublicSettings).
type publicSettings struct {
	Initialized bool `json:"initialized"`
}

// settingsPublic reads the instance's initialization state. The route is
// registered before the ADMIN guard (server/routes/index.ts), which makes it
// both the readiness probe the container healthcheck uses and the gate the
// onboarding flow is built on.
func (a *seerrAPI) settingsPublic(ctx context.Context) (publicSettings, error) {
	var out publicSettings
	if err := a.cl.GET(apiRoot+"/settings/public").OK(http.StatusOK).DoInto(ctx, &out); err != nil {
		return publicSettings{}, err
	}
	return out, nil
}

// jellyfinLogin is exactly the body the first-run wizard posts to
// POST /api/v1/auth/jellyfin (src/components/Setup/JellyfinSetup.tsx) and the
// keys server/routes/auth.ts reads. The call is unauthenticated and does two
// things at once: it verifies the Jellyfin credentials and, on a fresh
// instance, creates admin user id 1 from them (and auto-mints Jellyfin's API
// key for Seerr). Hostname must be reachable from inside the Seerr container:
// Jellyfin's container name on apps-net; "localhost" would resolve to the
// Seerr container itself.
type jellyfinLogin struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	Hostname   string `json:"hostname"`
	Port       int    `json:"port"`
	UseSSL     bool   `json:"useSsl"`
	URLBase    string `json:"urlBase"`
	Email      string `json:"email"`
	ServerType int    `json:"serverType"`
}

// loginWithJellyfin performs the wizard's sign-in step. It is the only way to
// create Seerr's first admin non-interactively.
func (a *seerrAPI) loginWithJellyfin(ctx context.Context, login jellyfinLogin) error {
	return a.cl.POST(apiRoot + "/auth/jellyfin").
		Anonymous().
		JSON(login).
		OK(http.StatusOK).
		// server/routes/auth.ts rejects a hostname once Jellyfin is configured
		// ("Jellyfin hostname already configured"), which is exactly the state a
		// re-run finds after a crash between this call and
		// settings/initialize: the admin user already exists, so there is
		// nothing left to create and the flow moves on to the admin steps.
		AlreadyDoneFunc(func(status int, body []byte) bool {
			return status == http.StatusInternalServerError && bytes.Contains(body, []byte(alreadyConfiguredError))
		}).
		Exec(ctx)
}

// jellyfinLibrary is one entry of settings.jellyfin.libraries
// (server/lib/settings/index.ts: id is Jellyfin's item key, enabled is what
// decides whether the scanner covers it).
type jellyfinLibrary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// syncJellyfinLibraries mirrors the library step of Seerr's own wizard: it
// re-reads the library list from Jellyfin, enables every library that is not
// enabled yet, then starts the full scan so the enabled libraries are actually
// populated.
//
// v3.5.0 gave each half its own route, and the wizard in SettingsJellyfin.tsx
// calls them in this same order. POST /settings/jellyfin/library/sync re-reads
// the list from Jellyfin and stores it, keeping the `enabled` flag of an entry
// it recognises; PUT /settings/jellyfin/library/{id} with {"enabled": true}
// turns one on (libraryUpdateSchema, one library per call). GET
// /settings/jellyfin/library is now a plain read: the v3.4.1 `?sync` and
// `?enable` query flags are gone, and a call that still sends them is a 200 that
// enables nothing.
//
// The sync answers 404 SYNC_ERROR_NO_LIBRARIES when Jellyfin serves no library
// at all, and it throws before it replaces the stored list. That is a state and
// not a failure, so this stops there: enabling the list a refused sync left
// behind would act on libraries Jellyfin no longer has.
func (a *seerrAPI) syncJellyfinLibraries(ctx context.Context, apiKey string) error {
	synced, err := a.cl.POST(apiRoot+"/settings/jellyfin/library/sync").
		Header(apiKeyHeader, apiKey).
		OK(http.StatusOK).
		AlreadyDone(http.StatusNotFound).
		Ensure(ctx)
	if err != nil {
		return err
	}
	if !synced {
		return nil
	}

	var libraries []jellyfinLibrary
	if err := a.cl.GET(apiRoot+"/settings/jellyfin/library").
		Header(apiKeyHeader, apiKey).
		OK(http.StatusOK).
		DoInto(ctx, &libraries); err != nil {
		return err
	}

	// Only the ones that are off: the call sets one library's state, so a re-run
	// that got partway through this loop has nothing to undo.
	for _, library := range libraries {
		if library.Enabled {
			continue
		}
		if err := a.cl.PUT(apiRoot+"/settings/jellyfin/library/"+library.ID).
			Header(apiKeyHeader, apiKey).
			JSON(map[string]bool{"enabled": true}).
			OK(http.StatusOK).
			Exec(ctx); err != nil {
			return err
		}
	}

	// server/routes/settings/index.ts: POST /jellyfin/sync reads body.start.
	return a.cl.POST(apiRoot+"/settings/jellyfin/sync").
		Header(apiKeyHeader, apiKey).
		JSON(map[string]bool{"start": true}).
		OK(http.StatusOK).
		Exec(ctx)
}

// initialize marks the instance as set up (server/routes/settings/index.ts:
// settings.public.initialized = true). Without it every visit lands on the
// setup wizard even though the instance is fully configured. ADMIN-only, no
// body.
func (a *seerrAPI) initialize(ctx context.Context, apiKey string) error {
	return a.cl.POST(apiRoot+"/settings/initialize").
		Header(apiKeyHeader, apiKey).
		OK(http.StatusOK).
		Exec(ctx)
}

// listDVRs reads one PVR list (GET /settings/radarr, GET /settings/sonarr:
// server/routes/settings/radarr.ts:9-13). An empty list is a valid 200.
func (a *seerrAPI) listDVRs(ctx context.Context, service dvrService, apiKey string) ([]dvrSettings, error) {
	var out []dvrSettings
	if err := a.cl.GET(service.path()).
		Header(apiKeyHeader, apiKey).
		OK(http.StatusOK).
		DoInto(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// createDVR appends a new entry (POST, answered 201 with the stored entry:
// server/routes/settings/radarr.ts:15-37). The route always appends: an update
// must go through updateDVR, otherwise a re-run would add a second instance of
// the same PVR.
func (a *seerrAPI) createDVR(ctx context.Context, service dvrService, apiKey string, dvr dvrSettings) error {
	return a.cl.POST(service.path()).
		Header(apiKeyHeader, apiKey).
		JSON(dvr).
		OK(http.StatusCreated).
		Exec(ctx)
}

// updateDVR replaces an existing entry in place (PUT /settings/<service>/<id>,
// answered 200: server/routes/settings/radarr.ts:77-108). The body is the
// whole entry, which is why the caller passes back the id it read.
func (a *seerrAPI) updateDVR(ctx context.Context, service dvrService, apiKey string, dvr dvrSettings) error {
	return a.cl.PUT(service.path()+"/"+strconv.Itoa(dvr.ID)).
		Header(apiKeyHeader, apiKey).
		JSON(dvr).
		OK(http.StatusOK).
		Exec(ctx)
}

// deleteDVR removes an entry (DELETE /settings/<service>/<id>, answered 200:
// server/routes/settings/radarr.ts:137-153), used to prune the entry of a PVR
// that is no longer installed.
func (a *seerrAPI) deleteDVR(ctx context.Context, service dvrService, apiKey string, id int) error {
	return a.cl.DELETE(service.path()+"/"+strconv.Itoa(id)).
		Header(apiKeyHeader, apiKey).
		OK(http.StatusOK).
		Exec(ctx)
}
