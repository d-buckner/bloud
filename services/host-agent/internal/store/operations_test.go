// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
)

func TestOperationStore_StartCreatesRunningRow(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	require.NoError(t, s.Start("jellyfin", "op-1", OpTypeInstall, OpPhasePlanning))

	op, err := s.Get("jellyfin")
	require.NoError(t, err)
	require.NotNil(t, op)
	require.Equal(t, "op-1", op.ID)
	require.Equal(t, OpTypeInstall, op.Type)
	require.Equal(t, OpPhasePlanning, op.Phase)
	require.Equal(t, OpStatusRunning, op.Status)
	require.True(t, op.Retryable)
	require.Empty(t, op.Cause)
}

func TestOperationStore_StartReplacesPreviousDrive(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	require.NoError(t, s.Start("jellyfin", "op-1", OpTypeInstall, OpPhasePoststart))
	require.NoError(t, s.Fail("jellyfin", OpPhasePoststart, "boom", false))

	// A new drive replaces the failed row entirely: fresh id/type,
	// cleared cause, retryable reset.
	require.NoError(t, s.Start("jellyfin", "op-2", OpTypeInstall, OpPhasePlanning))
	op, err := s.Get("jellyfin")
	require.NoError(t, err)
	require.Equal(t, "op-2", op.ID)
	require.Equal(t, OpStatusRunning, op.Status)
	require.Empty(t, op.Cause)

	// Exactly one row per app.
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM operations WHERE app_name='jellyfin'`).Scan(&n))
	require.Equal(t, 1, n)
}

func TestOperationStore_AdvancePhaseOnlyMovesRunning(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	require.NoError(t, s.Start("immich", "op-1", OpTypeInstall, OpPhasePrestart))
	require.NoError(t, s.AdvancePhase("immich", OpPhaseHealth))
	op, _ := s.Get("immich")
	require.Equal(t, OpPhaseHealth, op.Phase)

	// Terminal rows never move.
	require.NoError(t, s.Fail("immich", OpPhaseHealth, "not ready", true))
	require.NoError(t, s.AdvancePhase("immich", OpPhasePoststart))
	op, _ = s.Get("immich")
	require.Equal(t, OpPhaseHealth, op.Phase, "failed row must not advance")
	require.Equal(t, OpStatusFailed, op.Status)
}

func TestOperationStore_CompleteOnlyClosesRunning(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	// No row at all: no error, nothing created.
	require.NoError(t, s.Complete("ghost"))
	op, err := s.Get("ghost")
	require.NoError(t, err)
	require.Nil(t, op)

	require.NoError(t, s.Start("navidrome", "op-1", OpTypeReconcile, OpPhaseTopology))
	require.NoError(t, s.Complete("navidrome"))
	op, _ = s.Get("navidrome")
	require.Equal(t, OpStatusDone, op.Status)
	require.Equal(t, OpPhaseComplete, op.Phase)

	// Complete is idempotent on terminal rows.
	require.NoError(t, s.Complete("navidrome"))
	op, _ = s.Get("navidrome")
	require.Equal(t, OpStatusDone, op.Status)
}

func TestOperationStore_MarkOrphansInterrupted(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	require.NoError(t, s.Start("a", "op-a", OpTypeInstall, OpPhasePrestart))
	require.NoError(t, s.Start("b", "op-b", OpTypeReconcile, OpPhaseHealth))
	require.NoError(t, s.Complete("a")) // a terminates before the crash

	n, err := s.MarkOrphansInterrupted()
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the running row flips")

	ob, _ := s.Get("b")
	require.Equal(t, OpStatusFailed, ob.Status)
	require.True(t, ob.Retryable)
	require.Contains(t, ob.Cause, "restart")
	require.Equal(t, OpPhaseHealth, ob.Phase, "phase preserved: last entered phase")

	oa, _ := s.Get("a")
	require.Equal(t, OpStatusDone, oa.Status, "completed row untouched")

	// Idempotent: nothing left running.
	n, err = s.MarkOrphansInterrupted()
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestOperationStore_ResolveFailedOnlyHealsReconcile(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	require.NoError(t, s.Start("immich", "op-r", OpTypeReconcile, OpPhasePoststart))
	require.NoError(t, s.Fail("immich", OpPhasePoststart, "stale dep failure", true))
	require.NoError(t, s.ResolveFailed("immich"))
	op, _ := s.Get("immich")
	require.Equal(t, OpStatusDone, op.Status)
	require.Equal(t, OpPhaseComplete, op.Phase)

	// A failed INSTALL is a user-intent failure: staleness healing must
	// NOT clear it; only an explicit new drive replaces it.
	require.NoError(t, s.Start("jellyfin", "op-i", OpTypeInstall, OpPhasePrestart))
	require.NoError(t, s.Fail("jellyfin", OpPhasePrestart, "install failure", true))
	require.NoError(t, s.ResolveFailed("jellyfin"))
	op, _ = s.Get("jellyfin")
	require.Equal(t, OpStatusFailed, op.Status)
	require.Equal(t, "install failure", op.Cause)
}
