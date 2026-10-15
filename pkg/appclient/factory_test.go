// SPDX-License-Identifier: AGPL-3.0-only

package appclient

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFactory_ZeroValueUsable(t *testing.T) {
	// A zero Factory must build a working client (no panic).
	c := Factory{}.New(Spec{Name: "x", BaseURL: "http://localhost:1"})
	require.NotNil(t, c)
}

func TestFactory_SharedTransportPointer(t *testing.T) {
	tr := &http.Transport{}
	f := Factory{Transport: tr}
	c1 := f.New(Spec{Name: "a", BaseURL: "http://localhost:1"})
	c2 := f.New(Spec{Name: "b", BaseURL: "http://localhost:2"})
	require.NotNil(t, c1)
	require.NotNil(t, c2)
	// Both clients must use the same transport instance (pooling).
	assert.Same(t, tr, transportOf(t, c1))
	assert.Same(t, tr, transportOf(t, c2))
}

func TestFactory_SpecOverridesTransport(t *testing.T) {
	shared := &http.Transport{}
	own := &http.Transport{}
	f := Factory{Transport: shared}
	c := f.New(Spec{Name: "a", BaseURL: "http://localhost:1", Transport: own})
	assert.Same(t, own, transportOf(t, c), "an explicit Spec transport wins over the factory's")
}

func transportOf(t *testing.T, c *Client) *http.Transport {
	t.Helper()
	return c.Transport()
}
