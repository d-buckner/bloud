# Bloud Authentication

## Overview

After first-user setup is complete, Bloud should require authentication to access. Users authenticate via Authentik (OIDC), and Bloud validates their session before allowing access.

## Current State

- First-user setup creates a user in Authentik and a local record
- No login enforcement - anyone can access Bloud after setup
- Authentik is already running and configured for app SSO

## Goals

1. Protect Bloud UI and API behind authentication
2. Use Authentik as the identity provider (OIDC)
3. Simple session management - no complex role/permission system yet

## Architecture Decision

### Options Considered

**Option A: Forward Auth via Traefik**
```
Browser → Traefik → Authentik Forward Auth → Bloud
```
- Pros: Already working for other apps, no code changes, session handled by Authentik
- Cons: Less control, can't have unauthenticated endpoints, adds latency

**Option B: OIDC Integration in Bloud**
```
Browser → Bloud → (redirect to Authentik) → Bloud validates token
```
- Pros: Full control over routes, mix auth/unauth endpoints, better UX
- Cons: More code, need to handle token refresh

**Option C: Hybrid - Forward Auth + API Tokens**
- Pros: Simple UI auth, supports API access
- Cons: Two auth mechanisms to maintain

### Decision: Option B (OIDC Integration)

Forward auth won't work well because:
1. `/api/setup/status` must be accessible without auth
2. We want to show a login page, not redirect immediately
3. Future features (user preferences, API tokens) need user context in the backend

---

## Phase 1: OIDC Login Flow

**Goal:** Users can log in via Authentik and access protected routes.

### 1.1 Create Bloud OAuth2 Application in Authentik

During Authentik's DynamicConfig phase, create an OAuth2 provider and application for Bloud itself:

```go
// In authentik configurator
func (c *Configurator) ensureBloudOAuthApp() error {
    // Create OAuth2 provider for Bloud
    // Client ID: "bloud"
    // Redirect URI: http://localhost:8080/auth/callback
    // Scopes: openid, profile, email
}
```

### 1.2 Auth Endpoints

```
GET  /auth/login     → Redirect to Authentik authorization URL
GET  /auth/callback  → Handle OAuth callback, create session
POST /auth/logout    → Clear session, redirect to Authentik logout
GET  /api/auth/me    → Return current user info (or 401)
```

### 1.3 Session Management

Store sessions server-side in PostgreSQL:

```sql
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,           -- Random session ID
    user_id INTEGER REFERENCES users(id),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP NOT NULL
);
```

Session ID stored in HTTP-only cookie.

### 1.4 Auth Middleware

```go
func (s *Server) authMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        session := s.getSessionFromCookie(r)
        if session == nil || session.IsExpired() {
            http.Error(w, "Unauthorized", http.StatusUnauthorized)
            return
        }
        ctx := context.WithValue(r.Context(), userContextKey, session.User)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

### 1.5 Route Protection

```go
s.router.Route("/api", func(r chi.Router) {
    // Public routes (no auth required)
    r.Get("/health", s.handleHealth)
    r.Route("/setup", func(r chi.Router) {
        r.Get("/status", s.handleSetupStatus)
        r.Post("/create-user", s.handleCreateUser)
    })

    // Auth routes (handle login/logout)
    r.Route("/auth", func(r chi.Router) {
        r.Get("/login", s.handleLogin)
        r.Get("/callback", s.handleOAuthCallback)
        r.Post("/logout", s.handleLogout)
        r.Get("/me", s.handleGetCurrentUser)
    })

    // Protected routes (require auth)
    r.Group(func(r chi.Router) {
        r.Use(s.authMiddleware)
        r.Route("/apps", func(r chi.Router) { /* ... */ })
        r.Route("/system", func(r chi.Router) { /* ... */ })
    })
})
```

### Phase 1 Testing

**Unit Tests:**
- `TestAuthMiddleware_NoSession` - Returns 401 without session
- `TestAuthMiddleware_ValidSession` - Returns 200 with valid session
- `TestAuthMiddleware_ExpiredSession` - Returns 401 with expired session

**Integration Tests:**
- `TestOIDCFlow_Login` - `/auth/login` redirects to Authentik
- `TestOIDCFlow_Callback` - Callback creates session and redirects

**Manual Verification:**
- [ ] Navigate to protected route without session → 401
- [ ] `/auth/login` redirects to Authentik
- [ ] After Authentik login, redirected back with session cookie
- [ ] Protected routes now accessible

### Phase 1 Tech Debt Opportunities

1. **OIDC Library Choice**: Currently implementing OIDC manually. Consider using `github.com/coreos/go-oidc` for better standards compliance and less maintenance burden.

2. **Session Storage**: In-memory sessions would be faster for single-node deployments. Current PostgreSQL approach is correct for durability but adds latency. Could add Redis caching layer later.

3. **State Parameter Handling**: OIDC state should be stored server-side with expiry to prevent CSRF. Currently using simple random string - could add cryptographic binding to session.

---

## Phase 2: Frontend Integration

**Goal:** Frontend shows login page when unauthenticated and handles auth state.

### 2.1 Auth State Store

```typescript
// lib/stores/auth.ts
interface AuthState {
    user: User | null;
    loading: boolean;
}

