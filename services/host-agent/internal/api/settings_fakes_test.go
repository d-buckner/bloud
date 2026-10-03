// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"fmt"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
)

// FakeSettingsAuthentikClient implements AuthentikUserManagerInterface for testing.
type FakeSettingsAuthentikClient struct {
	users            map[string]*authentik.ManagedUserInfo
	userIDCounter    int
	lastAddedGroup   string
	lastRemovedGroup string
	lastCreatedUser  string
	listCalled       bool
	// failCreateUsername, when set, makes CreateUser fail with a duplicate
	// error for that username (simulates a user that already exists).
	failCreateUsername string
	lastSetPasswords   map[int]string
}

// NewFakeSettingsAuthentikClient creates a fake Authentik client for testing.
func NewFakeSettingsAuthentikClient() *FakeSettingsAuthentikClient {
	return &FakeSettingsAuthentikClient{
		users:         make(map[string]*authentik.ManagedUserInfo),
		userIDCounter: 1,
	}
}

func (f *FakeSettingsAuthentikClient) IsAvailable(context.Context) bool { return true }

func (f *FakeSettingsAuthentikClient) CreateUser(ctx context.Context, username, password string) (int, error) {
	if username == f.failCreateUsername {
		return 0, fmt.Errorf("creating user: status 400: {\"username\":[\"This field must be unique.\"]}")
	}
	id := f.userIDCounter
	f.userIDCounter++
	f.users[username] = &authentik.ManagedUserInfo{
		ID:       id,
		Username: username,
		Email:    f.ManagedUserEmail(username),
		IsAdmin:  false,
	}
	f.lastCreatedUser = username
	return id, nil
}

func (f *FakeSettingsAuthentikClient) SetUserPassword(ctx context.Context, userID int, password string) error {
	if f.lastSetPasswords == nil {
		f.lastSetPasswords = make(map[int]string)
	}
	f.lastSetPasswords[userID] = password
	return nil
}

func (f *FakeSettingsAuthentikClient) SetUserEmail(ctx context.Context, userID int, email string) error {
	u, ok := f.usersByPk(userID)
	if !ok {
		return fmt.Errorf("user %d not found", userID)
	}
	u.Email = email
	return nil
}

func (f *FakeSettingsAuthentikClient) usersByPk(pk int) (*authentik.ManagedUserInfo, bool) {
	for _, u := range f.users {
		if u.ID == pk {
			return u, true
		}
	}
	return nil, false
}

func (f *FakeSettingsAuthentikClient) ManagedUserEmail(username string) string {
	return username + "@localhost.local"
}

func (f *FakeSettingsAuthentikClient) ListUsers(ctx context.Context) ([]authentik.ManagedUserInfo, error) {
	f.listCalled = true
	var result []authentik.ManagedUserInfo
	for _, u := range f.users {
		result = append(result, *u)
	}
	return result, nil
}

func (f *FakeSettingsAuthentikClient) DeleteUser(ctx context.Context, username string) error {
	delete(f.users, username)
	return nil
}

func (f *FakeSettingsAuthentikClient) AddUserToGroup(ctx context.Context, userID int, groupName string) error {
	f.lastAddedGroup = groupName
	for _, u := range f.users {
		if u.ID == userID {
			u.IsAdmin = groupName == "authentik Admins"
			break
		}
	}
	return nil
}

func (f *FakeSettingsAuthentikClient) RemoveUserFromGroup(ctx context.Context, userID int, groupName string) error {
	f.lastRemovedGroup = groupName
	for _, u := range f.users {
		if u.ID == userID {
			u.IsAdmin = false
			break
		}
	}
	return nil
}

func (f *FakeSettingsAuthentikClient) FindUserID(ctx context.Context, username string) (int, error) {
	if u, ok := f.users[username]; ok {
		return u.ID, nil
	}
	return 0, fmt.Errorf("user not found: %s", username)
}

// Ensure interface compliance
var _ AuthentikUserManagerInterface = (*FakeSettingsAuthentikClient)(nil)

// ---- Types ----
