// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package store

import (
	"database/sql"
	"fmt"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
)

// Host is one admin-configured (custom) host. Built-in hosts
// (localhost, bloud.local) live in the hostset package, not in the store.
type Host struct {
	Hostname string `json:"hostname"`
	Primary  bool   `json:"primary"`
}

// HostStoreInterface defines the interface for managing custom hosts.
type HostStoreInterface interface {
	// List returns all stored custom hosts in stable order.
	List() ([]Host, error)
	// Replace atomically swaps the stored custom hosts for the given list.
	// primary may be "" (no stored primary) or the hostname of one of the
	// given hosts. Built-in hosts passed in are ignored.
	Replace(hosts []string, primary string) error
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
	rows, err := s.db.Query(`SELECT hostname, is_primary FROM hosts ORDER BY hostname`)
	if err != nil {
		return nil, fmt.Errorf("failed to query hosts: %w", err)
	}
	defer rows.Close()

	hosts := []Host{}
	for rows.Next() {
		var h Host
		var primary int
		if err := rows.Scan(&h.Hostname, &primary); err != nil {
			return nil, fmt.Errorf("failed to scan host: %w", err)
		}
		h.Primary = primary == 1
		hosts = append(hosts, h)
	}
	return hosts, nil
}

// Replace atomically swaps the stored custom hosts.
func (s *HostStore) Replace(hosts []string, primary string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin hosts transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM hosts`); err != nil {
		return fmt.Errorf("failed to clear hosts: %w", err)
	}
	seen := map[string]bool{}
	for _, hostname := range hosts {
		if hostname = hostset.Normalize(hostname); hostname == "" {
			return fmt.Errorf("invalid hostname %q", hostname)
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
		if _, err := tx.Exec(`INSERT INTO hosts (hostname, is_primary) VALUES (?, ?)`, hostname, isPrimary); err != nil {
			return fmt.Errorf("failed to insert host %q: %w", hostname, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit hosts: %w", err)
	}
	return nil
}
