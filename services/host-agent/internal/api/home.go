package api

import (
	"encoding/json"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

type appWithPosition struct {
	*store.InstalledApp
	SSOLaunchPath string `json:"sso_launch_path,omitempty"`
	X             *int   `json:"x"`
	Y             *int   `json:"y"`
	W             int    `json:"w"`
	H             int    `json:"h"`
}

type widgetPosition struct {
	ID string `json:"id"`
	X  *int   `json:"x"`
	Y  *int   `json:"y"`
	W  int    `json:"w"`
	H  int    `json:"h"`
}

type homeResponse struct {
	Apps    []appWithPosition `json:"apps"`
	Widgets []widgetPosition  `json:"widgets"`
}

// handleGetHome returns all installed user apps with grid positions, plus widget positions.
func (s *Server) handleGetHome(w http.ResponseWriter, r *http.Request) {
	username := ""
	if user := getUserFromContext(r.Context()); user != nil {
		username = user.Username
	}

	apps, err := s.appStore.GetAll()
	if err != nil {
		s.logger.Error("failed to get apps for home", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get apps")
		return
	}

	positions, err := s.positionStore.GetForUser(username)
	if err != nil {
		s.logger.Error("failed to get positions for home", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get positions")
		return
	}

	posMap := make(map[string]store.Position, len(positions))
	for _, p := range positions {
		posMap[p.ElementID] = p
	}

	launchPaths := s.buildLaunchPaths()

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

	respondJSON(w, http.StatusOK, homeResponse{
		Apps:    appItems,
		Widgets: widgetItems,
	})
}

// handleSetLayout replaces the user's full grid layout.
func (s *Server) handleSetLayout(w http.ResponseWriter, r *http.Request) {
	username := ""
	if user := getUserFromContext(r.Context()); user != nil {
		username = user.Username
	}

	var positions []store.Position
	if err := json.NewDecoder(r.Body).Decode(&positions); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.positionStore.SetForUser(username, positions); err != nil {
		s.logger.Error("failed to set layout", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to save layout")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}
