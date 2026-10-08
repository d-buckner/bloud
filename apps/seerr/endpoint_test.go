// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Seerr stores a host, a port and a TLS flag rather than a URL, so the split
// has to be right for both a container on Bloud's network and a server the
// operator pointed at from off-host. Getting it wrong from the external
// vantage is what produced a DVR entry reading `http://:0`: the container-only
// fields are empty there, and Seerr happily stores what it is given.
func TestSplitEndpoint(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		host    string
		port    int
		useSSL  bool
		wantErr bool
	}{
		{"container on the app network", "http://apps-jellyfin:8096", "apps-jellyfin", 8096, false, false},
		{"remote https with an explicit port", "https://jellyfin.example.com:8443", "jellyfin.example.com", 8443, true, false},
		{"remote https defaults to 443", "https://media.example.com", "media.example.com", 443, true, false},
		{"remote http defaults to 80", "http://media.example.com", "media.example.com", 80, false, false},
		{"empty address", "", "", 0, false, true},
		{"no host", "http://:8096", "", 0, false, true},
		{"unsupported scheme", "ftp://media.example.com", "", 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep, err := splitEndpoint(tc.baseURL)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.host, ep.host)
			assert.Equal(t, tc.port, ep.port)
			assert.Equal(t, tc.useSSL, ep.useSSL)
		})
	}
}

// A DVR entry built for an off-host PVR carries the address the operator
// registered. Built from Node and Port instead it reads `http://:0`, because
// those two fields describe a container Bloud created and there is no such
// container here.
func TestDvrSettingsForRemotePVRCarriesTheRegisteredAddress(t *testing.T) {
	remote := pvrTarget{
		service:         dvrSonarr,
		name:            "Sonarr",
		activeDirectory: showsDirectory,
		binding: configurator.PVRBinding{
			ProviderRef: configurator.ProviderRef{
				Kind:      configurator.ProviderKindExternalApp,
				App:       "sonarr",
				Installed: true,
				BaseURL:   "https://sonarr.lan.example:6789",
			},
			APIKey: "real-key",
		},
	}

	ep, err := splitEndpoint(remote.binding.BaseURL)
	require.NoError(t, err)
	settings := dvrSettingsFor(remote, "real-key", pvrQualityProfile{ID: 1, Name: "Any"}, ep)

	assert.Equal(t, "sonarr.lan.example", settings.Hostname)
	assert.Equal(t, 6789, settings.Port)
	assert.True(t, settings.UseSSL, "an https registration must not be downgraded to http")
	assert.NotEqual(t, 0, settings.Port, "a port of 0 is what made Seerr show http://:0")
}

// The local case keeps working unchanged: the container name and its port come
// through the same helper.
func TestDvrSettingsForLocalPVRStillUsesTheContainerAddress(t *testing.T) {
	local := pvrTarget{
		service: dvrSonarr,
		name:    "Sonarr",
		binding: configurator.PVRBinding{
			ProviderRef: configurator.ProviderRef{
				Kind:      configurator.ProviderKindApp,
				App:       "sonarr",
				Node:      "apps-sonarr",
				Port:      8989,
				BaseURL:   "http://apps-sonarr:8989",
				Installed: true,
			},
		},
	}

	ep, err := splitEndpoint(local.binding.BaseURL)
	require.NoError(t, err)
	settings := dvrSettingsFor(local, "k", pvrQualityProfile{ID: 1, Name: "Any"}, ep)

	assert.Equal(t, "apps-sonarr", settings.Hostname)
	assert.Equal(t, 8989, settings.Port)
	assert.False(t, settings.UseSSL)
}
