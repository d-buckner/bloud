// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

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
// Routing and sharing are deliberately absent — route generation is not
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

// Operation is the current-or-last lifecycle drive for one app.
// Exactly one row per app: the drive's identity changes when new work
// starts, phases move in place, and the terminal status persists until
// the next drive replaces it.
//
// This row is authoritative for failure context and user-intent
// outcome ONLY. Graph node status remains authoritative for convergence
// control; no consumer may read both to decide one thing (see
// docs/plans/operation-state-design.md §4).
type Operation struct {
	AppName   string `json:"app"`
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

// Start begins operation opID for appName as opType at the given
// phase, replacing any previous row. The write happens BEFORE the work
// starts: a crash after this point leaves a "running" row naming the
// last-entered phase, which is the crash detector.
func (s *OperationStore) Start(appName, opID, opType, phase string) error {
	_, err := s.db.Exec(`
		INSERT INTO operations (app_name, id, type, phase, status, retryable, cause, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 1, '', datetime('now'), datetime('now'))
		ON CONFLICT(app_name) DO UPDATE SET
			id = excluded.id,
			type = excluded.type,
			phase = excluded.phase,
			status = excluded.status,
			retryable = 1,
			cause = '',
			started_at = excluded.started_at,
			updated_at = excluded.updated_at`,
		appName, opID, opType, phase, OpStatusRunning)
	if err != nil {
		return fmt.Errorf("start operation %s/%s: %w", appName, opType, err)
	}
	return nil
}

// AdvancePhase records entry into a new phase of the running operation.
// It only touches a row that is currently running: a phase update must
// never resurrect or overwrite a terminal state.
func (s *OperationStore) AdvancePhase(appName, phase string) error {
	_, err := s.db.Exec(`
		UPDATE operations
		SET phase = ?, updated_at = datetime('now')
		WHERE app_name = ? AND status = ?`,
		phase, appName, OpStatusRunning)
	if err != nil {
		return fmt.Errorf("advance operation %s to %s: %w", appName, phase, err)
	}
	return nil
}

// Fail records a failed terminal state for the running operation,
// naming the phase the failure happened in and whether retry is
// expected to help.
func (s *OperationStore) Fail(appName, phase, cause string, retryable bool) error {
	_, err := s.db.Exec(`
		UPDATE operations
		SET phase = ?, status = ?, cause = ?, retryable = ?, updated_at = datetime('now')
		WHERE app_name = ? AND status = ?`,
		phase, OpStatusFailed, cause, boolToInt(retryable), appName, OpStatusRunning)
	if err != nil {
		return fmt.Errorf("fail operation %s at %s: %w", appName, phase, err)
	}
	return nil
}

// Complete records the terminal success state. Ordering matters at the
// call site: 'complete' must be the LAST durable write of a successful
// drive, so a running row always means "not finished, honestly".
func (s *OperationStore) Complete(appName string) error {
	_, err := s.db.Exec(`
		UPDATE operations
		SET phase = ?, status = ?, cause = '', updated_at = datetime('now')
		WHERE app_name = ? AND status = ?`,
		OpPhaseComplete, OpStatusDone, appName, OpStatusRunning)
	if err != nil {
		return fmt.Errorf("complete operation %s: %w", appName, err)
	}
	return nil
}

// Get returns the current-or-last operation for appName, or nil if
// the app has no recorded operation.
func (s *OperationStore) Get(appName string) (*Operation, error) {
	var op Operation
	var retryable int
	err := s.db.QueryRow(`
		SELECT app_name, id, type, phase, status, retryable, cause, started_at, updated_at
		FROM operations WHERE app_name = ?`, appName).
		Scan(&op.AppName, &op.ID, &op.Type, &op.Phase, &op.Status, &retryable, &op.Cause, &op.StartedAt, &op.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get operation %s: %w", appName, err)
	}
	op.Retryable = retryable != 0
	return &op, nil
}

// ResolveFailed clears a stale failure: when a staleness re-run
// succeeds for an app whose last recorded operation was a failed
// reconcile drive, the old failure is no longer the app's last word.
// Only failed *reconcile* rows resolve this way — a failed install
// keeps its failure until an explicit new drive replaces it.
func (s *OperationStore) ResolveFailed(appName string) error {
	_, err := s.db.Exec(`
		UPDATE operations
		SET phase = ?, status = ?, updated_at = datetime('now')
		WHERE app_name = ? AND status = ? AND type = ?`,
		OpPhaseComplete, OpStatusDone, appName, OpStatusFailed, OpTypeReconcile)
	if err != nil {
		return fmt.Errorf("resolve failed operation %s: %w", appName, err)
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

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
