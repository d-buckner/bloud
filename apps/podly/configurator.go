// SPDX-License-Identifier: AGPL-3.0-only

// Package podly configures Podly, the ad-stripping podcast server.
//
// Podly takes its whole configuration from process environment variables and
// has no API for its own boot-time settings, so PreStart writes an env file the
// runtime reads at container create time. PostStart then does the one thing the
// environment cannot: put a password the operator can actually use into an app
// that reads its admin password exactly once, while its user table is still
// empty.
//
// That constraint is the whole shape of this configurator. Podly's bootstrap
// seeds PODLY_ADMIN_PASSWORD only when no user exists, so the value delivered
// through the environment is useful exactly once, on the first boot. Bloud
// therefore uses it for an account nobody signs in with (bloud-service) and
// creates the account a person does sign in with (bloud) through the app's own
// user API, which is the only path that can set a password after first boot and
// so the only path a Bloud-side rotation can travel.
package podly

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
)

const appName = "podly"

// nodeName is this app's graph node and container name.
const nodeName = "apps-podly"

// defaultPort is the app's own web port, matching metadata.yaml's `port`.
const defaultPort = 5001

const (
	// configDir holds the host-only files this configurator writes. It is not
	// mounted: the runtime reads the env file on the host when it creates the
	// container, so nothing inside it ever reaches the app's filesystem.
	configDir = "config"
	// envFileName is the container's env file, declared in metadata.yaml as the
	// container's `envFile`.
	envFileName = "env"
	// stateFileName records what PostStart last applied to the app, so a
	// steady-state resync makes no request at all.
	stateFileName = "accounts.state"
	// whisperStateFileName is the same idea for the transcription endpoint: the
	// digest of the last settings Bloud delivered through the app's config API.
	whisperStateFileName = "whisper.state"
)

const (
	// serviceUser is the admin account the image bootstraps from the environment.
	// Its password is a host-held secret that never rotates and is never
	// revealed: it exists so Bloud always has an authenticated path into an app
	// whose only other credential belongs to a person.
	serviceUser = "bloud-service"
	// humanUser is the account a person signs in with, and the one the revealed
	// client password belongs to. The name is fixed because the reveal snippet
	// carries a URL and a password and has no field for a username.
	humanUser = "bloud"
	// adminRole is what both accounts get. Podly's non-admin role carries a feed
	// allowance and cannot manage users; the person who owns the instance gets
	// neither limit.
	adminRole = "admin"
)

// clientPasswordSecret is the key this app publishes under its clientPassword
// contract: the value a user reveals once and types into Podly's sign-in form.
const clientPasswordSecret = "password"

// signingKeyBytes is the entropy of Podly's Flask session-signing key. The app
// generates an ephemeral one when PODLY_SECRET_KEY is unset, which signs every
// session out on each container start, so Bloud mints one and keeps it.
const signingKeyBytes = 32

// clientPasswordBytes is the entropy of the sign-in password a person reveals.
// It is longer than the service password on purpose: this one is typed by hand
// into a sign-in form, so it is the value an attacker gets to guess at, and
// base64url costs it a character per 6 bits of entropy.
const clientPasswordBytes = 24

type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	api     *podlyAPI
	logger  *slog.Logger
}

// NewConfigurator builds the configurator from host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:    port,
		secrets: deps.Secrets,
		logger:  logger.With("app", appName),
	}
	if deps.HTTP != nil {
		c.api = newAPI(deps.HTTP, func() string { return fmt.Sprintf("http://localhost:%d", port) })
	}
	return c
}

func (c *Configurator) Name() string {
	return nodeName
}

// Port is the app port this node was constructed with, which is the catalog
// `port:` when registration passed 0. The conformance harness compares it
// against metadata.yaml, because only one of the two routes traffic.
func (c *Configurator) Port() int { return c.port }

