// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appclient

import (
	"net/http"
	"strconv"
	"time"
)

// RetryPolicy bounds how a call retries transient failures. The zero value is
// valid: New fills the defaults from DefaultRetry.
type RetryPolicy struct {
	// MaxAttempts is the total attempt budget. 0 means unbounded (bounded
	// only by Deadline and the caller's context).
	MaxAttempts int
	// Deadline is the wall-clock budget across all attempts. 0 means no
	// wall-clock bound beyond the caller's context.
	Deadline time.Duration
	// Initial is the first backoff interval.
	Initial time.Duration
	// MaxInterval caps a single backoff interval (before Retry-After).
	MaxInterval time.Duration
	// Factor multiplies the interval each attempt.
	Factor float64
	// Jitter is a 0..1 fraction of the interval applied as ±randomness so
	// concurrent clients do not stampede.
	Jitter float64
}

// DefaultRetry is the policy for ordinary calls: a handful of attempts over a
// short window, enough to ride out a blip without masking a real failure.
var DefaultRetry = RetryPolicy{
	MaxAttempts: 5,
	Deadline:    30 * time.Second,
	Initial:     500 * time.Millisecond,
	MaxInterval: 5 * time.Second,
	Factor:      2,
	Jitter:      0.2,
}

// WaitPolicy is the default for calls marked with Ready(): long and generous,
// because the job is "wait out an app boot", not "paper over a blip".
var WaitPolicy = RetryPolicy{
	MaxAttempts: 0,
	Deadline:    3 * time.Minute,
	Initial:     time.Second,
	MaxInterval: 5 * time.Second,
	Factor:      1.5,
	Jitter:      0.1,
}

// withDefaults returns a copy with any zero/unset field filled from the
// DefaultRetry knobs (never from a zero policy, which would spin).
func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.Initial <= 0 {
		p.Initial = DefaultRetry.Initial
	}
	if p.MaxInterval <= 0 {
		p.MaxInterval = DefaultRetry.MaxInterval
	}
	if p.Factor <= 1 {
		p.Factor = DefaultRetry.Factor
	}
	if p.Jitter < 0 {
		p.Jitter = 0
	}
	if p.Jitter > 1 {
		p.Jitter = 1
	}
	return p
}

// isTransientStatus reports whether an HTTP status is retried under any policy
// (unless the call opted out with NoRetry).
func isTransientStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, // 408
		425, // Too Early
		http.StatusTooManyRequests, // 429
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// parseRetryAfter parses a Retry-After header value (delta-seconds or an
// HTTP-date) into a duration. Returns 0 when absent, unparseable, or already
// in the past.
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// clampRetryAfter bounds a Retry-After value to maxInterval*3 so a hostile or
// buggy upstream cannot park a reconciliation pass indefinitely.
func clampRetryAfter(d, maxInterval time.Duration) time.Duration {
	limit := maxInterval * 3
	if d > limit {
		return limit
	}
	return d
}
