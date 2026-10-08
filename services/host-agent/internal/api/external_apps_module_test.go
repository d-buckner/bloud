// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingOrchestrator captures submitted intents and does nothing else, so a
// test can prove a handler submitted instead of writing a store directly.
type recordingOrchestrator struct {
	intents []orchestrator.Intent
}

func (r *recordingOrchestrator) Submit(intent orchestrator.Intent) {
	r.intents = append(r.intents, intent)
}

func newExternalAppsTestStore(t *testing.T) *store.ExternalAppStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, initTestDB(db))
	return store.NewExternalAppStore(db)
}

// TestExternalAppsModule_AddSubmitsIntentAndDoesNotWriteStore is the
// single-writer guard: the handler submits an add intent and never touches the
// store itself. The recording orchestrator does nothing, so an empty store
// after the call proves the handler wrote no row.
func TestExternalAppsModule_AddSubmitsIntentAndDoesNotWriteStore(t *testing.T) {
	extStore := newExternalAppsTestStore(t)
	orch := &recordingOrchestrator{}
	mod := &externalAppsModule{externalApps: extStore, orch: orch, logger: newTestSlogger()}

	req := httptest.NewRequest(http.MethodPost, "/api/external-apps",
		strings.NewReader(`{"name":"Photos","url":"https://photos.example.com","icon":"photo"}`))
	w := httptest.NewRecorder()
	mod.AddHandler()(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)
	require.Len(t, orch.intents, 1)
	add, ok := orch.intents[0].(orchestrator.AddExternalAppIntent)
	require.True(t, ok)
	assert.Equal(t, "Photos", add.Name)
	assert.Equal(t, "https://photos.example.com", add.URL)

	apps, err := extStore.GetAll()
	require.NoError(t, err)
	assert.Empty(t, apps)
}

func TestExternalAppsModule_AddRejectsBadURL(t *testing.T) {
	mod := &externalAppsModule{orch: &recordingOrchestrator{}, logger: newTestSlogger()}

	req := httptest.NewRequest(http.MethodPost, "/api/external-apps",
		strings.NewReader(`{"name":"Photos","url":"ftp://photos.example.com"}`))
	w := httptest.NewRecorder()
	mod.AddHandler()(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestExternalAppsModule_ListReturnsEmptyWhenNoStore(t *testing.T) {
	mod := &externalAppsModule{logger: newTestSlogger()}
	req := httptest.NewRequest(http.MethodGet, "/api/external-apps", nil)
	w := httptest.NewRecorder()
	mod.ListHandler()(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `[]`, w.Body.String())
}
