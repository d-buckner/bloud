// SPDX-License-Identifier: AGPL-3.0-only

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

// requestContext derives this call's per-request deadline: the Timeout()
// override when set, otherwise the client default. The caller's context is the
// parent, so a caller cancellation always wins over the request deadline, and
// the caller's context (not this derived one) is what decides whether a failure
// is terminal.
func (x *Call) requestContext(caller context.Context) (context.Context, context.CancelFunc) {
	d := x.timeoutOverride
	if d <= 0 {
		d = x.c.timeout
	}
	if d <= 0 {
		return caller, func() {}
	}
	return context.WithTimeout(caller, d)
}

// attemptOnce performs a single request and classifies it. On a transient or
// fatal outcome it returns an *HTTPError; on success/alreadyDone it returns a
// result. A 401 with a token source triggers invalidate + one refetch + retry
// (the behavior no configurator has today) before surfacing the error.
//
// ctx is the caller's context. The per-request deadline is derived from it and
// is never consulted for terminality: exceeding it is a transient failure, so a
// slow attempt cannot end a readiness wait that still has budget left.
//
// ready is the readiness predicate when this attempt is one probe of a Wait, and
// nil for a plain call. It is a parameter rather than a Call field because it
// changes only how the response body is read (see readBody); the request itself
// is identical either way.
func (x *Call) attemptOnce(ctx context.Context, attempt int, ready ReadyFunc) (result, error) {
	reqCtx, cancel := x.requestContext(ctx)
	defer cancel()

	req, err := x.buildRequest(reqCtx)
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
		return result{}, x.httpError(req, 0, []byte(err.Error()), attempt)
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := x.readBody(resp, ready)
	if readErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result{}, ctxErr
		}
		// A wait against a streaming endpoint (server-sent events) can hit the
		// per-request deadline after the answer already arrived: the server
		// keeps the stream open and never reaches EOF. In wait mode, hand the
		// partial body to the readiness predicate instead of discarding it, so
		// a complete answer is judged on its own merits rather than on the
		// stream's refusal to end.
		if ready != nil && len(body) > 0 {
			return result{status: resp.StatusCode, body: body}, nil
		}
		return result{}, x.httpError(req, resp.StatusCode, capBody([]byte("read body: "+readErr.Error())), attempt)
	}

	// 401 with a managed token: invalidate, refetch once, retry once.
	if resp.StatusCode == http.StatusUnauthorized && x.c.spec.Tokens != nil && !x.anonymous && !x.tokenRetried {
		x.tokenRetried = true
		x.c.spec.Tokens.Source.Invalidate()
		return x.attemptOnce(ctx, attempt, ready)
	}

	oc := x.classify(resp.StatusCode, body)
	x.logAttempt(resp.Status, resp.StatusCode, attempt, classifyLabel(oc))

	switch oc {
	case outcomeSuccess:
		return result{status: resp.StatusCode, body: body}, nil
	case outcomeAlreadyDone:
		return result{status: resp.StatusCode, body: body, alreadyDone: true}, nil
	default:
		he := x.httpError(req, resp.StatusCode, capBody(body), attempt)
		he.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), nowFunc())
		return result{}, he
	}
}

// httpError builds the error for one failed attempt against req. The body is
// stored as given: the transport path passes a raw message, the response path
// passes an already-capped one.
func (x *Call) httpError(req *http.Request, status int, body []byte, attempt int) *HTTPError {
	return &HTTPError{
		Name:    x.c.name,
		Method:  x.method,
		URL:     req.URL.String(),
		Status:  status,
		Body:    body,
		Attempt: attempt,
	}
}

// readBody reads the response body. When ready is non-nil (a wait probe) it
// stops at the first chunk the readiness predicate accepts, so a
// server-sent-events stream that stays open after its answer does not hold the
// read open until an EOF that may never come. A plain call (ready == nil) reads
// to EOF exactly as before.
func (x *Call) readBody(resp *http.Response, ready ReadyFunc) ([]byte, error) {
	if ready == nil {
		return io.ReadAll(resp.Body)
	}
	var acc []byte
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			if ready(resp.StatusCode, acc) {
				return acc, nil
			}
		}
		if err == io.EOF {
			return acc, nil
		}
		if err != nil {
			return acc, err
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
	reqCtx, cancel := x.requestContext(ctx)
	defer cancel()

	req, err := x.buildRequest(reqCtx)
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
	policy := x.effectivePolicy()
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
