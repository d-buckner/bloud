package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// User represents a Bloud user (credentials stored in Authentik)
type User struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

// GridItem represents an item (app or widget) in the layout grid
type GridItem struct {
	Type    string `json:"type"`    // "app" or "widget"
	ID      string `json:"id"`      // app name or widget id
	Col     int    `json:"col"`     // 1-based column position
	Row     int    `json:"row"`     // 1-based row position
	Colspan int    `json:"colspan"` // number of columns to span
	Rowspan int    `json:"rowspan"` // number of rows to span
}

// Layout represents the user's home page layout
type Layout struct {
	Items         []GridItem                `json:"items"`
	WidgetConfigs map[string]map[string]any `json:"widgetConfigs"`
}

// UserStore manages users in the database
type UserStore struct {
	db *sql.DB
}

// NewUserStore creates a new user store
func NewUserStore(db *sql.DB) *UserStore {
	return &UserStore{db: db}
}

// HasUsers checks if any users exist (fast - stops at first row)
func (s *UserStore) HasUsers() (bool, error) {
	var exists bool
	err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM users)").Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check users: %w", err)
	}
	return exists, nil
}

// Create adds a new user to the database
func (s *UserStore) Create(username string) error {
	_, err := s.db.Exec(
		"INSERT INTO users (username) VALUES ($1)",
		username,
	)
	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

// GetByUsername returns a user by username
func (s *UserStore) GetByUsername(username string) (*User, error) {
	var user User
	err := s.db.QueryRow(
		"SELECT id, username, created_at FROM users WHERE username = $1",
		username,
	).Scan(&user.ID, &user.Username, &user.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	return &user, nil
}

// GetLayout returns the user's layout preferences
func (s *UserStore) GetLayout(userID string) (*Layout, error) {
	var prefsJSON []byte
	err := s.db.QueryRow(
		"SELECT COALESCE(layout, '{\"items\":[],\"widgetConfigs\":{}}') FROM users WHERE id = $1",
		userID,
	).Scan(&prefsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get layout: %w", err)
	}

	var prefs Layout
	if err := json.Unmarshal(prefsJSON, &prefs); err != nil {
		return nil, fmt.Errorf("failed to parse layout: %w", err)
	}
	return &prefs, nil
}

// SetLayout updates the user's layout preferences
func (s *UserStore) SetLayout(userID string, prefs *Layout) error {
	prefsJSON, err := json.Marshal(prefs)
	if err != nil {
		return fmt.Errorf("failed to marshal layout: %w", err)
	}

	_, err = s.db.Exec(
		"UPDATE users SET layout = $1 WHERE id = $2",
		prefsJSON, userID,
	)
	if err != nil {
		return fmt.Errorf("failed to update layout: %w", err)
	}
	return nil
}

// AddAppToLayout adds an app to the user's layout at the next available position
func (s *UserStore) AddAppToLayout(userID string, appName string) error {
	prefs, err := s.GetLayout(userID)
	if err != nil {
		return err
	}
	if prefs == nil {
		prefs = &Layout{Items: []GridItem{}, WidgetConfigs: map[string]map[string]any{}}
	}

	// Check if app already exists
	for _, item := range prefs.Items {
		if item.Type == "app" && item.ID == appName {
			return nil // Already exists
		}
	}

	// Find next available position (simple algorithm: find first empty 1x1 slot)
	pos := findNextAvailablePosition(prefs.Items, 1, 1)

	prefs.Items = append(prefs.Items, GridItem{
		Type:    "app",
		ID:      appName,
		Col:     pos.col,
		Row:     pos.row,
		Colspan: 1,
		Rowspan: 1,
	})

	return s.SetLayout(userID, prefs)
}

// RemoveAppFromLayout removes an app from the user's layout
func (s *UserStore) RemoveAppFromLayout(userID string, appName string) error {
	prefs, err := s.GetLayout(userID)
	if err != nil {
		return err
	}
	if prefs == nil {
		return nil // No prefs, nothing to remove
	}

	// Filter out the app
	newItems := make([]GridItem, 0, len(prefs.Items))
	for _, item := range prefs.Items {
		if !(item.Type == "app" && item.ID == appName) {
			newItems = append(newItems, item)
		}
	}
	prefs.Items = newItems

	return s.SetLayout(userID, prefs)
}

// position helper for grid placement
type position struct {
	col int
	row int
}

// findNextAvailablePosition finds the next available grid position for an item
func findNextAvailablePosition(items []GridItem, colspan, rowspan int) position {
	const gridCols = 6

	// Find max row currently in use
	maxRow := 0
	for _, item := range items {
		endRow := item.Row + item.Rowspan - 1
		if endRow > maxRow {
			maxRow = endRow
		}
	}

	// Search for available space row by row
	for row := 1; row <= maxRow+10; row++ {
		for col := 1; col <= gridCols-colspan+1; col++ {
			canPlace := true
			for c := col; c < col+colspan && canPlace; c++ {
				for r := row; r < row+rowspan && canPlace; r++ {
					if isCellOccupied(items, c, r) {
						canPlace = false
					}
				}
			}
			if canPlace {
				return position{col: col, row: row}
			}
		}
	}

	// Fallback: place at end
	return position{col: 1, row: maxRow + 1}
}

// isCellOccupied checks if a cell is occupied by any item
func isCellOccupied(items []GridItem, col, row int) bool {
	for _, item := range items {
		endCol := item.Col + item.Colspan - 1
		endRow := item.Row + item.Rowspan - 1
		if col >= item.Col && col <= endCol && row >= item.Row && row <= endRow {
			return true
		}
	}
	return false
}

// AddAppToAllUsersLayouts adds an app to all users' layouts
// Called when an app is installed
func (s *UserStore) AddAppToAllUsersLayouts(appName string) error {
	// Get all user IDs
	rows, err := s.db.Query("SELECT id FROM users")
	if err != nil {
		return fmt.Errorf("failed to get users: %w", err)
	}
	defer rows.Close()

	var userIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("failed to scan user id: %w", err)
		}
		userIDs = append(userIDs, id)
	}

	// Add app to each user's layout
	for _, userID := range userIDs {
		if err := s.AddAppToLayout(userID, appName); err != nil {
			return fmt.Errorf("failed to add app to user %s layout: %w", userID, err)
		}
	}

	return nil
}

// RemoveAppFromAllUsersLayouts removes an app from all users' layouts
// Called when an app is uninstalled
func (s *UserStore) RemoveAppFromAllUsersLayouts(appName string) error {
	// Get all user IDs
	rows, err := s.db.Query("SELECT id FROM users")
	if err != nil {
		return fmt.Errorf("failed to get users: %w", err)
	}
	defer rows.Close()

	var userIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("failed to scan user id: %w", err)
		}
		userIDs = append(userIDs, id)
	}

	// Remove app from each user's layout
	for _, userID := range userIDs {
		if err := s.RemoveAppFromLayout(userID, appName); err != nil {
			return fmt.Errorf("failed to remove app from user %s layout: %w", userID, err)
		}
	}

	return nil
}