// PreStart writes the env file the container is created from.
//
// It reports a recreate whenever the file changed, because these values are read
// once, at container create: a rewritten file with a running container is a file
// that says one thing while the app runs with another.
//
// The session-signing key is read back out of the existing file and reused. That
// is what makes a steady-state pass a read-only diff, and it is what makes the
// key survive a restart: Podly signs its session cookies with it, so a key that
// changed on every boot would log everyone out on every reconcile. The only
// thing that rotates it is RevokeSessions, which is the point.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	if state == nil {
		return configurator.NoRestart(), fmt.Errorf("%s: no app state", appName)
	}

	dir := filepath.Join(state.DataPath, configDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return configurator.NoRestart(), fmt.Errorf("%s: create config dir: %w", appName, err)
	}
	path := filepath.Join(dir, envFileName)

	signingKey := readEnvFile(path)["PODLY_SECRET_KEY"]
	if signingKey == "" {
		minted, err := randomToken(signingKeyBytes)
		if err != nil {
			return configurator.NoRestart(), fmt.Errorf("%s: mint session key: %w", appName, err)
		}
		signingKey = minted
	}

	servicePassword, err := c.servicePassword()
	if err != nil {
		return configurator.NoRestart(), err
	}
	if servicePassword == "" {
		// No secret store: a CLI or conformance context, where no container is
		// coming up. Writing the file without the password keeps the pass
		// deterministic (a value minted here would differ every pass and make
		// the file churn), and the app refuses to start with REQUIRE_AUTH set and
		// no password, which is the right outcome for a real install whose
		// secret store is broken.
		c.logger.Warn("no secret store; writing the app env file without a service password")
	}

	content := renderEnvFile(servicePassword, signingKey)
	changed, err := managedfile.Write(path, []byte(content), managedfile.ModeHostOnly)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("%s: write %s: %w", appName, envFileName, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}

	c.logger.Info("wrote app env file, recreate required to pick it up",
		"path", path, "servicePassword", servicePassword != "")
	return configurator.MustRestart("app env file changed; the values are read at container create"), nil
}

// PostStart makes the revealed password the password the app accepts.
//
// The order is deliberate. The client password is minted here rather than in
// PreStart because nothing in the container reads it: it is delivered by an API
// call, not by the environment. Then reconcileAccount does the part that talks
// to the app.
//
// A steady-state pass reads the app and writes nothing. The read is not wasted:
// it is how an account deleted inside the app gets put back. What the state file
// spares is the write: without it, every resync would re-send the password and
// the app would re-hash it with bcrypt and rewrite the row, on a loop, forever.
// The alternative that also avoids the write, probing whether the current
// password already works, would cost a bcrypt verification every minute and
// stamp the account's last-active time as if a person had signed in.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if state == nil {
		return fmt.Errorf("%s: no app state", appName)
	}
	if c.secrets == nil {
		c.logger.Warn("no secret store; skipping account setup")
		return nil
	}

	if err := c.api.waitAuthReady(ctx); err != nil {
		return fmt.Errorf("%s: waiting for the app's auth API: %w", appName, err)
	}
	status, err := c.api.authStatus(ctx)
	if err != nil {
		return fmt.Errorf("%s: reading auth status: %w", appName, err)
	}
	if !status.RequireAuth {
		// Worth a warning rather than a failed node: the app is up and serving,
		// and parking it in ERROR would take its feeds down over a condition the
		// operator has to fix in the app's own configuration anyway.
		c.logger.Warn("the app is serving with its own authentication disabled; " +
			"REQUIRE_AUTH did not reach the container")
		return nil
	}

	password, err := c.ensureClientPassword()
	if err != nil {
		return err
	}
	if password == "" {
		return nil
	}
	signedIn, err := c.reconcileAccount(ctx, state, password)
	if err != nil || !signedIn {
		return err
	}
	if err := c.reconcileTranscription(ctx, state); err != nil {
		return err
	}
	return c.reconcileInference(ctx, state)
}

