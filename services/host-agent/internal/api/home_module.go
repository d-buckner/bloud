// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
)

// HomeModule encapsulates user home screen layout management.
type homeModuleSimple struct {
	positionStore  store.PositionStoreInterface
	appStore       store.AppStoreInterface
	getLaunchPaths func() map[string]string
	logger         *slog.Logger
	// getHeadless returns the catalog ids of apps that have no browser UI of
	// their own. Optional: with no lookup wired, nothing is marked headless.
	getHeadless func() map[string]bool
	// getClientAccess returns the catalog ids of apps that publish a
	// clientAccess credential. Optional: with no lookup wired, nothing is
	// offered the reveal surface.
	getClientAccess func() map[string]bool
	// externalApps is the operator-declared external app registry. Optional:
	// with no store wired, the home payload carries no launchers.
	externalApps store.ExternalAppStoreInterface
}

// SetHeadlessLookup wires the catalog-derived headless set so the home payload
// can carry it. The dashboard draws no tile for a headless app; the app stays
// in the payload, so its status and the event stream still describe it.
func (m *homeModuleSimple) SetHeadlessLookup(fn func() map[string]bool) {
	m.getHeadless = fn
}

// SetClientAccessLookup wires the catalog-derived clientAccess set so the home
// payload can carry it. The dashboard's right-click menu offers the reveal
// surface only for the apps in this set.
func (m *homeModuleSimple) SetClientAccessLookup(fn func() map[string]bool) {
	m.getClientAccess = fn
}

// SetExternalApps wires the external app registry so the home payload can
// carry launchers alongside installed apps.
func (m *homeModuleSimple) SetExternalApps(s store.ExternalAppStoreInterface) {
	m.externalApps = s
}

// NewHomeModule creates a new HomeModule.
// getLaunchPaths returns a map of catalog ID → SSO launch path.
func NewHomeModule(
	positionStore store.PositionStoreInterface,
	appStore store.AppStoreInterface,
	getLaunchPaths func() map[string]string,
	logger *slog.Logger,
) *homeModuleSimple {
	return &homeModuleSimple{
		positionStore:  positionStore,
		appStore:       appStore,
		getLaunchPaths: getLaunchPaths,
		logger:         logger,
	}
}

// GetLayout returns all installed user apps with grid positions, plus widget positions.
func (m *homeModuleSimple) GetLayout(username string) (*homeResponse, error) {
	apps, err := m.appStore.GetAll()
	if err != nil {
		return nil, fmt.Errorf("get apps for home: %w", err)
	}

	positions, err := m.positionStore.GetForUser(username)
	if err != nil {
		return nil, fmt.Errorf("get positions for home: %w", err)
	}

	posMap := make(map[string]store.Position, len(positions))
	for _, p := range positions {
		posMap[p.ElementID] = p
	}

	launchers, err := m.launcherItems(posMap)
	if err != nil {
		return nil, fmt.Errorf("get launchers for home: %w", err)
	}

	return &homeResponse{
		Apps:      m.homeAppItems(apps, posMap),
		Launchers: launchers,
		Widgets:   homeWidgetItems(positions),
	}, nil
}

// homeAppItems pairs each non-system app with its saved grid position and the
// SSO launch path the dashboard opens it on.
func (m *homeModuleSimple) homeAppItems(apps []*store.InstalledApp, posMap map[string]store.Position) []appWithPosition {
	launchPaths := m.getLaunchPaths()
	headless := m.headlessIDs()
	clientAccess := m.clientAccessIDs()
	items := make([]appWithPosition, 0, len(apps))
	for _, app := range apps {
		if app.IsSystem {
			continue
		}
		pos := posMap[app.CatalogID]
		w, h := atLeastOne(pos)
		items = append(items, appWithPosition{
			InstalledApp:    app,
			SSOLaunchPath:   launchPaths[app.CatalogID],
			Headless:        headless[app.CatalogID],
			HasClientAccess: clientAccess[app.CatalogID],
			X:               pos.X,
			Y:               pos.Y,
			W:               w,
			H:               h,
		})
	}
	return items
}

// headlessIDs returns the catalog ids marked headless, or an empty set when no
// lookup is wired. A nil map reads as false for every key, so the caller needs
// no guard.
func (m *homeModuleSimple) headlessIDs() map[string]bool {
	if m.getHeadless == nil {
		return nil
	}
	return m.getHeadless()
}

