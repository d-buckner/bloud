// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
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

	// csrfTokenHeader / csrfTokenCookie name the double-submit CSRF pair Seerr
	// issues when settings.network.csrfProtection is on (server/index.ts mounts
	// csurf with the `_csrf` cookie and mirrors the token into XSRF-TOKEN for its
	// own axios client). Off by default, which is why every other call in this
	// file posts with no token at all; read back when present so a hardened
	// instance works the same as the default one.
	csrfTokenHeader = "X-CSRF-TOKEN"
	csrfTokenCookie = "XSRF-TOKEN"
	csrfCookie      = "_csrf"

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
//
// Two clients, because Seerr has two auth positions. `cl` is the one the
// configurator uses: every admin call it makes carries X-API-Key, so it needs
// no cookies and must not accumulate any. `session` is the one the remote
// sign-in exchange uses: Seerr hands an admin its API key only to a logged-in
// user (server/routes/settings/index.ts omits apiKey for anyone else), and a
// login session is a cookie, so that flow needs a jar.
type seerrAPI struct {
	cl      *appclient.Client
	session *appclient.Client
	jar     http.CookieJar
	baseURL func() string
}

// newAPI builds the typed client against a base-URL resolver.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *seerrAPI {
	// The only error case is a nil cookie-jar option, which this call does not
	// pass.
	jar, _ := cookiejar.New(nil)
	return &seerrAPI{
		cl:      f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn}),
		session: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn, Jar: jar}),
		jar:     jar,
		baseURL: baseURLFn,
	}
}

