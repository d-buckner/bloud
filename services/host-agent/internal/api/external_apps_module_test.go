// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
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
	assert.Equal(t, "Photos", add.Spec.Name)
	assert.Equal(t, "https://photos.example.com", add.Spec.URL)
	assert.Equal(t, string(store.ExternalAppKindLauncher), add.Spec.Kind)

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

// TestExternalAppsModule_PatchOmittedFieldsComeFromTheRecord is the PATCH
// contract: a body that names only the field being edited must not have to
// resend the rest, and must not blank what it did not name. Before the merge,
// a rename alone failed validation on a URL the client never touched.
func TestExternalAppsModule_PatchOmittedFieldsComeFromTheRecord(t *testing.T) {
	extStore := newExternalAppsTestStore(t)
	require.NoError(t, extStore.Upsert(&store.ExternalApp{
		ID:     "ext-1",
		Kind:   string(store.ExternalAppKindProvider),
		Source: store.ExternalAppSourceForApp("affine"),
		Name:   "Old name",
		URL:    "https://affine.example.com",
		Icon:   "https://affine.example.com/icon.png",
		Values: `{"appApi":{"username":"op@example.com"}}`,
	}))

	orch := &recordingOrchestrator{}
	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{
		CatalogID:   "affine",
		DisplayName: "AFFiNE",
		Port:        3010,
		Provides: catalog.Provides{
			"appApi": catalog.ContractProvides{
				Secrets:       []string{"password"},
				RuntimeValues: []string{"username", "workspaceId"},
			},
		},
	})
	mod := &externalAppsModule{externalApps: extStore, catalog: cat, orch: orch, logger: newTestSlogger()}

	req := httptest.NewRequest(http.MethodPatch, "/api/external-apps/ext-1",
		strings.NewReader(`{"name":"New name"}`))
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Patch("/api/external-apps/{id}", mod.UpdateHandler())
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, orch.intents, 1)
	upd, ok := orch.intents[0].(orchestrator.UpdateExternalAppIntent)
	require.True(t, ok)
	assert.Equal(t, "New name", upd.Spec.Name)
	assert.Equal(t, "https://affine.example.com", upd.Spec.URL, "the endpoint the body did not name is carried over")
	assert.Equal(t, store.ExternalAppSourceForApp("affine"), upd.Spec.Source)
	assert.Equal(t, "https://affine.example.com/icon.png", upd.Spec.Icon)
	assert.Equal(t, map[string]map[string]string{"appApi": {"username": "op@example.com"}}, upd.Spec.Values)
}

// TestExternalAppsModule_PatchStillValidatesWhatItNames: filling in the
// omitted fields is not a licence to accept a bad one. A body that does name
// the URL is still held to the scheme rule.
func TestExternalAppsModule_PatchStillValidatesWhatItNames(t *testing.T) {
	extStore := newExternalAppsTestStore(t)
	require.NoError(t, extStore.Upsert(&store.ExternalApp{
		ID:   "ext-1",
		Kind: string(store.ExternalAppKindLauncher),
		Name: "Photos",
		URL:  "https://photos.example.com",
	}))

	orch := &recordingOrchestrator{}
	mod := &externalAppsModule{externalApps: extStore, orch: orch, logger: newTestSlogger()}

	req := httptest.NewRequest(http.MethodPatch, "/api/external-apps/ext-1",
		strings.NewReader(`{"url":"ftp://photos.example.com"}`))
	w := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Patch("/api/external-apps/{id}", mod.UpdateHandler())
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, orch.intents, "a rejected patch submits nothing")
}
