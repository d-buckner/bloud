// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
)

// ListUsersHandler returns all managed users from Authentik with their roles.
func (m *settingsModule) ListUsersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.authentikClient == nil {
			respondError(w, http.StatusServiceUnavailable, "Authentik not available")
			return
		}

		users, err := m.authentikClient.ListUsers(r.Context())
		if err != nil {
			m.logger.Error("failed to list users", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to list users")
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"users": users,
		})
	}
}

// CreateManagedUserHandler creates a new user in Authentik and ensures local preferences.
func (m *settingsModule) CreateManagedUserHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.authentikClient == nil {
			respondError(w, http.StatusServiceUnavailable, "Authentik not available")
			return
		}

		var req createUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		if req.Username == "" || req.Password == "" {
			respondError(w, http.StatusBadRequest, "username and password are required")
			return
		}

		if req.Role == "" {
			req.Role = store.RoleMember
		}

		if req.Role != store.RoleAdmin && req.Role != store.RoleMember {
			respondError(w, http.StatusBadRequest, "role must be 'admin' or 'member'")
			return
		}

		userID, err := m.authentikClient.CreateUser(r.Context(), req.Username, req.Password)
		if err != nil {
			m.logger.Error("failed to create user in Authentik", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to create user")
			return
		}

		if req.Role == store.RoleAdmin {
			if err := m.authentikClient.AddUserToGroup(r.Context(), userID, "authentik Admins"); err != nil {
				m.logger.Error("failed to add user to admin group", "error", err)
				respondError(w, http.StatusInternalServerError, "user created but failed to set admin role")
				return
			}
		}

		if err := m.prefsStore.EnsureUser(req.Username); err != nil {
			m.logger.Error("failed to create local user preferences", "error", err)
		}

		respondJSON(w, http.StatusCreated, map[string]any{
			"id":       userID,
			"username": req.Username,
			"role":     req.Role,
		})
	}
}

// DeleteManagedUserHandler deletes a user from Authentik and cleans up local data.
func (m *settingsModule) DeleteManagedUserHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.authentikClient == nil {
			respondError(w, http.StatusServiceUnavailable, "Authentik not available")
			return
		}

		username := chi.URLParam(r, "username")
		if username == "" {
			respondError(w, http.StatusBadRequest, "username is required")
			return
		}

		currentUser := getUserFromContext(r.Context())
		if currentUser != nil && currentUser.Username == username {
			respondError(w, http.StatusBadRequest, "cannot delete your own account")
			return
		}

		if err := m.authentikClient.DeleteUser(r.Context(), username); err != nil {
			m.logger.Error("failed to delete user from Authentik", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to delete user")
			return
		}

		if m.sessionStore != nil {
			if err := m.sessionStore.DeleteByUsername(username); err != nil {
				m.logger.Warn("failed to invalidate user sessions", "username", username, "error", err)
			}
		}

		if err := m.prefsStore.DeleteUser(username); err != nil {
			m.logger.Warn("failed to delete user preferences", "username", username, "error", err)
		}

		respondJSON(w, http.StatusOK, map[string]string{
			"status": "deleted",
		})
	}
}

// wouldOrphanAdmin reports whether the current admin count is <= 1, i.e.
// demoting another user would leave nobody with admin access.
func (m *settingsModule) wouldOrphanAdmin(ctx context.Context) (bool, error) {
	users, err := m.authentikClient.ListUsers(ctx)
	if err != nil {
		return false, err
	}
	adminCount := 0
	for _, u := range users {
		if u.IsAdmin {
			adminCount++
		}
	}
	return adminCount <= 1, nil
}

// SetUserRoleHandler changes a user's role by adding/removing from Authentik Admins group.
func (m *settingsModule) SetUserRoleHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.authentikClient == nil {
			respondError(w, http.StatusServiceUnavailable, "Authentik not available")
			return
		}

		username := chi.URLParam(r, "username")
		if username == "" {
			respondError(w, http.StatusBadRequest, "username is required")
			return
		}

		req, ok := decodeUserRoleRequest(w, r)
		if !ok {
			return
		}

		if m.isSelfDemotion(r, username, req.Role) {
			respondError(w, http.StatusBadRequest, "cannot demote your own account")
			return
		}

		userID, ok := m.lookupRoleUser(w, r.Context(), username)
		if !ok {
			return
		}

		if m.isLastAdminDemotion(w, r.Context(), req.Role) {
			return
		}

		if err := m.applyRole(r.Context(), userID, req.Role); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to update role")
			return
		}

		if m.sessionStore != nil {
			if err := m.sessionStore.DeleteByUsername(username); err != nil {
				m.logger.Warn("failed to invalidate user sessions", "username", username, "error", err)
			}
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"username": username,
			"role":     req.Role,
		})
	}
}

// decodeUserRoleRequest reads and validates the role payload: only the two
// roles the product has are accepted.
func decodeUserRoleRequest(w http.ResponseWriter, r *http.Request) (setUserRoleRequest, bool) {
	var req setUserRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return setUserRoleRequest{}, false
	}
	if req.Role != store.RoleAdmin && req.Role != store.RoleMember {
		respondError(w, http.StatusBadRequest, "role must be 'admin' or 'member'")
		return setUserRoleRequest{}, false
	}
	return req, true
}

// isSelfDemotion reports whether the caller is demoting the account they are
// logged in as, which is how an admin removes their own way back in.
func (m *settingsModule) isSelfDemotion(r *http.Request, username string, role store.Role) bool {
	if role != store.RoleMember {
		return false
	}
	currentUser := getUserFromContext(r.Context())
	return currentUser != nil && currentUser.Username == username
}

// isLastAdminDemotion reports, and answers, a demotion that would leave the
// instance with no admin at all. The count check runs after the user lookup so
// a request naming nobody still gets 404 rather than a confusing
// last-admin message.
func (m *settingsModule) isLastAdminDemotion(w http.ResponseWriter, ctx context.Context, role store.Role) bool {
	if role != store.RoleMember {
		return false
	}
	lastAdmin, err := m.wouldOrphanAdmin(ctx)
	if err != nil {
		m.logger.Error("failed to list users for last-admin check", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to verify admin count")
		return true
	}
	if lastAdmin {
		respondError(w, http.StatusBadRequest, "cannot demote the last admin")
		return true
	}
	return false
}

// lookupRoleUser resolves a username to its Authentik id, answering 404 when no
// such user exists.
func (m *settingsModule) lookupRoleUser(w http.ResponseWriter, ctx context.Context, username string) (int, bool) {
	userID, err := m.authentikClient.FindUserID(ctx, username)
	if err != nil {
		m.logger.Error("failed to find user", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to find user")
		return 0, false
	}
	if userID == 0 {
		respondError(w, http.StatusNotFound, "user not found")
		return 0, false
	}
	return userID, true
}

// applyRole moves the user into, or out of, the Authentik admins group.
func (m *settingsModule) applyRole(ctx context.Context, userID int, role store.Role) error {
	if role == store.RoleAdmin {
		if err := m.authentikClient.AddUserToGroup(ctx, userID, "authentik Admins"); err != nil {
			m.logger.Error("failed to add user to admin group", "error", err)
			return err
		}
		return nil
	}
	if err := m.authentikClient.RemoveUserFromGroup(ctx, userID, "authentik Admins"); err != nil {
		m.logger.Error("failed to remove user from admin group", "error", err)
		return err
	}
	return nil
}

// ---- Router ----

// setUserRoleRequest is the request body for PUT /api/admin/users/{username}/role.
type setUserRoleRequest struct {
	Role store.Role `json:"role"`
}
