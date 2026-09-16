// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Call is one declared HTTP request. Build it with a verb method on Client,
// add modifiers, then terminate with Do / DoInto / Ensure / Wait. A Call is a
// value builder; it is not executed until a terminal method is called.
type Call struct {
	c      *Client
	method string
	path   string

	body        []byte
	contentType string
	query       url.Values
	headers     map[string]string

	timeoutOverride time.Duration
	anonymous       bool
	buildErr        error
	tokenRetried    bool

	// Outcome contract (where idempotency is declared).
	okStatuses       []int
	alreadyStatuses  []int
	alreadyFunc      func(status int, body []byte) bool
	extraRetryStatus []int
	noRetry          bool
	declaredContract bool

	// Wait mode.
	ready            ReadyFunc
	interval         time.Duration
	stable           int
	tolerateFailures bool

	// retryOverride replaces the client's retry policy for this call only.
	// Used by waits that need a different attempt cap than the client
	// default (e.g. a short bounded check vs a long readiness poll).
	retryOverride *RetryPolicy
}

// GET starts a GET call against path.
func (c *Client) GET(path string) *Call { return &Call{c: c, method: http.MethodGet, path: path} }

// POST starts a POST call against path.
func (c *Client) POST(path string) *Call { return &Call{c: c, method: http.MethodPost, path: path} }

// PUT starts a PUT call against path.
func (c *Client) PUT(path string) *Call { return &Call{c: c, method: http.MethodPut, path: path} }

// PATCH starts a PATCH call against path.
func (c *Client) PATCH(path string) *Call { return &Call{c: c, method: http.MethodPatch, path: path} }

// DELETE starts a DELETE call against path.
func (c *Client) DELETE(path string) *Call { return &Call{c: c, method: http.MethodDelete, path: path} }

// --- request body / modifiers ---

// JSON sets a JSON request body and Content-Type: application/json.
func (x *Call) JSON(v any) *Call {
	b, err := json.Marshal(v)
	if err != nil {
		x.buildErr = fmt.Errorf("marshal JSON body: %w", err)
		return x
	}
	x.body = b
	x.contentType = "application/json"
	return x
}

// Form sets an x-www-form-urlencoded body.
func (x *Call) Form(v url.Values) *Call {
	x.body = []byte(v.Encode())
	x.contentType = "application/x-www-form-urlencoded"
	return x
}

// Body sets a raw body with an explicit content type.
func (x *Call) Body(raw []byte, contentType string) *Call {
	x.body = raw
	x.contentType = contentType
	return x
}

// Query adds a query parameter.
func (x *Call) Query(k, v string) *Call {
	if x.query == nil {
		x.query = url.Values{}
	}
	x.query.Add(k, v)
	return x
}

// Header adds a request header.
func (x *Call) Header(k, v string) *Call {
	if x.headers == nil {
		x.headers = map[string]string{}
	}
	x.headers[k] = v
	return x
}

// Timeout overrides the client's per-request timeout for this call.
func (x *Call) Timeout(d time.Duration) *Call {
	x.timeoutOverride = d
	return x
}

// Anonymous skips token/auth application for this call (e.g. the login call).
func (x *Call) Anonymous() *Call {
	x.anonymous = true
	return x
}

// --- outcome contract ---

// OK declares the exact accepted success codes.
func (x *Call) OK(status ...int) *Call {
	x.okStatuses = append(x.okStatuses, status...)
	x.declaredContract = true
	return x
}

// AlreadyDone declares status codes that mean "success, but nothing changed".
func (x *Call) AlreadyDone(status ...int) *Call {
	x.alreadyStatuses = append(x.alreadyStatuses, status...)
	x.declaredContract = true
	return x
}

// AlreadyDoneFunc declares a predicate for the already-done case (for APIs
// that give no distinct status code and must be matched on the body).
func (x *Call) AlreadyDoneFunc(p func(status int, body []byte) bool) *Call {
	x.alreadyFunc = p
	x.declaredContract = true
	return x
}

