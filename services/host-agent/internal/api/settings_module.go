// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"log/slog"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"github.com/go-chi/chi/v5"
)

// AuthentikUserManagerInterface abstracts the subset of Authentik Client
// methods needed for user management (separate from OIDC client methods).
type AuthentikUserManagerInterface interface {
	CreateUser(ctx context.Context, username, password string) (int, error)
	SetUserPassword(ctx context.Context, userID int, password string) error
	SetUserEmail(ctx context.Context, userID int, email string) error
	ManagedUserEmail(username string) string
	ListUsers(ctx context.Context) ([]authentik.ManagedUserInfo, error)
	DeleteUser(ctx context.Context, username string) error
	AddUserToGroup(ctx context.Context, userID int, groupName string) error
	RemoveUserFromGroup(ctx context.Context, userID int, groupName string) error
	FindUserID(ctx context.Context, username string) (int, error)
}

// SettingsModule encapsulates all settings operations: tailnet management,
// host (domain) configuration, initial setup wizard, and user administration.
type settingsModule struct {
	tailnetStore    store.TailnetStoreInterface
	prefsStore      store.PreferencesStoreInterface
	sessionStore    store.SessionStoreInterface
	authentikClient AuthentikUserManagerInterface
	orch            orchestratorCaller
	authConfig      *AuthRef
	hostState       *hostset.State
	settingsStore   store.SettingsStoreInterface
	// selfPort is host-agent's own bind port. First-run adoption needs it to
	// tell a browser origin apart from an automation origin; see
	// adoptFirstRunHost.
	selfPort int
	logger   *slog.Logger
}

func NewSettingsModule(
	tailnetStore store.TailnetStoreInterface,
	prefsStore store.PreferencesStoreInterface,
	sessionStore store.SessionStoreInterface,
	authClient AuthentikUserManagerInterface,
	orch orchestratorCaller,
	authConfig *AuthRef,
	hostState *hostset.State,
	settingsStore store.SettingsStoreInterface,
	selfPort int,
	logger *slog.Logger,
) *settingsModule {
	return &settingsModule{
		tailnetStore:    tailnetStore,
		prefsStore:      prefsStore,
		sessionStore:    sessionStore,
		authentikClient: authClient,
		orch:            orch,
		authConfig:      authConfig,
		hostState:       hostState,
		settingsStore:   settingsStore,
		selfPort:        selfPort,
		logger:          logger,
	}
}

// ---- Tailnet ----

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

		authentikReady := m.authentikClientIsAvailable(r.Context(), m.authentikClient)

		respondJSON(w, http.StatusOK, SetupStatusResponse{
			SetupRequired:  !hasUsers,
			AuthentikReady: authentikReady,
			AuthReady:      m.authConfig.Get() != nil,
		})
	}
}

// authentikClientIsAvailable checks whether the Authentik client is ready.
func (m *settingsModule) authentikClientIsAvailable(ctx context.Context, client AuthentikUserManagerInterface) bool {
	if client == nil {
		return false
	}
	// A nil *authentik.Client stored in this interface is not a nil interface,
	// so the assertion below would succeed and call a nil receiver. Public
	// handlers reach this path before setup completes, where no Authentik client
	// exists yet: return false instead of panicking.
	if c, ok := client.(*authentik.Client); ok && c == nil {
		return false
	}
	if ac, ok := client.(interface{ IsAvailable(context.Context) bool }); ok {
		return ac.IsAvailable(ctx)
	}
	return false
}

// NewSetupRouter registers the first-run bootstrap routes. They MUST be public:
// before setup completes there is no user, so no admin can exist to authorize
// them. They are self-limiting instead: CreateFirstUserHandler refuses once any
// user exists (409), which is what makes unauthenticated registration safe.
func NewSetupRouter(mod *settingsModule, r chi.Router) {
	r.Get("/setup/status", mod.SetupStatusHandler())
	r.Post("/setup/create-user", mod.CreateFirstUserHandler())
}

// NewSettingsRouter registers the admin-only settings surface. Bootstrap routes
// deliberately live in NewSetupRouter (public) rather than here: registering the
// same pattern on two routers leaves the effective middleware up to chi's
// last-registration-wins order.
func NewSettingsRouter(mod *settingsModule, r chi.Router) {
	r.Get("/settings/public-url", mod.GetPublicURLHandler())
	r.Put("/settings/public-url", mod.SetPublicURLHandler())

	r.Get("/settings/tailnet", mod.GetTailnetHandler())
	r.Post("/settings/tailnet", mod.SetTailnetHandler())
	r.Delete("/settings/tailnet", mod.DeleteTailnetHandler())

	r.Get("/admin/users", mod.ListUsersHandler())
	r.Post("/admin/users", mod.CreateManagedUserHandler())
	r.Delete("/admin/users/{username}", mod.DeleteManagedUserHandler())
	r.Put("/admin/users/{username}/role", mod.SetUserRoleHandler())
}

// SetupStatusResponse represents the response for GET /api/setup/status.
type SetupStatusResponse struct {
	SetupRequired  bool `json:"setupRequired"`
	AuthentikReady bool `json:"authentikReady"`
	AuthReady      bool `json:"authReady"`
}