export const auth = writable<AuthState>({ user: null, loading: true });

export async function checkAuth(): Promise<boolean> {
    const res = await fetch('/api/auth/me');
    if (res.ok) {
        const user = await res.json();
        auth.set({ user, loading: false });
        return true;
    }
    auth.set({ user: null, loading: false });
    return false;
}
```

### 2.2 Layout Update

```svelte
<!-- +layout.svelte -->
{#if loading}
    <LoadingSpinner />
{:else if setupRequired}
    <SetupWizard />
{:else if !$auth.user}
    <LoginPage />
{:else}
    <App />
{/if}
```

### 2.3 Login Page

Simple page with "Sign in with Authentik" button that redirects to `/auth/login`.

### Phase 2 Testing

**Manual Verification:**
- [ ] Fresh page load shows loading spinner briefly
- [ ] Unauthenticated user sees login page
- [ ] "Sign in" button redirects to Authentik
- [ ] After login, dashboard loads
- [ ] Refresh maintains auth state

### Phase 2 Tech Debt Opportunities

1. **Auth State Persistence**: Currently re-checking `/api/auth/me` on every page load. Could cache auth state in localStorage with short TTL to reduce API calls.

2. **Loading States**: Single loading boolean. Consider a state machine (`idle | checking | authenticated | unauthenticated`) for clearer state management.

3. **Error Handling**: No handling for network failures during auth check. Should show error state with retry option.

---

## Phase 3: Session Lifecycle

**Goal:** Handle token refresh and session expiry gracefully.

### 3.1 Session Duration Strategy

OIDC access tokens expire (typically 5-60 minutes). Options:

1. **Short sessions, re-login** - Simple but annoying
2. **Refresh tokens** - Store refresh token, use to get new access token
3. **Long session cookie** - Session lasts longer than access token, re-validate periodically

**Decision:** Option 3 - Session cookie lasts 7 days, periodic Authentik validation.

### 3.2 Periodic Validation

```go
func (s *Server) authMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        session := s.getSessionFromCookie(r)
        if session == nil || session.IsExpired() {
            http.Error(w, "Unauthorized", http.StatusUnauthorized)
            return
        }

        // Re-validate with Authentik if last check was > 1 hour ago
        if session.NeedsRevalidation() {
            if !s.authentikClient.ValidateUser(session.UserID) {
                s.sessionStore.Delete(session.ID)
                http.Error(w, "Unauthorized", http.StatusUnauthorized)
                return
            }
            s.sessionStore.UpdateLastValidated(session.ID)
        }

        ctx := context.WithValue(r.Context(), userContextKey, session.User)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

### 3.3 Session Cleanup

Background job to delete expired sessions:

```go
func (s *Server) cleanupExpiredSessions() {
    ticker := time.NewTicker(1 * time.Hour)
    for range ticker.C {
        s.sessionStore.DeleteExpired()
    }
}
```

### Phase 3 Testing

**Unit Tests:**
- `TestSessionStore_ExpiredSession` - Expired sessions return nil
- `TestSessionStore_DeleteExpired` - Cleanup removes old sessions

**Manual Verification:**
- [ ] Session persists across browser restarts
- [ ] After 7 days, session expires and login required
- [ ] Deactivating user in Authentik invalidates session within 1 hour

### Phase 3 Tech Debt Opportunities

1. **Refresh Token Flow**: Current periodic validation requires Authentik to be available. Proper refresh token flow would be more resilient.

2. **Session Revocation**: No real-time session revocation when user is deactivated. Webhook from Authentik could enable immediate invalidation.

3. **Cleanup Job**: Simple ticker-based cleanup. Could use PostgreSQL's `pg_cron` or a proper job scheduler for more reliability.

---

## Phase 4: Users Store

**Goal:** Maintain local user records for session association and user management.

### 4.1 Overview

Bloud maintains a local `users` table as a lightweight reference to users whose credentials are stored in Authentik. This separation ensures:

- **Single source of truth for credentials**: Authentik manages passwords, MFA, and authentication
- **Local user context**: Bloud can associate data (preferences, sessions) with users without querying Authentik
- **Fast lookups**: Session validation doesn't require Authentik API calls

### 4.2 Database Schema

```sql
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

### 4.3 UserStore Interface

Located in `internal/store/users.go`:

```go
type UserStore struct {
    db *sql.DB
}

// HasUsers checks if any users exist (used for first-user setup detection)
func (s *UserStore) HasUsers() (bool, error)

// Create adds a new user to the database
func (s *UserStore) Create(username string) error

// GetByUsername returns a user by username
func (s *UserStore) GetByUsername(username string) (*User, error)

// GetByID returns a user by ID
func (s *UserStore) GetByID(id int) (*User, error)

// List returns all users
func (s *UserStore) List() ([]*User, error)

// Delete removes a user by ID
func (s *UserStore) Delete(id int) error
```

### 4.4 Synchronization with Authentik

The local users table may become out of sync with Authentik if users are created/deleted directly in Authentik.

**Strategy: On-demand sync** - When user logs in via OIDC but has no local record, create one:

```go
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
    // ... validate token, get user info from Authentik ...

    user, err := s.userStore.GetByUsername(userInfo.Username)
    if err != nil {
        // Handle error
    }
    if user == nil {
        // User authenticated but no local record - create one
        if err := s.userStore.Create(userInfo.Username); err != nil {
            // Log but don't fail - user can still use the system
        }
        user, _ = s.userStore.GetByUsername(userInfo.Username)
    }

    // Create session with user
}
```

### Phase 4 Testing

**Unit Tests:**
```go
func TestUserStore_HasUsers(t *testing.T) {
    db := setupTestDB(t)
    store := NewUserStore(db)

    has, err := store.HasUsers()
    require.NoError(t, err)
    assert.False(t, has)

    err = store.Create("testuser")
    require.NoError(t, err)

    has, err = store.HasUsers()
    require.NoError(t, err)
    assert.True(t, has)
}

