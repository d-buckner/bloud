// SPDX-License-Identifier: AGPL-3.0-only

package radicale

import (
	"context"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// probePath is a top-level collection path asked about without credentials.
// Radicale resolves any top-level segment as a user's collection tree, so this
// needs nothing to exist: the answer comes from the auth layer before the
// storage layer is consulted. A real account named `bloud-probe` changes
// nothing, because the challenge happens before ownership is considered.
const probePath = "/bloud-probe/"

// radicaleAPI is the typed surface over the running server's HTTP interface.
// Transport, retry, and timeouts live in appclient.
type radicaleAPI struct {
	cl *appclient.Client
}

func newAPI(f configurator.ClientFactory, baseURLFn func() string) *radicaleAPI {
	return &radicaleAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// probeUnauthenticated issues an unauthenticated GET on a collection path and
// reports the status the server chose.
//
// Every response is declared a satisfied outcome, because the probe's answer is
// the status code itself rather than whether the request succeeded: a 401 is a
// good answer and a 200 is a bad one, and neither is a transport failure. The
// caller classifies; this only reports.
func (a *radicaleAPI) probeUnauthenticated(ctx context.Context) (int, error) {
	status := 0
	_, err := a.cl.GET(probePath).
		AlreadyDoneFunc(func(s int, _ []byte) bool {
			status = s
			return true
		}).
		Do(ctx)
	if status == 0 {
		return 0, err
	}
	return status, nil
}
