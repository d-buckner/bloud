// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Operation type values. Kept to what drive paths actually record: a
// constant with no writer is vocabulary nobody can observe. When a
// deliberate reconfigure flow lands, add OpTypeReconfigure with it.
const (
	OpTypeInstall   = "install"
	OpTypeUninstall = "uninstall"
	OpTypeReconcile = "reconcile"
)

// Operation phase values: the phases runFullLifecycle records on entry.
// Routing and sharing are deliberately absent: route generation is not
// recorded as an operation phase (that step is its own open debt; see
// docs/operations/tech-debt.md).
const (
	OpPhasePlanning  = "planning"
	OpPhaseTopology  = "topology"
	OpPhasePrestart  = "prestart"
	OpPhaseHealth    = "health"
	OpPhasePoststart = "poststart"
	OpPhaseComplete  = "complete"
)

// Operation status values.
const (
	OpStatusRunning = "running"
	OpStatusFailed  = "failed"
	OpStatusDone    = "complete"
)

// Operation is the current-or-last lifecycle drive for one container
// node. Exactly one row per node: the drive's identity changes when new
// work starts, phases move in place, and the terminal status persists
// until the next drive replaces it.
//
// The row is keyed by node because the engine's unit of work is a node:
// `processLevel` converges the nodes of one app concurrently, so an
// app-keyed row put every container of a multi-container app behind one
// UPDATE and let the app's phase be decided by write order.
//
// This row is authoritative for failure context and user-intent
// outcome ONLY. Graph node status remains authoritative for convergence
// control; no consumer may read both to decide one thing (see
// docs/plans/archive/operation-state-design.md §4).
type Operation struct {
	AppName   string `json:"app"`
	Node      string `json:"node"`
	ID        string `json:"id"`
	Type      string `json:"type"`
	Phase     string `json:"phase"`
	Status    string `json:"status"`
	Retryable bool   `json:"retryable"`
	Cause     string `json:"cause"`
	StartedAt string `json:"startedAt"`
	UpdatedAt string `json:"updatedAt"`
}

// OperationStore persists lifecycle operation state. Writes come only
// from the orchestrator's drive path (invariant #1); reads are free.
type OperationStore struct {
	db *sql.DB
}

// NewOperationStore creates an OperationStore on an open DB.
func NewOperationStore(db *sql.DB) *OperationStore {
	return &OperationStore{db: db}
}

// Start begins operation opID for one node of appName as opType at the
// given phase, replacing any previous row for that node. The write
// happens BEFORE the work starts: a crash after this point leaves a
// "running" row naming the last-entered phase, which is the crash
// detector.
func (s *OperationStore) Start(appName, node, opID, opType, phase string) error {
	_, err := s.db.Exec(`
		INSERT INTO operations (app_name, node, id, type, phase, status, retryable, cause, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, '', datetime('now'), datetime('now'))
		ON CONFLICT(app_name, node) DO UPDATE SET
			id = excluded.id,
			type = excluded.type,
			phase = excluded.phase,
			status = excluded.status,
			retryable = 1,
			cause = '',
			started_at = excluded.started_at,
			updated_at = excluded.updated_at`,
		appName, node, opID, opType, phase, OpStatusRunning)
	if err != nil {
		return fmt.Errorf("start operation %s/%s: %w", appName, opType, err)
	}
	return nil
}

// Ensure gives one node a running row for a drive the app is already
// in, without disturbing a row that is already running. A node that
// joins a drive in progress needs its own row so its own failures land
// somewhere; the drive identity comes from the drive, not from the node.
func (s *OperationStore) Ensure(appName, node, opID, opType, phase string) error {
	_, err := s.db.Exec(`
		INSERT INTO operations (app_name, node, id, type, phase, status, retryable, cause, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, '', datetime('now'), datetime('now'))
		ON CONFLICT(app_name, node) DO UPDATE SET
			id = excluded.id,
			type = excluded.type,
			phase = excluded.phase,
			status = excluded.status,
			retryable = 1,
			cause = '',
			started_at = excluded.started_at,
			updated_at = excluded.updated_at
		WHERE operations.status <> ?`,
		appName, node, opID, opType, phase, OpStatusRunning, OpStatusRunning)
	if err != nil {
		return fmt.Errorf("ensure operation %s/%s: %w", appName, node, err)
	}
	return nil
}

// AdvancePhase records entry into a new phase of the node's running
// operation. It only touches a row that is currently running: a phase
// update must never resurrect or overwrite a terminal state.
func (s *OperationStore) AdvancePhase(appName, node, phase string) error {
	_, err := s.db.Exec(`
		UPDATE operations
		SET phase = ?, updated_at = datetime('now')
		WHERE app_name = ? AND node = ? AND status = ?`,
		phase, appName, node, OpStatusRunning)
	if err != nil {
		return fmt.Errorf("advance operation %s/%s to %s: %w", appName, node, phase, err)
	}
	return nil
}

// Fail records a failed terminal state for the node's running
// operation, naming the phase the failure happened in and whether retry
// is expected to help.
func (s *OperationStore) Fail(appName, node, phase, cause string, retryable bool) error {
	_, err := s.db.Exec(`
		UPDATE operations
		SET phase = ?, status = ?, cause = ?, retryable = ?, updated_at = datetime('now')
		WHERE app_name = ? AND node = ? AND status = ?`,
		phase, OpStatusFailed, cause, boolToInt(retryable), appName, node, OpStatusRunning)
	if err != nil {
		return fmt.Errorf("fail operation %s/%s at %s: %w", appName, node, phase, err)
	}
	return nil
}

