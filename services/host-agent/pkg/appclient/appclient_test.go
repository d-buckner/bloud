// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(Spec{Name: "test", BaseURL: srv.URL, Retry: RetryPolicy{MaxAttempts: 5, Initial: time.Millisecond, MaxInterval: 5 * time.Millisecond, Factor: 2, Jitter: 0}})
	c.WithSleeper(func(time.Duration) {})
	return c
}

func TestGet_RetriesTransientThenSucceeds(t *testing.T) {
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	body, err := c.GET("/thing").Do(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls), "GET retries 503 until success")
}

func TestPost_RetriesOnlyWithDeclaredContract(t *testing.T) {
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	_, err := c.POST("/thing").Do(context.Background())
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "undeclared POST does not retry")

	atomic.StoreInt32(&calls, 0)
	c2 := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	_, err = c2.POST("/thing").OK(http.StatusOK, http.StatusNoContent).Do(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "declared POST retries transient")
}

func TestRetryAfter_HonoredOverBackoff(t *testing.T) {
	var calls int32
	var slept []time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c := New(Spec{Name: "test", BaseURL: srv.URL, Retry: RetryPolicy{MaxAttempts: 5, Initial: time.Millisecond, MaxInterval: 10 * time.Millisecond, Factor: 2, Jitter: 0}})
	c.WithSleeper(func(d time.Duration) { slept = append(slept, d) })

	_, err := c.GET("/x").Do(context.Background())
	require.NoError(t, err)
	require.Len(t, slept, 1)
	assert.Equal(t, 30*time.Millisecond, slept[0], "Retry-After clamped to MaxInterval*3")
}

func TestWithRetry_FactorUnset_KeepsDeclaredInterval(t *testing.T) {
	// Regression (jellyfin lifecycle CI): policies declared with only
	// Initial/MaxInterval must poll at the declared cadence. When
	// effectivePolicy returned the WithRetry override without applying
	// withDefaults, the zero Factor collapsed every delay past the first
	// attempt to 0; the 60×1s wizard wait burned its whole budget in ~1s
	// and went terminal-error while Jellyfin was still legitimately loading.
	var slept []time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	c := New(Spec{Name: "test", BaseURL: srv.URL})
	c.WithSleeper(func(d time.Duration) { slept = append(slept, d) })

	err := c.GET("/x").
		WithRetry(RetryPolicy{MaxAttempts: 4, Initial: 30 * time.Millisecond, MaxInterval: 30 * time.Millisecond}).
		Ready(func(status int, body []byte) bool { return status == http.StatusOK }).
		Wait(context.Background())

	require.Error(t, err)
	assert.Equal(t,
		[]time.Duration{30 * time.Millisecond, 30 * time.Millisecond, 30 * time.Millisecond},
		slept, "unset Factor must not collapse the poll interval to zero")
}

func TestNonTransient_FailsImmediately(t *testing.T) {
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	_, err := c.GET("/x").Do(context.Background())
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "400 fails fast")
	assert.Equal(t, 400, StatusOf(err))
}

func TestContextCancel_NoExtraAttempts(t *testing.T) {
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.GET("/x").Do(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "wrapped context.Canceled: %v", err)
	assert.Equal(t, int32(0), atomic.LoadInt32(&calls), "no attempt after cancel")
}

func TestDoInto_DecodeErrorCarriesBody(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	var out map[string]string
	err := c.GET("/x").DoInto(context.Background(), &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not json", "decode error surfaces the body")
}

func TestJitter_Bounds(t *testing.T) {
	d := 100.0
	j := 0.2
	for i := 0; i < 1000; i++ {
		v := jitterDuration(d, j)
		assert.GreaterOrEqual(t, v, d*(1-j))
		assert.LessOrEqual(t, v, d*(1+j))
	}
}

func TestHTTPError_Format(t *testing.T) {
	e := &HTTPError{Name: "jellyfin", Method: "POST", URL: "/Startup/User", Status: 503, Body: []byte("loading"), Attempt: 4}
	assert.Equal(t, "jellyfin: POST /Startup/User → 503 (attempt 4): loading", e.Error())
	assert.True(t, e.IsTransient())
}

func TestEnsure_ChangedOnlyWhenDifferent(t *testing.T) {
	var applied int32
	changed, err := Ensure(context.Background(), EnsureSpec[int]{
		Name:    "n",
		Current: func(context.Context) (int, error) { return 5, nil },
		Desired: 5,
		Apply:   func(context.Context, int) error { atomic.AddInt32(&applied, 1); return nil },
	})
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, int32(0), atomic.LoadInt32(&applied), "no Apply when equal")

	changed, err = Ensure(context.Background(), EnsureSpec[int]{
		Name:    "n",
		Current: func(context.Context) (int, error) { return 3, nil },
		Desired: 5,
		Apply:   func(context.Context, int) error { atomic.AddInt32(&applied, 1); return nil },
	})
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, int32(1), atomic.LoadInt32(&applied))
}

func TestAlreadyDone_ChangedFalse(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("First user already created"))
	}))
	changed, err := c.POST("/setup").
		AlreadyDoneFunc(func(s int, b []byte) bool {
			return s == http.StatusForbidden && string(b) == "First user already created"
		}).
		Ensure(context.Background())
	require.NoError(t, err)
	assert.False(t, changed, "AlreadyDoneFunc → changed=false")
}

func TestJar_PresentsCookieOnLaterCalls(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/issue":
			http.SetCookie(w, &http.Cookie{Name: "csrftoken", Value: "token-value", Path: "/"})
			_, _ = w.Write([]byte("issued"))
		case "/verify":
			cookie, err := r.Cookie("csrftoken")
			if err != nil || cookie.Value != "token-value" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Spec{Name: "test", BaseURL: srv.URL, Jar: jar})

	_, err = c.GET("/issue").Do(context.Background())
	require.NoError(t, err)
	body, err := c.GET("/verify").Do(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body), "the cookie issued by the first call must be sent with the next")
}
