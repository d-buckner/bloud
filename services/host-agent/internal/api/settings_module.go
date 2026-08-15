package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"unicode/utf8"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"github.com/go-chi/chi/v5"
)

// AuthentikUserManagerInterface abstracts the subset of Authentik Client
// methods needed for user management (separate from OIDC client methods).
type AuthentikUserManagerInterface interface {
	CreateUser(username, password string) (int, error)
	ListUsers() ([]authentik.ManagedUserInfo, error)
	DeleteUser(username string) error
	AddUserToGroup(userID int, groupName string) error
	RemoveUserFromGroup(userID int, groupName string) error
	FindUserID(username string) (int, error)
}

// SettingsModule encapsulates all settings operations: tailnet management,
// initial setup wizard, and user administration.
type settingsModule struct {
	tailnetStore    store.TailnetStoreInterface
	prefsStore      store.PreferencesStoreInterface
	sessionStore    store.SessionStoreInterface
	authentikClient AuthentikUserManagerInterface
	orch            orchestratorCaller
	authConfig      *authConfigRef
	logger          *slog.Logger
}

// NewSettingsModule creates a new SettingsModule.
func NewSettingsModule(
	tailnetStore store.TailnetStoreInterface,
	prefsStore store.PreferencesStoreInterface,
	sessionStore store.SessionStoreInterface,
	authClient AuthentikUserManagerInterface,
	orch orchestratorCaller,
	authConfig *authConfigRef,
	logger *slog.Logger,
) *settingsModule {
	return &settingsModule{
		tailnetStore:    tailnetStore,
		prefsStore:      prefsStore,
		sessionStore:    sessionStore,
		authentikClient: authClient,
		orch:            orch,
		authConfig:      authConfig,
		logger:          logger,
	}
}

// ---- Tailnet ----

// GetTailnetHandler returns the current active tailnet connection, or null if none.
func (m *settingsModule) GetTailnetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := m.tailnetStore.GetActive()
		if err != nil {
			m.logger.Error("failed to get tailnet connection", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to get tailnet connection")
			return
		}

		if conn == nil {
			respondJSON(w, http.StatusOK, nil)
			return
		}

		respondJSON(w, http.StatusOK, toTailnetResponse(conn))
	}
}

// SetTailnetHandler validates the request and enqueues a SetTailnetIntent.
func (m *settingsModule) SetTailnetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req setTailnetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		if req.Type != "tailscale" && req.Type != "headscale" {
			respondError(w, http.StatusBadRequest, "type must be 'tailscale' or 'headscale'")
			return
		}
		if req.AuthKey == "" {
			respondError(w, http.StatusBadRequest, "authKey is required")
			return
		}
		if req.Type == "headscale" && req.ControlURL == "" {
			respondError(w, http.StatusBadRequest, "controlUrl is required for headscale")
			return
		}

		if m.orch == nil {
			respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
			return
		}

		intent := orchestrator.NewSetTailnetIntent(req.Name, req.Type, req.AuthKey, req.ControlURL)
		m.orch.Enqueue(intent)

		respondJSON(w, http.StatusAccepted, map[string]string{
			"intentId": intent.IntentID(),
		})
	}
}

// DeleteTailnetHandler validates that a connection exists and enqueues a DeleteTailnetIntent.
func (m *settingsModule) DeleteTailnetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := m.tailnetStore.GetActive()
		if err != nil {
			m.logger.Error("failed to get tailnet connection", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to get tailnet connection")
			return
		}
		if conn == nil {
			respondError(w, http.StatusNotFound, "no tailnet connection configured")
			return
		}

		if m.orch == nil {
			respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
			return
		}

		intent := orchestrator.NewDeleteTailnetIntent()
		m.orch.Enqueue(intent)

		respondJSON(w, http.StatusAccepted, map[string]string{
			"intentId": intent.IntentID(),
		})
	}
}

// ---- Setup Wizard ----

// SetupStatusHandler returns whether initial setup is required.
func (m *settingsModule) SetupStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hasUsers, err := m.prefsStore.HasUsers()
		if err != nil {
			m.logger.Error("failed to check users", "error", err)
			respondJSON(w, http.StatusInternalServerError, SetupStatusResponse{
				SetupRequired:  false,
				AuthentikReady: false,
			})
			return
		}

		authentikReady := m.authentikClientIsAvailable(m.authentikClient)

		respondJSON(w, http.StatusOK, SetupStatusResponse{
			SetupRequired:  !hasUsers,
			AuthentikReady: authentikReady,
			AuthReady:      m.authConfig.Get() != nil,
		})
	}
}

// authentikClientIsAvailable checks whether the Authentik client is ready.
func (m *settingsModule) authentikClientIsAvailable(client AuthentikUserManagerInterface) bool {
	if ac, ok := client.(interface{ IsAvailable() bool }); ok {
		return ac.IsAvailable()
	}
	return false
}