// RetryStatus adds statuses to the transient (retried) set for this call.
func (x *Call) RetryStatus(status ...int) *Call {
	x.extraRetryStatus = append(x.extraRetryStatus, status...)
	return x
}

// NoRetry disables retry for this call.
func (x *Call) NoRetry() *Call {
	x.noRetry = true
	return x
}

// WithRetry overrides the retry policy for this call. In wait mode it bounds
// the total poll attempts / deadline; for a plain call it bounds the
// transient retries. Lets one client serve both a short bounded check and a
// long readiness poll.
func (x *Call) WithRetry(p RetryPolicy) *Call {
	cp := p
	x.retryOverride = &cp
	return x
}

// --- terminal operations ---

// Exec executes the call for its side effect only, discarding the response
// body. Used by void verbs (POST/PUT/DELETE) where the caller cares only about
// success/error, not the returned bytes.
func (x *Call) Exec(ctx context.Context) error {
	_, err := x.run(ctx)
	return err
}

// Do executes the call and returns the response body on success.
func (x *Call) Do(ctx context.Context) ([]byte, error) {
	res, err := x.run(ctx)
	if err != nil {
		return nil, err
	}
	return res.body, nil
}

// DoInto executes the call and JSON-decodes the body into out.
func (x *Call) DoInto(ctx context.Context, out any) error {
	res, err := x.run(ctx)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(res.body, out); err != nil {
		return fmt.Errorf("%s: %s %s → decode JSON: %w (body: %s)",
			x.c.name, x.method, x.path, err, truncate(res.body, 200))
	}
	return nil
}

// Ensure executes the call and reports whether it changed anything.
// changed = the call succeeded and was NOT an AlreadyDone outcome.
func (x *Call) Ensure(ctx context.Context) (bool, error) {
	res, err := x.run(ctx)
	if err != nil {
		return false, err
	}
	return !res.alreadyDone, nil
}

// Stream executes the call and, on a successful (2xx) outcome, hands the
// response body to consume for streaming (download-to-disk, hashing while
// reading). consume is called at most once per attempt with a fresh reader;
// the same retry policy applies. Body-based outcome predicates are not
// evaluated in stream mode (classification is status-only), which is correct
// for downloads.
func (x *Call) Stream(ctx context.Context, consume func(body io.Reader) error) error {
	return x.runStream(ctx, consume)
}

// result is the outcome of a successful (non-error) execution.
type result struct {
	status      int
	body        []byte
	alreadyDone bool
}

// retriesAllowed implements the verb retry policy (D3): GET/HEAD retry by
// default; a mutating verb retries only when it declares an outcome contract
// or is a wait.
func (x *Call) retriesAllowed() bool {
	if x.noRetry {
		return false
	}
	if x.ready != nil {
		return true
	}
	switch x.method {
	case http.MethodGet, http.MethodHead:
		return true
	default:
		return x.declaredContract
	}
}

// classify maps a response status/body to an outcome.
func (x *Call) classify(status int, body []byte) outcome {
	if x.isAlreadyDone(status, body) {
		return outcomeAlreadyDone
	}
	if x.isSuccess(status) {
		return outcomeSuccess
	}
	if x.isTransient(status) {
		return outcomeTransient
	}
	return outcomeFatal
}

func (x *Call) isSuccess(status int) bool {
	if len(x.okStatuses) > 0 {
		return intIn(status, x.okStatuses)
	}
	return status >= 200 && status < 300
}

func (x *Call) isAlreadyDone(status int, body []byte) bool {
	if intIn(status, x.alreadyStatuses) {
		return true
	}
	if x.alreadyFunc != nil && x.alreadyFunc(status, body) {
		return true
	}
	return false
}

func (x *Call) isTransient(status int) bool {
	if intIn(status, x.extraRetryStatus) {
		return true
	}
	return isTransientStatus(status)
}