func TestUserStore_Create(t *testing.T) {
    db := setupTestDB(t)
    store := NewUserStore(db)

    err := store.Create("testuser")
    require.NoError(t, err)

    // Duplicate username should fail
    err = store.Create("testuser")
    assert.Error(t, err)
}

func TestUserStore_GetByUsername(t *testing.T) {
    db := setupTestDB(t)
    store := NewUserStore(db)

    user, err := store.GetByUsername("nonexistent")
    require.NoError(t, err)
    assert.Nil(t, user)

    store.Create("testuser")
    user, err = store.GetByUsername("testuser")
    require.NoError(t, err)
    assert.Equal(t, "testuser", user.Username)
}
```

### Phase 4 Tech Debt Opportunities

1. **Authentik User ID**: Currently only storing username. Should store Authentik's user ID (`pk`) for more reliable sync and to handle username changes.

2. **Email Field**: No email stored locally. Would be needed for notifications feature.

3. **Soft Delete**: Currently hard-deleting users. Soft delete with `deleted_at` timestamp would preserve audit trail and allow recovery.

4. **User Metadata**: No way to store additional user preferences locally. Consider JSON `metadata` column.

---

## Phase 5: User Management

**Goal:** Admins can add and remove users through the Bloud UI.

### 5.1 User Creation Flow

```
┌─────────────────────────────────────────────────────────────────────┐
│                      Add User Flow                                   │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  1. Admin clicks "Add User" in settings                             │
│                                                                     │
│  2. Admin enters: username, password                                │
│                                                                     │
│  3. Frontend: POST /api/users                                       │
│     {                                                               │
│       "username": "newuser",                                        │
│       "password": "userpassword"                                    │
│     }                                                               │
│                                                                     │
│  4. Backend:                                                        │
│     a. Validate admin is authenticated                              │
│     b. Create user in Authentik via API                             │
│     c. Create local user record                                     │
│     d. Return success                                               │
│                                                                     │
│  5. New user can now log in via Authentik OIDC                      │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### 5.2 API Endpoints

#### POST /api/users

Create a new user. Requires authentication.

**Request:**
```json
{
  "username": "string (3-30 chars, alphanumeric + underscore)",
  "password": "string (min 8 chars)"
}
```

**Response (success):**
```json
{
  "id": 2,
  "username": "newuser",
  "created_at": "2024-01-15T10:30:00Z"
}
```

**Error cases:**
- 401 Unauthorized - Not authenticated
- 400 Bad Request - Invalid input or username already exists
- 503 Service Unavailable - Authentik not available

#### GET /api/users

List all users. Requires authentication.

