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
