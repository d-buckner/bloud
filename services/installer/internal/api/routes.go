package api

import (
	"net/http"
	"os"
	"path/filepath"
)

func (s *Server) setupRoutes() {
	s.router.Get("/api/health", s.handleHealth)
	s.router.Get("/api/status", s.handleStatus)
	s.router.Get("/api/disks", s.handleDisks)
	s.router.Post("/api/install", s.handleInstall)
	s.router.Get("/api/progress", s.handleProgress)
	s.router.Post("/api/reboot", s.handleReboot)

	s.setupFrontend()
}

func (s *Server) setupFrontend() {
	buildDir := filepath.Join("web", "build")

	if _, err := os.Stat(buildDir); os.IsNotExist(err) {
		return
	}

	s.router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		urlPath := r.URL.Path
		if urlPath == "/" {
			urlPath = "/index.html"
		}
		filePath := filepath.Join(buildDir, filepath.Clean(urlPath))

		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			http.ServeFile(w, r, filepath.Join(buildDir, "index.html"))
			return
		}

		http.ServeFile(w, r, filePath)
	})
}
