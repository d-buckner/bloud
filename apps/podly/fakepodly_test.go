// SPDX-License-Identifier: AGPL-3.0-only

package podly

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// fakePodly is a stand-in for Podly's auth API, shaped by what the real routes
// do rather than by what is convenient: the status endpoint answers with no
// session, every user route demands one, a wrong password is a 401 rather than
// a redirect, and a duplicate username on create is a 409. Those four facts are
// what the configurator's logic branches on, so they are what a regression has
// to keep true.
//
// The session cookie is opaque here. Flask signs its own; what matters to the
// configurator is only that a login issues one and that the user routes refuse
// requests without it.
type fakePodly struct {
	mu sync.Mutex

	// requireAuth mirrors REQUIRE_AUTH. With it off the app answers 404 on every
	// auth route, including login, which is the real behaviour.
	requireAuth bool
	// users maps a username to its password; roles maps it to its role.
	users map[string]string
	roles map[string]string
	// sessions maps an issued cookie value to the username it authenticates.
	sessions map[string]string

	requests []string
}

// sessionCookieName is the name the app actually sets: Flask's default, renamed
// by the app to podly_session (src/app/__init__.py, _configure_session).
const sessionCookieName = "podly_session"

func newFakePodly() *fakePodly {
	return &fakePodly{
		requireAuth: true,
		users:       map[string]string{serviceUser: "service-password"},
		roles:       map[string]string{serviceUser: adminRole},
		sessions:    map[string]string{},
	}
}

func (f *fakePodly) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)

	switch {
	case r.URL.Path == authStatusPath:
		writeFakeJSON(w, http.StatusOK, map[string]any{
			"require_auth":         f.requireAuth,
			"landing_page_enabled": false,
		})
	case r.URL.Path == loginPath && r.Method == http.MethodPost:
		f.login(w, r)
	case r.URL.Path == usersPath && r.Method == http.MethodGet:
		f.listUsers(w, r)
	case r.URL.Path == usersPath && r.Method == http.MethodPost:
		f.createUser(w, r)
	case strings.HasPrefix(r.URL.Path, usersPath+"/") && r.Method == http.MethodPatch:
		f.patchUser(w, r, strings.TrimPrefix(r.URL.Path, usersPath+"/"))
	default:
		writeFakeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (f *fakePodly) login(w http.ResponseWriter, r *http.Request) {
	if !f.requireAuth {
		writeFakeJSON(w, http.StatusNotFound, map[string]string{"error": "Authentication is disabled."})
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decodeFakeBody(r, &body)
	if body.Username == "" || body.Password == "" {
		writeFakeJSON(w, http.StatusBadRequest, map[string]string{"error": "Username and password are required."})
		return
	}
	if stored, ok := f.users[body.Username]; !ok || stored != body.Password {
		writeFakeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid username or password."})
		return
	}
	sid := "sid-" + body.Username
	f.sessions[sid] = body.Username
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: sid, Path: "/"})
	writeFakeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{"username": body.Username, "role": f.roles[body.Username]},
	})
}

func (f *fakePodly) listUsers(w http.ResponseWriter, r *http.Request) {
	if !f.requireAdmin(w, r) {
		return
	}
	users := make([]map[string]any, 0, len(f.users))
	for name, role := range f.roles {
		users = append(users, map[string]any{"username": name, "role": role})
	}
	writeFakeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (f *fakePodly) createUser(w http.ResponseWriter, r *http.Request) {
	if !f.requireAdmin(w, r) {
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	decodeFakeBody(r, &body)
	if _, dup := f.users[body.Username]; dup {
		writeFakeJSON(w, http.StatusConflict, map[string]string{"error": "A user with that username already exists."})
		return
	}
	f.users[body.Username] = body.Password
	f.roles[body.Username] = body.Role
	writeFakeJSON(w, http.StatusCreated, map[string]any{"user": map[string]any{"username": body.Username}})
}

func (f *fakePodly) patchUser(w http.ResponseWriter, r *http.Request, username string) {
	if !f.requireAdmin(w, r) {
		return
	}
	if _, ok := f.users[username]; !ok {
		writeFakeJSON(w, http.StatusNotFound, map[string]string{"error": "User not found."})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	decodeFakeBody(r, &body)
	// The real route ignores an empty password rather than clearing one.
	if body.Password != "" {
		f.users[username] = body.Password
	}
	writeFakeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// requireAdmin enforces the gate every user route carries: no session is a 401,
// a session that is not an admin is a 403.
func (f *fakePodly) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !f.requireAuth {
		writeFakeJSON(w, http.StatusNotFound, map[string]string{"error": "Authentication is disabled."})
		return false
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		writeFakeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Authentication required."})
		return false
	}
	user, ok := f.sessions[cookie.Value]
	if !ok {
		writeFakeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Authentication required."})
		return false
	}
	if f.roles[user] != adminRole {
		writeFakeJSON(w, http.StatusForbidden, map[string]string{"error": "Admin privileges required."})
		return false
	}
	return true
}

// passwordOf reads the password the fake currently holds for a user.
func (f *fakePodly) passwordOf(username string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pw, ok := f.users[username]
	return pw, ok
}

func (f *fakePodly) hasUser(username string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.users[username]
	return ok
}

// roleOf reads the role the fake holds for a user.
func (f *fakePodly) roleOf(username string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.roles[username]
}

// count returns how many requests matched "METHOD /path".
func (f *fakePodly) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := method + " " + path
	n := 0
	for _, got := range f.requests {
		if got == want {
			n++
		}
	}
	return n
}

// removeUser deletes an account the way the app's own UI does, so a test can
// model an operator undoing what Bloud created.
func (f *fakePodly) removeUser(username string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.users, username)
	delete(f.roles, username)
}

// total is every request the fake has served.
func (f *fakePodly) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakePodly) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}

func decodeFakeBody(r *http.Request, out any) {
	defer func() { _ = r.Body.Close() }()
	_ = json.NewDecoder(r.Body).Decode(out)
}

func writeFakeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