**Response:**
```json
{
  "users": [
    { "id": 1, "username": "admin", "created_at": "2024-01-01T00:00:00Z" },
    { "id": 2, "username": "newuser", "created_at": "2024-01-15T10:30:00Z" }
  ]
}
```

#### DELETE /api/users/:id

Delete a user. Requires authentication.

**Behavior:**
1. Removes user from local database
2. Deactivates user in Authentik (does not delete, for audit trail)
3. Invalidates all active sessions for that user

**Error cases:**
- 401 Unauthorized - Not authenticated
- 403 Forbidden - Cannot delete yourself
- 404 Not Found - User doesn't exist

### 5.3 Handler Implementation

```go
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
    var req CreateUserRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "Invalid request", http.StatusBadRequest)
        return
    }

    if err := validateUsername(req.Username); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    existing, err := s.userStore.GetByUsername(req.Username)
    if err != nil {
        http.Error(w, "Failed to check existing user", http.StatusInternalServerError)
        return
    }
    if existing != nil {
        http.Error(w, "Username already exists", http.StatusBadRequest)
        return
    }

    _, err = s.authentikClient.CreateUser(req.Username, req.Password)
    if err != nil {
        http.Error(w, "Failed to create user in Authentik", http.StatusInternalServerError)
        return
    }

    if err := s.userStore.Create(req.Username); err != nil {
        http.Error(w, "Failed to create local user", http.StatusInternalServerError)
        return
    }

    user, _ := s.userStore.GetByUsername(req.Username)
    json.NewEncoder(w).Encode(user)
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
    users, err := s.userStore.List()
    if err != nil {
        http.Error(w, "Failed to list users", http.StatusInternalServerError)
        return
    }
    json.NewEncoder(w).Encode(map[string]interface{}{"users": users})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
    userID, _ := strconv.Atoi(chi.URLParam(r, "id"))
    currentUser := getUserFromContext(r.Context())

    if currentUser.ID == userID {
        http.Error(w, "Cannot delete yourself", http.StatusForbidden)
        return
    }

    user, err := s.userStore.GetByID(userID)
    if err != nil || user == nil {
        http.Error(w, "User not found", http.StatusNotFound)
        return
    }

    if err := s.authentikClient.DeactivateUser(user.Username); err != nil {
        // Log but continue - local deletion is the priority
    }

    if err := s.userStore.Delete(userID); err != nil {
        http.Error(w, "Failed to delete user", http.StatusInternalServerError)
        return
    }

    s.sessionStore.DeleteByUserID(userID)

    w.WriteHeader(http.StatusNoContent)
}
```

### 5.4 Routes

```go
r.Group(func(r chi.Router) {
    r.Use(s.authMiddleware)

    r.Route("/users", func(r chi.Router) {
        r.Get("/", s.handleListUsers)
        r.Post("/", s.handleCreateUser)
        r.Delete("/{id}", s.handleDeleteUser)
    })
})
```

### Phase 5 Testing

**Unit Tests:**
```go
func TestCreateUser_DuplicateUsername(t *testing.T) {
    server := setupTestServer(t)
    adminSession := createAdminSession(t, server)

    server.userStore.Create("existinguser")

    body := `{"username": "existinguser", "password": "testpass123"}`
    req := httptest.NewRequest("POST", "/api/users", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    req.AddCookie(&http.Cookie{Name: "session", Value: adminSession})
    rec := httptest.NewRecorder()

    server.router.ServeHTTP(rec, req)

    assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDeleteUser_CannotDeleteSelf(t *testing.T) {
    server := setupTestServer(t)

    server.userStore.Create("admin")
    admin, _ := server.userStore.GetByUsername("admin")
    session, _ := server.sessionStore.Create(admin.ID, 24*time.Hour)

    req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/users/%d", admin.ID), nil)
    req.AddCookie(&http.Cookie{Name: "session", Value: session.ID})
    rec := httptest.NewRecorder()

    server.router.ServeHTTP(rec, req)

    assert.Equal(t, http.StatusForbidden, rec.Code)
}
```

**Integration Tests:**
```go
func TestCreateUser_Success(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }

    server := setupIntegrationServer(t)
    adminSession := createAdminSession(t, server)

    body := `{"username": "newuser", "password": "testpass123"}`
    req := httptest.NewRequest("POST", "/api/users", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    req.AddCookie(&http.Cookie{Name: "session", Value: adminSession})
    rec := httptest.NewRecorder()

    server.router.ServeHTTP(rec, req)

    assert.Equal(t, http.StatusOK, rec.Code)

    var user User
    json.Unmarshal(rec.Body.Bytes(), &user)
    assert.Equal(t, "newuser", user.Username)
}
```

