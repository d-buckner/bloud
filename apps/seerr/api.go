// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// Everything Seerr-specific about Bloud's HTTP conversation with the app lives
// in this file: paths, payloads, field names. They are taken from
// seerr-team/seerr v3.4.1 (the pinned image tag), cited per method, so a future
// version bump can re-verify each one against the same files:
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
// configurator should undo.
func (s dvrSettings) sameWiring(other dvrSettings) bool {
	return s.Name == other.Name &&
		s.Hostname == other.Hostname &&
		s.Port == other.Port &&
		s.APIKey == other.APIKey &&
		s.UseSSL == other.UseSSL &&
		s.BaseURL == other.BaseURL &&
		s.ActiveProfileID == other.ActiveProfileID &&
		s.ActiveProfileName == other.ActiveProfileName &&
		s.ActiveDirectory == other.ActiveDirectory &&
		s.IsDefault == other.IsDefault &&
		s.Is4k == other.Is4k &&
		s.SyncEnabled == other.SyncEnabled &&
		s.SeriesType == other.SeriesType &&
		s.AnimeSeriesType == other.AnimeSeriesType &&
		s.EnableSeasonFolders == other.EnableSeasonFolders &&
		s.MinimumAvailability == other.MinimumAvailability
}

// seerrAPI is the typed surface over Seerr's HTTP API for one instance.
type seerrAPI struct {
	cl *appclient.Client
}

// newAPI builds the typed client against a base-URL resolver.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *seerrAPI {
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
// (server/lib/settings/index.ts: id is Jellyfin's item key).
type jellyfinLibrary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// syncJellyfinLibraries mirrors the library step of Seerr's own wizard: it
// re-reads the library list from Jellyfin and enables every library it finds,
// then starts the full scan so the enabled libraries are actually populated.
//
// v3.4.1 exposes this as GET /settings/jellyfin/library with query flags
// (server/routes/settings/index.ts, the `if (req.query.sync)` branch and the
// `req.query.enable` mapping that follows it); an absent ?enable means "no
// library enabled", so the second call is what turns them on. The
// `POST /jellyfin/library/sync` + `PUT /jellyfin/library/{id}` shape only
// exists on the develop branch, which is in no release yet; see
// INTEGRATION.md.
func (a *seerrAPI) syncJellyfinLibraries(ctx context.Context, apiKey string) error {
	var libraries []jellyfinLibrary
	if err := a.cl.GET(apiRoot+"/settings/jellyfin/library").
		Query("sync", "true").
		Header(apiKeyHeader, apiKey).
		OK(http.StatusOK).
		DoInto(ctx, &libraries); err != nil {
		return err
	}
	if len(libraries) == 0 {
		return nil
	}

	ids := make([]string, 0, len(libraries))
	for _, library := range libraries {
		ids = append(ids, library.ID)
	}
	if err := a.cl.GET(apiRoot+"/settings/jellyfin/library").
		Query("enable", strings.Join(ids, ",")).
		Header(apiKeyHeader, apiKey).
		OK(http.StatusOK).
		Exec(ctx); err != nil {
		return err
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
