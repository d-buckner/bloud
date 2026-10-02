// SPDX-License-Identifier: AGPL-3.0-only

package calino

import (
	"bytes"
	"context"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// indexPath is the SPA shell. Calino makes no server-side calls of its own and
// owns no API, so the shell is the only surface this app can be checked on:
// if Caddy is serving it, the bundle is published and reachable.
const indexPath = "/"

// bundleProbe is what has to appear in the served shell for it to be Calino's.
// `id="root"` is where the React app mounts. A Caddy error page, a wrong
// document root, or a half-copied bundle all fail it, and every one of those
// looks like a healthy container from the outside: the process is up, the port
// answers, and the thing being served is not the app.
const bundleProbe = `id="root"`

// calinoAPI is the typed surface over the running container's HTTP interface.
// Transport, retry, and timeouts live in appclient.
type calinoAPI struct {
	cl *appclient.Client
}

func newAPI(f configurator.ClientFactory, baseURLFn func() string) *calinoAPI {
	return &calinoAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// probeShell fetches the SPA shell and reports the status the server chose and
// whether the body carries the app's mount point.
//
// Every response is a satisfied outcome rather than a failure: the answer this
// probe exists to produce is the status and the body content, and a 404 is
// information about the app, not a transport fault. The caller classifies.
func (a *calinoAPI) probeShell(ctx context.Context) (int, bool, error) {
	status := 0
	mounted := false
	_, err := a.cl.GET(indexPath).
		AlreadyDoneFunc(func(s int, body []byte) bool {
			status = s
			mounted = bytes.Contains(body, []byte(bundleProbe))
			return true
		}).
		Do(ctx)
	if status == 0 {
		return 0, false, err
	}
	return status, mounted, nil
}
