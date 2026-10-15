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

	require.NoError(t, s.Start("jellyfin", "jellyfin", "op-1", OpTypeInstall, OpPhasePlanning))

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

	require.NoError(t, s.Start("jellyfin", "jellyfin", "op-1", OpTypeInstall, OpPhasePoststart))
	require.NoError(t, s.Fail("jellyfin", "jellyfin", OpPhasePoststart, "boom", false))

	// A new drive replaces the failed row entirely: fresh id/type,
	// cleared cause, retryable reset.
	require.NoError(t, s.Start("jellyfin", "jellyfin", "op-2", OpTypeInstall, OpPhasePlanning))
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

	require.NoError(t, s.Start("immich", "immich", "op-1", OpTypeInstall, OpPhasePrestart))
	require.NoError(t, s.AdvancePhase("immich", "immich", OpPhaseHealth))
	op, _ := s.Get("immich")
	require.Equal(t, OpPhaseHealth, op.Phase)

	// Terminal rows never move.
	require.NoError(t, s.Fail("immich", "immich", OpPhaseHealth, "not ready", true))
	require.NoError(t, s.AdvancePhase("immich", "immich", OpPhasePoststart))
	op, _ = s.Get("immich")
	require.Equal(t, OpPhaseHealth, op.Phase, "failed row must not advance")
	require.Equal(t, OpStatusFailed, op.Status)
}

func TestOperationStore_CompleteOnlyClosesRunning(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	// No row at all: no error, nothing created.
	require.NoError(t, s.Complete("ghost", "ghost"))
	op, err := s.Get("ghost")
	require.NoError(t, err)
	require.Nil(t, op)

	require.NoError(t, s.Start("navidrome", "navidrome", "op-1", OpTypeReconcile, OpPhaseTopology))
	require.NoError(t, s.Complete("navidrome", "navidrome"))
	op, _ = s.Get("navidrome")
	require.Equal(t, OpStatusDone, op.Status)
	require.Equal(t, OpPhaseComplete, op.Phase)

	// Complete is idempotent on terminal rows.
	require.NoError(t, s.Complete("navidrome", "navidrome"))
	op, _ = s.Get("navidrome")
	require.Equal(t, OpStatusDone, op.Status)
}

func TestOperationStore_MarkOrphansInterrupted(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	require.NoError(t, s.Start("a", "a", "op-a", OpTypeInstall, OpPhasePrestart))
	require.NoError(t, s.Start("b", "b", "op-b", OpTypeReconcile, OpPhaseHealth))
	require.NoError(t, s.Complete("a", "a")) // a terminates before the crash

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

	require.NoError(t, s.Start("immich", "immich", "op-r", OpTypeReconcile, OpPhasePoststart))
	require.NoError(t, s.Fail("immich", "immich", OpPhasePoststart, "stale dep failure", true))
	require.NoError(t, s.ResolveFailed("immich", "immich"))
	op, _ := s.Get("immich")
	require.Equal(t, OpStatusDone, op.Status)
	require.Equal(t, OpPhaseComplete, op.Phase)

	// A failed INSTALL is a user-intent failure: staleness healing must
	// NOT clear it; only an explicit new drive replaces it.
	require.NoError(t, s.Start("jellyfin", "jellyfin", "op-i", OpTypeInstall, OpPhasePrestart))
	require.NoError(t, s.Fail("jellyfin", "jellyfin", OpPhasePrestart, "install failure", true))
	require.NoError(t, s.ResolveFailed("jellyfin", "jellyfin"))
	op, _ = s.Get("jellyfin")
	require.Equal(t, OpStatusFailed, op.Status)
	require.Equal(t, "install failure", op.Cause)
}

// The whole point of the per-node key: two containers of one app each own
// a row, and neither overwrites the other. Under the app key, the second
// Start replaced the first and the app's phase was whichever write
// landed last.
func TestOperationStore_NodesOfOneAppGetSeparateRows(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	require.NoError(t, s.Start("immich", "apps-immich-postgres", "op-1", OpTypeInstall, OpPhaseHealth))
	require.NoError(t, s.Start("immich", "apps-immich-server", "op-1", OpTypeInstall, OpPhasePrestart))

	pg, err := s.GetNode("immich", "apps-immich-postgres")
	require.NoError(t, err)
	require.Equal(t, OpPhaseHealth, pg.Phase)

	srv, err := s.GetNode("immich", "apps-immich-server")
	require.NoError(t, err)
	require.Equal(t, OpPhasePrestart, srv.Phase)

	// One failure on one node does not speak for its siblings.
	require.NoError(t, s.Fail("immich", "apps-immich-server", OpPhasePoststart, "ml unreachable", true))
	pg, err = s.GetNode("immich", "apps-immich-postgres")
	require.NoError(t, err)
	require.Equal(t, OpStatusRunning, pg.Status, "a sibling's failure must not touch this node's row")
}

