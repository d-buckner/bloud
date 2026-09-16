// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package configurator

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

func TestClientFactory_ZeroValueUsable(t *testing.T) {
	// A zero ClientFactory must build a working client (no panic).
	c := ClientFactory{}.New(appclient.Spec{Name: "x", BaseURL: "http://localhost:1"})
	require.NotNil(t, c)
}

func TestClientFactory_SharedTransportPointer(t *testing.T) {
	tr := &http.Transport{}
	f := ClientFactory{Transport: tr}
	c1 := f.New(appclient.Spec{Name: "a", BaseURL: "http://localhost:1"})
	c2 := f.New(appclient.Spec{Name: "b", BaseURL: "http://localhost:2"})
	require.NotNil(t, c1)
	require.NotNil(t, c2)
	// Both clients must use the same transport instance (pooling).
	assert.Same(t, tr, transportOf(t, c1))
	assert.Same(t, tr, transportOf(t, c2))
}

func TestClientFactory_SpecOverridesTransport(t *testing.T) {
	shared := &http.Transport{}
	own := &http.Transport{}
	f := ClientFactory{Transport: shared}
	c := f.New(appclient.Spec{Name: "a", BaseURL: "http://localhost:1", Transport: own})
	assert.Same(t, own, transportOf(t, c), "an explicit Spec transport wins over the factory's")
}

// transportOf returns the client's transport.
func transportOf(t *testing.T, c *appclient.Client) *http.Transport {
	t.Helper()
	return c.Transport()
}