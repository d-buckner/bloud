package orchestrator

import (
	"log/slog"
	"os"
)

// newTestLogger creates a logger that only outputs errors during tests.
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
}

// indexOf returns the index of target in slice, or -1 if not found.
func indexOf(slice []string, target string) int {
	for i, v := range slice {
		if v == target {
			return i
		}
	}
	return -1
}
