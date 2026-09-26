// SPDX-License-Identifier: AGPL-3.0-only

package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSecrets struct {
	password string
	err      error
	calls    int
}

func (f *fakeSecrets) GetAppSecret(string, string) string        { return "" }
func (f *fakeSecrets) SetAppSecret(string, string, string) error { return nil }
func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) {
	f.calls++
	return f.password, f.err
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

var testAcct = Account{Username: "bloud-admin", Email: "bloud-admin@localhost", FullName: "Bloud Admin"}

// scriptOps returns Ops whose login results come from a queue, so a test can
// describe exactly what the app answered on each call.
func scriptOps(logins []string, loginErrs []error, createErr error) (Ops, *int, *int) {
	var loginCalls, createCalls int
	return Ops{
		Login: func(_ context.Context, _ Account, _ string) (string, error) {
			i := loginCalls
			loginCalls++
			if i >= len(logins) {
				return "", errors.New("login queue exhausted")
			}
			return logins[i], loginErrs[i]
		},
		Create: func(_ context.Context, _ Account, _ string) error {
			createCalls++
			return createErr
		},
	}, &loginCalls, &createCalls
}

func TestEnsureFastPathWhenAccountAlreadyWorks(t *testing.T) {
	ops, logins, creates := scriptOps([]string{"tok"}, []error{nil}, nil)
	tok, out, err := Ensure(context.Background(), quietLogger(), "app", &fakeSecrets{password: "pw"}, testAcct, ops)
	require.NoError(t, err)
	assert.Equal(t, "tok", tok)
	assert.Equal(t, AlreadyPresent, out)
	assert.Equal(t, 1, *logins, "the fast path must cost exactly one login")
	assert.Equal(t, 0, *creates, "an existing account must not be re-created")
}

func TestEnsureCreatesWhenLoginFails(t *testing.T) {
	ops, logins, creates := scriptOps(
		[]string{"", "tok"},
		[]error{errors.New("401"), nil},
		nil,
	)
	tok, out, err := Ensure(context.Background(), quietLogger(), "app", &fakeSecrets{password: "pw"}, testAcct, ops)
	require.NoError(t, err)
	assert.Equal(t, "tok", tok)
	assert.Equal(t, Created, out)
	assert.Equal(t, 2, *logins, "create must be followed by a verifying login")
	assert.Equal(t, 1, *creates)
}

// The policy this package exists to enforce: an unverifiable admin is reported,
// never a failure. A node that went ERROR here would stay failed until a human
// intervened, over a condition that does not stop SSO users using the app.
func TestEnsureUnverifiedIsNotAnError(t *testing.T) {
	ops, _, creates := scriptOps(
		[]string{"", ""},
		[]error{errors.New("401"), errors.New("401")},
		nil,
	)
	tok, out, err := Ensure(context.Background(), quietLogger(), "app", &fakeSecrets{password: "pw"}, testAcct, ops)
	require.NoError(t, err, "an unverifiable admin must not fail the caller")
	assert.Equal(t, "", tok)
	assert.Equal(t, Unverified, out)
	assert.Equal(t, 1, *creates)
}

func TestEnsureEmptyTokenWithNilErrorIsRejection(t *testing.T) {
	// An app that answers "no" without the call itself failing must not be
	// mistaken for success.
	ops, _, _ := scriptOps([]string{"", ""}, []error{nil, nil}, nil)
	tok, out, err := Ensure(context.Background(), quietLogger(), "app", &fakeSecrets{password: "pw"}, testAcct, ops)
	require.NoError(t, err)
	assert.Equal(t, "", tok)
	assert.Equal(t, Unverified, out)
}

func TestEnsureCreateFailureIsAnError(t *testing.T) {
	ops, _, _ := scriptOps([]string{"", ""}, []error{errors.New("401"), nil}, errors.New("boom"))
	_, out, err := Ensure(context.Background(), quietLogger(), "app", &fakeSecrets{password: "pw"}, testAcct, ops)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating the internal admin account")
	assert.Equal(t, Unverified, out)
}

func TestEnsureNoSecretsIsAnError(t *testing.T) {
	ops, _, _ := scriptOps([]string{"tok"}, []error{nil}, nil)
	_, _, err := Ensure(context.Background(), quietLogger(), "app", nil, testAcct, ops)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoSecrets)
}

func TestEnsurePasswordGenerationFailureIsAnError(t *testing.T) {
	ops, _, creates := scriptOps([]string{"tok"}, []error{nil}, nil)
	_, _, err := Ensure(context.Background(), quietLogger(), "app", &fakeSecrets{err: errors.New("kms down")}, testAcct, ops)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generating admin password")
	assert.Equal(t, 0, *creates, "must not touch the app when there is no password")
}

func TestEnsureMissingOpsRejected(t *testing.T) {
	_, _, err := Ensure(context.Background(), quietLogger(), "app", &fakeSecrets{password: "pw"}, testAcct, Ops{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Login and a Create")
}

func TestOutcomeString(t *testing.T) {
	assert.Equal(t, "already-present", AlreadyPresent.String())
	assert.Equal(t, "created", Created.String())
	assert.Equal(t, "unverified", Unverified.String())
	assert.Equal(t, "unknown", Outcome(99).String())
}
