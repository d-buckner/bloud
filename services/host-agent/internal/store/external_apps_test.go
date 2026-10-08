// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExternalAppStoreUpsertGetDelete(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewExternalAppStore(db)

	app := &ExternalApp{
		ID:     "launcher-1",
		Kind:   string(ExternalAppKindLauncher),
		Source: "",
		Name:   "Photos",
		URL:    "https://photos.example.com",
		Icon:   "photo",
		Values: "{}",
	}
	require.NoError(t, s.Upsert(app))

	got, err := s.Get("launcher-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "launcher-1", got.ID)
	assert.Equal(t, string(ExternalAppKindLauncher), got.Kind)
	assert.Equal(t, "Photos", got.Name)
	assert.Equal(t, "https://photos.example.com", got.URL)

	// Upsert is idempotent and preserves the original created_at.
	app.Name = "Family Photos"
	require.NoError(t, s.Upsert(app))
	got, err = s.Get("launcher-1")
	require.NoError(t, err)
	assert.Equal(t, "Family Photos", got.Name)
	assert.NotEmpty(t, got.CreatedAt)

	require.NoError(t, s.Delete("launcher-1"))
	got, err = s.Get("launcher-1")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestExternalAppTableHasNoCredentialColumns pins the exact column set so a
// credential cannot slip into the table unnoticed. Credentials belong in the
// secrets manager under an external/<id> scope, never on this row.
func TestExternalAppTableHasNoCredentialColumns(t *testing.T) {
	db := testdb.SetupTestDB(t)

	rows, err := db.Query("PRAGMA table_info(external_apps)")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	var columns []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		require.NoError(t, rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk))
		columns = append(columns, name)
	}
	require.NoError(t, rows.Err())

	assert.Equal(t, []string{"id", "kind", "source", "name", "url", "icon", "values_json", "created_at", "updated_at"}, columns)
}