// CreateFirstUserHandler creates the first admin user during initial setup.
func (m *settingsModule) CreateFirstUserHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hasUsers, err := m.prefsStore.HasUsers()
		if err != nil {
			m.logger.Error("failed to check existing users", "error", err)
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

		if m.authentikClient == nil || !m.authentikClientIsAvailable(m.authentikClient) {
			respondJSON(w, http.StatusServiceUnavailable, CreateUserResponse{
				Success: false,
				Error:   "Authentik is not available. Please wait for it to start.",
			})
			return
		}

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

		authentikUserID, err := m.authentikClient.CreateUser(req.Username, req.Password)
		if err != nil {
			m.logger.Error("failed to create user in Authentik", "error", err)
			respondJSON(w, http.StatusInternalServerError, CreateUserResponse{
				Success: false,
				Error:   "Failed to create user in Authentik",
			})
			return
		}

		if err := m.authentikClient.AddUserToGroup(authentikUserID, "authentik Admins"); err != nil {
			m.logger.Warn("failed to add user to admins group", "error", err)
		}

		if err := m.prefsStore.EnsureUser(req.Username); err != nil {
			m.logger.Error("failed to create local user", "error", err)
			respondJSON(w, http.StatusInternalServerError, CreateUserResponse{
				Success: false,
				Error:   "Failed to create local user record",
			})
			return
		}

		if err := m.authentikClient.DeleteUser("akadmin"); err != nil {
			m.logger.Warn("failed to delete akadmin user", "error", err)
		} else {
			m.logger.Info("deleted default akadmin user")
		}

		m.logger.Info("first user created successfully", "username", req.Username)

		respondJSON(w, http.StatusOK, CreateUserResponse{
			Success: true,
		})
	}
}

// ---- User Management ----

