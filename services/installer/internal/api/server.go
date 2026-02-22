package api

import (
	"encoding/json"
	"net/http"
	"os"

	"codeberg.org/d-buckner/bloud-v3/services/installer/internal/installer"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type Server struct {
	installer *installer.Installer
	router    *chi.Mux
	mock      bool
}

func NewServer(inst *installer.Installer) *Server {
	s := &Server{
		installer: inst,
		router:    chi.NewRouter(),
		mock:      os.Getenv("INSTALLER_MOCK") == "1",
	}
	s.router.Use(middleware.RealIP)
	s.router.Use(middleware.Recoverer)
	s.setupRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}
