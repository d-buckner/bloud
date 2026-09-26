// SPDX-License-Identifier: AGPL-3.0-only

package appclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the two budgets a readiness wait actually has: the
// per-request deadline (Timeout) and the total wait budget (Within). Before
// this existed neither was wired: Timeout stored a value nothing read, and a
// Ready() wait ran on DefaultRetry's 5 attempts / 30s, so an app whose
// first boot outlasted that landed in a terminal ERROR the orchestrator
// never retries.

// blockingHandler sleeps up to sleepFor, but reports immediately if the
// request's own context is cancelled first, so a test can tell "the server
// was slow" apart from "the client's per-request deadline fired".
func blockingHandler(sleepFor time.Duration, cancelled *atomic.Bool, calls *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		select {
		case <-time.After(sleepFor):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
		case <-r.Context().Done():
			if cancelled != nil {
				cancelled.Store(true)
			}
			// The client hung up; there is nothing to answer.
			return
		}
	})
}

func TestTimeout_AppliesARealPerRequestDeadline(t *testing.T) {
	var cancelled atomic.Bool
	srv := httptest.NewServer(blockingHandler(2*time.Second, &cancelled, nil))
	t.Cleanup(srv.Close)

	c := New(Spec{Name: "test", BaseURL: srv.URL})
	c.WithSleeper(func(time.Duration) {})

	start := time.Now()
	_, err := c.GET("/slow").NoRetry().Timeout(50 * time.Millisecond).Do(context.Background())
	elapsed := time.Since(start)

	require.Error(t, err, "a request that outlasts Timeout() must fail")
	assert.True(t, cancelled.Load(),
		"the per-request deadline must actually cancel the request (server never saw a client hang up)")
	assert.Less(t, elapsed, 1*time.Second,
		"must give up at the 50ms deadline, not wait out the 2s handler (took %s)", elapsed)
}

func TestTimeout_CanExtendPastTheClientDefault(t *testing.T) {
	srv := httptest.NewServer(blockingHandler(120*time.Millisecond, nil, nil))
	t.Cleanup(srv.Close)

	// A 30ms client-level default must not cap a call that asked for 300ms.
	// With a client-level http.Client.Timeout this failed at 30ms.
	c := New(Spec{Name: "test", BaseURL: srv.URL, Timeout: 30 * time.Millisecond})
	c.WithSleeper(func(time.Duration) {})

	_, err := c.GET("/slowish").NoRetry().Timeout(300 * time.Millisecond).Do(context.Background())
	require.NoError(t, err, "Timeout() must be able to extend past the client default")
}

func TestReady_DefaultsThePolicyToWaitPolicy(t *testing.T) {
	c := New(Spec{Name: "test", BaseURL: "http://localhost"})

	call := c.GET("/x").Ready(StatusIs(http.StatusOK))
	pol := call.effectivePolicy()

	assert.Equal(t, WaitPolicy.Deadline, pol.Deadline,
		"a Ready() wait must default to WaitPolicy's deadline, not DefaultRetry's 30s")
	assert.Equal(t, WaitPolicy.MaxAttempts, pol.MaxAttempts,
		"a Ready() wait must not be capped at DefaultRetry's 5 attempts")
}

func TestWithRetry_StillWinsOverTheReadyDefault(t *testing.T) {
	c := New(Spec{Name: "test", BaseURL: "http://localhost"})
	custom := RetryPolicy{MaxAttempts: 7, Deadline: 42 * time.Second, Initial: time.Second, MaxInterval: 2 * time.Second, Factor: 1.5}

	// Explicit policy before Ready().
	before := c.GET("/x").WithRetry(custom).Ready(StatusIs(http.StatusOK))
	assert.Equal(t, 7, before.effectivePolicy().MaxAttempts)
	assert.Equal(t, 42*time.Second, before.effectivePolicy().Deadline)

	// Explicit policy after Ready() also wins.
	after := c.GET("/x").Ready(StatusIs(http.StatusOK)).WithRetry(custom)
	assert.Equal(t, 7, after.effectivePolicy().MaxAttempts)
}

func TestWithin_SetsTheWaitDeadline(t *testing.T) {
	c := New(Spec{Name: "test", BaseURL: "http://localhost"})

	call := c.GET("/x").Within(4 * time.Minute).Ready(StatusIs(http.StatusOK))
	assert.Equal(t, 4*time.Minute, call.effectivePolicy().Deadline)

	// Order-independent: Within after Ready sets the same thing.
	call2 := c.GET("/x").Ready(StatusIs(http.StatusOK)).Within(4 * time.Minute)
	assert.Equal(t, 4*time.Minute, call2.effectivePolicy().Deadline)
}

func TestWithin_AboveMaxWaitBudgetFailsAtWait(t *testing.T) {
	c := New(Spec{Name: "test", BaseURL: "http://localhost"})
	c.WithSleeper(func(time.Duration) {})

	err := c.GET("/x").Within(MaxWaitBudget+time.Minute).
		Ready(StatusIs(http.StatusOK)).
		Wait(context.Background())

	require.Error(t, err, "a wait budget the framework cannot honour must fail loudly, not truncate")
	assert.Contains(t, err.Error(), "MaxWaitBudget")
}

// TestWait_PerRequestTimeoutIsTransientNotTerminal is the regression that
// matters most: a single probe blowing its per-request deadline must not end
// a readiness wait that still has budget. If the derived request context
// were consulted for terminality, the wait would abort here.
func TestWait_PerRequestTimeoutIsTransientNotTerminal(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			// First probe is slow: it will blow the 50ms per-request deadline.
			select {
			case <-time.After(300 * time.Millisecond):
				w.WriteHeader(http.StatusOK)
				return
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	}))
	t.Cleanup(srv.Close)

	c := New(Spec{Name: "test", BaseURL: srv.URL})
	c.WithSleeper(func(time.Duration) {})

	err := c.GET("/status").
		Timeout(50 * time.Millisecond).
		Within(10 * time.Second).
		Ready(StatusIs(http.StatusOK)).
		Wait(context.Background())

	require.NoError(t, err, "a slow first probe must not end the wait")
	assert.GreaterOrEqual(t, calls.Load(), int32(2), "the wait must have polled past the timed-out probe")
}

// TestWait_PollsPastFiveAttempts pins that a Ready() wait is not bounded by
// DefaultRetry's 5-attempt cap, which is what truncated cold boots.
func TestWait_PollsPastFiveAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	c := New(Spec{Name: "test", BaseURL: srv.URL})
	c.WithSleeper(func(time.Duration) {})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := c.GET("/status").Ready(StatusIs(http.StatusOK)).Wait(ctx)
	require.Error(t, err, "a server that never becomes ready must eventually fail")
	assert.Greater(t, int(calls.Load()), 5,
		"a readiness wait must keep polling past DefaultRetry's 5 attempts (got %d)", calls.Load())
}

// TestWait_CallerContextStillTerminal guards the other side: making the
// per-request deadline non-terminal must not make the caller's own
// cancellation non-terminal.
func TestWait_CallerContextStillTerminal(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	c := New(Spec{Name: "test", BaseURL: srv.URL})
	c.WithSleeper(func(time.Duration) {})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.GET("/status").Timeout(50 * time.Millisecond).
		Within(10 * time.Second).
		Ready(StatusIs(http.StatusOK)).
		Wait(ctx)
	require.Error(t, err, "a cancelled caller context must end the wait immediately")
	assert.LessOrEqual(t, calls.Load(), int32(1), "must not keep polling after cancellation")
}