// clientAccessIDs returns the catalog ids that publish a clientAccess
// credential, or an empty set when no lookup is wired.
func (m *homeModuleSimple) clientAccessIDs() map[string]bool {
	if m.getClientAccess == nil {
		return nil
	}
	return m.getClientAccess()
}

// launcherItems pairs each launcher external app with its saved grid position.
// A provider external app is not a tile, so it is left out of the home payload
// (it reaches the grid only through the developer graph, in a later PR).
func (m *homeModuleSimple) launcherItems(posMap map[string]store.Position) ([]launcherWithPosition, error) {
	if m.externalApps == nil {
		return []launcherWithPosition{}, nil
	}
	apps, err := m.externalApps.GetAll()
	if err != nil {
		return nil, err
	}
	items := make([]launcherWithPosition, 0, len(apps))
	for _, app := range apps {
		if app.Kind != string(store.ExternalAppKindLauncher) {
			continue
		}
		pos := posMap[app.ID]
		w, h := atLeastOne(pos)
		items = append(items, launcherWithPosition{
			ID:   app.ID,
			Name: app.Name,
			URL:  app.URL,
			Icon: app.Icon,
			X:    pos.X,
			Y:    pos.Y,
			W:    w,
			H:    h,
		})
	}
	return items, nil
}

// homeWidgetItems picks the widget-typed positions out of the user's full set.
func homeWidgetItems(positions []store.Position) []widgetPosition {
	items := make([]widgetPosition, 0)
	for _, p := range positions {
		if p.ElementType != "widget" {
			continue
		}
		w, h := atLeastOne(p)
		items = append(items, widgetPosition{
			ID: p.ElementID,
			X:  p.X,
			Y:  p.Y,
			W:  w,
			H:  h,
		})
	}
	return items
}

// atLeastOne clamps a stored size to one cell. A zero or negative w/h renders
// an invisible tile the user can never drag back.
func atLeastOne(p store.Position) (w, h int) {
	w, h = p.W, p.H
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

// SetLayout replaces the user's full grid layout.
func (m *homeModuleSimple) SetLayout(username string, positions []store.Position) error {
	if err := m.positionStore.SetForUser(username, positions); err != nil {
		return fmt.Errorf("save layout: %w", err)
	}
	return nil
}

// NewHomeRouter registers home layout routes on the given router.
func NewHomeRouter(mod *homeModuleSimple, r chi.Router) {
	r.Get("/user/home", mod.GetLayoutHandler())
	r.Put("/user/layout", mod.SetLayoutHandler())
}

// GetLayoutHandler returns the user's home layout.
func (m *homeModuleSimple) GetLayoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := ""
		if user := getUserFromContext(r.Context()); user != nil {
			username = user.Username
		}

		layout, err := m.GetLayout(username)
		if err != nil {
			m.logger.Error("failed to get home layout", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to get layout")
			return
		}
		respondJSON(w, http.StatusOK, layout)
	}
}

// SetLayoutHandler saves the user's grid layout.
func (m *homeModuleSimple) SetLayoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := ""
		if user := getUserFromContext(r.Context()); user != nil {
			username = user.Username
		}

		var positions []store.Position
		if err := json.NewDecoder(r.Body).Decode(&positions); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		if err := m.SetLayout(username, positions); err != nil {
			m.logger.Error("failed to set layout", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to save layout")
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "saved"})
	}
}

// ---- Shared types ----

type appWithPosition struct {
	*store.InstalledApp
	SSOLaunchPath string `json:"sso_launch_path,omitempty"`
	// Headless is catalog-derived: the app has no UI to open, so the
	// dashboard leaves it off the grid.
	Headless bool `json:"headless,omitempty"`
	// HasClientAccess is catalog-derived: the app publishes a credential for a
	// human-held client, so the dashboard offers the reveal surface in the
	// right-click menu.
	HasClientAccess bool `json:"has_client_access,omitempty"`
	X               *int `json:"x"`
	Y               *int `json:"y"`
	W               int  `json:"w"`
	H               int  `json:"h"`
}

type widgetPosition struct {
	ID string `json:"id"`
	X  *int   `json:"x"`
	Y  *int   `json:"y"`
	W  int    `json:"w"`
	H  int    `json:"h"`
}

type homeResponse struct {
	Apps      []appWithPosition      `json:"apps"`
	Launchers []launcherWithPosition `json:"launchers"`
	Widgets   []widgetPosition       `json:"widgets"`
}

type launcherWithPosition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	Icon string `json:"icon"`
	X    *int   `json:"x"`
	Y    *int   `json:"y"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}
