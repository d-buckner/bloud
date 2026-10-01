// SPDX-License-Identifier: AGPL-3.0-only

// Package inference holds the value types behind the modelSource and inference
// contracts: the parsed upstream endpoint, the stored settings shape, and the
// validation that keeps a half-understood URL from becoming a binding an app
// cannot dial.
//
// It is deliberately free of store and orchestrator imports so the settings API,
// the resolver, and a configurator can all depend on it without pulling each
// other in.
package inference

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Endpoint is a parsed OpenAI-compatible base URL.
//
// Unlike the instance's public URL, an inference endpoint carries a path: `/v1`
// is part of what makes an endpoint OpenAI-compatible, and the operator types it.
// Rejecting the path would force Bloud to guess, and guessing wrong produces a 404
// that is expensive to diagnose.
type Endpoint struct {
	// Scheme is "http" or "https".
	Scheme string
	// Host is the lowercase hostname or IP literal.
	Host string
	// Port is the explicit port, or 0 when the scheme's default applies.
	Port int
	// Path is the normalized path with no trailing slash. Empty means the
	// endpoint is served at the origin root.
	Path string
	// SchemeAssumed records that the input carried no scheme and http was
	// supplied. Surfaced so the UI can show what was assumed rather than
	// silently rewriting the operator's input.
	SchemeAssumed bool
}

// ParseEndpoint parses and validates an OpenAI-compatible base URL.
//
// It rejects a query, a fragment, credentials, a scheme other than http(s), and
// an out-of-range port, because a partially understood URL registers a value
// that never matches what a client sends. A path is allowed and preserved.
func ParseEndpoint(raw string) (Endpoint, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Endpoint{}, fmt.Errorf("endpoint is empty")
	}

	assumed := false
	if !strings.Contains(s, "://") {
		s = "http://" + s
		assumed = true
	}

	u, err := url.Parse(s)
	if err != nil {
		return Endpoint{}, fmt.Errorf("not a valid URL: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return Endpoint{}, fmt.Errorf("scheme %q is not http or https", u.Scheme)
	}
	if u.User != nil {
		return Endpoint{}, fmt.Errorf("an inference endpoint cannot carry credentials")
	}
	if u.RawQuery != "" {
		return Endpoint{}, fmt.Errorf("an inference endpoint cannot carry a query string")
	}
	if u.Fragment != "" {
		return Endpoint{}, fmt.Errorf("an inference endpoint cannot carry a fragment")
	}

	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return Endpoint{}, fmt.Errorf("host %q is not a valid hostname", u.Hostname())
	}

	port := 0
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Endpoint{}, fmt.Errorf("port %q is out of range", p)
		}
		port = n
	}

	return Endpoint{
		Scheme:        scheme,
		Host:          host,
		Port:          port,
		Path:          normalizePath(u.Path),
		SchemeAssumed: assumed,
	}, nil
}

// normalizePath strips a trailing slash and collapses duplicate separators, so
// "http://h/v1/", "http://h/v1" and "http://h//v1" all canonicalize alike and a
// save that changed nothing is caught by the no-op guard.
func normalizePath(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// String renders the canonical endpoint URL.
func (e Endpoint) String() string {
	out := e.Scheme + "://" + e.Host
	if e.Port != 0 {
		out += ":" + strconv.Itoa(e.Port)
	}
	return out + e.Path
}

// Origin renders the endpoint without its path, for display.
func (e Endpoint) Origin() string {
	out := e.Scheme + "://" + e.Host
	if e.Port != 0 {
		out += ":" + strconv.Itoa(e.Port)
	}
	return out
}

// ModelsURL is where the live model catalog lives for this endpoint: GET
// /models resolved against the endpoint exactly as the operator entered it.
//
// It deliberately does not invent an API prefix. The OpenAI-compatible clients
// downstream append /chat/completions to this same base, so a reachability
// check has to test the URL they will actually dial. Falling back to
// Origin()+"/v1/models" made a path-less endpoint pass the settings check
// while every real call from the app 404'd: the check reported on a URL
// nothing would ever use, which is worse than no check at all because it
// reads as a green light.
func (e Endpoint) ModelsURL() string {
	return e.String() + "/models"
}

// MissingAPIPrefixHint names the most common reason a probe fails against an
// endpoint entered without a path: the OpenAI wire is not served at the origin
// root, it is served under a prefix (almost always /v1), and the client
// appends /chat/completions to whatever base it was given. Empty when the
// endpoint already carries a path, so a caller never appends advice that does
// not apply to the input.
func (e Endpoint) MissingAPIPrefixHint() string {
	if e.Path != "" {
		return ""
	}
	return " (this endpoint has no API base path; OpenAI-compatible clients append " +
		"/chat/completions to the URL as entered, so it needs the prefix the API " +
		"is served under, usually " + e.Origin() + "/v1)"
}
