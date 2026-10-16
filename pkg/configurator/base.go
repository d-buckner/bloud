// SPDX-License-Identifier: AGPL-3.0-only

package configurator

import (
	"fmt"
	"log/slog"
)

// Base carries the mechanical half every configurator repeats: the resolved
// port, the node name, and the base-URL test seam. Embed it, drop the matching
// port and baseURL fields and the Name and Port methods, and keep the
// app-specific fields and API clients.
type Base struct {
	port    int
	node    string
	baseURL string
}

// NewBase resolves port against defaultPort and records the node name.
func NewBase(port, defaultPort int, node string) Base {
	if port == 0 {
		port = defaultPort
	}
	return Base{port: port, node: node}
}

// Name returns the graph node this configurator owns.
func (b *Base) Name() string { return b.node }

// Port returns the app port this configurator was constructed with.
func (b *Base) Port() int { return b.port }

// SetBaseURL points API clients at a fixed address (test seam).
func (b *Base) SetBaseURL(u string) { b.baseURL = u }

// BaseURL returns the localhost base URL, honoring the test seam.
func (b *Base) BaseURL() string {
	if b.baseURL != "" {
		return b.baseURL
	}
	return fmt.Sprintf("http://localhost:%d", b.port)
}

// BaseURLFn returns the base URL as a function, for clients that resolve it
// lazily on every call.
func (b *Base) BaseURLFn() func() string {
	return func() string { return b.BaseURL() }
}

// AppLogger returns logger stamped with the app name, defaulting a nil logger.
func AppLogger(logger *slog.Logger, app string) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	return logger.With("app", app)
}
