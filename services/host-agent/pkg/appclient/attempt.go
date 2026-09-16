// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// outcome is the classification of one HTTP attempt.
type outcome int

const (
	outcomeSuccess outcome = iota
	outcomeAlreadyDone
	outcomeTransient
	outcomeFatal
)

// attemptOnce performs a single request and classifies it. On a transient or
// fatal outcome it returns an *HTTPError; on success/alreadyDone it returns a
// result. A 401 with a token source triggers invalidate + one refetch + retry
// (the behavior no configurator has today) before surfacing the error.
func (x *Call) attemptOnce(ctx context.Context, attempt int) (result, error) {
	req, err := x.buildRequest(ctx)
	if err != nil {
		return result{}, err
	}

	resp, err := x.c.http.Do(req)
	if err != nil {
		// Transport-level failure: transient (retried) unless opted out.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result{}, ctxErr
		}
		x.logAttempt("", 0, attempt, "transport-error")
		return result{}, &HTTPError{
			Name:    x.c.name,
			Method:  x.method,
			URL:     req.URL.String(),
			Status:  0,
			Body:    []byte(err.Error()),
			Attempt: attempt,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result{}, ctxErr
		}
		return result{}, &HTTPError{
			Name:    x.c.name,
			Method:  x.method,
			URL:     req.URL.String(),
			Status:  resp.StatusCode,
			Body:    capBody([]byte("read body: " + readErr.Error())),
			Attempt: attempt,
		}
	}

	// 401 with a managed token: invalidate, refetch once, retry once.
	if resp.StatusCode == http.StatusUnauthorized && x.c.spec.Tokens != nil && !x.anonymous && !x.tokenRetried {
		x.tokenRetried = true
		x.c.spec.Tokens.Source.Invalidate()
		return x.attemptOnce(ctx, attempt)
	}

	oc := x.classify(resp.StatusCode, body)
	x.logAttempt(resp.Status, resp.StatusCode, attempt, classifyLabel(oc))

	switch oc {
	case outcomeSuccess:
		return result{status: resp.StatusCode, body: body}, nil
	case outcomeAlreadyDone:
		return result{status: resp.StatusCode, body: body, alreadyDone: true}, nil
	default:
		return result{}, &HTTPError{
			Name:       x.c.name,
			Method:     x.method,
			URL:        req.URL.String(),
			Status:     resp.StatusCode,
			Body:       capBody(body),
			Attempt:    attempt,
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), nowFunc()),
		}
	}
}

func classifyLabel(o outcome) string {
	switch o {
	case outcomeSuccess:
		return "success"
	case outcomeAlreadyDone:
		return "already-done"
	case outcomeTransient:
		return "transient"
	default:
		return "fatal"
	}
}

// retryAfterOf extracts a Retry-After duration from an *HTTPError if present.
func retryAfterOf(err error) time.Duration {
	var he *HTTPError
	if errors.As(err, &he) && he.retryAfter > 0 {
		return he.retryAfter
	}
	return 0
}

// attemptStream performs one request and, on a 2xx outcome, streams the body
// through consume. Classification is status-only (no body predicates).
func (x *Call) attemptStream(ctx context.Context, attempt int, consume func(io.Reader) error) error {
	req, err := x.buildRequest(ctx)
	if err != nil {
		return err
	}
	resp, err := x.c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		x.logAttempt("", 0, attempt, "transport-error")
		return &HTTPError{Name: x.c.name, Method: x.method, URL: req.URL.String(), Status: 0, Body: []byte(err.Error()), Attempt: attempt}
	}
	defer func() { _ = resp.Body.Close() }()

	// 401 with a managed token: invalidate, refetch once, retry once.
	if resp.StatusCode == http.StatusUnauthorized && x.c.spec.Tokens != nil && !x.anonymous && !x.tokenRetried {
		x.tokenRetried = true
		x.c.spec.Tokens.Source.Invalidate()
		return x.attemptStream(ctx, attempt, consume)
	}

	oc := x.classify(resp.StatusCode, nil)
	x.logAttempt(resp.Status, resp.StatusCode, attempt, classifyLabel(oc))
	if oc == outcomeSuccess || oc == outcomeAlreadyDone {
		return consume(resp.Body)
	}
	return &HTTPError{
		Name:       x.c.name,
		Method:     x.method,
		URL:        req.URL.String(),
		Status:     resp.StatusCode,
		Body:       capBody([]byte(resp.Status)),
		Attempt:    attempt,
		retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), nowFunc()),
	}
}

// runStream drives attemptStream with the retry policy.
func (x *Call) runStream(ctx context.Context, consume func(io.Reader) error) error {
	if x.buildErr != nil {
		return x.buildErr
	}
	policy := x.c.retry
	start := time.Now()
	attempt := 0
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return fmt.Errorf("%s: %w (last: %v)", x.c.name, err, lastErr)
			}
			return err
		}
		attempt++
		err := x.attemptStream(ctx, attempt, consume)
		if err == nil {
			return nil
		}
		lastErr = err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if !isTransientErr(err) || !x.retriesAllowed() {
			return err
		}
		if policy.MaxAttempts > 0 && attempt >= policy.MaxAttempts {
			return fmt.Errorf("%s: exhausted %d attempts: %w", x.c.name, attempt, err)
		}
		if policy.Deadline > 0 && time.Since(start) >= policy.Deadline {
			return fmt.Errorf("%s: retry deadline exceeded after %d attempts: %w", x.c.name, attempt, err)
		}
		x.c.sleeper(x.nextDelay(policy, attempt, err))
	}
}
