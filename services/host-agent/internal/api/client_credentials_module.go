// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/go-chi/chi/v5"
)

// revealedKeyPrefix namespaces the setting that records which credential value
// was already shown, so a reveal-once credential cannot be revealed twice.
const revealedKeyPrefix = "client_credential_revealed"

// catalogSource is the read side of the catalog this module needs. Kept narrow
// so the module can be handed a fake without a loader.
type catalogSource interface {
	Get(name string) (*catalog.App, error)
}

// clientCredentialsModule serves the reveal and rotate surface for credentials a
// provider declares under `clientAccess`.
//
// The whole module is built around one rule: the credential value leaves this
// process only through a POST that the operator explicitly invoked. Every GET
// returns descriptors and never a value. That is what makes reveal-once mean
// something, because a surface that could be polled for the secret would make
// "once" a policy about the modal rather than about the API, and the modal is
// not the security boundary.
type clientCredentialsModule struct {
	catalog   catalogSource
	secrets   configurator.AppSecretsProvider
	settings  settingsKV
	hostState *hostset.State
	orch      orchestratorCaller
	logger    *slog.Logger
}

// settingsKV is the slice of the settings store this module uses.
type settingsKV interface {
	Get(key string) (string, error)
	Set(key, value string) error
}

// NewClientCredentialsModule wires the module. A nil catalog or secrets store
// makes every route answer 503 rather than panic, because both are absent in
// degraded contexts and a missing dependency should read as unavailable, not
// as a crash.
func NewClientCredentialsModule(
	catalogCache catalogSource,
	secrets configurator.AppSecretsProvider,
	settings settingsKV,
	hostState *hostset.State,
	orch orchestratorCaller,
	logger *slog.Logger,
) *clientCredentialsModule {
	if logger == nil {
		logger = slog.Default()
	}
	return &clientCredentialsModule{
		catalog:   catalogCache,
		secrets:   secrets,
		settings:  settings,
		hostState: hostState,
		orch:      orch,
		logger:    logger.With("module", "client-credentials"),
	}
}

// Register mounts the module's routes on an already-authenticated, admin-only
// router.
func (m *clientCredentialsModule) Register(r chi.Router) {
	r.Get("/apps/{name}/client-credentials", m.ListHandler())
	r.Post("/apps/{name}/client-credentials/{secret}/reveal", m.revealHandler())
	r.Post("/apps/{name}/client-credentials/{secret}/rotate", m.rotateHandler())
}

