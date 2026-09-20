// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appclient

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

// TokenSource supplies a bearer-style token and can be invalidated to force a
// refetch (e.g. after a 401).
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	Invalidate()
}

// TokenSpec describes how a managed token is attached to a request. It covers
// every auth-header dialect observed across the apps:
//
//	immich / HA / authentik: Authorization: Bearer <t>   (defaults)
//	navidrome:               X-ND-Authorization: Bearer <t>
//	jellyfin:                Authorization: MediaBrowser ... Token="<t>"
type TokenSpec struct {
	// Source fetches (and caches) the token.
	Source TokenSource
	// Header is the request header name. Default "Authorization".
	Header string
	// Format is a printf template with one %s for the token.
	// Default "Bearer %s".
	Format string
}

// apply attaches the token to req using the spec's header + format.
func (ts *TokenSpec) apply(req *http.Request) error {
	if ts == nil || ts.Source == nil {
		return nil
	}
	tok, err := ts.Source.Token(req.Context())
	if err != nil {
		return fmt.Errorf("obtain auth token: %w", err)
	}
	header := ts.Header
	if header == "" {
		header = "Authorization"
	}
	format := ts.Format
	if format == "" {
		format = "Bearer %s"
	}
	req.Header.Set(header, fmt.Sprintf(format, tok))
	return nil
}

// cachedToken memoizes a fetch func with single-flight so concurrent callers
// share one login. Invalidate clears the cache so the next Token refetches.
type cachedToken struct {
	mu       sync.Mutex
	fetch    func(ctx context.Context) (string, error)
	value    string
	hasVal   bool
	fetching bool
	waiters  chan struct{}
}

// CachedToken wraps a fetch func with memoization + single-flight.
func CachedToken(fetch func(ctx context.Context) (string, error)) TokenSource {
	return &cachedToken{fetch: fetch}
}

// Token returns the cached token, fetching it once (single-flight) on a miss.
func (c *cachedToken) Token(ctx context.Context) (string, error) {
	for {
		c.mu.Lock()
		if c.hasVal {
			v := c.value
			c.mu.Unlock()
			return v, nil
		}
		if !c.fetching {
			c.fetching = true
			c.waiters = make(chan struct{})
			c.mu.Unlock()
			return c.doFetch(ctx)
		}
		wait := c.waiters
		c.mu.Unlock()
		select {
		case <-wait:
			// Loop: re-check cache (now filled or failed).
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// doFetch runs the fetch under the fetching flag and publishes the result.
func (c *cachedToken) doFetch(ctx context.Context) (string, error) {
	v, err := c.fetch(ctx)
	c.mu.Lock()
	if err == nil {
		c.value = v
		c.hasVal = true
	}
	c.fetching = false
	close(c.waiters)
	c.waiters = nil
	c.mu.Unlock()
	if err != nil {
		return "", err
	}
	return v, nil
}

// Invalidate clears the cached token so the next Token call refetches.
func (c *cachedToken) Invalidate() {
	c.mu.Lock()
	c.value = ""
	c.hasVal = false
	c.mu.Unlock()
}