// reconcileAccount makes the app's copy of the sign-in password the host's copy.
//
// Two outcomes are warnings rather than errors, and both are the same shape: a
// state only the operator can change, reached from inside the app. Failing the
// node for either would take the app's feeds down over something Bloud cannot
// fix by retrying.
//
// It reports whether the session is usable, because the caller has more work
// that needs the same admin cookie and must not attempt it after a warning path
// that never signed in.
func (c *Configurator) reconcileAccount(ctx context.Context, state *configurator.AppState, password string) (bool, error) {
	servicePassword, err := c.servicePassword()
	if err != nil {
		return false, err
	}
	if servicePassword == "" {
		c.logger.Warn("no service password; cannot reconcile the sign-in account")
		return false, nil
	}

	signedIn, err := c.api.login(ctx, serviceUser, servicePassword)
	if err != nil {
		return false, fmt.Errorf("%s: signing in as %s: %w", appName, serviceUser, err)
	}
	if !signedIn {
		// The app's stored password for the service account no longer matches
		// what the host holds, which means it was changed inside the app. Bloud
		// cannot follow that change (the app has no API that sets a password
		// without an authenticated admin), so the honest move is to say so and
		// leave the node running.
		c.logger.Warn("the app no longer accepts the service account password; " +
			"it was changed inside the app, and Bloud cannot reconcile accounts without it")
		return false, nil
	}

	exists, err := c.humanUserExists(ctx)
	if err != nil {
		return false, err
	}
	if exists && passwordApplied(state, password) {
		return true, nil
	}
	if err := c.writePassword(ctx, password, exists); err != nil {
		return false, err
	}
	if err := recordAppliedPassword(state, password); err != nil {
		return false, err
	}

	c.logger.Info("reconciled the sign-in account", "account", humanUser, "created", !exists)
	return true, nil
}

// reconcileTranscription points the app's speech-to-text at the endpoint the
// instance declares, when it declares one.
//
// With no binding the app is left alone rather than reset: an operator who set a
// key in the app's own UI before Bloud ever had an opinion keeps it, and a pass
// that cleared it would be a pass that broke a working install because nothing
// happened to be wired in Bloud.
//
// The write is gated on a digest of what Bloud applied, not on a read of what
// the app holds, and that is forced rather than convenient: the config API masks
// the credential it returns, so the app cannot answer "is the key you are about
// to send me the key I have?". The digest answers the only question that matters
// (has this exact value been delivered), and it is what keeps a steady-state pass
// from rewriting the app's config every minute.
func (c *Configurator) reconcileTranscription(ctx context.Context, state *configurator.AppState) error {
	binding, ok := pickTranscription(state)
	if !ok {
		return nil
	}
	desired := whisperSection{
		WhisperType: "remote",
		BaseURL:     binding.Endpoint,
		APIKey:      orPlaceholder(binding.APIKey.String()),
		Model:       binding.Model,
	}
	if transcriptionApplied(state, desired) {
		return nil
	}
	if _, err := c.api.setWhisper(ctx, desired); err != nil {
		return fmt.Errorf("%s: setting the transcription endpoint: %w", appName, err)
	}
	if err := recordTranscriptionApplied(state, desired); err != nil {
		return err
	}

	c.logger.Info("configured the app's transcription endpoint",
		"endpoint", desired.BaseURL, "model", orEndpointDefault(desired.Model))
	return nil
}

// pickTranscription returns the endpoint to dial. The first installed one wins:
// the contract is single-provider by construction, so a second is a registry
// someone edited by hand rather than a choice this app gets to make.
func pickTranscription(state *configurator.AppState) (bindings.TranscriptionBinding, bool) {
	for _, b := range state.Integrations.Transcription {
		if b.Installed && strings.TrimSpace(b.Endpoint) != "" {
			return b, true
		}
	}
	return bindings.TranscriptionBinding{}, false
}