// Get is a projection with a rule, not a surviving write. The precedence
// is what makes the app's answer the same on every read.
func TestOperationStore_GetPrefersFailureOverRunningAndComplete(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	require.NoError(t, s.Start("immich", "apps-immich-ml", "op-1", OpTypeInstall, OpPhaseHealth))
	require.NoError(t, s.Start("immich", "apps-immich-redis", "op-1", OpTypeInstall, OpPhaseHealth))
	require.NoError(t, s.Complete("immich", "apps-immich-redis"))
	require.NoError(t, s.Fail("immich", "apps-immich-ml", OpPhaseHealth, "model download failed", true))

	op, err := s.Get("immich")
	require.NoError(t, err)
	require.Equal(t, OpStatusFailed, op.Status, "one broken container makes the app not working")
	require.Equal(t, "apps-immich-ml", op.Node)
	require.Equal(t, "model download failed", op.Cause)

	// With the failure gone, the live node is the app's answer, and a
	// completed sibling does not make the app look finished.
	require.NoError(t, s.Start("immich", "apps-immich-ml", "op-2", OpTypeInstall, OpPhasePoststart))
	op, err = s.Get("immich")
	require.NoError(t, err)
	require.Equal(t, OpStatusRunning, op.Status)
	require.Equal(t, "apps-immich-ml", op.Node)
}

// Ensure gives a node that joins a drive in progress its own row without
// disturbing a row that is already running.
func TestOperationStore_EnsureAddsANodeWithoutDisturbingARunningRow(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	require.NoError(t, s.Start("immich", "apps-immich-postgres", "op-1", OpTypeReconcile, OpPhaseTopology))
	require.NoError(t, s.Ensure("immich", "apps-immich-server", "op-1", OpTypeReconcile, OpPhasePrestart))

	srv, err := s.GetNode("immich", "apps-immich-server")
	require.NoError(t, err)
	require.Equal(t, "op-1", srv.ID, "a node that joins a drive shares the drive's identity")
	require.Equal(t, OpStatusRunning, srv.Status)

	// A node already running keeps the phase it reported, not the one the
	// newcomer carried.
	require.NoError(t, s.AdvancePhase("immich", "apps-immich-postgres", OpPhaseHealth))
	require.NoError(t, s.Ensure("immich", "apps-immich-postgres", "op-9", OpTypeReconcile, OpPhaseTopology))
	pg, err := s.GetNode("immich", "apps-immich-postgres")
	require.NoError(t, err)
	require.Equal(t, OpPhaseHealth, pg.Phase, "Ensure must not roll back a running row")
	require.Equal(t, "op-1", pg.ID)
}

// The tie-break has to be a rule, not row order: SQLite's datetime('now')
// has one-second resolution, so rows written in the same pass share a
// timestamp on every read.
func TestOperationStore_GetTieBreakIsDeterministic(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	require.NoError(t, s.Start("immich", "apps-immich-server", "op-1", OpTypeInstall, OpPhaseHealth))
	require.NoError(t, s.Start("immich", "apps-immich-ml", "op-1", OpTypeInstall, OpPhaseHealth))

	first, err := s.Get("immich")
	require.NoError(t, err)
	for range 5 {
		again, err := s.Get("immich")
		require.NoError(t, err)
		require.Equal(t, first.Node, again.Node, "two rows with the same timestamp must always rank the same way")
	}
	require.Equal(t, "apps-immich-ml", first.Node, "the lower node name wins a tie")
}

// The defect this change closes: `processLevel` converges the nodes of
// one app concurrently, so every node of a multi-container app used to
// issue UPDATE ... WHERE app_name = ? against one row. busy_timeout made
// those writes wait instead of fail, but the writes were still serialized
// on a row none of the writers owned. With one row per node, each writer
// touches only its own row, and a run that would have contended before
// now does not.
//
// Run under -race. The assertion is that every write succeeded: a
// SQLITE_BUSY that outlives the busy_timeout surfaces here as an error,
// and a lost write surfaces as a phase that never arrived.
func TestOperationStore_ConcurrentNodesOfOneAppDoNotContend(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewOperationStore(db)

	const nodes = 12
	drives := 6

	errCh := make(chan error, nodes*drives*3)
	done := make(chan struct{})
	for i := range nodes {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			node := "apps-immich-" + string(rune('a'+i))
			for d := range drives {
				opID := "op-" + string(rune('0'+d))
				if err := s.Start("immich", node, opID, OpTypeReconcile, OpPhaseTopology); err != nil {
					errCh <- err
					return
				}
				for _, phase := range []string{OpPhasePrestart, OpPhaseHealth, OpPhasePoststart} {
					if err := s.AdvancePhase("immich", node, phase); err != nil {
						errCh <- err
						return
					}
				}
				if err := s.Complete("immich", node); err != nil {
					errCh <- err
					return
				}
			}
		}(i)
	}
	for range nodes {
		<-done
	}
	close(errCh)
	for err := range errCh {
		require.NoError(t, err, "a concurrent node write failed")
	}

	// Every node kept its own row, and each one reached its terminal
	// state: nothing was overwritten by a sibling.
	for i := range nodes {
		node := "apps-immich-" + string(rune('a'+i))
		op, err := s.GetNode("immich", node)
		require.NoError(t, err)
		require.NotNil(t, op, "node %s lost its row", node)
		require.Equal(t, OpStatusDone, op.Status, "node %s did not keep its own drive to completion", node)
	}

	op, err := s.Get("immich")
	require.NoError(t, err)
	require.Equal(t, OpStatusDone, op.Status)
}