**Manual Verification:**
- [ ] Navigate to Settings > Users
- [ ] Click "Add User", enter credentials
- [ ] User appears in list
- [ ] Log out, log in as new user
- [ ] Delete user (not yourself)
- [ ] Deleted user cannot log in

### Phase 5 Tech Debt Opportunities

1. **Role-Based Access Control**: Currently all authenticated users can manage users. Should restrict to admins only. Requires adding `role` or `is_admin` column.

2. **Invite Flow**: Current flow requires admin to set password. Better UX would be invite link where user sets their own password.

3. **Password Policy Display**: No visibility into Authentik's password requirements. Should fetch and display password policy in UI.

4. **Transactional Consistency**: Creating user in Authentik then locally isn't atomic. If local creation fails, orphan user exists in Authentik. Should implement compensation/rollback.

5. **Audit Logging**: No record of who created/deleted users. Add audit log table for compliance.

---

## Security Considerations

1. **Session cookies**: HTTP-only, Secure (in production), SameSite=Lax
2. **CSRF protection**: For state-changing requests
3. **Session invalidation**: On logout, on password change
4. **Rate limiting**: On login endpoints

## Migration

For existing installations (users exist but no session):
- All requests will get 401 until user logs in
- Frontend detects 401 and shows login page

---

## End-to-End Test Checklist

### Fresh Installation
- [ ] Navigate to http://localhost:8080
- [ ] Setup wizard appears
- [ ] Create admin user with valid credentials
- [ ] Redirected to login page
- [ ] Login with created credentials
- [ ] Dashboard loads successfully

### Session Persistence
- [ ] Close browser, reopen
- [ ] Session remains valid (no re-login required)
- [ ] After 7 days (or configured expiry), session expires

### Adding Users
- [ ] Navigate to Settings > Users
- [ ] Click "Add User"
- [ ] Enter new username and password
- [ ] User appears in list
- [ ] Log out, log in as new user

### Deleting Users
- [ ] As admin, delete a non-admin user
- [ ] User removed from list
- [ ] Deleted user cannot log in
- [ ] Cannot delete yourself

### Protected Routes
- [ ] Without session: `/api/apps` returns 401
- [ ] Without session: `/api/setup/status` returns 200 (public)
- [ ] With valid session: `/api/apps` returns 200

### Logout
- [ ] Click logout
- [ ] Session invalidated
- [ ] Redirected to login page
- [ ] Back button doesn't restore session

---

## Test Utilities

```go
// test_helpers.go

func setupTestDB(t *testing.T) *sql.DB {
    db, err := sql.Open("postgres", "postgres://test:test@localhost/bloud_test?sslmode=disable")
    require.NoError(t, err)

    _, err = db.Exec(schemaSQL)
    require.NoError(t, err)

    t.Cleanup(func() {
        db.Exec("TRUNCATE users, sessions CASCADE")
        db.Close()
    })

    return db
}

func setupTestServer(t *testing.T) *Server {
    db := setupTestDB(t)
    return NewServer(Config{
        DB:              db,
        AuthentikClient: &MockAuthentikClient{},
    })
}

func createAdminSession(t *testing.T, s *Server) string {
    s.userStore.Create("admin")
    user, _ := s.userStore.GetByUsername("admin")
    session, _ := s.sessionStore.Create(user.ID, 24*time.Hour)
    return session.ID
}

type MockAuthentikClient struct{}

func (m *MockAuthentikClient) CreateUser(username, password string) (int, error) {
    return 1, nil
}

func (m *MockAuthentikClient) DeactivateUser(username string) error {
    return nil
}

func (m *MockAuthentikClient) IsAvailable() bool {
    return true
}
```

---

## Open Questions

1. Do we need role-based access control? (admin vs regular user)
2. Should API routes support both session and API token auth?
3. Should we add email field to users table for notifications?

---

## Files to Create/Modify

**New files:**
- `internal/api/auth.go` - Auth handlers (login, callback, logout, me)
- `internal/api/users.go` - User management handlers
- `internal/auth/oidc.go` - OIDC client wrapper
- `internal/store/sessions.go` - Session store
- `web/src/lib/stores/auth.ts` - Auth state
- `web/src/lib/components/LoginPage.svelte` - Login UI

**Modified files:**
- `internal/db/schema.sql` - Add sessions table
- `internal/api/routes.go` - Add auth routes, protect existing routes
- `internal/api/server.go` - Initialize OIDC client
- `apps/authentik/configurator.go` - Create Bloud OAuth app
- `web/src/routes/+layout.svelte` - Add auth check
