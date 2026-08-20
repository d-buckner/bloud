// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"
)

const (
	defaultSessionTTL = 7 * 24 * time.Hour // 7 days

	// sessionPurgeInterval is how often the background purger removes
	// expired sessions. SQLite has no TTL, so without this, sessions of
	// users who never return would accumulate forever.
	sessionPurgeInterval = 1 * time.Hour
)

// formatSessionTime renders a time as RFC3339 in UTC so stored values
// sort correctly as strings and compare consistently in SQL.
func formatSessionTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// Session represents an authenticated user session.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SessionStore manages sessions in SQLite.
type SessionStore struct {
	db  *sql.DB
	ttl time.Duration
}

// NewSessionStore creates a new SQLite-backed session store.
func NewSessionStore(db *sql.DB) *SessionStore {
	return &SessionStore{
		db:  db,
		ttl: defaultSessionTTL,
	}
}

// Create creates a new session for a user.
func (s *SessionStore) Create(userID, username string, role Role) (*Session, error) {
	sessionID, err := generateSessionID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate session ID: %w", err)
	}

	now := time.Now()
	session := &Session{
		ID:        sessionID,
		UserID:    userID,
		Username:  username,
		Role:      role,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}

	_, err = s.db.Exec(
		"INSERT INTO sessions (id, user_id, username, role, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)",
		session.ID, session.UserID, session.Username, string(session.Role),
		formatSessionTime(session.CreatedAt), formatSessionTime(session.ExpiresAt),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to store session: %w", err)
	}

	return session, nil
}

// Get retrieves a session by ID.
func (s *SessionStore) Get(sessionID string) (*Session, error) {
	var createdAt, expiresAt string
	var userID, username, role string

	err := s.db.QueryRow(
		"SELECT user_id, username, role, created_at, expires_at FROM sessions WHERE id = ?",
		sessionID,
	).Scan(&userID, &username, &role, &createdAt, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, nil // Session not found
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	parsedCreatedAt, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("failed to parse created_at: %w", err)
	}
	parsedExpiresAt, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("failed to parse expires_at: %w", err)
	}

	return &Session{
		ID:        sessionID,
		UserID:    userID,
		Username:  username,
		Role:      Role(role),
		CreatedAt: parsedCreatedAt,
		ExpiresAt: parsedExpiresAt,
	}, nil
}

// Delete removes a session.
func (s *SessionStore) Delete(sessionID string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", sessionID)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

// DeleteByUserID removes all sessions for a user.
func (s *SessionStore) DeleteByUserID(userID string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
	if err != nil {
		return fmt.Errorf("failed to delete sessions by user ID: %w", err)
	}
	return nil
}

// DeleteByUsername removes all sessions for a user by username.
func (s *SessionStore) DeleteByUsername(username string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE username = ?", username)
	if err != nil {
		return fmt.Errorf("failed to delete sessions by username: %w", err)
	}
	return nil
}

// Refresh extends a session's TTL.
func (s *SessionStore) Refresh(sessionID string) error {
	// Get current session
	session, err := s.Get(sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found")
	}

	// Update expiry
	newExpiresAt := time.Now().Add(s.ttl)
	_, err = s.db.Exec(
		"UPDATE sessions SET expires_at = ? WHERE id = ?",
		formatSessionTime(newExpiresAt), sessionID,
	)
	if err != nil {
		return fmt.Errorf("failed to update session: %w", err)
	}
	return nil
}

// PurgeExpired removes all sessions whose expiry has passed and returns
// the number of rows removed.
func (s *SessionStore) PurgeExpired() (int64, error) {
	res, err := s.db.Exec(
		"DELETE FROM sessions WHERE expires_at <= ?",
		formatSessionTime(time.Now()),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to purge expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to count purged sessions: %w", err)
	}
	return n, nil
}

// StartSessionPurger removes expired sessions immediately and then
// periodically until ctx is cancelled. Intended to be called once at
// startup; the goroutine exits when the context is done.
func StartSessionPurger(ctx context.Context, s *SessionStore, logger *slog.Logger) {
	go func() {
		purgeExpiredSessions(ctx, s, logger)
		ticker := time.NewTicker(sessionPurgeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				purgeExpiredSessions(ctx, s, logger)
			}
		}
	}()
}

func purgeExpiredSessions(ctx context.Context, s *SessionStore, logger *slog.Logger) {
	n, err := s.PurgeExpired()
	if err != nil {
		logger.Error("session purge failed", "error", err)
		return
	}
	if n > 0 {
		logger.Info("purged expired sessions", "count", n)
	}
}

// Close is a no-op for SQLite session store (no connection to close).
func (s *SessionStore) Close() error {
	return nil
}

// generateSessionID creates a cryptographically secure random session ID.
func generateSessionID() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
