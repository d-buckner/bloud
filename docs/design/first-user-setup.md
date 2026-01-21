# First User Setup Flow

## Overview

When a user visits the Bloud UI for the first time and no user account exists, the system should present a setup wizard to create the initial admin user. The user's credentials are created in Authentik (not stored locally), and all subsequent authentication flows through Authentik's OIDC.

## Goals

1. **Zero-config first run**: User can set up Bloud without editing config files
2. **Secure credential handling**: Passwords are never stored in Bloud's database
3. **Single source of truth**: Authentik manages all user identities
4. **Seamless transition**: After setup, normal OIDC login flow takes over

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         First Visit Flow                            │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  Browser ──► GET /                                                  │
│              │                                                      │
│              ▼                                                      │
│  Frontend checks: GET /api/setup/status                             │
│              │                                                      │
│              ├── { "setupRequired": true }  ──► Show Setup Wizard   │
│              │                                                      │
│              └── { "setupRequired": false } ──► Normal App (Auth)   │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────┐
│                         Setup Wizard Flow                           │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  1. User enters: username, password                                 │
│                                                                     │
│  2. Frontend: POST /api/setup/create-user                           │
│     {                                                               │
│       "username": "admin",                                          │
│       "password": "secretpassword"                                  │
│     }                                                               │
│                                                                     │
│  3. Backend:                                                        │
│     a. Verify no users exist (prevents hijacking)                   │
│     b. Create user in Authentik via API                             │
│     c. Add user to "authentik Admins" group                         │
│     d. Mark setup as complete (local flag)                          │
│     e. Return success                                               │
│                                                                     │
│  4. Frontend: Redirect to login (Authentik OIDC)                    │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

## API Design

### GET /api/setup/status

Check if initial setup is required.

**Response:**
```json
{
  "setupRequired": boolean,
  "authentikReady": boolean
}
```

- `setupRequired`: true if no admin user has been created
- `authentikReady`: true if Authentik is running and API is accessible (setup cannot proceed without this)

### POST /api/setup/create-user

Create the first admin user. Only works when `setupRequired` is true.

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
  "success": true,
  "loginUrl": "/auth/login"
}
```

**Response (error):**
```json
{
  "success": false,
  "error": "string describing the error"
}
```

**Error cases:**
- Setup already completed (409 Conflict)
- Authentik not available (503 Service Unavailable)
- Invalid input (400 Bad Request)
- User creation failed (500 Internal Server Error)

## Implementation Details

### Backend Changes

#### 1. Database Schema

Add a minimal users table to track Bloud users:

```sql
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

#### 2. Setup State Detection

Setup is required when no users exist in the local database:

```go
// HasUsers checks if any users exist (fast - stops at first row)
func (s *UserStore) HasUsers() (bool, error) {
    var exists bool
    err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM users)").Scan(&exists)
    return exists, err
}
```

#### 3. Authentik Client Extensions

Add to `pkg/authentik/client.go`:

```go
// CreateUser creates a new user in Authentik and sets their password
func (c *Client) CreateUser(username, password string) (int, error) {
    payload := map[string]interface{}{
        "username":  username,
        "name":      username,
        "path":      "users",
        "is_active": true,
    }
    // POST /api/v3/core/users/
    // Then set password via /api/v3/core/users/{id}/set_password/
}
```

#### 3. New API Routes

```go
// In routes.go
r.Route("/api/setup", func(r chi.Router) {
    r.Get("/status", s.handleSetupStatus)
    r.Post("/create-user", s.handleCreateUser)
})
```

#### 4. Handler Implementation

