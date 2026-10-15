// SPDX-License-Identifier: AGPL-3.0-only

package appclient

import (
	"log/slog"
	"net/http"
)

// Factory builds app HTTP clients for one process. It stamps in the shared
// transport, the default retry policy, and the shared logger, so every
// configurator's calls pool connections and behave the same way.
//
// It is a value type and the zero value is usable: Factory{}.New(spec) falls
// back to the process-shared transport, DefaultRetry, and slog.Default(). A
// configurator receives it through configurator.Deps.HTTP, which is typed as
// the configurator.HTTPFactory interface. The host injects this concrete type.
type Factory struct {
	// Transport is the shared *http.Transport. nil → DefaultTransport().
	Transport *http.Transport
	// Retry is the default retry policy for clients built here. Zero →
	// DefaultRetry (unless the Spec sets its own).
	Retry RetryPolicy
	// Logger is stamped onto clients that do not set their own.
	Logger *slog.Logger
}

// New builds a Client from a Spec, filling the factory's shared transport,
// retry policy, and logger into any unset Spec fields. The Spec always wins
// where it is explicit.
func (f Factory) New(s Spec) *Client {
	if s.Transport == nil {
		s.Transport = f.Transport
	}
	if s.Retry == (RetryPolicy{}) {
		s.Retry = f.Retry
	}
	if s.Logger == nil {
		s.Logger = f.Logger
	}
	return New(s)
}
