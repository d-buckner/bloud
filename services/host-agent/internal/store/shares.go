package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Share represents a share of a local app with a remote guest
type Share struct {
	ID            string     `json:"id"`
	AppID         string     `json:"app_id"`
	SSOStrategy   string     `json:"sso_strategy"`
	GuestID       string     `json:"guest_id"`
	NodeShareLink string     `json:"node_share_link"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
}

// ShareStore manages shares in the database
type ShareStore struct {
	db *sql.DB
}

// NewShareStore creates a new share store
func NewShareStore(db *sql.DB) *ShareStore {
	return &ShareStore{db: db}
}

// Create inserts a new share
func (s *ShareStore) Create(share Share) error {
	_, err := s.db.Exec(`
		INSERT INTO shares (id, app_id, sso_strategy, guest_id, node_share_link, status)
		VALUES (?, ?, ?, ?, ?, ?)
	`, share.ID, share.AppID, share.SSOStrategy, share.GuestID, share.NodeShareLink, share.Status)
	if err != nil {
		return fmt.Errorf("failed to insert share: %w", err)
	}
	return nil
}

// GetByID returns a share by ID, or (nil, nil) if not found
func (s *ShareStore) GetByID(id string) (*Share, error) {
	row := s.db.QueryRow(`
		SELECT id, app_id, sso_strategy, guest_id, node_share_link, status, created_at, revoked_at
		FROM shares
		WHERE id = ?
	`, id)

	share, err := s.scanShareRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get share: %w", err)
	}
	return share, nil
}

// List returns all shares
func (s *ShareStore) List() ([]*Share, error) {
	rows, err := s.db.Query(`
		SELECT id, app_id, sso_strategy, guest_id, node_share_link, status, created_at, revoked_at
		FROM shares
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query shares: %w", err)
	}
	defer rows.Close()

	shares := []*Share{}
	for rows.Next() {
		share, err := s.scanShare(rows)
		if err != nil {
			return nil, err
		}
		shares = append(shares, share)
	}
	return shares, nil
}

// Revoke sets a share's status to 'revoked' and records the revocation time
func (s *ShareStore) Revoke(id string) error {
	result, err := s.db.Exec(`
		UPDATE shares SET status = 'revoked', revoked_at = datetime('now')
		WHERE id = ?
	`, id)
	if err != nil {
		return fmt.Errorf("failed to revoke share: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("share not found: %s", id)
	}
	return nil
}

func (s *ShareStore) scanShare(rows *sql.Rows) (*Share, error) {
	var share Share
	var createdAt string
	var revokedAt sql.NullString

	err := rows.Scan(
		&share.ID,
		&share.AppID,
		&share.SSOStrategy,
		&share.GuestID,
		&share.NodeShareLink,
		&share.Status,
		&createdAt,
		&revokedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan share: %w", err)
	}

	share.CreatedAt = parseSQLiteTime(createdAt)
	if revokedAt.Valid {
		t := parseSQLiteTime(revokedAt.String)
		share.RevokedAt = &t
	}

	return &share, nil
}

func (s *ShareStore) scanShareRow(row *sql.Row) (*Share, error) {
	var share Share
	var createdAt string
	var revokedAt sql.NullString

	err := row.Scan(
		&share.ID,
		&share.AppID,
		&share.SSOStrategy,
		&share.GuestID,
		&share.NodeShareLink,
		&share.Status,
		&createdAt,
		&revokedAt,
	)
	if err != nil {
		return nil, err
	}

	share.CreatedAt = parseSQLiteTime(createdAt)
	if revokedAt.Valid {
		t := parseSQLiteTime(revokedAt.String)
		share.RevokedAt = &t
	}

	return &share, nil
}
