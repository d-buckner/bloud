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

// ModelsURL is where the live model catalog lives for this endpoint: the
// OpenAI-compatible GET /models, resolved against the endpoint rather than
// guessed at its origin, because a gateway may serve /models under its own path.
func (e Endpoint) ModelsURL() string {
	base := e.String()
	if strings.HasSuffix(base, "/v1") {
		return base + "/models"
	}
	return e.Origin() + "/v1/models"
}