func orEndpointDefault(model string) string {
	if model == "" {
		return "(endpoint default)"
	}
	return model
}

// writePassword sends the password to the app: create when the account is
// missing, patch when it exists and the stored value is stale.
func (c *Configurator) writePassword(ctx context.Context, password string, exists bool) error {
	if exists {
		if _, err := c.api.setPassword(ctx, humanUser, password); err != nil {
			return fmt.Errorf("%s: setting the sign-in password: %w", appName, err)
		}
		return nil
	}
	if _, err := c.api.createUser(ctx, humanUser, password, adminRole); err != nil {
		return fmt.Errorf("%s: creating the sign-in account: %w", appName, err)
	}
	return nil
}

// RevokeSessions ends every live Podly session by rotating the key the app signs
// its session cookies with.
//
// Podly has no server-side session store to clear: a session is a cookie signed
// under PODLY_SECRET_KEY, and the app never looks at it again after validating
// the signature. There is no record to delete, so the only way to end a session
// is to make the signature stop verifying. That is a blunt instrument by design,
// which is exactly what the revoke control claims: every session ends.
//
// The rewrite happens here rather than in PreStart because a plain resync must
// not end sessions: the key has to change only when someone asked for it. The
// orchestrator forces the recreate that installs the new key.
func (c *Configurator) RevokeSessions(_ context.Context, state *configurator.AppState) error {
	if state == nil {
		return fmt.Errorf("%s: no app state", appName)
	}
	path := filepath.Join(state.DataPath, configDir, envFileName)

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Nothing has ever started, so there is nothing signed out.
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: reading %s: %w", appName, envFileName, err)
	}

	minted, err := randomToken(signingKeyBytes)
	if err != nil {
		return fmt.Errorf("%s: mint session key: %w", appName, err)
	}

	lines := strings.Split(string(raw), "\n")
	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "PODLY_SECRET_KEY=") {
			lines[i] = "PODLY_SECRET_KEY=" + minted
			replaced = true
		}
	}
	if !replaced {
		// The app is running with the ephemeral key it generates for itself,
		// which it replaces on every start anyway; there is no session Bloud
		// holds the key to.
		c.logger.Warn("no session key in the app env file; nothing to revoke")
		return nil
	}

	if _, err := managedfile.Write(path, []byte(strings.Join(lines, "\n")), managedfile.ModeHostOnly); err != nil {
		return fmt.Errorf("%s: rewrite %s: %w", appName, envFileName, err)
	}
	c.logger.Warn("rotated the app's session-signing key; every live session is now invalid",
		"container", nodeName)
	return nil
}

// servicePassword returns the host-held password for the service account. An
// empty value with a nil error means there is no secret store to ask.
func (c *Configurator) servicePassword() (string, error) {
	if c.secrets == nil {
		return "", nil
	}
	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		return "", fmt.Errorf("%s: generate service password: %w", appName, err)
	}
	return password, nil
}

// ensureClientPassword returns the revealed sign-in password, minting and
// publishing one on first use. The reveal endpoint reads the same key, which is
// what makes the value Bloud hands the app the same value it shows a person.
func (c *Configurator) ensureClientPassword() (string, error) {
	if existing := c.secrets.GetAppSecret(appName, clientPasswordSecret); existing != "" {
		return existing, nil
	}
	minted, err := randomToken(clientPasswordBytes)
	if err != nil {
		return "", fmt.Errorf("%s: generate client password: %w", appName, err)
	}
	if err := c.secrets.SetAppSecret(appName, clientPasswordSecret, minted); err != nil {
		return "", fmt.Errorf("%s: persist client password: %w", appName, err)
	}
	c.logger.Info("minted the sign-in password", "secret", clientPasswordSecret)
	return minted, nil
}