```go
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
    hasUsers, err := s.userStore.HasUsers()
    if err != nil {
        http.Error(w, "Failed to check users", http.StatusInternalServerError)
        return
    }

    authentikReady := s.authentikClient != nil && s.authentikClient.IsAvailable()

    json.NewEncoder(w).Encode(map[string]bool{
        "setupRequired":  !hasUsers,
        "authentikReady": authentikReady,
    })
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
    // 1. Check no users exist (prevents hijacking)
    hasUsers, err := s.userStore.HasUsers()
    if err != nil {
        http.Error(w, "Failed to check existing users", http.StatusInternalServerError)
        return
    }
    if hasUsers {
        http.Error(w, "Setup already completed", http.StatusConflict)
        return
    }

    // 2. Validate input
    var req CreateUserRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "Invalid request", http.StatusBadRequest)
        return
    }

    // 3. Create user in Authentik
    authentikUserID, err := s.authentikClient.CreateUser(req.Username, req.Password)
    if err != nil {
        http.Error(w, "Failed to create user", http.StatusInternalServerError)
        return
    }

    // 4. Add to admins group
    if err := s.authentikClient.AddUserToGroup(authentikUserID, "authentik Admins"); err != nil {
        // Log but don't fail - user can be added manually
    }

    // 5. Create local user record
    if err := s.userStore.Create(req.Username); err != nil {
        http.Error(w, "Failed to create local user", http.StatusInternalServerError)
        return
    }

    json.NewEncoder(w).Encode(map[string]interface{}{
        "success":  true,
        "loginUrl": "/auth/login",
    })
}
```

### Frontend Changes

#### 1. Setup Status Check

In root layout or a dedicated setup guard:

```svelte
<!-- +layout.svelte -->
<script lang="ts">
  import { onMount } from 'svelte';
  import SetupWizard from '$lib/components/SetupWizard.svelte';

  let setupRequired = false;
  let loading = true;

  onMount(async () => {
    const res = await fetch('/api/setup/status');
    const data = await res.json();
    setupRequired = data.setupRequired;
    loading = false;
  });
</script>

{#if loading}
  <LoadingSpinner />
{:else if setupRequired}
  <SetupWizard />
{:else}
  <slot />
{/if}
```

#### 2. Setup Wizard Component

```svelte
<!-- SetupWizard.svelte -->
<script lang="ts">
  let username = '';
  let password = '';
  let confirmPassword = '';
  let error = '';
  let submitting = false;

  async function handleSubmit() {
    if (password !== confirmPassword) {
      error = 'Passwords do not match';
      return;
    }

    submitting = true;
    error = '';

    const res = await fetch('/api/setup/create-user', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password })
    });

    const data = await res.json();

    if (data.success) {
      window.location.href = data.loginUrl;
    } else {
      error = data.error;
      submitting = false;
    }
  }
</script>

<div class="setup-wizard">
  <h1>Welcome to Bloud</h1>
  <p>Create your admin account to get started.</p>

  <form on:submit|preventDefault={handleSubmit}>
    <input type="text" bind:value={username} placeholder="Username" required />
    <input type="password" bind:value={password} placeholder="Password" required />
    <input type="password" bind:value={confirmPassword} placeholder="Confirm Password" required />

    {#if error}
      <p class="error">{error}</p>
    {/if}

    <button type="submit" disabled={submitting}>
      {submitting ? 'Creating...' : 'Create Account'}
    </button>
  </form>
</div>
```

## Security Considerations

1. **Race condition protection**: The `HasUsers()` check before creating ensures only one user can be created. A race is theoretically possible but extremely unlikely in practice (two people submitting setup at the exact same millisecond).

2. **HTTPS required**: Password is transmitted in plaintext to backend. In production, must use HTTPS (Traefik handles this)

3. **Rate limiting**: Add rate limiting to `/api/setup/create-user` to prevent brute force attempts during the brief setup window

4. **Input validation**:
   - Username: 3-30 chars, alphanumeric + underscore
   - Password: Minimum 8 characters

5. **No credential storage**: Bloud never stores the password - it's immediately sent to Authentik

6. **Authentik dependency**: Setup cannot proceed if Authentik is down. This is intentional - we need Authentik to store credentials.

## Migration Path

For existing installations, no migration needed - if Authentik already has users, `HasUsers()` returns true and setup wizard won't appear.

## Future Enhancements

1. **Invite flow**: Admin can generate invite links for additional users
2. **Password requirements display**: Show Authentik's password policy in the UI
3. **Email field**: Optional email for password recovery
4. **Recovery options**: Setup backup codes

## Testing Plan

1. **Fresh installation**: Verify setup wizard appears and user can be created
2. **Existing installation**: Verify setup wizard does not appear
3. **Authentik down**: Verify appropriate error message
4. **Invalid input**: Verify validation errors shown
5. **Concurrent requests**: Verify only one user can be created
6. **After setup**: Verify normal login flow works
