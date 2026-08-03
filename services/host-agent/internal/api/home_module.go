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
type HomeModule interface {
	GetLayout(username string) (*homeResponse, error)
	SetLayout(username string, positions []store.Position) error
}

type homeModuleSimple struct {
	positionStore  store.PositionStoreInterface
	appStore       store.AppStoreInterface
	getLaunchPaths func() map[string]string
	logger         *slog.Logger
}

// NewHomeModule creates a new HomeModule.
// getLaunchPaths returns a map of catalog ID → SSO launch path.
func NewHomeModule(
	positionStore store.PositionStoreInterface,
	appStore store.AppStoreInterface,
	getLaunchPaths func() map[string]string,
	logger *slog.Logger,
) HomeModule {
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

	launchPaths := m.getLaunchPaths()

	appItems := make([]appWithPosition, 0, len(apps))
	for _, app := range apps {
		if app.IsSystem {
			continue
		}
		pos := posMap[app.CatalogID]
		pw, ph := pos.W, pos.H
		if pw < 1 {
			pw = 1
		}
		if ph < 1 {
			ph = 1
		}
		appItems = append(appItems, appWithPosition{
			InstalledApp:  app,
			SSOLaunchPath: launchPaths[app.CatalogID],
			X:             pos.X,
			Y:             pos.Y,
			W:             pw,
			H:             ph,
		})
	}

	widgetItems := make([]widgetPosition, 0)
	for _, p := range positions {
		if p.ElementType != "widget" {
			continue
		}
		pw, ph := p.W, p.H
		if pw < 1 {
			pw = 1
		}
		if ph < 1 {
			ph = 1
		}
		widgetItems = append(widgetItems, widgetPosition{
			ID: p.ElementID,
			X:  p.X,
			Y:  p.Y,
			W:  pw,
			H:  ph,
		})
	}

	return &homeResponse{
		Apps:    appItems,
		Widgets: widgetItems,
	}, nil
}

// SetLayout replaces the user's full grid layout.
func (m *homeModuleSimple) SetLayout(username string, positions []store.Position) error {
	if err := m.positionStore.SetForUser(username, positions); err != nil {
		return fmt.Errorf("save layout: %w", err)
	}
	return nil
}

// NewHomeRouter returns a chi.Router with home layout routes.
func NewHomeRouter(mod *homeModuleSimple) *chi.Mux {
	r := chi.NewRouter()

	r.Route("/api", func(api chi.Router) {
		api.Get("/user/home", mod.GetLayoutHandler())
		api.Put("/user/layout", mod.SetLayoutHandler())
	})

	return r
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
