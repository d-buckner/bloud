// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/timezone"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The settings GET handlers answer with the live state a container spec is built
// from, not the stored setting.
//
// The dashboard's convergence poll depends on that: it settles when the read
// equals the value the save reported, so "confirmed" means the orchestrator
// applied the change. A handler that read the store instead would still answer
// 200 with a plausible value, every other test in this package would pass, and
// the wait would settle on the stored intent before anything was applied.
//
// These tests state both values and require the live one.

func TestGetTimezoneHandler_ReadsLiveStateNotTheStore(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.timezone = timezone.NewState("Europe/Berlin")
	// The same key/value fake the system tests use. The point is that it holds a
	// different value that the handler must ignore.
	mod.settingsStore = &fakeAISettings{
		values: map[string]string{store.SettingTimezone: "America/New_York"},
	}

	w := getTimezone(t, mod)
	require.Equal(t, http.StatusOK, w.Code)

	var resp timezoneResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "Europe/Berlin", resp.Zone, "the answer must be the live clock, not the stored one")
}

func TestGetPublicURLHandler_ReadsLiveStateNotTheStore(t *testing.T) {
	mod := newSettingsModule(t, nil)
	public, err := hostset.ParsePublicURL("https://live.example.com")
	require.NoError(t, err)
	mod.hostState = hostset.NewState(hostset.New(public))
	mod.settingsStore = &fakeAISettings{
		values: map[string]string{store.SettingPublicURL: "https://stored.example.com"},
	}

	w := getPublicURL(t, mod)
	require.Equal(t, http.StatusOK, w.Code)

	var resp publicURLResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "https://live.example.com", resp.URL, "the answer must be the live address, not the stored one")
}
