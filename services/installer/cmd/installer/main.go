package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"codeberg.org/d-buckner/bloud-v3/services/installer/internal/api"
	"codeberg.org/d-buckner/bloud-v3/services/installer/internal/installer"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	port := os.Getenv("INSTALLER_PORT")
	if port == "" {
		port = "3001"
	}

	inst := installer.New()
	server := api.NewServer(inst)

	addr := fmt.Sprintf("0.0.0.0:%s", port)
	logger.Info("installer starting", "addr", addr)

	if err := http.ListenAndServe(addr, server); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}
