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

// Wait is one readiness poll over a plain Call.
//
// The split is by what each value can mean on its own. A Call owns the
// request: verb, path, body, headers, auth, the per-request Timeout, and the
// outcome contract (OK / AlreadyDone / Ensure). A Wait owns everything that
// only has a meaning while polling for a state change: the readiness
// predicate, the cadence, how many consecutive good reads are required,
// whether a prior good read makes a later timeout survivable, and the total
// wall-clock budget.
//
// Build one with Call.Wait and terminate it with Do. Modifiers return the Wait,
// so a wait reads as one chain:
//
//	err := cl.GET("/api/health").
//		Wait(appclient.StatusIs(http.StatusOK)).
//		Interval(2 * time.Second).
//		Within(4 * time.Minute).
//		Do(ctx)
//
// A Wait is a value builder like Call: nothing is polled until Do runs.
type Wait struct {
	call *Call

	ready            ReadyFunc
	interval         time.Duration
	stable           int
	tolerateFailures bool

	// policy replaces WaitPolicy for this wait. Nil means WaitPolicy.
	policy *RetryPolicy

	// budgetErr records a declared budget the framework cannot honor. Do
	// surfaces it instead of letting the wait be truncated in silence.
	budgetErr error
}

// Wait starts a readiness wait over this call, polling the request until
// `ready` holds.
//
// The predicate is an argument rather than a modifier because a wait with no
// predicate is not a wait: previously Wait() returned the runtime error "Wait
// called without a Ready predicate", which is a build mistake that the type
// system can catch instead.
//
// The wait defaults to WaitPolicy, not to the client's DefaultRetry. Waiting
// out an app's first-boot migrations is a different job from riding out a
// network blip, and the short default attempt budget would land a
// slow-booting app in a terminal ERROR that the orchestrator never retries.
// An explicit WithRetry still wins.
func (x *Call) Wait(ready ReadyFunc) *Wait {
	w := &Wait{call: x, ready: ready, stable: 1}
	// A policy already declared on the call carries over, so a wait built as
	// GET(p).WithRetry(p).Wait(pred) behaves as GET(p).Wait(pred).WithRetry(p).
	// Silently dropping it would be the worse outcome: the caller asked for a
	// bound and got WaitPolicy instead.
	if x.retryOverride != nil {
		cp := *x.retryOverride
		w.policy = &cp
	}
	return w
}

// Interval sets the poll interval, overriding the policy's backoff starting
// point. The rest of the policy (cap, factor, jitter) still applies on top.
func (w *Wait) Interval(d time.Duration) *Wait {
	w.interval = d
	return w
}

// Stable requires the readiness predicate to hold n consecutive polls before
// the wait returns nil (guards against an oscillating value).
func (w *Wait) Stable(n int) *Wait {
	w.stable = n
	if w.stable < 1 {
		w.stable = 1
	}
	return w
}

// TolerateFailures makes a later timeout non-fatal once a good read has been
// observed (the jellyfin "keep the last good read and fall through" case).
func (w *Wait) TolerateFailures() *Wait {
	w.tolerateFailures = true
	return w
}

// Within sets the total elapsed budget of the wait, the wall-clock ceiling
// across all of its polls. It is the counterpart to Call.Timeout, which
// bounds one request: `Timeout(10s)` on the call plus `Within(5m)` on the
// wait means "each probe may take ten seconds, and the whole wait may take
// five minutes".
//
// It sets the policy Deadline, so it composes with WithRetry and Interval.
// A value above MaxWaitBudget is reported at Do time rather than being
// silently truncated by the framework's own PostStart ceiling.
func (w *Wait) Within(d time.Duration) *Wait {
	if w.policy == nil {
		wp := WaitPolicy
		w.policy = &wp
	}
	w.policy.Deadline = d
	if d > MaxWaitBudget {
		w.budgetErr = fmt.Errorf(
			"%s: wait budget %s exceeds MaxWaitBudget %s: the framework cancels PostStart at that ceiling, so this wait could never expire on its own",
			w.call.c.name, d, MaxWaitBudget)
	}
	return w
}

// WithRetry overrides the retry policy for this wait, bounding the total poll
// attempts and deadline. Lets one client serve both a short bounded check and
// a long readiness poll.
func (w *Wait) WithRetry(p RetryPolicy) *Wait {
	cp := p
	w.policy = &cp
	return w
}

