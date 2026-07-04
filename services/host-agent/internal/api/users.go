package api

import (
	"encoding/json"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
)

// handleListUsers returns all managed users from Authentik with their roles
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if s.authentikClient == nil {
		respondError(w, http.StatusServiceUnavailable, "Authentik not available")
		return
	}

	users, err := s.authentikClient.ListUsers()
	if err != nil {
		s.logger.Error("failed to list users", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to list users")
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"users": users,
	})
}

// createUserRequest represents the request body for creating a user
type createUserRequest struct {
	Username string     `json:"username"`
	Password string     `json:"password"`
	Role     store.Role `json:"role"`
}

// handleCreateManagedUser creates a new user in Authentik and ensures local preferences
func (s *Server) handleCreateManagedUser(w http.ResponseWriter, r *http.Request) {
	if s.authentikClient == nil {
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

	// Create user in Authentik
	userID, err := s.authentikClient.CreateUser(req.Username, req.Password)
	if err != nil {
		s.logger.Error("failed to create user in Authentik", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	// If admin, add to authentik Admins group
	if req.Role == store.RoleAdmin {
		if err := s.authentikClient.AddUserToGroup(userID, "authentik Admins"); err != nil {
			s.logger.Error("failed to add user to admin group", "error", err)
			respondError(w, http.StatusInternalServerError, "user created but failed to set admin role")
			return
		}
	}

	// Ensure local preferences row
	if err := s.prefsStore.EnsureUser(req.Username); err != nil {
		s.logger.Error("failed to create local user preferences", "error", err)
	}

	respondJSON(w, http.StatusCreated, map[string]interface{}{
		"id":       userID,
		"username": req.Username,
		"role":     req.Role,
	})
}

// handleDeleteManagedUser deletes a user from Authentik and cleans up local data
func (s *Server) handleDeleteManagedUser(w http.ResponseWriter, r *http.Request) {
	if s.authentikClient == nil {
		respondError(w, http.StatusServiceUnavailable, "Authentik not available")
		return
	}

	username := chi.URLParam(r, "username")
	if username == "" {
		respondError(w, http.StatusBadRequest, "username is required")
		return
	}

	// Prevent self-deletion
	currentUser := getUserFromContext(r.Context())
	if currentUser != nil && currentUser.Username == username {
		respondError(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}

	// Delete from Authentik
	if err := s.authentikClient.DeleteUser(username); err != nil {
		s.logger.Error("failed to delete user from Authentik", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}

	// Invalidate sessions
	if s.sessionStore != nil {
		if err := s.sessionStore.DeleteByUsername(r.Context(), username); err != nil {
			s.logger.Warn("failed to invalidate user sessions", "username", username, "error", err)
		}
	}

	// Clean up local preferences
	if err := s.prefsStore.DeleteUser(username); err != nil {
		s.logger.Warn("failed to delete user preferences", "username", username, "error", err)
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status": "deleted",
	})
}

// setUserRoleRequest represents the request body for changing a user's role
type setUserRoleRequest struct {
	Role store.Role `json:"role"`
}

// handleSetUserRole changes a user's role by adding/removing from Authentik Admins group
func (s *Server) handleSetUserRole(w http.ResponseWriter, r *http.Request) {
	if s.authentikClient == nil {
		respondError(w, http.StatusServiceUnavailable, "Authentik not available")
		return
	}

	username := chi.URLParam(r, "username")
	if username == "" {
		respondError(w, http.StatusBadRequest, "username is required")
		return
	}

	var req setUserRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Role != store.RoleAdmin && req.Role != store.RoleMember {
		respondError(w, http.StatusBadRequest, "role must be 'admin' or 'member'")
		return
	}

	// Prevent self-demotion
	currentUser := getUserFromContext(r.Context())
	if currentUser != nil && currentUser.Username == username && req.Role == store.RoleMember {
		respondError(w, http.StatusBadRequest, "cannot demote your own account")
		return
	}

	// Find user ID in Authentik
	userID, err := s.authentikClient.FindUserID(username)
	if err != nil {
		s.logger.Error("failed to find user", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to find user")
		return
	}
	if userID == 0 {
		respondError(w, http.StatusNotFound, "user not found")
		return
	}

	// Check last-admin protection: if demoting, ensure at least one other admin remains
	if req.Role == store.RoleMember {
		users, err := s.authentikClient.ListUsers()
		if err != nil {
			s.logger.Error("failed to list users for last-admin check", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to verify admin count")
			return
		}

		adminCount := 0
		for _, u := range users {
			if u.IsAdmin {
				adminCount++
			}
		}

		if adminCount <= 1 {
			respondError(w, http.StatusBadRequest, "cannot demote the last admin")
			return
		}
	}

	// Apply role change
	if req.Role == store.RoleAdmin {
		if err := s.authentikClient.AddUserToGroup(userID, "authentik Admins"); err != nil {
			s.logger.Error("failed to add user to admin group", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to update role")
			return
		}
	} else {
		if err := s.authentikClient.RemoveUserFromGroup(userID, "authentik Admins"); err != nil {
			s.logger.Error("failed to remove user from admin group", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to update role")
			return
		}
	}

	// Invalidate user sessions so they re-login with new role
	if s.sessionStore != nil {
		if err := s.sessionStore.DeleteByUsername(r.Context(), username); err != nil {
			s.logger.Warn("failed to invalidate user sessions", "username", username, "error", err)
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"username": username,
		"role":     req.Role,
	})
}
