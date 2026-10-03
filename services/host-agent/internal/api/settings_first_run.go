// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"unicode/utf8"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// adoptFirstRunHost makes the origin the first-run admin is standing on the
// primary host, so the login they are about to create round-trips on the
// address they actually use instead of bouncing to a localhost the outside
// world cannot reach.
//
// This is the only place a host becomes primary without an authenticated
// admin call, and what makes it safe is when it runs: the caller has just
// committed the admin credential on a box that had no users a moment
// earlier. Anonymous traffic cannot reach it, and once any user exists the
// handler 409s before this point is ever reached.
//
// It is deliberately not wired into /auth/login: an unauthenticated login
// redirect must never widen the OAuth client's redirect-URI allowlist.
//
// Returns the origin it adopted, or "" when there was nothing to adopt
// (already the public address, an unusable Host header, or no orchestrator to
// route through).
func (m *settingsModule) adoptFirstRunHost(r *http.Request) string {
	if m.orch == nil || m.hostState == nil {
		return ""
	}
	// The Host header keeps the port the client used, and that port is part of
	// the origin the redirect URI has to match. hostOnly() strips it, which is
	// right for matching a hostname and wrong here: adopting
	// http://bloud.example.com when the operator is on :8443 registers a
	// redirect URI for a port nothing serves.
	//
	// The exception is this process's own port, and it is the important one.
	// A first admin created through the loopback API (the CLI, the e2e
	// harness, any automation) arrives with Host: localhost:3000, which says
	// nothing about where the instance is publicly reachable: :3000 is the
	// internal ops bind, never a public entrypoint. Adopting it stores an
	// address that every later OAuth redirect is refused on, because login on
	// the agent port is exactly what isDirectAgentRequest rejects, so the
	// install ends up unable to log itself in. The browser-origin rule stops
	// here and the existing address is kept.
	observed, err := hostset.ParsePublicURL(string(requestScheme(r)) + "://" + r.Host)
	if err != nil {
		return ""
	}
	if m.selfPort > 0 && observed.Port == m.selfPort {
		m.logger.Info("not adopting the agent's own port as the public address",
			"observed", observed.Origin(), "agentPort", m.selfPort)
		return ""
	}
	if err != nil {
		return ""
	}

	hs := m.hostState.Get()
	if hs.PrimaryBaseURL() == observed.Origin() {
		return ""
	}

	m.orch.Submit(orchestrator.NewSetPublicURLIntent(observed.Origin()))
	m.logger.Info("adopted the origin this install was set up from as the public address",
		"url", observed.Origin(), "previous", hs.PrimaryBaseURL())
	return observed.Origin()
}

// CreateFirstUserHandler creates the first admin user during initial setup.
func (m *settingsModule) CreateFirstUserHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := m.decodeFirstUser(w, r)
		if !ok {
			return
		}
		authentikUserID, err := m.provisionFirstAdmin(r.Context(), req)
		if err != nil {
			respondSetupFailure(w, err)
			return
		}
		m.finishFirstUser(w, r, req, authentikUserID)
	}
}

// firstUserError is a setup step that failed with a client-visible status and
// reason, so the wizard can show what went wrong without the handler threading
// a status code through every branch.
type firstUserError struct {
	status int
	msg    string
}

func (e *firstUserError) Error() string { return e.msg }

// respondSetupFailure writes a setup failure. A firstUserError carries its own
// status and message; anything else is a plain 500.
func respondSetupFailure(w http.ResponseWriter, err error) {
	var fail *firstUserError
	if errors.As(err, &fail) {
		respondJSON(w, fail.status, CreateUserResponse{Success: false, Error: fail.msg})
		return
	}
	respondJSON(w, http.StatusInternalServerError, CreateUserResponse{Success: false, Error: err.Error()})
}

