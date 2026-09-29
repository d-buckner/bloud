// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"fmt"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
)

// Host is one admin-configured (custom) host. Built-in hosts
// (localhost, bloud.local) live in the hostset package, not in the store.
//
// Scheme is the URL scheme this host is served under: "http", "https", or
// "" for the default. It exists because a host behind a TLS-terminating
// proxy is reached at https:// while Bloud itself speaks plain http, so the
// scheme cannot be inferred from anything Bloud observes at its own socket.
// It is stored per host rather than globally because redirect URIs are
// registered per host and every one of them has to be exact.
type Host struct {
	Hostname string `json:"hostname"`
	Primary  bool   `json:"primary"`
	Scheme   string `json:"scheme,omitempty"`
}

// HostStoreInterface defines the interface for managing custom hosts.
type HostStoreInterface interface {
	// List returns all stored custom hosts in stable order.
	List() ([]Host, error)
	// Replace atomically swaps the stored custom hosts for the given list.
	// primary may be "" (no stored primary) or the hostname of one of the
	// given hosts. Built-in hosts passed in are ignored.
	Replace(hosts []Host, primary string) error
}

// Compile-time assertion that HostStore implements HostStoreInterface
var _ HostStoreInterface = (*HostStore)(nil)

// HostStore manages custom hosts in the database.
type HostStore struct {
	db *sql.DB
}

// NewHostStore creates a new host store.
func NewHostStore(db *sql.DB) *HostStore {
	return &HostStore{db: db}
}

// List returns all stored custom hosts ordered by hostname.
func (s *HostStore) List() ([]Host, error) {
	rows, err := s.db.Query(`SELECT hostname, is_primary, scheme FROM hosts ORDER BY hostname`)
	if err != nil {
		return nil, fmt.Errorf("failed to query hosts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	hosts := []Host{}
	for rows.Next() {
		var h Host
		var primary int
		if err := rows.Scan(&h.Hostname, &primary, &h.Scheme); err != nil {
			return nil, fmt.Errorf("failed to scan host: %w", err)
		}
		h.Primary = primary == 1
		hosts = append(hosts, h)
	}
	return hosts, nil
}

// Replace atomically swaps the stored custom hosts.
func (s *HostStore) Replace(hosts []Host, primary string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin hosts transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM hosts`); err != nil {
		return fmt.Errorf("failed to clear hosts: %w", err)
	}
	seen := map[string]bool{}
	for _, in := range hosts {
		hostname := hostset.Normalize(in.Hostname)
		if hostname == "" {
			return fmt.Errorf("invalid hostname %q", in.Hostname)
		}
		scheme := hostset.NormalizeScheme(in.Scheme)
		if in.Scheme != "" && scheme == "" {
			return fmt.Errorf("invalid scheme %q for host %q", in.Scheme, hostname)
		}
		if hostset.BuiltinSet()[hostname] {
			continue // built-ins are implicit, never stored
		}
		if seen[hostname] {
			continue
		}
		seen[hostname] = true
		isPrimary := 0
		if primary == hostname {
			isPrimary = 1
		}
		if _, err := tx.Exec(`INSERT INTO hosts (hostname, is_primary, scheme) VALUES (?, ?, ?)`, hostname, isPrimary, scheme); err != nil {
			return fmt.Errorf("failed to insert host %q: %w", hostname, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit hosts: %w", err)
	}
	return nil
}