func intIn(v int, set []int) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// buildRequest constructs the *http.Request for one attempt.
func (x *Call) buildRequest(ctx context.Context) (*http.Request, error) {
	if x.buildErr != nil {
		return nil, x.buildErr
	}
	full := x.fullURL()
	var bodyReader io.Reader
	if x.body != nil {
		bodyReader = bytes.NewReader(x.body)
	}
	req, err := http.NewRequestWithContext(ctx, x.method, full, bodyReader)
	if err != nil {
		return nil, err
	}
	if x.contentType != "" {
		req.Header.Set("Content-Type", x.contentType)
	}
	for k, v := range x.c.spec.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range x.headers {
		req.Header.Set(k, v)
	}
	if !x.anonymous {
		if err := x.applyAuth(req); err != nil {
			return nil, err
		}
	}
	return req, nil
}

// fullURL joins the resolved base URL with the path and query.
func (x *Call) fullURL() string {
	base := x.c.resolveBaseURL()
	full := base + x.path
	if len(x.query) > 0 {
		if containsByte(full, '?') {
			full += "&" + x.query.Encode()
		} else {
			full += "?" + x.query.Encode()
		}
	}
	return full
}

func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}

// applyAuth attaches credentials to the request per the client's auth config.
func (x *Call) applyAuth(req *http.Request) error {
	if x.c.spec.StaticAuth != nil {
		x.c.spec.StaticAuth(req)
	}
	if x.c.spec.Tokens != nil {
		return x.c.spec.Tokens.apply(req)
	}
	return nil
}

// effectivePolicy returns the per-call override when set, else the client's
// retry policy.
func (x *Call) effectivePolicy() RetryPolicy {
	if x.retryOverride != nil {
		// Defaults must be applied here too: a declared policy that omits
		// Factor (e.g. jellyfin's fixed-cadence wait policies) would otherwise
		// multiply its interval by zero from the second attempt on, collapsing
		// a 60 s wait into a ~1 s spin.
		return x.retryOverride.withDefaults()
	}
	return x.c.retry
}

// run executes the call with the retry policy and returns the result.
func (x *Call) run(ctx context.Context) (result, error) {
	if x.buildErr != nil {
		return result{}, x.buildErr
	}
	policy := x.effectivePolicy()
	start := time.Now()
	attempt := 0
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return result{}, fmt.Errorf("%s: %w (last: %v)", x.c.name, err, lastErr)
			}
			return result{}, err
		}
		attempt++
		res, err := x.attemptOnce(ctx, attempt)
		if err == nil {
			return res, nil
		}
		lastErr = err
		// A context error mid-attempt is terminal, not transient.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return result{}, err
		}
		// Only transient failures retry; a non-transient status fails fast
		// regardless of the verb policy.
		if !isTransientErr(err) || !x.retriesAllowed() {
			return result{}, err
		}
		if policy.MaxAttempts > 0 && attempt >= policy.MaxAttempts {
			return result{}, fmt.Errorf("%s: exhausted %d attempts: %w", x.c.name, attempt, err)
		}
		if policy.Deadline > 0 && time.Since(start) >= policy.Deadline {
			return result{}, fmt.Errorf("%s: retry deadline exceeded after %d attempts: %w", x.c.name, attempt, err)
		}
		x.c.sleeper(x.nextDelay(policy, attempt, err))
	}
}

// nextDelay computes the backoff for the next attempt, honoring Retry-After
// when the error carries one.
func (x *Call) nextDelay(policy RetryPolicy, attempt int, err error) time.Duration {
	if ra := retryAfterOf(err); ra > 0 {
		return clampRetryAfter(ra, policy.MaxInterval)
	}
	d := float64(policy.Initial)
	for i := 1; i < attempt; i++ {
		d *= policy.Factor
		if d > float64(policy.MaxInterval) {
			d = float64(policy.MaxInterval)
			break
		}
	}
	if policy.Jitter > 0 {
		d = jitterDuration(d, policy.Jitter)
	}
	if d < 0 {
		d = 0
	}
	return time.Duration(d)
}

// jitterDuration applies ±jitter (a fraction of d) as pseudo-randomness so
// concurrent clients do not stampede.
func jitterDuration(d, jitter float64) float64 {
	r := (randFloat()*2 - 1) * jitter * d
	return d + r
}