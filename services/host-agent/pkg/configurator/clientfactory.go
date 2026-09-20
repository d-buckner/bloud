// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package configurator

import (
	"log/slog"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// ClientFactory builds app HTTP clients for this process, stamping in the
// shared transport, default retry policy, and shared logger so every
// configurator's calls pool connections and behave consistently.
//
// It is a value type and the zero value is usable: ClientFactory{}.New(spec)
// falls back to the process-shared transport, the appclient default retry
// policy, and slog.Default(). This keeps the "tolerate nil deps in CLI/test
// contexts" contract intact: a client never panics on a zero Deps.
type ClientFactory struct {
	// Transport is the shared *http.Transport. nil → appclient.DefaultTransport().
	Transport *http.Transport
	// Retry is the default retry policy for clients built here. Zero →
	// appclient.DefaultRetry (unless the Spec sets its own).
	Retry appclient.RetryPolicy
	// Logger is stamped onto clients that do not set their own.
	Logger *slog.Logger
}

// New builds an *appclient.Client from a Spec, filling the factory's shared
// transport / retry / logger into any unset Spec fields. The Spec always wins
// where it is explicit.
func (f ClientFactory) New(s appclient.Spec) *appclient.Client {
	if s.Transport == nil {
		s.Transport = f.Transport
	}
	if s.Retry == (appclient.RetryPolicy{}) {
		s.Retry = f.Retry
	}
	if s.Logger == nil {
		s.Logger = f.Logger
	}
	return appclient.New(s)
}
