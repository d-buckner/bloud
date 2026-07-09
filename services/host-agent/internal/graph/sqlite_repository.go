package graph

import (
	"database/sql"
	"fmt"
)

// SQLiteRepository implements GraphRepository using SQLite.
// Use NewSQLiteRepository to create one; provide an open *sql.DB whose schema
// already contains the graph_nodes and graph_edges tables.
type SQLiteRepository struct {
	db *sql.DB
}

// NewSQLiteRepository creates a GraphRepository backed by an open *sql.DB.
func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

func (r *SQLiteRepository) SaveNode(node Node) error {
	_, err := r.db.Exec(
		`INSERT INTO graph_nodes (id, target_status, actual_status, error) VALUES (?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     target_status = excluded.target_status,
		     actual_status = excluded.actual_status,
		     error         = excluded.error`,
		node.ID, string(node.TargetStatus), string(node.ActualStatus), node.Error,
	)
	return err
}

func (r *SQLiteRepository) GetNode(id string) (*Node, error) {
	var n Node
	var target, actual string
	err := r.db.QueryRow(
		`SELECT id, target_status, actual_status, error FROM graph_nodes WHERE id = ?`, id,
	).Scan(&n.ID, &target, &actual, &n.Error)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get graph node %q: %w", id, err)
	}
	n.TargetStatus = NodeStatus(target)
	n.ActualStatus = NodeStatus(actual)
	return &n, nil
}

func (r *SQLiteRepository) GetNodes() ([]Node, error) {
	rows, err := r.db.Query(`SELECT id, target_status, actual_status, error FROM graph_nodes`)
	if err != nil {
		return nil, fmt.Errorf("get graph nodes: %w", err)
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		var target, actual string
		if err := rows.Scan(&n.ID, &target, &actual, &n.Error); err != nil {
			return nil, fmt.Errorf("scan graph node: %w", err)
		}
		n.TargetStatus = NodeStatus(target)
		n.ActualStatus = NodeStatus(actual)
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func (r *SQLiteRepository) DeleteNode(id string) error {
	result, err := r.db.Exec(`DELETE FROM graph_nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("node %q not found", id)
	}
	return nil
}

func (r *SQLiteRepository) SaveEdge(dependentID, dependencyID string) error {
	_, err := r.db.Exec(
		`INSERT OR IGNORE INTO graph_edges (dependent_id, dependency_id) VALUES (?, ?)`,
		dependentID, dependencyID,
	)
	return err
}

func (r *SQLiteRepository) GetDependencies(nodeID string) ([]string, error) {
	rows, err := r.db.Query(
		`SELECT dependency_id FROM graph_edges WHERE dependent_id = ?`, nodeID,
	)
	if err != nil {
		return nil, fmt.Errorf("get dependencies for %q: %w", nodeID, err)
	}
	defer rows.Close()

	var deps []string
	for rows.Next() {
		var dep string
		if err := rows.Scan(&dep); err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}
	return deps, rows.Err()
}

func (r *SQLiteRepository) GetDependents(nodeID string) ([]string, error) {
	rows, err := r.db.Query(
		`SELECT dependent_id FROM graph_edges WHERE dependency_id = ?`, nodeID,
	)
	if err != nil {
		return nil, fmt.Errorf("get dependents for %q: %w", nodeID, err)
	}
	defer rows.Close()

	var deps []string
	for rows.Next() {
		var dep string
		if err := rows.Scan(&dep); err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}
	return deps, rows.Err()
}

func (r *SQLiteRepository) DeleteEdge(dependentID, dependencyID string) error {
	result, err := r.db.Exec(
		`DELETE FROM graph_edges WHERE dependent_id = ? AND dependency_id = ?`,
		dependentID, dependencyID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("edge %q → %q not found", dependentID, dependencyID)
	}
	return nil
}
