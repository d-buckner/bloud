// SPDX-License-Identifier: AGPL-3.0-only

package appclient

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ReadyFunc decides whether an observed response means "ready".
type ReadyFunc func(status int, body []byte) bool

// Ready switches the call into wait mode with the given readiness predicate.
//
// A wait defaults to WaitPolicy, not to the client's DefaultRetry: waiting out
// an app's first-boot migrations is a different job from riding out a network
// blip, and the short default attempt budget is what used to land a slow-booting
// app in a terminal ERROR that the orchestrator never retries. An explicit
// WithRetry, set before or after this call, still wins.
func (x *Call) Ready(p ReadyFunc) *Call {
	x.ready = p
	if x.stable <= 0 {
		x.stable = 1
	}
	if x.retryOverride == nil {
		wp := WaitPolicy
		x.retryOverride = &wp
	}
	return x
}

// Interval sets the poll interval for a wait (overrides the policy backoff).
func (x *Call) Interval(d time.Duration) *Call {
	x.interval = d
	return x
}

// Stable requires the readiness predicate to hold n consecutive polls before
// the wait returns nil (guards against an oscillating value).
func (x *Call) Stable(n int) *Call {
	x.stable = n
	if x.ready != nil && x.stable < 1 {
		x.stable = 1
	}
	return x
}

// TolerateFailures makes a later timeout non-fatal once a good read has been
// observed (the jellyfin "keep the last good read and fall through" case).
func (x *Call) TolerateFailures() *Call {
	x.tolerateFailures = true
	return x
}

// Wait polls until the readiness predicate holds for Stable consecutive polls,
// an AlreadyDone outcome is seen, or the deadline/ctx expires. In wait mode an
// unexpected status means "not ready yet" and is retried (surfaced in the
// final error), never a hard failure.
func (x *Call) Wait(ctx context.Context) error {
	if x.ready == nil {
		return fmt.Errorf("%s: Wait called without a Ready predicate", x.c.name)
	}
	// A declared budget the framework cannot honour is a caller bug, not a
	// runtime condition to discover by truncation. Fail loudly here.
	if x.budgetErr != nil {
		return x.budgetErr
	}
	policy := x.effectivePolicy()
	if x.interval > 0 {
		policy.Initial = x.interval
	}
	start := time.Now()
	attempt := 0
	streak := 0
	sawGood := false
	var lastStatus int
	var lastBody []byte
	var lastErr error

	for {
		if err := ctx.Err(); err != nil {
			return x.conclude(sawGood, attempt, lastStatus, lastBody, lastErr, err)
		}
		attempt++
		res, err := x.attemptOnce(ctx, attempt)
		if err != nil {
			if errorsIsContext(err) {
				return x.conclude(sawGood, attempt, lastStatus, lastBody, lastErr, err)
			}
			lastErr = err
			streak = 0
		} else {
			lastStatus, lastBody = res.status, res.body
			lastErr = nil
			sawGood = true // a successful HTTP read (any status) counts as a good read
			if res.alreadyDone {
				return nil
			}
			if x.ready(res.status, res.body) {
				streak++
				if streak >= x.stable {
					x.c.logger.Debug("wait converged", "path", x.path, "attempts", attempt)
					return nil
				}
			} else {
				streak = 0
			}
		}

		if policy.MaxAttempts > 0 && attempt >= policy.MaxAttempts {
			return x.conclude(sawGood, attempt, lastStatus, lastBody, lastErr, fmt.Errorf("exhausted %d attempts", attempt))
		}
		if policy.Deadline > 0 && time.Since(start) >= policy.Deadline {
			return x.conclude(sawGood, attempt, lastStatus, lastBody, lastErr, fmt.Errorf("deadline exceeded"))
		}
		x.c.sleeper(x.nextDelay(policy, attempt, lastErr))
	}
}

// conclude decides a wait's terminal return: with TolerateFailures and a prior
// good read the wait succeeds despite the terminal condition ("keep the last
// good read and fall through"); otherwise it errors naming the cause.
func (x *Call) conclude(sawGood bool, attempt, lastStatus int, lastBody []byte, lastErr, cause error) error {
	if x.tolerateFailures && sawGood {
		return nil
	}
	return x.waitError(attempt, lastStatus, lastBody, lastErr, cause)
}

// waitError builds the terminal wait error naming the probe, attempt count, and
// last observation.
func (x *Call) waitError(attempt, lastStatus int, lastBody []byte, lastErr error, cause error) error {
	msg := fmt.Sprintf("%s: wait %s %s did not become ready after %d attempts", x.c.name, x.method, x.path, attempt)
	if lastErr != nil {
		msg += fmt.Sprintf(" (last error: %v)", lastErr)
	} else if lastStatus != 0 {
		msg += fmt.Sprintf(" (last status %d: %s)", lastStatus, truncate(lastBody, 120))
	}
	return fmt.Errorf("%s: %w", msg, cause)
}

// errorsIsContext reports whether err is a context cancellation/deadline.
func errorsIsContext(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
