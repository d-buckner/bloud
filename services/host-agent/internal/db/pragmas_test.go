// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// PR 7 (docs/plans/tech-debt-repayment.md): `foreign_keys` and
// `busy_timeout` are PER-CONNECTION settings in SQLite. When they were
// applied with a one-off db.Exec at boot, exactly one pooled connection
// got them and every other connection silently ran with
// foreign_keys=OFF (cascades stopped firing) and busy_timeout=0
// (contended writes failed immediately with SQLITE_BUSY; the
// operation-ledger WARN the ledger recorded twice). These tests open
// the real production path, db.InitDB, with the pool widened, which is
// the only way to see the defect: testdb's single-connection pinning
// masked it. The pre-fix tree fails all three.

// TestInitDB_PragmasApplyToEveryPooledConnection checks the settings
// on every connection the pool opens after boot, not just the one InitDB
// happened to Exec against. (journal_mode is per-database-file and
// persists, so the per-connection asserts below are the ones that
// discriminate.)
func TestInitDB_PragmasApplyToEveryPooledConnection(t *testing.T) {
	database, err := InitDB(t.TempDir())
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	const poolSize = 4
	database.SetMaxOpenConns(poolSize)

	// Hold poolSize distinct connections concurrently: the pool cannot
	// satisfy them without opening new connections, each of which gets
	// the DSN pragmas at open (or, on the pre-fix tree, nothing).
	var wg sync.WaitGroup
	errs := make(chan error, poolSize)
	for i := 0; i < poolSize; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ctx := context.Background()
			conn, err := database.Conn(ctx)
			if err != nil {
				errs <- fmt.Errorf("conn %d: %w", n, err)
				return
			}
			defer func() { _ = conn.Close() }()

			var foreignKeys int
			if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
				errs <- fmt.Errorf("conn %d: foreign_keys: %w", n, err)
				return
			}
			if foreignKeys != 1 {
				errs <- fmt.Errorf("conn %d: foreign_keys = %d, want 1", n, foreignKeys)
			}

			var busyTimeout int
			if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
				errs <- fmt.Errorf("conn %d: busy_timeout: %w", n, err)
				return
			}
			if busyTimeout != 5000 {
				errs <- fmt.Errorf("conn %d: busy_timeout = %d, want 5000", n, busyTimeout)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestInitDB_ForeignKeyCascadeWorksOnEveryConnection exercises the
// exact structural loss the ledger recorded: guests -> shares
// ON DELETE CASCADE, whose orphan rows silently accumulate when
// foreign_keys is off. On a connection without enforcement the orphan
// insert is accepted and the parent delete leaves the orphan behind.
func TestInitDB_ForeignKeyCascadeWorksOnEveryConnection(t *testing.T) {
	database, err := InitDB(t.TempDir())
	require.NoError(t, err)
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(3)
	ctx := context.Background()

	seed, err := database.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = seed.Close() }()
	_, err = seed.ExecContext(ctx,
		`INSERT INTO apps (catalog_id, display_name) VALUES ('cascade-probe', 'cascade probe')`)
	require.NoError(t, err)
	_, err = seed.ExecContext(ctx,
		`INSERT INTO guests (id, name) VALUES ('probe-guest', 'probe')`)
	require.NoError(t, err)
	var appID int64
	require.NoError(t, seed.QueryRowContext(ctx,
		`SELECT id FROM apps WHERE catalog_id = 'cascade-probe'`).Scan(&appID))
	_, err = seed.ExecContext(ctx,
		`INSERT INTO shares (id, app_id, guest_id) VALUES ('probe-share', ?, 'probe-guest')`, appID)
	require.NoError(t, err)

	// A distinct connection must enforce the FK: a share pointing at a
	// nonexistent guest is rejected outright.
	other, err := database.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = other.Close() }()
	_, err = other.ExecContext(ctx,
		`INSERT INTO shares (id, app_id, guest_id) VALUES ('orphan-share', ?, 'missing-guest')`, appID)
	require.Error(t, err, "with foreign_keys off, the orphan insert is silently accepted")
	require.ErrorContains(t, err, "FOREIGN KEY")

	// And the cascade must fire on this connection too: deleting the
	// parent removes the dependent share.
	res, err := other.ExecContext(ctx, `DELETE FROM guests WHERE id = 'probe-guest'`)
	require.NoError(t, err)
	rows, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	var remaining int
	require.NoError(t, seed.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM shares WHERE guest_id = 'probe-guest'`).Scan(&remaining))
	require.Equal(t, 0, remaining, "cascade must remove the dependent share")
}

// TestInitDB_ContendedWriteWaitsInsteadOfFailingBusy proves
// busy_timeout is live on every connection: while one connection holds
// the write lock, a second writer must block in the busy handler and
// succeed once the lock releases. On the pre-fix pool (busy_timeout=0
// on every connection but one) the second write returns SQLITE_BUSY
// within milliseconds.
func TestInitDB_ContendedWriteWaitsInsteadOfFailingBusy(t *testing.T) {
	database, err := InitDB(t.TempDir())
	require.NoError(t, err)
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(2)
	ctx := context.Background()

	// Occupy the write lock: the transaction's INSERT takes SQLite's
	// RESERVED lock and holds it until commit.
	holder, err := database.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Close() }()
	tx, err := holder.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO hosts (hostname) VALUES ('holder.bloud.local')`)
	require.NoError(t, err)

	written := make(chan error, 1)
	go func() {
		_, err := database.ExecContext(ctx,
			`INSERT INTO hosts (hostname) VALUES ('waiter.bloud.local')`)
		written <- err
	}()

	// While the lock is held the write can neither complete nor fail: it
	// is parked in the busy handler. The pre-fix tree lands in the first
	// branch with an immediate SQLITE_BUSY error.
	select {
	case err := <-written:
		t.Fatalf("second writer finished while the write lock was held: %v", err)
	case <-time.After(250 * time.Millisecond):
	}

	require.NoError(t, tx.Commit())

	select {
	case err := <-written:
		require.NoError(t, err, "the contended write must succeed after the lock releases")
	case <-time.After(5 * time.Second):
		t.Fatal("second writer never unblocked after the lock released")
	}

	var count int
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hosts`).Scan(&count))
	require.Equal(t, 2, count)
}
