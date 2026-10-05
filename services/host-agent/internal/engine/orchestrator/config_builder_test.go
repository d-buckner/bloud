// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
)

func TestShouldCleanupAuthentik_NilApp(t *testing.T) {
	assert.False(t, shouldCleanupAuthentik(nil))
}

func TestShouldCleanupAuthentik_EmptyStrategy(t *testing.T) {
	app := &catalog.App{
		CatalogID: "test",
		SSO:       catalog.SSO{Strategy: ""},
	}
	assert.False(t, shouldCleanupAuthentik(app))
}

func TestShouldCleanupAuthentik_NoneStrategy(t *testing.T) {
	app := &catalog.App{
		CatalogID: "test",
		SSO:       catalog.SSO{Strategy: "none"},
	}
	assert.False(t, shouldCleanupAuthentik(app))
}

func TestShouldCleanupAuthentik_NativeOIDC(t *testing.T) {
	app := &catalog.App{
		CatalogID: "miniflux",
		SSO:       catalog.SSO{Strategy: "native-oidc"},
	}
	assert.True(t, shouldCleanupAuthentik(app))
}

func TestShouldCleanupAuthentik_ForwardAuth(t *testing.T) {
	app := &catalog.App{
		CatalogID: "adguard-home",
		SSO:       catalog.SSO{Strategy: "forward-auth"},
	}
	assert.True(t, shouldCleanupAuthentik(app))
}