// credentialDescriptor is the API shape of one client-accessible credential.
// It deliberately has no value field: a descriptor is safe to poll, cache, and
// log, and the absence of the field is what guarantees that rather than a
// convention about which field to omit.
type credentialDescriptor struct {
	Secret  string `json:"secret"`
	Label   string `json:"label,omitempty"`
	Reveal  string `json:"reveal"`
	Rotate  string `json:"rotate"`
	Reaches string `json:"reaches,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	// Revealed reports whether this exact credential value has already been
	// shown. Under `reveal: once` a true here means the value cannot be shown
	// again and the operator must rotate to get a showable one.
	Revealed bool `json:"revealed"`
	// Published reports whether the provider has actually stored a value. A
	// declared-but-unpublished credential is a provider that has not finished
	// installing, not a credential whose value was lost.
	Published bool `json:"published"`
}

// ListHandler returns the descriptors for every client-accessible credential an
// app declares. No value is included, at any reveal policy.
func (m *clientCredentialsModule) ListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appName := chi.URLParam(r, "name")
		offers, err := m.clientAccessOffers(appName)
		if err != nil {
			m.fail(w, err)
			return
		}

		descriptors := make([]credentialDescriptor, 0, len(offers))
		for _, offer := range offers {
			descriptors = append(descriptors, m.describe(appName, offer))
		}
		respondJSON(w, http.StatusOK, map[string]any{"credentials": descriptors})
	}
}

// revealHandler returns the credential value once and records that it was shown.
//
// The audit record is written before the value goes out on the wire, not after.
// A reveal that logged on success would lose the record on any later failure in
// the response path, and the record is the thing that makes a reveal
// investigable.
func (m *clientCredentialsModule) revealHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appName := chi.URLParam(r, "name")
		secretName := chi.URLParam(r, "secret")

		access, err := m.lookupAccess(appName, secretName)
		if err != nil {
			m.fail(w, err)
			return
		}
		if !access.Revealable() {
			respondError(w, http.StatusForbidden,
				"this credential is not revealable under its declared policy")
			return
		}

		value := m.value(appName, secretName)
		if value == "" {
			respondError(w, http.StatusNotFound,
				"the provider has not published this credential yet")
			return
		}

		if access.Reveal == catalog.ClientRevealOnce && m.alreadyRevealed(appName, secretName, value) {
			respondError(w, http.StatusConflict,
				"this credential was already revealed; rotate to get one that can be shown")
			return
		}

		if err := m.markRevealed(appName, secretName, value); err != nil {
			// Refusing to send rather than sending without the record: an
			// unrecorded reveal is the failure mode this whole path exists to
			// avoid, and a silent one is worse than a 500 the operator retries.
			m.logger.Error("could not record reveal", "app", appName, "secret", secretName, "err", err)
			respondError(w, http.StatusInternalServerError, "could not record the reveal")
			return
		}

		m.logger.Warn("client credential revealed",
			"app", appName,
			"secret", secretName,
			"policy", string(access.Reveal),
			"remote", r.RemoteAddr)

		respondJSON(w, http.StatusOK, map[string]string{
			"secret":  secretName,
			"value":   value,
			"snippet": m.renderSnippet(access.Snippet, value),
		})
	}
}

// clientCredentialBytes is the entropy of a rotated credential. Matches what
// the app-side configurator mints on install so a fresh install and a rotate
// hand over the same shape.
const clientCredentialBytes = 24

// randomClientCredential returns a URL-safe random token.
func randomClientCredential() (string, error) {
	buf := make([]byte, clientCredentialBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// notFound marks a lookup miss as distinct from a dependency failure, so the
// HTTP layer does not have to guess from the message text. A missing catalog is
// 503 because the operator can fix it by starting the catalog; a credential
// the app never declared is 404 because nothing the operator does will make it
// appear.
type notFound struct{ msg string }

func (e *notFound) Error() string { return e.msg }

func notFoundf(format string, args ...any) error { return &notFound{msg: fmt.Sprintf(format, args...)} }

// rotateHandler mints a new value and asks the orchestrator to converge so the
// app picks it up.
//
// It does not restart the container itself. The API submits a reconcile intent
// and the orchestrator's resync re-runs PreStart, which renders a different
// config file, reports RestartNeeded, and recreates the container. That is the
// same path any config change takes, and it is what keeps invariant 1 true: the
// API writes a credential into the store and asks for convergence, it does not
// touch a container.
//
// The new value is returned in the response. A rotate that did not show the new
// value would be unusable under `reveal: once`, because the only way to see a
// credential is to reveal it and a fresh one has never been revealed.
//
// What rotate does NOT do is end live sessions. The app signs sessions with its
// own persisted key and records no binding to the credential that minted them,
// so a client signed in before the rotate keeps working until its TTL. That is
// the gap the pilot plan records as the follow-up "rotation should revoke".
func (m *clientCredentialsModule) rotateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appName := chi.URLParam(r, "name")
		secretName := chi.URLParam(r, "secret")

		access, err := m.lookupAccess(appName, secretName)
		if err != nil {
			m.fail(w, err)
			return
		}
		if !access.RotateAllowed() {
			respondError(w, http.StatusForbidden,
				"this credential is not rotatable under its declared policy")
			return
		}
		if m.secrets == nil {
			respondError(w, http.StatusServiceUnavailable, "secret store unavailable")
			return
		}

		value, err := randomClientCredential()
		if err != nil {
			m.logger.Error("could not generate a new client credential",
				"app", appName, "secret", secretName, "err", err)
			respondError(w, http.StatusInternalServerError, "could not generate a new credential")
			return
		}
		if err := m.secrets.SetAppSecret(appName, secretName, value); err != nil {
			m.logger.Error("could not store the rotated credential",
				"app", appName, "secret", secretName, "err", err)
			respondError(w, http.StatusInternalServerError, "could not store the new credential")
			return
		}

		// Ask for convergence so the resync re-renders the app's config and
		// recreates the container. Without this the new value sits in the store
		// while the app keeps authenticating the old one.
		if m.orch != nil {
			m.orch.Submit(orchestrator.NewReconcileIntent())
		}

		m.logger.Warn("client credential rotated",
			"app", appName,
			"secret", secretName,
			"sessionsSurvive", true,
			"remote", r.RemoteAddr)

		respondJSON(w, http.StatusOK, map[string]any{
			"secret":          secretName,
			"value":           value,
			"snippet":         m.renderSnippet(access.Snippet, value),
			"sessionsSurvive": true,
		})
	}
}

// clientAccessOffers returns the app's offers that declare a client access
// block.
func (m *clientCredentialsModule) clientAccessOffers(appName string) ([]namedOffer, error) {
	if m.catalog == nil {
		return nil, fmt.Errorf("catalog unavailable")
	}
	app, err := m.catalog.Get(appName)
	if err != nil || app == nil {
		return nil, notFoundf("unknown app %q", appName)
	}
	out := make([]namedOffer, 0, len(app.Provides))
	for contractName, offer := range app.Provides {
		if offer.ClientAccess != nil {
			out = append(out, namedOffer{contract: contractName, offer: offer})
		}
	}
	return out, nil
}

// namedOffer pairs an offer with the contract it was made under.
type namedOffer struct {
	contract string
	offer    catalog.ContractProvides
}

func (m *clientCredentialsModule) describe(appName string, entry namedOffer) credentialDescriptor {
	access := entry.offer.ClientAccess
	secretName := access.Secret
	if secretName == "" && len(entry.offer.Secrets) == 1 {
		secretName = entry.offer.Secrets[0]
	}
	value := m.value(appName, secretName)
	return credentialDescriptor{
		Secret:    secretName,
		Label:     access.Label,
		Reveal:    string(access.EffectiveReveal()),
		Rotate:    string(access.EffectiveRotate()),
		Reaches:   access.Reaches,
		Snippet:   access.Snippet,
		Revealed:  value != "" && m.alreadyRevealed(appName, secretName, value),
		Published: value != "",
	}
}

// lookupAccess resolves one (app, secret) pair to its access block, so a
// reveal cannot name an arbitrary secret and get whatever the store holds for
// the app.
func (m *clientCredentialsModule) lookupAccess(appName, secretName string) (*catalog.ClientAccess, error) {
	offers, err := m.clientAccessOffers(appName)
	if err != nil {
		return nil, err
	}
	for _, entry := range offers {
		access := entry.offer.ClientAccess
		named := access.Secret
		if named == "" && len(entry.offer.Secrets) == 1 {
			named = entry.offer.Secrets[0]
		}
		if named == secretName {
			return access, nil
		}
	}
	return nil, notFoundf(
		"app %q declares no client-accessible credential named %q", appName, secretName)
}

func (m *clientCredentialsModule) value(appName, secretName string) string {
	if m.secrets == nil || secretName == "" {
		return ""
	}
	return m.secrets.GetAppSecret(appName, secretName)
}

// revealedKey records which value was shown for one (app, secret) pair.
func revealedKey(appName, secretName string) string {
	return revealedKeyPrefix + ":" + appName + ":" + secretName
}

// alreadyRevealed compares the stored fingerprint of the value that was shown
// against the current one.
//
// The fingerprint rather than the value goes into the settings store, so the
// store does not end up holding a second copy of a credential it has no
// reason to hold. The comparison is constant-time: this is a secret, and a
// timing signal on a stored-vs-current comparison is a signal worth not
// giving away.
func (m *clientCredentialsModule) alreadyRevealed(appName, secretName, value string) bool {
	if m.settings == nil {
		return false
	}
	stored, err := m.settings.Get(revealedKey(appName, secretName))
	if err != nil || stored == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(fingerprint(value))) == 1
}

func (m *clientCredentialsModule) markRevealed(appName, secretName, value string) error {
	if m.settings == nil {
		return fmt.Errorf("settings store unavailable")
	}
	return m.settings.Set(revealedKey(appName, secretName), fingerprint(value))
}

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// renderSnippet builds the copy-ready form the provider asked for. The URL is
// the live instance address rather than anything stored with the credential,
// so a reveal after an address change hands over the address that currently
// works.
func (m *clientCredentialsModule) renderSnippet(shape, value string) string {
	base := m.publicURL()
	switch shape {
	case catalog.SnippetURLAndPassword:
		return fmt.Sprintf("URL: %s\nPassword: %s", base, value)
	case catalog.SnippetURLAndKey:
		return fmt.Sprintf("URL: %s\nAPI key: %s", base, value)
	case catalog.SnippetConfigBlock:
		return fmt.Sprintf("url = %s\ntoken = %s\n", base, value)
	default:
		return value
	}
}

func (m *clientCredentialsModule) publicURL() string {
	if m.hostState == nil {
		return hostset.DefaultPublicURL
	}
	return m.hostState.Get().PrimaryBaseURL()
}

// fail maps a lookup error to a status. A typed notFound is a 404 and anything
// else is a 503, because the only other failure this path has is a missing
// dependency.
func (m *clientCredentialsModule) fail(w http.ResponseWriter, err error) {
	var missing *notFound
	if errors.As(err, &missing) {
		respondError(w, http.StatusNotFound, missing.Error())
		return
	}
	respondError(w, http.StatusServiceUnavailable, err.Error())
}
