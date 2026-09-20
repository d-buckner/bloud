// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToken_RefreshesOnceOn401(t *testing.T) {
	var fetches int32
	var authed int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer tok-1" {
			atomic.AddInt32(&authed, 1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	src := CachedToken(func(ctx context.Context) (string, error) {
		n := atomic.AddInt32(&fetches, 1)
		if n == 1 {
			return "bad", nil // first token is rejected
		}
		return "tok-1", nil
	})
	c := New(Spec{Name: "t", BaseURL: srv.URL, Tokens: &TokenSpec{Source: src}})
	c.WithSleeper(func(time.Duration) {})

	_, err := c.GET("/secure").Do(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&fetches), "refetched once after 401")
	assert.Equal(t, int32(1), atomic.LoadInt32(&authed))
}

func TestToken_Still401AfterRefresh_SurfacesError(t *testing.T) {
	var fetches int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	src := CachedToken(func(ctx context.Context) (string, error) {
		atomic.AddInt32(&fetches, 1)
		return "bad", nil
	})
	c := New(Spec{Name: "t", BaseURL: srv.URL, Tokens: &TokenSpec{Source: src}})
	c.WithSleeper(func(time.Duration) {})
	_, err := c.GET("/secure").Do(context.Background())
	require.Error(t, err)
	assert.Equal(t, 401, StatusOf(err))
	assert.Equal(t, int32(2), atomic.LoadInt32(&fetches), "initial fetch + one refresh, no more")
}

func TestCachedToken_SingleFlight(t *testing.T) {
	var fetches int32
	src := CachedToken(func(ctx context.Context) (string, error) {
		atomic.AddInt32(&fetches, 1)
		time.Sleep(20 * time.Millisecond)
		return "tok", nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := src.Token(context.Background())
			assert.NoError(t, err)
			assert.Equal(t, "tok", v)
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), atomic.LoadInt32(&fetches), "single-flight: one fetch for 8 callers")
}

func TestWait_ConvergesOnStable(t *testing.T) {
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 4 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("pending"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	}))
	err := c.GET("/status").
		Ready(func(s int, b []byte) bool { return string(b) == "ready" }).
		Stable(2).
		Wait(context.Background())
	require.NoError(t, err)
	// 3 pending + 2 ready consecutive = 5.
	assert.Equal(t, int32(5), atomic.LoadInt32(&calls))
}

func TestWait_StableDoesNotReturnOnOscillation(t *testing.T) {
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
		// Oscillates ready/pending forever: never 2 consecutive.
		if n%2 == 1 {
			_, _ = w.Write([]byte("ready"))
		} else {
			_, _ = w.Write([]byte("pending"))
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := c.GET("/osc").
		Ready(func(s int, b []byte) bool { return string(b) == "ready" }).
		Stable(2).
		Wait(ctx)
	require.Error(t, err, "oscillating body never satisfies Stable(2)")
}

func TestWait_TolerateFailuresReturnsNilAfterGoodRead(t *testing.T) {
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("good"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	err := c.GET("/tol").
		Ready(func(s int, b []byte) bool { return false }). // never ready
		TolerateFailures().
		Wait(ctx)
	require.NoError(t, err, "tolerate + a good read earlier → nil on timeout")
	assert.Greater(t, atomic.LoadInt32(&calls), int32(1))
}

func TestWait_AlreadyDoneReturnsNil(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // wizard already complete
	}))
	err := c.GET("/startup").
		AlreadyDone(http.StatusUnauthorized).
		Ready(func(s int, b []byte) bool { return false }).
		Wait(context.Background())
	require.NoError(t, err, "AlreadyDone short-circuits the wait")
}

func TestWait_WithoutReadyErrors(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	err := c.GET("/x").Wait(context.Background())
	require.Error(t, err)
}

func TestPredicates(t *testing.T) {
	assert.True(t, StatusIn(200, 204)(204, nil))
	assert.False(t, StatusIn(200, 204)(500, nil))
	assert.True(t, StatusIs(200)(200, nil))
	assert.True(t, StatusNot(400)(401, nil))
	assert.True(t, StatusLT(500)(404, nil))
	assert.True(t, JSONHas("url")(200, []byte("{\"url\":\"http://x\"}")))
	assert.False(t, JSONHas("url")(200, []byte("{\"other\":1}")))
}
