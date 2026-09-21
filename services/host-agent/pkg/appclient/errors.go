// SPDX-License-Identifier: AGPL-3.0-only

// Package appclient is the shared HTTP transport for Bloud app configurators.
//
// It knows HTTP, retry classification, timeouts, token plumbing, and waits,
// and nothing about any particular app. A configurator builds a Client from a
// Spec, then expresses each call as a small chain of declared intent
// (verb + path + expected outcome), so the transport mechanics live in exactly
// one place instead of being re-implemented per app.
//
// See docs/plans/app-client.md for the design rationale.
package appclient

import (
	"errors"
	"fmt"
	"time"
)

// maxErrorBody caps the response body retained in an HTTPError so a large or
// binary error page cannot balloon logs and error strings.
const maxErrorBody = 512

// HTTPError is the error type for a non-success HTTP outcome. It carries the
// request coordinates, the status, a capped body, and the 1-based attempt
// number that produced it (Attempt > 1 means the call was retried).
type HTTPError struct {
	// Name is the client's app/node identity ("" when unset).
	Name string
	// Method is the HTTP method (GET, POST, ...).
	Method string
	// URL is the full request URL.
	URL string
	// Status is the HTTP status code (0 for a transport-level failure).
	Status int
	// Body is the response body, capped at maxErrorBody with a trailing
	// "… (+N bytes)" marker when truncated.
	Body []byte
	// Attempt is the 1-based attempt that produced this error.
	Attempt int

	// retryAfter carries a parsed Retry-After header (429/503) so the retry
	// loop can honor it instead of its computed backoff. Unexported: it is
	// transport metadata, not part of the error's public shape.
	retryAfter time.Duration
}

// Error renders the canonical one-line form:
//
//	jellyfin: POST /Startup/User → 503 (attempt 4): <body>
func (e *HTTPError) Error() string {
	prefix := ""
	if e.Name != "" {
		prefix = e.Name + ": "
	}
	attempt := "attempt 1"
	if e.Attempt > 1 {
		attempt = fmt.Sprintf("attempt %d", e.Attempt)
	}
	if e.Status == 0 {
		return fmt.Sprintf("%s%s %s → transport error (%s): %s", prefix, e.Method, e.URL, attempt, string(e.Body))
	}
	return fmt.Sprintf("%s%s %s → %d (%s): %s", prefix, e.Method, e.URL, e.Status, attempt, string(e.Body))
}

// IsTransient reports whether the status is in the retried set. A transport
// error (Status == 0) is transient.
func (e *HTTPError) IsTransient() bool {
	if e.Status == 0 {
		return true
	}
	return isTransientStatus(e.Status)
}

// StatusOf returns the HTTP status carried by err, or 0 when err is not an
// *HTTPError (including a nil error).
func StatusOf(err error) int {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// BodyOf returns the response body carried by err, or nil when err is not an
// *HTTPError.
func BodyOf(err error) []byte {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Body
	}
	return nil
}

// isTransientErr reports whether err is a transient failure worth retrying:
// a transport error (HTTPError with Status 0) or a transient HTTP status. A
// non-HTTP error (build error, token-obtain error) is not transient.
func isTransientErr(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.IsTransient()
	}
	return false
}

// capBody truncates b to maxErrorBody bytes, appending a marker naming how many
// bytes were dropped so the operator knows the body was longer than shown.
func capBody(b []byte) []byte {
	if len(b) <= maxErrorBody {
		return b
	}
	suffix := fmt.Sprintf("… (+%d bytes)", len(b)-maxErrorBody)
	out := make([]byte, 0, maxErrorBody+len(suffix))
	out = append(out, b[:maxErrorBody]...)
	out = append(out, suffix...)
	return out
}

// truncate is a short-body helper for decode-error messages (smaller cap than
// HTTPError's, since the message already carries the decode error).
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
