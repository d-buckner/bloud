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

	// whisper is the app's combined-config whisper section, as the config API
	// stores it. The real row carries language, timeout and chunk size too; they
	// are here so a test can prove Bloud leaves them alone rather than resetting
	// them to the image's defaults.
	whisper fakeWhisper
	// llm is the app's combined-config llm section: the ad classifier's model and
	// the endpoint it is dialed on.
	llm fakeLLM
	// configWrites counts PUT /api/config, so a test can assert a steady-state
	// pass touches nothing.
	configWrites int

	requests []string
}

// fakeWhisper is the whisper section of the app's config store.
type fakeWhisper struct {
	whisperType string
	baseURL     string
	apiKey      string
	model       string
	language    string
}

// fakeLLM is the llm section of the app's config store.
type fakeLLM struct {
	model   string
	baseURL string
	apiKey  string
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
	case r.URL.Path == configPath && r.Method == http.MethodPut:
		f.putConfig(w, r)
	case r.URL.Path == configPath && r.Method == http.MethodGet:
		f.getConfig(w, r)
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

// putConfig models PUT /api/config. The real route merges only the keys the
// payload carries (a partial whisper section leaves language, timeout and chunk
// size as they were), refuses to write a field an environment variable owns, and
// answers 400 on anything it cannot apply. The env-authority case is out of
// scope for the fake because Bloud never sets a whisper variable; what the fake
// has to keep true is the merge and the admin gate.
func (f *fakePodly) putConfig(w http.ResponseWriter, r *http.Request) {
	if !f.requireAdmin(w, r) {
		return
	}
	var body struct {
		Whisper map[string]any `json:"whisper"`
		LLM     map[string]any `json:"llm"`
	}
	decodeFakeBody(r, &body)
	if body.Whisper == nil && body.LLM == nil {
		writeFakeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid configuration"})
		return
	}
	if t, ok := body.Whisper["whisper_type"].(string); ok {
		f.whisper.whisperType = t
	}
	// The real writer switches on the section type before it maps any field, so
	// `model` lands in the remote model column only once the type is remote.
	if f.whisper.whisperType == "remote" {
		for key, dst := range map[string]*string{
			"base_url": &f.whisper.baseURL,
			"api_key":  &f.whisper.apiKey,
			"model":    &f.whisper.model,
			"language": &f.whisper.language,
		} {
			if v, ok := body.Whisper[key].(string); ok && v != "" {
				*dst = v
			}
		}
	}
	if body.LLM != nil {
		for key, dst := range map[string]*string{
			"llm_model":       &f.llm.model,
			"openai_base_url": &f.llm.baseURL,
			"llm_api_key":     &f.llm.apiKey,
		} {
			if v, ok := body.LLM[key].(string); ok && v != "" {
				*dst = v
			}
		}
	}
	f.configWrites++
	writeFakeJSON(w, http.StatusOK, f.configForClient())
}

// getConfig models GET /api/config, including the part that forces the
// configurator's design: the credential is returned masked, so a caller cannot
// compare the key it holds against the key the app holds.
func (f *fakePodly) getConfig(w http.ResponseWriter, r *http.Request) {
	if !f.requireAdmin(w, r) {
		return
	}
	writeFakeJSON(w, http.StatusOK, f.configForClient())
}

func (f *fakePodly) configForClient() map[string]any {
	whisper := map[string]any{
		"whisper_type": f.whisper.whisperType,
		"base_url":     f.whisper.baseURL,
		"model":        f.whisper.model,
		"language":     f.whisper.language,
	}
	if f.whisper.apiKey != "" {
		whisper["api_key_preview"] = "**********"
	}
	out := map[string]any{"whisper": whisper}
	llm := map[string]any{
		"llm_model":       f.llm.model,
		"openai_base_url": f.llm.baseURL,
	}
	if f.llm.apiKey != "" {
		llm["llm_api_key_preview"] = "**********"
	}
	out["llm"] = llm
	return map[string]any{"config": out}
}

// llmState reads what the fake's config store holds for the ad classifier, so a
// test can assert on the delivered values rather than on the request.
func (f *fakePodly) llmState() fakeLLM {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.llm
}

// whisperState reads what the fake's config store holds, so a test can assert on
// the delivered values rather than on the request that carried them.
func (f *fakePodly) whisperState() fakeWhisper {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.whisper
}

// setWhisper seeds the app's own whisper settings, standing in for an operator
// who configured transcription in the app's UI before Bloud had an opinion.
func (f *fakePodly) setWhisper(w fakeWhisper) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.whisper = w
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