// ListUsersHandler returns all managed users from Authentik with their roles.
func (m *settingsModule) ListUsersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.authentikClient == nil {
			respondError(w, http.StatusServiceUnavailable, "Authentik not available")
			return
		}

		users, err := m.authentikClient.ListUsers()
		if err != nil {
			m.logger.Error("failed to list users", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to list users")
			return
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
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

		userID, err := m.authentikClient.CreateUser(req.Username, req.Password)
		if err != nil {
			m.logger.Error("failed to create user in Authentik", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to create user")
			return
		}

		if req.Role == store.RoleAdmin {
			if err := m.authentikClient.AddUserToGroup(userID, "authentik Admins"); err != nil {
				m.logger.Error("failed to add user to admin group", "error", err)
				respondError(w, http.StatusInternalServerError, "user created but failed to set admin role")
				return
			}
		}

		if err := m.prefsStore.EnsureUser(req.Username); err != nil {
			m.logger.Error("failed to create local user preferences", "error", err)
		}

		respondJSON(w, http.StatusCreated, map[string]interface{}{
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

		if err := m.authentikClient.DeleteUser(username); err != nil {
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

		var req setUserRoleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		if req.Role != store.RoleAdmin && req.Role != store.RoleMember {
			respondError(w, http.StatusBadRequest, "role must be 'admin' or 'member'")
			return
		}

		currentUser := getUserFromContext(r.Context())
		if currentUser != nil && currentUser.Username == username && req.Role == store.RoleMember {
			respondError(w, http.StatusBadRequest, "cannot demote your own account")
			return
		}

		userID, err := m.authentikClient.FindUserID(username)
		if err != nil {
			m.logger.Error("failed to find user", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to find user")
			return
		}
		if userID == 0 {
			respondError(w, http.StatusNotFound, "user not found")
			return
		}

		if req.Role == store.RoleMember {
			users, err := m.authentikClient.ListUsers()
			if err != nil {
				m.logger.Error("failed to list users for last-admin check", "error", err)
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

		if req.Role == store.RoleAdmin {
			if err := m.authentikClient.AddUserToGroup(userID, "authentik Admins"); err != nil {
				m.logger.Error("failed to add user to admin group", "error", err)
				respondError(w, http.StatusInternalServerError, "failed to update role")
				return
			}
		} else {
			if err := m.authentikClient.RemoveUserFromGroup(userID, "authentik Admins"); err != nil {
				m.logger.Error("failed to remove user from admin group", "error", err)
				respondError(w, http.StatusInternalServerError, "failed to update role")
				return
			}
		}

		if m.sessionStore != nil {
			if err := m.sessionStore.DeleteByUsername(username); err != nil {
				m.logger.Warn("failed to invalidate user sessions", "username", username, "error", err)
			}
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"username": username,
			"role":     req.Role,
		})
	}
}

// ---- Router ----

// NewSettingsRouter registers all settings-related routes on the given router.
func NewSettingsRouter(mod *settingsModule, r chi.Router) {
	r.Get("/settings/tailnet", mod.GetTailnetHandler())
	r.Post("/settings/tailnet", mod.SetTailnetHandler())
	r.Delete("/settings/tailnet", mod.DeleteTailnetHandler())

	r.Get("/setup/status", mod.SetupStatusHandler())
	r.Post("/setup/create-user", mod.CreateFirstUserHandler())

	r.Get("/admin/users", mod.ListUsersHandler())
	r.Post("/admin/users", mod.CreateManagedUserHandler())
	r.Delete("/admin/users/{username}", mod.DeleteManagedUserHandler())
	r.Put("/admin/users/{username}/role", mod.SetUserRoleHandler())
}

// FakeSettingsAuthentikClient implements AuthentikUserManagerInterface for testing.
type FakeSettingsAuthentikClient struct {
	users          map[string]*authentik.ManagedUserInfo
	userIDCounter  int
	lastAddedGroup string
	lastRemovedGroup string
	lastCreatedUser string
	listCalled     bool
}

// NewFakeSettingsAuthentikClient creates a fake Authentik client for testing.
func NewFakeSettingsAuthentikClient() *FakeSettingsAuthentikClient {
	return &FakeSettingsAuthentikClient{
		users:         make(map[string]*authentik.ManagedUserInfo),
		userIDCounter: 1,
	}
}

func (f *FakeSettingsAuthentikClient) IsAvailable() bool { return true }

func (f *FakeSettingsAuthentikClient) CreateUser(username, password string) (int, error) {
	id := f.userIDCounter
	f.userIDCounter++
	f.users[username] = &authentik.ManagedUserInfo{
		ID:       id,
		Username: username,
		IsAdmin:  false,
	}
	f.lastCreatedUser = username
	return id, nil
}

func (f *FakeSettingsAuthentikClient) ListUsers() ([]authentik.ManagedUserInfo, error) {
	f.listCalled = true
	var result []authentik.ManagedUserInfo
	for _, u := range f.users {
		result = append(result, *u)
	}
	return result, nil
}

func (f *FakeSettingsAuthentikClient) DeleteUser(username string) error {
	delete(f.users, username)
	return nil
}

func (f *FakeSettingsAuthentikClient) AddUserToGroup(userID int, groupName string) error {
	f.lastAddedGroup = groupName
	for _, u := range f.users {
		if u.ID == userID {
			u.IsAdmin = groupName == "authentik Admins"
			break
		}
	}
	return nil
}

func (f *FakeSettingsAuthentikClient) RemoveUserFromGroup(userID int, groupName string) error {
	f.lastRemovedGroup = groupName
	for _, u := range f.users {
		if u.ID == userID {
			u.IsAdmin = false
			break
		}
	}
	return nil
}

func (f *FakeSettingsAuthentikClient) FindUserID(username string) (int, error) {
	if u, ok := f.users[username]; ok {
		return u.ID, nil
	}
	return 0, fmt.Errorf("user not found: %s", username)
}

// Ensure interface compliance
var _ AuthentikUserManagerInterface = (*FakeSettingsAuthentikClient)(nil)

// ---- Types ----

// tailnetResponse is the API response for a tailnet connection.
// The auth key is never exposed to the frontend.
type tailnetResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	HasAuthKey bool   `json:"hasAuthKey"`
	ControlURL string `json:"controlUrl"`
	Status     string `json:"status"`
}

// toTailnetResponse converts a store.TailnetConnection to tailnetResponse.
func toTailnetResponse(conn *store.TailnetConnection) tailnetResponse {
	return tailnetResponse{
		ID:         conn.ID,
		Name:       conn.Name,
		Type:       conn.Type,
		HasAuthKey: conn.AuthKey != "",
		ControlURL: conn.ControlURL,
		Status:     conn.Status,
	}
}

// setTailnetRequest is the request body for POST /api/settings/tailnet.
type setTailnetRequest struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	AuthKey    string `json:"authKey"`
	ControlURL string `json:"controlUrl"`
}

// SetupStatusResponse represents the response for GET /api/setup/status.
type SetupStatusResponse struct {
	SetupRequired  bool `json:"setupRequired"`
	AuthentikReady bool `json:"authentikReady"`
	AuthReady      bool `json:"authReady"`
}

// CreateUserRequest represents the request body for POST /api/setup/create-user.
type CreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CreateUserResponse represents the response for POST /api/setup/create-user.
type CreateUserResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// createUserRequest is the request body for POST /api/admin/users.
type createUserRequest struct {
	Username string     `json:"username"`
	Password string     `json:"password"`
	Role     store.Role `json:"role"`
}

// setUserRoleRequest is the request body for PUT /api/admin/users/{username}/role.
type setUserRoleRequest struct {
	Role store.Role `json:"role"`
}

// validateCreateUserRequest validates the create user request.
func validateCreateUserRequest(req CreateUserRequest) error {
	usernameLen := utf8.RuneCountInString(req.Username)
	if usernameLen < 3 || usernameLen > 30 {
		return &validationError{"Username must be between 3 and 30 characters"}
	}
	usernameRegex := regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	if !usernameRegex.MatchString(req.Username) {
		return &validationError{"Username can only contain letters, numbers, and underscores"}
	}
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