// humanUserExists asks the app whether the sign-in account is there. It is
// checked on every pass that gets this far, so an account deleted inside the app
// comes back on the next one.
func (c *Configurator) humanUserExists(ctx context.Context) (bool, error) {
	users, err := c.api.listUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("%s: listing app users: %w", appName, err)
	}
	for _, u := range users {
		if strings.EqualFold(u.Username, humanUser) {
			return true, nil
		}
	}
	return false, nil
}

// renderEnvFile builds the container's env file. The format is the one the
// runtime's env-file parser reads: one KEY=value per line, `#` comments ignored.
// Values are written unquoted; every generator here uses the base64url alphabet,
// which has no character the parser or the shell would take apart.
func renderEnvFile(servicePassword, signingKey string) string {
	var b strings.Builder
	b.WriteString("# Generated by Bloud. Do not edit: the next reconcile overwrites this.\n")
	// Auth on, always. With it off the app serves every feed to anyone who finds
	// the URL, and its feed ids are sequential integers.
	b.WriteString("REQUIRE_AUTH=true\n")
	fmt.Fprintf(&b, "PODLY_ADMIN_USERNAME=%s\n", serviceUser)
	if servicePassword != "" {
		fmt.Fprintf(&b, "PODLY_ADMIN_PASSWORD=%s\n", servicePassword)
	}
	fmt.Fprintf(&b, "PODLY_SECRET_KEY=%s\n", signingKey)
	return b.String()
}

// readEnvFile reads back the file PreStart wrote, so a pass can reuse the value
// it already committed to. A file that is not there reads as empty: the first
// pass has nothing to reuse and mints.
func readEnvFile(path string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

// passwordApplied reports whether the app was already given this exact password.
// The state file holds a digest rather than the password: the value is a
// credential, and there is no reason to write it to a second place on disk when
// all this needs to answer is "same as last time".
func passwordApplied(state *configurator.AppState, password string) bool {
	raw, err := os.ReadFile(filepath.Join(state.DataPath, configDir, stateFileName))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(raw)) == passwordDigest(password)
}

// recordAppliedPassword writes the state file after the app has accepted the
// password, never before: a pass that applied nothing must not claim it did.
func recordAppliedPassword(state *configurator.AppState, password string) error {
	path := filepath.Join(state.DataPath, configDir, stateFileName)
	_, err := managedfile.Write(path, []byte(passwordDigest(password)+"\n"), managedfile.ModeHostOnly)
	if err != nil {
		return fmt.Errorf("%s: write %s: %w", appName, stateFileName, err)
	}
	return nil
}

func passwordDigest(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

// transcriptionApplied reports whether this exact endpoint, key and model were
// already delivered. The digest covers all three because a change to any of them
// is a change the app has to be told about: the key is what the endpoint
// authenticates with, and the model is what it charges for.
func transcriptionApplied(state *configurator.AppState, want whisperSection) bool {
	raw, err := os.ReadFile(filepath.Join(state.DataPath, configDir, whisperStateFileName))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(raw)) == whisperDigest(want)
}

// recordTranscriptionApplied writes the state file after the app has accepted
// the settings, never before: a pass that applied nothing must not claim it did.
func recordTranscriptionApplied(state *configurator.AppState, want whisperSection) error {
	path := filepath.Join(state.DataPath, configDir, whisperStateFileName)
	_, err := managedfile.Write(path, []byte(whisperDigest(want)+"\n"), managedfile.ModeHostOnly)
	if err != nil {
		return fmt.Errorf("%s: write %s: %w", appName, whisperStateFileName, err)
	}
	return nil
}

// whisperDigest hashes the three fields with separators, so an endpoint and a
// model that could be confused by concatenation (a model named after the tail of
// a URL) still digest apart.
func whisperDigest(w whisperSection) string {
	h := sha256.New()
	for _, part := range []string{w.WhisperType, w.BaseURL, w.APIKey, w.Model} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// randomToken returns n bytes of cryptographic randomness as unpadded
// base64url: safe unquoted in an env file and safe to type or paste.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
