// SPDX-License-Identifier: AGPL-3.0-only

package podly

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// Podly's own HTTP surface, as far as Bloud needs it. Everything here is the
// app's documented auth API: the app has no administrative API outside it, and
// the configurator never reaches past it into the SQLite file the app owns.
const (
	authStatusPath = "/api/auth/status"
	loginPath      = "/api/auth/login"
	usersPath      = "/api/auth/users"
	configPath     = "/api/config"
)

// authStatus is the reply to GET /api/auth/status, one of the app's public
// paths (it is what the sign-in form consults before it renders).
type authStatus struct {
	RequireAuth bool `json:"require_auth"`
}

// userEntry is one row of GET /api/auth/users. Only the fields Bloud acts on
// are decoded; the app sends timestamps and feed allowances alongside them.
type userEntry struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// usersResponse is the envelope of GET /api/auth/users.
type usersResponse struct {
	Users []userEntry `json:"users"`
}

// podlyAPI is the typed client for one Podly node.
//
// Two clients, because Podly has two auth positions and no header-only
// credential. The anonymous one logs in and probes a password; the session one
// carries the cookie that a signed-in admin gets, which is the only way into
// /api/auth/users. Podly derives the acting user from the session cookie alone,
// so the jar is not an optimization, it is the credential.
type podlyAPI struct {
	anon *appclient.Client
	sess *appclient.Client
}

func newAPI(f configurator.ClientFactory, baseURLFn func() string) *podlyAPI {
	// The only error case is a nil cookie-jar option, which this call does not
	// pass.
	jar, _ := cookiejar.New(nil)
	return &podlyAPI{
		anon: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn}),
		sess: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn, Jar: jar}),
	}
}

// waitAuthReady polls the public auth-status endpoint until the app answers.
//
// The container's health check already gates PostStart on `/`, but `/` is served
// by the web process while every user mutation is executed by the writer
// process over a local IPC socket. The writer is up before the web app starts
// (the image's start script waits for its socket), so this is a formality kept
// short: it exists so a PostStart that runs against a half-started app says so
// instead of reporting a 404 as a missing user.
func (a *podlyAPI) waitAuthReady(ctx context.Context) error {
	return a.anon.GET(authStatusPath).
		Wait(appclient.JSONHas("require_auth")).
		Interval(2 * time.Second).
		Within(2 * time.Minute).
		Do(ctx)
}

// authStatus reads whether the app has its own authentication switched on.
func (a *podlyAPI) authStatus(ctx context.Context) (authStatus, error) {
	var out authStatus
	err := a.anon.GET(authStatusPath).DoInto(ctx, &out)
	return out, err
}

// login signs in on the session client, so a success leaves its cookie in the
// jar that every later admin call is made with.
//
// A wrong password is an answer, not a failure: it comes back false with no
// error. Anything else (a transport error, a 5xx, the 429 the app's login
// backoff returns) is returned as an error, because the caller cannot tell
// "wrong password" from "cannot tell" without the status.
func (a *podlyAPI) login(ctx context.Context, username, password string) (bool, error) {
	_, err := a.sess.POST(loginPath).
		Anonymous().
		JSON(map[string]string{"username": username, "password": password}).
		Do(ctx)
	switch {
	case err == nil:
		return true, nil
	case appclient.StatusOf(err) == http.StatusUnauthorized:
		return false, nil
	default:
		return false, err
	}
}

// listUsers reads the app's user list. It requires the session cookie, so it
// answers 401 when no service login has happened yet and 404 when the app is
// running with authentication off.
func (a *podlyAPI) listUsers(ctx context.Context) ([]userEntry, error) {
	var out usersResponse
	if err := a.sess.GET(usersPath).DoInto(ctx, &out); err != nil {
		return nil, err
	}
	return out.Users, nil
}

// createUser creates an account. Podly hashes the password in its writer
// process; a duplicate username answers 409, which is declared as already-done
// so a pass that races its own previous attempt converges instead of erroring.
func (a *podlyAPI) createUser(ctx context.Context, username, password, role string) (bool, error) {
	return a.sess.POST(usersPath).
		JSON(map[string]string{"username": username, "password": password, "role": role}).
		OK(http.StatusCreated).
		AlreadyDone(http.StatusConflict).
		Ensure(ctx)
}

// setPassword sets another account's password as the signed-in admin.
//
// This is the call that makes a Bloud-side rotation reach the app. It exists
// because the app's own bootstrap reads PODLY_ADMIN_PASSWORD only while its
// users table is empty, so an environment-only delivery could never change a
// password once the account it created exists.
func (a *podlyAPI) setPassword(ctx context.Context, username, password string) (bool, error) {
	return a.sess.PATCH(usersPath + "/" + username).
		JSON(map[string]string{"password": password}).
		OK(http.StatusOK).
		Ensure(ctx)
}

// whisperSection is the `whisper` section of the app's combined config, as far
// as Bloud writes it.
//
// The app's writer merges only the keys present in the payload, so the fields
// Bloud has no opinion about (language, timeout, chunk size) are left alone
// rather than reset to the image's defaults. `whisper_type` is written in the
// same payload because the writer switches on it before it maps any field: the
// key named `model` lands in the remote model column only once the section is
// already a remote one.
type whisperSection struct {
	WhisperType string `json:"whisper_type"`
	BaseURL     string `json:"base_url,omitempty"`
	APIKey      string `json:"api_key,omitempty"`
	Model       string `json:"model,omitempty"`
}

// setWhisper writes the transcription settings through the app's own config
// API rather than through the environment.
//
// That choice is forced. The app treats its environment as authoritative over
// its database: an env-provided value is re-applied on every config hydration,
// and the config API refuses to overwrite a field an env var set. So a whisper
// endpoint delivered as env would be one the operator could never change again
// from the app's own Settings screen. Delivered through the API it lands where a
// human edit lands, and a later edit by that human simply wins.
func (a *podlyAPI) setWhisper(ctx context.Context, w whisperSection) (bool, error) {
	return a.sess.PUT(configPath).
		JSON(map[string]any{"whisper": w}).
		OK(http.StatusOK).
		Ensure(ctx)
}

// llmSection is the `llm` half of the app's config: the model that decides which
// transcript spans are advertising, and the endpoint it is dialed on. Ad removal
// is this model's job, so an app with whisper wired and no llm wired transcribes
// and then cannot cut anything.
type llmSection struct {
	Model   string `json:"llm_model"`
	BaseURL string `json:"openai_base_url,omitempty"`
	APIKey  string `json:"llm_api_key,omitempty"`
}

// setLLM writes the ad-classifier settings, through the config API for the same
// reason setWhisper does: an env-provided value is re-applied on every hydration
// and the API refuses to overwrite it, so env would hand the operator a setting
// they could never change again.
func (a *podlyAPI) setLLM(ctx context.Context, l llmSection) (bool, error) {
	return a.sess.PUT(configPath).
		JSON(map[string]any{"llm": l}).
		OK(http.StatusOK).
		Ensure(ctx)
}
