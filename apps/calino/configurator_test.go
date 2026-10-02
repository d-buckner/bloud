// SPDX-License-Identifier: AGPL-3.0-only

package calino

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- helpers ----

// logSink is a configurator logger whose records land in a buffer, so what the
// configurator chose to say is under test alongside what it chose to do.
func logSink(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})), buf
}

// newTestConfigurator builds a configurator whose client resolves to the given
// handler instead of the real app port. A nil handler means nothing is
// listening, which is how the unreachable case is exercised.
func newTestConfigurator(t *testing.T, app http.Handler) *Configurator {
	t.Helper()
	logger, _ := logSink(t)
	c := NewConfigurator(0, configurator.Deps{Logger: logger})
	if app != nil {
		server := httptest.NewServer(app)
		t.Cleanup(server.Close)
		c.baseURL = server.URL
	}
	c.app.cl.WithSleeper(func(time.Duration) {})
	return c
}

// shellHTML is the minimum a served Calino shell has to look like for the probe
// to accept it: the mount point the bundle renders into.
const shellHTML = `<!doctype html><html><body><div id="root"></div></body></html>`

func shellHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, shellHTML)
	})
}

func davState(address, path string) *configurator.AppState {
	return &configurator.AppState{
		DataPath: "unused",
		Integrations: configurator.Integrations{
			CalDAVServers: []configurator.CalDAVBinding{{
				ProviderRef: configurator.ProviderRef{
					App:       "radicale",
					Installed: true,
					Node:      "apps-radicale",
					Port:      5232,
					BaseURL:   "http://apps-radicale:5232",
				},
				PublicURL: address,
				Path:      path,
			}},
		},
	}
}

// ---- PreStart ----

func TestPreStartWritesNothingAndAsksForNoRestart(t *testing.T) {
	c := newTestConfigurator(t, nil)

	for pass := 1; pass <= 2; pass++ {
		res, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: t.TempDir()})
		require.NoError(t, err, "pass %d", pass)
		assert.False(t, res.RestartNeeded, "pass %d must not ask for a recreate", pass)
	}
}

// ---- PostStart ----

func TestPostStartAcceptsTheCalinoShell(t *testing.T) {
	c := newTestConfigurator(t, shellHandler())

	require.NoError(t, c.PostStart(context.Background(), davState("http://calino.localhost:8080", "/")))
}

func TestPostStartRejectsAnErrorPage(t *testing.T) {
	c := newTestConfigurator(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))

	err := c.PostStart(context.Background(), &configurator.AppState{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestPostStartRejectsAPageThatIsNotTheBundle(t *testing.T) {
	// A server that answers 200 with something that is not the SPA: the wrong
	// document root, or a bundle that never finished copying. The process looks
	// healthy and the port answers; only the body says otherwise.
	c := newTestConfigurator(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html><body>It works</body></html>")
	}))

	err := c.PostStart(context.Background(), &configurator.AppState{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), bundleProbe)
}

func TestPostStartToleratesAnUnreachableContainer(t *testing.T) {
	// Liveness belongs to the health check, which runs before PostStart. A
	// probe that cannot connect is a race, not a fault, and must not park the
	// node in a terminal state.
	c := newTestConfigurator(t, nil)

	require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{}))
}

// ---- the required provider, reported ----

func TestPostStartReportsThePairedDAVServerAddress(t *testing.T) {
	logger, out := logSink(t)
	c := newTestConfigurator(t, shellHandler())
	c.logger = logger

	require.NoError(t, c.PostStart(context.Background(),
		davState("http://radicale.localhost:8080", "/")))

	// The address is the thing the operator has to tell the user, so it has to
	// appear composed and in the form the user types: public origin plus the
	// DAV root the provider declared.
	logged := out.String()
	assert.Contains(t, logged, "http://radicale.localhost:8080/")
	assert.Contains(t, logged, "radicale")
}

func TestPostStartFallsBackToTheContainerAddressWithoutAPublicURL(t *testing.T) {
	logger, out := logSink(t)
	c := newTestConfigurator(t, shellHandler())
	c.logger = logger

	state := davState("", "/")
	state.Integrations.CalDAVServers[0].PublicURL = ""

	require.NoError(t, c.PostStart(context.Background(), state))
	assert.Contains(t, out.String(), "http://apps-radicale:5232/")
}

func TestPostStartWarnsWhenNoProviderIsBound(t *testing.T) {
	logger, out := logSink(t)
	c := newTestConfigurator(t, shellHandler())
	c.logger = logger

	require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{}))
	assert.Contains(t, out.String(), "no CalDAV provider")
}

func TestPostStartWarnsWhenTheProviderIsGone(t *testing.T) {
	logger, out := logSink(t)
	c := newTestConfigurator(t, shellHandler())
	c.logger = logger

	state := davState("http://radicale.localhost:8080", "/")
	state.Integrations.CalDAVServers[0].Installed = false

	require.NoError(t, c.PostStart(context.Background(), state))
	assert.Contains(t, out.String(), "no CalDAV provider")
	assert.NotContains(t, out.String(), "radicale.localhost")
}

// ---- metadata agreement ----

func TestBundleProbeIsNotATrivialMatch(t *testing.T) {
	// The probe string has to be specific enough that a generic error page
	// cannot satisfy it, and present in what the image actually serves. Both
	// halves matter: a probe that matches everything verifies nothing.
	assert.False(t, strings.Contains("An Error Occurred", bundleProbe))
	assert.Equal(t, `id="root"`, bundleProbe)
}
