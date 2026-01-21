package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"unicode/utf8"
)

// SetupStatusResponse represents the response for GET /api/setup/status
type SetupStatusResponse struct {
	SetupRequired  bool `json:"setupRequired"`
	AuthentikReady bool `json:"authentikReady"`
}

// CreateUserRequest represents the request body for POST /api/setup/create-user
type CreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CreateUserResponse represents the response for POST /api/setup/create-user
type CreateUserResponse struct {
	Success  bool   `json:"success"`
	LoginURL string `json:"loginUrl,omitempty"`
	Error    string `json:"error,omitempty"`
}

// handleSetupStatus returns whether initial setup is required
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.userStore.HasUsers()
	if err != nil {
		s.logger.Error("failed to check users", "error", err)
		respondJSON(w, http.StatusInternalServerError, SetupStatusResponse{
			SetupRequired:  false,
			AuthentikReady: false,
		})
		return
	}

	authentikReady := s.authentikClient != nil && s.authentikClient.IsAvailable()

	respondJSON(w, http.StatusOK, SetupStatusResponse{
		SetupRequired:  !hasUsers,
		AuthentikReady: authentikReady,
	})
}

// handleCreateUser creates the first admin user
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	// Check no users exist (prevents hijacking)
	hasUsers, err := s.userStore.HasUsers()
	if err != nil {
		s.logger.Error("failed to check existing users", "error", err)
		respondJSON(w, http.StatusInternalServerError, CreateUserResponse{
			Success: false,
			Error:   "Failed to check existing users",
		})
		return
	}
	if hasUsers {
		respondJSON(w, http.StatusConflict, CreateUserResponse{
			Success: false,
			Error:   "Setup already completed",
		})
		return
	}

	// Check Authentik is available
	if s.authentikClient == nil || !s.authentikClient.IsAvailable() {
		respondJSON(w, http.StatusServiceUnavailable, CreateUserResponse{
			Success: false,
			Error:   "Authentik is not available. Please wait for it to start.",
		})
		return
	}

	// Parse and validate input
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, CreateUserResponse{
			Success: false,
			Error:   "Invalid request body",
		})
		return
	}

	if err := validateCreateUserRequest(req); err != nil {
		respondJSON(w, http.StatusBadRequest, CreateUserResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	// Create user in Authentik
	authentikUserID, err := s.authentikClient.CreateUser(req.Username, req.Password)
	if err != nil {
		s.logger.Error("failed to create user in Authentik", "error", err)
		respondJSON(w, http.StatusInternalServerError, CreateUserResponse{
			Success: false,
			Error:   "Failed to create user in Authentik",
		})
		return
	}

	// Add to admins group
	if err := s.authentikClient.AddUserToGroup(authentikUserID, "authentik Admins"); err != nil {
		s.logger.Warn("failed to add user to admins group", "error", err)
		// Don't fail - user can be added manually
	}

	// Create local user record
	if err := s.userStore.Create(req.Username); err != nil {
		s.logger.Error("failed to create local user", "error", err)
		respondJSON(w, http.StatusInternalServerError, CreateUserResponse{
			Success: false,
			Error:   "Failed to create local user record",
		})
		return
	}

	// Delete the default akadmin user now that we have a real admin
	if err := s.authentikClient.DeleteUser("akadmin"); err != nil {
		s.logger.Warn("failed to delete akadmin user", "error", err)
		// Don't fail - not critical, but log for awareness
	} else {
		s.logger.Info("deleted default akadmin user")
	}

	s.logger.Info("first user created successfully", "username", req.Username)

	respondJSON(w, http.StatusOK, CreateUserResponse{
		Success:  true,
		LoginURL: "/auth/login",
	})
}

// validateCreateUserRequest validates the create user request
func validateCreateUserRequest(req CreateUserRequest) error {
	// Username validation: 3-30 chars, alphanumeric + underscore
	usernameLen := utf8.RuneCountInString(req.Username)
	if usernameLen < 3 || usernameLen > 30 {
		return &validationError{"Username must be between 3 and 30 characters"}
	}
	usernameRegex := regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	if !usernameRegex.MatchString(req.Username) {
		return &validationError{"Username can only contain letters, numbers, and underscores"}
	}

	// Password validation: minimum 8 characters
	passwordLen := utf8.RuneCountInString(req.Password)
	if passwordLen < 8 {
		return &validationError{"Password must be at least 8 characters"}
	}

	return nil
}

type validationError struct {
	message string
}

func (e *validationError) Error() string {
	return e.message
}
