package config

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRuntimeModeDefaultsToPortable(t *testing.T) {
	t.Setenv("BLOUD_RUNTIME", "")
	t.Setenv("BLOUD_DATA_DIR", t.TempDir())

	cfg := LoadWithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.Equal(t, "portable", cfg.RuntimeMode)
}

func TestRuntimeModeAllowsExplicitLegacyOverride(t *testing.T) {
	t.Setenv("BLOUD_RUNTIME", "nix")
	t.Setenv("BLOUD_DATA_DIR", t.TempDir())

	cfg := LoadWithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.Equal(t, "nix", cfg.RuntimeMode)
}
