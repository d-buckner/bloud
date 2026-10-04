// SPDX-License-Identifier: AGPL-3.0-only

package caldavmcp

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func caldavBinding() configurator.CalDAVBinding {
	return configurator.CalDAVBinding{
		ProviderRef: configurator.ProviderRef{
			App:       "radicale",
			Installed: true,
			Node:      "apps-radicale",
			Port:      5232,
			BaseURL:   "http://apps-radicale:5232",
		},
		Path: "/",
	}
}

func credential() configurator.AppAPIBinding {
	return configurator.AppAPIBinding{
		ProviderRef: configurator.ProviderRef{
			App:       "radicale",
			Installed: true,
			Node:      "apps-radicale",
			Port:      5232,
			BaseURL:   "http://apps-radicale:5232",
		},
		Username: "caldav-service",
		Password: "secret-password",
	}
}

func TestRenderRunScriptExportsTheEnvironment(t *testing.T) {
	cred := credential()
	content, err := renderRunScript("http://apps-radicale:5232/", true, cred, true)
	require.NoError(t, err)

	assert.Contains(t, content, "#!/bin/sh")
	assert.Contains(t, content, "export CALDAV_BASE_URL='http://apps-radicale:5232/'")
	assert.Contains(t, content, "export CALDAV_USERNAME='caldav-service'")
	assert.Contains(t, content, "export CALDAV_PASSWORD='secret-password'")
	assert.Contains(t, content, "exec npx -y "+caldavPackage)
}

func TestRenderRunScriptOmitsWhatIsNotReady(t *testing.T) {
	content, err := renderRunScript("http://apps-radicale:5232/", true, configurator.AppAPIBinding{}, false)
	require.NoError(t, err)

	assert.Contains(t, content, "export CALDAV_BASE_URL='http://apps-radicale:5232/'")
	assert.NotContains(t, content, "CALDAV_USERNAME")
	assert.NotContains(t, content, "CALDAV_PASSWORD")
	assert.Contains(t, content, "exec npx -y "+caldavPackage)
}

func TestCaldavBaseURLConsidersOnlyUsableProviders(t *testing.T) {
	baseURL, ok := caldavBaseURL(&configurator.AppState{
		Integrations: configurator.Integrations{
			CalDAVServers: []configurator.CalDAVBinding{caldavBinding()},
		},
	})
	require.True(t, ok)
	assert.Equal(t, "http://apps-radicale:5232/", baseURL)

	notInstalled := caldavBinding()
	notInstalled.Installed = false
	_, ok = caldavBaseURL(&configurator.AppState{
		Integrations: configurator.Integrations{CalDAVServers: []configurator.CalDAVBinding{notInstalled}},
	})
	assert.False(t, ok, "an uninstalled provider has no address to dial")
}

func TestCaldavCredentialRequiresTheWholeCredential(t *testing.T) {
	cred, ok := caldavCredential(&configurator.AppState{
		Integrations: configurator.Integrations{AppAPIs: []configurator.AppAPIBinding{credential()}},
	})
	require.True(t, ok)
	assert.Equal(t, "caldav-service", cred.Username)
	assert.Equal(t, "secret-password", cred.Password)

	noPassword := credential()
	noPassword.Password = ""
	_, ok = caldavCredential(&configurator.AppState{
		Integrations: configurator.Integrations{AppAPIs: []configurator.AppAPIBinding{noPassword}},
	})
	assert.False(t, ok, "an empty password is the provider's 'not published yet' state")
}
