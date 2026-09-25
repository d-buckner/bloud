// SPDX-License-Identifier: AGPL-3.0-only

// Package bootstrap holds the sequence every Bloud-managed app goes through to
// get an internal admin account: log in with the known password, create the
// account if that fails, verify by logging in again.
//
// The sequence is the same for every app. What differed, app to app, was what
// happened when verification failed: one app failed the node hard, another
// returned an empty token with a nil error, another never verified at all.
// Those are three different answers to the same question, and the apps that
// disagreed were not the ones that had thought about it hardest. This package
// puts one policy in one place.
//
// The policy: an admin that cannot be verified is a reported condition, not a
// failure. SSO users do not need the internal admin to use the app, so a
// rejected internal credential must not put the app into ERROR, which is
// terminal and would need human intervention. The next reconciliation retries.
// What IS an error is not being able to reach the app at all, or being unable
// to create the account.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// Account is the internal admin identity a configurator drives an app's own
// API with. It is not the operator's account and is never presented as one:
// it exists so the configurator can call endpoints the app reserves to
// admins.
type Account struct {
	// Username is the login name. Some apps key on a username, others on an
	// email; fill in whichever the app authenticates with.
	Username string

	// Email is the account's email address, for apps that identify by it.
	Email string

	// FullName is the display name, for apps that take one.
	FullName string
}

// Outcome is what Ensure did. It is an enum rather than a bool because
// "already there" and "could not be verified" both produce no error and need
// to be told apart by the caller's logs and follow-on steps.
type Outcome int

const (
	// AlreadyPresent means the account existed and the stored password
	// authenticated on the first try.
	AlreadyPresent Outcome = iota

	// Created means the account did not exist, was created, and then
	// authenticated.
	Created

	// Unverified means the account was created (or was already there) but
	// the credential did not authenticate. This is not an error: the app
	// works for SSO users regardless, and the next reconciliation retries.
	// A caller that needs an admin API token gets an empty one and should
	// skip whatever needed it rather than fail.
	Unverified
)

// String names the outcome for logs.
func (o Outcome) String() string {
	switch o {
	case AlreadyPresent:
		return "already-present"
	case Created:
		return "created"
	case Unverified:
		return "unverified"
	default:
		return "unknown"
	}
}

// Ops is the pair of app-specific calls Ensure drives. Each app's API client
// supplies them; the sequence and the failure policy stay here.
type Ops struct {
	// Login authenticates and returns a token. An empty token with a nil
	// error is a valid "rejected" answer: the app said no without the call
	// itself failing.
	Login func(ctx context.Context, acct Account, password string) (string, error)

	// Create makes the account exist. It is only called when Login did not
	// succeed.
	Create func(ctx context.Context, acct Account, password string) error
}

// ErrNoSecrets is returned when the host gave the configurator no secret
// store, so there is no password to bootstrap with.
var ErrNoSecrets = errors.New("no secrets provider")

// Ensure runs the login-fast-path / create / verify sequence for one app and
// returns the admin API token alongside what happened.
//
// It is idempotent by construction: the fast path makes a second run cost one
// request, and Create is only reached when that path did not authenticate.
func Ensure(ctx context.Context, log *slog.Logger, app string, secrets configurator.AppSecretsProvider, acct Account, ops Ops) (string, Outcome, error) {
	if secrets == nil {
		return "", Unverified, fmt.Errorf("%s: %w", app, ErrNoSecrets)
	}
	if ops.Login == nil || ops.Create == nil {
		return "", Unverified, fmt.Errorf("%s: bootstrap needs both a Login and a Create operation", app)
	}

	password, err := secrets.GenerateAppAdminPassword(app)
	if err != nil {
		return "", Unverified, fmt.Errorf("%s: generating admin password: %w", app, err)
	}

	// Fast path: the account already exists and the stored password works.
	// This is the normal state on every pass after the first.
	token, err := ops.Login(ctx, acct, password)
	if err == nil && token != "" {
		return token, AlreadyPresent, nil
	}

	log.Info("bootstrapping internal admin account", "app", app, "user", acct.Username)
	if err := ops.Create(ctx, acct, password); err != nil {
		return "", Unverified, fmt.Errorf("%s: creating the internal admin account: %w", app, err)
	}

	// Verify by logging in. A rejection here is reported, not failed: the
	// account may already have been owned by someone else, or the endpoint
	// may be rate-limited, and neither makes the app unusable for SSO users.
	token, err = ops.Login(ctx, acct, password)
	if err != nil || token == "" {
		log.Warn("internal admin account created but not verified; the next reconciliation will retry",
			"app", app, "user", acct.Username, "error", err)
		return "", Unverified, nil
	}

	log.Info("internal admin account created and verified", "app", app, "user", acct.Username)
	return token, Created, nil
}
