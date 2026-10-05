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
// exact structural loss the ledger recorded: a cascade whose orphan rows
// silently accumulate when foreign_keys is off. On a connection without
// enforcement the orphan insert is accepted and the parent delete leaves
// the orphan behind.
//
// The fixture is user_preferences -> user_app_positions, the live cascade
// pair in the baseline. It replaced guests -> shares, which left the schema
// with the sharing feature.
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
		`INSERT INTO user_preferences (username) VALUES ('cascade-probe')`)
	require.NoError(t, err)
	_, err = seed.ExecContext(ctx,
		`INSERT INTO user_app_positions (username, element_id, element_type) VALUES ('cascade-probe', 'w1', 'widget')`)
	require.NoError(t, err)

	// A distinct connection must enforce the FK: a position pointing at a
	// nonexistent preference is rejected outright.
	other, err := database.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = other.Close() }()
	_, err = other.ExecContext(ctx,
		`INSERT INTO user_app_positions (username, element_id, element_type) VALUES ('missing-user', 'w2', 'widget')`)
	require.Error(t, err, "with foreign_keys off, the orphan insert is silently accepted")
	require.ErrorContains(t, err, "FOREIGN KEY")

	// And the cascade must fire on this connection too: deleting the
	// parent removes the dependent position.
	res, err := other.ExecContext(ctx, `DELETE FROM user_preferences WHERE username = 'cascade-probe'`)
	require.NoError(t, err)
	rows, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	var remaining int
	require.NoError(t, seed.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_app_positions WHERE username = 'cascade-probe'`).Scan(&remaining))
	require.Equal(t, 0, remaining, "cascade must remove the dependent position")
}

// TestInitDB_ContendedWriteWaitsInsteadOfFailingBusy proves
// busy_timeout is live on every connection: while one connection holds
// the write lock, a second writer must block in the busy handler and
// succeed once the lock releases. On the pre-fix pool (busy_timeout=0
// on every connection but one) the second write returns SQLITE_BUSY
// within milliseconds.
//
// The fixture is the settings table: a plain table with no foreign keys and
// no triggers, so the only thing the two writers contend for is the write
// lock. It replaced the retired hosts table, where a dropped-table error made
// the holder's INSERT fail and left the transaction open, hanging the test
// on connection close instead of proving anything about busy handling.
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
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('holder', 'x')`)
	require.NoError(t, err)

	written := make(chan error, 1)
	go func() {
		_, err := database.ExecContext(ctx,
			`INSERT INTO settings (key, value) VALUES ('waiter', 'y')`)
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
		`SELECT COUNT(*) FROM settings`).Scan(&count))
	require.Equal(t, 2, count)
}
