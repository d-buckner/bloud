// SPDX-License-Identifier: AGPL-3.0-only

package appclient

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Process-shared transport, built once and reused by every client that does not
// supply its own. Connection pooling across reconciliation cycles is the whole
// point: one dial timeout, one keep-alive policy for the whole runtime.
var (
	sharedTransportOnce sync.Once
	sharedTransport     *http.Transport
)

// DefaultTransport returns the process-shared *http.Transport used when a Spec
// does not set one. It is safe for concurrent use.
func DefaultTransport() *http.Transport {
	sharedTransportOnce.Do(func() {
		sharedTransport = newSharedTransport()
	})
	return sharedTransport
}

// newSharedTransport builds the shared transport. Split out so tests can build
// an independent instance without touching the process singleton.
func newSharedTransport() *http.Transport {
	return &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
}