// Do polls until the readiness predicate holds for Stable consecutive polls,
// an AlreadyDone outcome is seen, or the deadline/ctx expires. In wait mode an
// unexpected status means "not ready yet" and is retried (surfaced in the
// final error), never a hard failure.
func (w *Wait) Do(ctx context.Context) error {
	// A declared budget the framework cannot honor is a caller bug, not a
	// runtime condition to discover by truncation. Fail loudly here.
	if w.budgetErr != nil {
		return w.budgetErr
	}
	policy := w.effectivePolicy()
	start := time.Now()
	attempt := 0
	var st waitState

	for {
		if err := ctx.Err(); err != nil {
			return w.conclude(st.sawGood, attempt, st.lastStatus, st.lastBody, st.lastErr, err)
		}
		attempt++
		res, err := w.call.attemptOnce(ctx, attempt, w.ready)
		if err != nil {
			if errorsIsContext(err) {
				return w.conclude(st.sawGood, attempt, st.lastStatus, st.lastBody, st.lastErr, err)
			}
			st.recordFailure(err)
		} else {
			st.recordSuccess(res, w.ready(res.status, res.body))
			if res.alreadyDone {
				return nil
			}
			if st.streak >= w.stable {
				w.call.c.logger.Debug("wait converged", "path", w.call.path, "attempts", attempt)
				return nil
			}
		}

		if policy.MaxAttempts > 0 && attempt >= policy.MaxAttempts {
			return w.conclude(st.sawGood, attempt, st.lastStatus, st.lastBody, st.lastErr, fmt.Errorf("exhausted %d attempts", attempt))
		}
		if policy.Deadline > 0 && time.Since(start) >= policy.Deadline {
			return w.conclude(st.sawGood, attempt, st.lastStatus, st.lastBody, st.lastErr, fmt.Errorf("deadline exceeded"))
		}
		w.call.c.sleeper(w.call.nextDelay(policy, attempt, st.lastErr))
	}
}

// effectivePolicy returns the declared policy when set, else WaitPolicy.
// Defaults are applied so a policy that omits Factor cannot multiply its
// interval by zero from the second attempt on.
func (w *Wait) effectivePolicy() RetryPolicy {
	if w.policy != nil {
		p := w.policy.withDefaults()
		if w.interval > 0 {
			p.Initial = w.interval
		}
		return p
	}
	p := WaitPolicy.withDefaults()
	if w.interval > 0 {
		p.Initial = w.interval
	}
	return p
}

// waitState is what a readiness wait remembers between probes: whether it ever
// got a readable answer, how long the ready streak currently is, and the last
// thing it saw. Holding that in one value is what keeps the loop body short
// enough to read as a sequence of decisions.
type waitState struct {
	sawGood    bool
	streak     int
	lastStatus int
	lastBody   []byte
	lastErr    error
}

// recordFailure folds one failed attempt in: any error breaks the streak and is
// kept as the last thing seen, so the terminal error can name it.
func (s *waitState) recordFailure(err error) {
	s.lastErr = err
	s.streak = 0
}

// recordSuccess folds one readable HTTP answer in. Any status counts as a good
// read; only a ready status extends the streak.
func (s *waitState) recordSuccess(res result, ready bool) {
	s.lastStatus, s.lastBody = res.status, res.body
	s.lastErr = nil
	s.sawGood = true
	if ready {
		s.streak++
		return
	}
	s.streak = 0
}

// conclude decides a wait's terminal return: with TolerateFailures and a prior
// good read the wait succeeds despite the terminal condition ("keep the last
// good read and fall through"); otherwise it errors naming the cause.
func (w *Wait) conclude(sawGood bool, attempt, lastStatus int, lastBody []byte, lastErr, cause error) error {
	if w.tolerateFailures && sawGood {
		return nil
	}
	return w.waitError(attempt, lastStatus, lastBody, lastErr, cause)
}

// waitError builds the terminal wait error naming the probe, attempt count, and
// last observation.
func (w *Wait) waitError(attempt, lastStatus int, lastBody []byte, lastErr error, cause error) error {
	x := w.call
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
