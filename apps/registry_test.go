// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package apps

import (
	"log/slog"
	"os"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterAll asserts that every user app this package claims to link
// actually has a configurator factory registered. This is the guard against
// the failure mode the registry exists to prevent: an app directory that
// installs but has no configurator, because the wiring was forgotten.
func TestRegisterAll(t *testing.T) {
	RegisterAll()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	reg := configurator.NewRegistry(logger, configurator.Deps{})

	missing := MissingConfigurators(reg)
	assert.Empty(t, missing, "every NodeNames() entry must have a registered configurator factory")
}

// TestNodeNamesStable pins the node-name list so a rename has to be a
// deliberate edit here (and in the app's registration.go), not a silent one.
func TestNodeNamesStable(t *testing.T) {
	names := NodeNames()
	require.NotEmpty(t, names)
	assert.Equal(t, []string{
		"apps-affine",
		"apps-hermes",
		"apps-homeassistant",
		"apps-immich-server",
		"apps-jellyfin",
		"apps-navidrome",
		"apps-paperless-ngx",
	}, names)
}