// publicSettings is the subset of Seerr's public settings Bloud reads
// (server/lib/settings/index.ts: PublicSettings).
// publicSettings is the part of GET /settings/public this code reads.
//
// Initialized is a pointer because the two kinds of "not initialized" are not
// the same problem: a Seerr still in its own wizard answers the field as false,
// and an address that is not a Seerr answers it not at all. Telling them apart is
// the difference between sending an operator to finish a setup and sending them to
// a different URL.
// initialized reports whether the instance has been through its own first-run
// wizard. GET /settings/public answers `initialized` (server/routes/index.ts
// registers it before the ADMIN guard, which is why it doubles as the container
// healthcheck), and it is the only way to tell the two kinds of "not yet" apart:
// a Seerr still in its own wizard answers the field as false, and an address that
// is not a Seerr answers it not at all. Guessing wrong there sends the operator to
// finish a setup that is already done.
func (a *seerrAPI) initialized(ctx context.Context) (bool, error) {
	var out struct {
		Initialized *bool `json:"initialized"`
	}
	if err := a.cl.GET(apiRoot+"/settings/public").OK(http.StatusOK).DoInto(ctx, &out); err != nil {
		return false, err
	}
	if out.Initialized == nil {
		return false, fmt.Errorf("%s answered /settings/public without an 'initialized' field, so it is not a Seerr API", a.baseURL())
	}
	return *out.Initialized, nil
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

// seerrUser is the account a successful sign-in acts as. `filter()` on the
// server keeps email and username and drops every credential field, so this is
// the whole readable shape (server/entity/User.ts).
type seerrUser struct {
	Email    string `json:"email"`
	Username string `json:"username"`
}

// displayName is how the signed-in account is named to other apps: the address
// Seerr itself shows and attributes requests to, falling back to the login name
// for an account created from a Jellyfin user that has no email.
func (u seerrUser) displayName() string {
	if u.Email != "" {
		return u.Email
	}
	return u.Username
}

// signInAs signs the session client in as one account and returns the Seerr
// user that session acts as.
//
// Two routes are tried, in this order, because one username/password box has to
// cover both shapes of "the admin account of a Seerr Bloud does not run":
//
//   - POST /auth/jellyfin with username and password only. On a configured
//     instance this logs the credentials into the Jellyfin *Seerr* has
//     configured (server/routes/auth.ts) and sets req.session.userId. The
//     hostname must be omitted: once settings.jellyfin.ip is set the route
//     refuses any caller that sends one, and seerr-api.yml requires only the
//     two fields sent here.
//   - POST /auth/local with email and password, for an instance whose admin is
//     a local Seerr account (settings.main.localLogin, on by default). Only
//     reached when the Jellyfin route refused, so a successful first sign-in is
//     never overwritten by a second one for a different user.
//
// Both are unauthenticated routes, so no CSRF token is needed for the first
// call; csrfToken covers an instance that has csrfProtection switched on.
func (a *seerrAPI) signInAs(ctx context.Context, login configurator.Login) (seerrUser, error) {
	csrf := a.csrfToken(ctx)
	user, jerr := a.loginJellyfinExisting(ctx, login, csrf)
	if jerr == nil {
		return user, nil
	}
	user, lerr := a.loginLocal(ctx, login, a.csrfToken(ctx))
	if lerr == nil {
		return user, nil
	}
	return seerrUser{}, fmt.Errorf(
		"neither sign-in was accepted (jellyfin: %v; local account: %v)", jerr, lerr)
}

// loginJellyfinExisting performs the already-configured form of the Jellyfin
// sign-in: credentials only, no hostname, no server type.
func (a *seerrAPI) loginJellyfinExisting(ctx context.Context, login configurator.Login, csrf string) (seerrUser, error) {
	var user seerrUser
	call := a.session.POST(apiRoot + "/auth/jellyfin").
		Anonymous().
		JSON(map[string]string{"username": login.Username, "password": login.Password}).
		OK(http.StatusOK).
		NoRetry()
	if csrf != "" {
		call = call.Header(csrfTokenHeader, csrf)
	}
	if err := call.DoInto(ctx, &user); err != nil {
		return seerrUser{}, err
	}
	return user, nil
}

// loginLocal performs the local-account sign-in. Seerr reads the first field as
// an email address, which is what lets one input take either an address or a
// Jellyfin username: whichever the account actually is, one of the two routes
// accepts it.
func (a *seerrAPI) loginLocal(ctx context.Context, login configurator.Login, csrf string) (seerrUser, error) {
	var user seerrUser
	call := a.session.POST(apiRoot + "/auth/local").
		Anonymous().
		JSON(map[string]string{"email": login.Username, "password": login.Password}).
		OK(http.StatusOK).
		NoRetry()
	if csrf != "" {
		call = call.Header(csrfTokenHeader, csrf)
	}
	if err := call.DoInto(ctx, &user); err != nil {
		return seerrUser{}, err
	}
	return user, nil
}

// mainAPIKey reads the API key out of the instance's own main settings, as the
// signed-in session. GET /settings/main answers a non-admin with the whole
// settings object minus apiKey (server/routes/settings/index.ts:
// filteredMainSettings), so an empty result is a definitive "this account is
// not an admin here", not a value that has not arrived yet.
func (a *seerrAPI) mainAPIKey(ctx context.Context) (string, error) {
	var out struct {
		APIKey string `json:"apiKey"`
	}
	if err := a.session.GET(apiRoot+"/settings/main").
		Anonymous().
		OK(http.StatusOK).
		NoRetry().
		DoInto(ctx, &out); err != nil {
		return "", err
	}
	return out.APIKey, nil
}

// csrfToken reads the CSRF token the instance issued to this jar, having asked
// for one first. An instance with csrfProtection off issues nothing and this
// returns "", which is the only correct thing to send it.
func (a *seerrAPI) csrfToken(ctx context.Context) string {
	// Any GET works; this one is public, anonymous, and already the readiness
	// probe the container's own healthcheck uses, so it cannot fail for reasons
	// that are this flow's fault.
	if err := a.session.GET(apiRoot + "/settings/public").
		Anonymous().
		OK(http.StatusOK).
		NoRetry().
		Exec(ctx); err != nil {
		return ""
	}
	u, err := url.Parse(a.baseURL())
	if err != nil {
		return ""
	}
	want := map[string]bool{csrfTokenCookie: true, csrfCookie: true}
	for _, c := range a.jar.Cookies(u) {
		if want[c.Name] && c.Value != "" {
			return c.Value
		}
	}
	return ""
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