// decodeFirstUser reads and validates the setup request, refusing it when setup
// has already completed, the identity provider is unreachable, or the payload
// is unusable.
func (m *settingsModule) decodeFirstUser(w http.ResponseWriter, r *http.Request) (CreateUserRequest, bool) {
	hasUsers, err := m.prefsStore.HasUsers()
	if err != nil {
		m.logger.Error("failed to check existing users", "error", err)
		respondJSON(w, http.StatusInternalServerError, CreateUserResponse{
			Success: false,
			Error:   "Failed to check existing users",
		})
		return CreateUserRequest{}, false
	}
	if hasUsers {
		respondJSON(w, http.StatusConflict, CreateUserResponse{
			Success: false,
			Error:   "Setup already completed",
		})
		return CreateUserRequest{}, false
	}

	if m.authentikClient == nil || !m.authentikClientIsAvailable(r.Context(), m.authentikClient) {
		respondJSON(w, http.StatusServiceUnavailable, CreateUserResponse{
			Success: false,
			Error:   "Authentik is not available. Please wait for it to start.",
		})
		return CreateUserRequest{}, false
	}

	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, CreateUserResponse{
			Success: false,
			Error:   "Invalid request body",
		})
		return CreateUserRequest{}, false
	}

	if err := validateCreateUserRequest(req); err != nil {
		respondJSON(w, http.StatusBadRequest, CreateUserResponse{
			Success: false,
			Error:   err.Error(),
		})
		return CreateUserRequest{}, false
	}
	return req, true
}

// provisionFirstAdmin creates the admin in Authentik and returns its id. A
// fresh install already has an "admin" user, because the bootstrap script
// creates it before Bloud's setup completes, so a duplicate username is
// expected rather than fatal: adopt that account by resetting its password.
// Adopted accounts may predate managed-user emails or carry an unusable one
// (no TLD), so the identity email is rewritten to a valid one for the SSO
// apps to build accounts from.
func (m *settingsModule) provisionFirstAdmin(ctx context.Context, req CreateUserRequest) (int, error) {
	authentikUserID, err := m.authentikClient.CreateUser(ctx, req.Username, req.Password)
	if err == nil {
		return authentikUserID, nil
	}
	existingID, findErr := m.authentikClient.FindUserID(ctx, req.Username)
	if findErr != nil || existingID == 0 {
		m.logger.Error("failed to create user in Authentik", "error", err)
		return 0, &firstUserError{status: http.StatusInternalServerError, msg: "Failed to create user in Authentik"}
	}
	if setErr := m.authentikClient.SetUserPassword(ctx, existingID, req.Password); setErr != nil {
		m.logger.Error("failed to set password for existing Authentik user", "error", setErr)
		return 0, &firstUserError{status: http.StatusInternalServerError, msg: "Failed to update the user in Authentik"}
	}
	if setErr := m.authentikClient.SetUserEmail(ctx, existingID, m.authentikClient.ManagedUserEmail(req.Username)); setErr != nil {
		m.logger.Warn("failed to set email for adopted Authentik user", "error", setErr)
	}
	m.logger.Info("adopted existing Authentik user for initial setup", "username", req.Username)
	return existingID, nil
}

// finishFirstUser completes the local side of setup: the admins group, the
// local user record, the removal of the install-time akadmin account, and the
// adoption of the origin the operator used.
func (m *settingsModule) finishFirstUser(w http.ResponseWriter, r *http.Request, req CreateUserRequest, authentikUserID int) {
	if err := m.authentikClient.AddUserToGroup(r.Context(), authentikUserID, "authentik Admins"); err != nil {
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

	if err := m.authentikClient.DeleteUser(r.Context(), "akadmin"); err != nil {
		m.logger.Warn("failed to delete akadmin user", "error", err)
	} else {
		m.logger.Info("deleted default akadmin user")
	}

	m.logger.Info("first user created successfully", "username", req.Username)

	// The account exists now, so setup is over and this is the last moment
	// the acting party is provably the admin. Adopt the origin they used so
	// the first login lands on it rather than on the default localhost.
	adopted := m.adoptFirstRunHost(r)

	respondJSON(w, http.StatusOK, CreateUserResponse{
		Success:    true,
		AdoptedURL: adopted,
	})
}

// ---- User Management ----

// CreateUserRequest represents the request body for POST /api/setup/create-user.
type CreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CreateUserResponse represents the response for POST /api/setup/create-user.
type CreateUserResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	// AdoptedURL is the origin first-run setup adopted for the install, when
	// it differed from the default. Empty when nothing was adopted. The wizard
	// uses its presence to know the SSO stack is about to be re-provisioned
	// and that it must wait for that before reloading.
	AdoptedURL string `json:"adoptedUrl,omitempty"`
}

// createUserRequest is the request body for POST /api/admin/users.
type createUserRequest struct {
	Username string     `json:"username"`
	Password string     `json:"password"`
	Role     store.Role `json:"role"`
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