// Complete records the terminal success state for one node. Ordering
// matters at the call site: 'complete' must be the LAST durable write of
// a successful drive, so a running row always means "not finished,
// honestly".
func (s *OperationStore) Complete(appName, node string) error {
	_, err := s.db.Exec(`
		UPDATE operations
		SET phase = ?, status = ?, cause = '', updated_at = datetime('now')
		WHERE app_name = ? AND node = ? AND status = ?`,
		OpPhaseComplete, OpStatusDone, appName, node, OpStatusRunning)
	if err != nil {
		return fmt.Errorf("complete operation %s/%s: %w", appName, node, err)
	}
	return nil
}

// Get returns the app-level view of the current-or-last drive, computed
// from the app's per-node rows. nil means the app has no recorded
// operation at all.
//
// The rule, in order:
//
//  1. Status precedence: failed beats running, running beats complete.
//     A failure is the thing an operator has to see, and one broken
//     container makes the app not working no matter what its siblings
//     finished.
//  2. Within the winning status, the row that changed most recently.
//     That is the node the drive is actually about right now.
//  3. A tie goes to the lower node name, so two rows written in the same
//     second produce the same answer on every read. SQLite's
//     datetime('now') has one-second resolution, so ties are normal, not
//     exotic.
//
// The chosen row supplies type, phase, cause, retryability, and the
// timestamps. This is a projection, not storage: nothing is written, and
// the per-node rows stay the record.
func (s *OperationStore) Get(appName string) (*Operation, error) {
	rows, err := s.db.Query(`
		SELECT app_name, node, id, type, phase, status, retryable, cause, started_at, updated_at
		FROM operations WHERE app_name = ?`, appName)
	if err != nil {
		return nil, fmt.Errorf("get operation %s: %w", appName, err)
	}
	defer func() { _ = rows.Close() }()

	var best *Operation
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, fmt.Errorf("get operation %s: %w", appName, err)
		}
		if best == nil || ranksAbove(op, best) {
			best = op
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get operation %s: %w", appName, err)
	}
	return best, nil
}

// GetNode returns the recorded operation for one node, or nil when that
// node has none.
func (s *OperationStore) GetNode(appName, node string) (*Operation, error) {
	row := s.db.QueryRow(`
		SELECT app_name, node, id, type, phase, status, retryable, cause, started_at, updated_at
		FROM operations WHERE app_name = ? AND node = ?`, appName, node)
	op, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get node operation %s/%s: %w", appName, node, err)
	}
	return op, nil
}

// ranksAbove reports whether a is the app's answer over b, applying the
// precedence documented on Get.
func ranksAbove(a, b *Operation) bool {
	ra, rb := statusRank(a.Status), statusRank(b.Status)
	if ra != rb {
		return ra > rb
	}
	if a.UpdatedAt != b.UpdatedAt {
		return a.UpdatedAt > b.UpdatedAt
	}
	return a.Node < b.Node
}

// statusRank orders the three terminal-and-live states for the app
// projection. Higher wins.
func statusRank(status string) int {
	switch status {
	case OpStatusFailed:
		return 3
	case OpStatusRunning:
		return 2
	default:
		return 1
	}
}

// ResolveFailed clears a stale failure on one node: when a staleness
// re-run succeeds for a node whose last recorded operation was a failed
// reconcile drive, the old failure is no longer that node's last word.
// Only failed *reconcile* rows resolve this way: a failed install keeps
// its failure until an explicit new drive replaces it.
func (s *OperationStore) ResolveFailed(appName, node string) error {
	_, err := s.db.Exec(`
		UPDATE operations
		SET phase = ?, status = ?, updated_at = datetime('now')
		WHERE app_name = ? AND node = ? AND status = ? AND type = ?`,
		OpPhaseComplete, OpStatusDone, appName, node, OpStatusFailed, OpTypeReconcile)
	if err != nil {
		return fmt.Errorf("resolve failed operation %s/%s: %w", appName, node, err)
	}
	return nil
}

// MarkOrphansInterrupted flips every running row to failed/retryable
// with an interrupt cause. Call at orchestrator startup BEFORE the
// first convergence: a running row surviving a process restart means
// the drive died mid-phase (writes happen before phase entry, so
// 'running' is always the last-entered phase). Normal convergence
// then re-drives the work; the row is diagnostic continuity, not a
// resume cursor. Returns the number of rows flipped.
func (s *OperationStore) MarkOrphansInterrupted() (int, error) {
	res, err := s.db.Exec(`
		UPDATE operations
		SET status = ?, retryable = 1, cause = 'interrupted by host-agent restart', updated_at = datetime('now')
		WHERE status = ?`,
		OpStatusFailed, OpStatusRunning)
	if err != nil {
		return 0, fmt.Errorf("mark orphan operations: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count orphan operations: %w", err)
	}
	return int(n), nil
}

// operationScanner is the subset of the row the projection reads.
type operationScanner interface {
	Scan(dest ...any) error
}

func scanOperation(row operationScanner) (*Operation, error) {
	var op Operation
	var retryable int
	err := row.Scan(&op.AppName, &op.Node, &op.ID, &op.Type, &op.Phase, &op.Status,
		&retryable, &op.Cause, &op.StartedAt, &op.UpdatedAt)
	if err != nil {
		return nil, err
	}
	op.Retryable = retryable != 0
	return &op, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
