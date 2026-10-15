// SPDX-License-Identifier: AGPL-3.0-only

package penpot

import (
	"context"
	"net/http"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// configJSPath is the file the frontend image renders from its environment at
// container start: penpotFlags, the public URI, and the SSO provider's label.
//
// It is the readiness probe *and* the verification, which is why it is a static
// asset of the frontend rather than an API call: a 200 for it proves nginx is
// up, and its contents prove the generated env file reached the container and
// the image's own entrypoint read it. A 200 from the SPA shell would prove
// neither, because the shell is baked into the image.
const configJSPath = "/js/config.js"

// bootProbePath is the RPC the SPA issues first, and the only request that
// reaches every part of the install: nginx, the JVM backend, PostgreSQL, and
// (when enable-admin-console is on, which it is) the admin console.
//
// backend/src/app/rpc/commands/profile.clj declares get-profile with
// ::rpc/auth false, so it answers 200 anonymously, and it ends in
// (with-nitrate-licence profile cfg). If that call to the admin console fails,
// backend/src/app/http/errors.clj turns it into 503 {:type
// :nitrate-unavailable}, the SPA renders "Penpot is temporarily unavailable due
// to a system issue", and there is no login page to sign in from. A node at
// RUNNING with a healthy TCP probe can still be in that state.
const bootProbePath = "/api/main/methods/get-profile"

// The two polls below split the node's PostStart ceiling between them: 2 minutes
// each inside the framework's 5 minute per-phase budget, so the second wait
// cannot be starved by the first. They are literals on purpose:
// apps/configtest evaluates every declared Within() against the phase budget,
// and a named constant it cannot evaluate is a wait that quietly stops being
// checked.

// penpotAPI is the typed surface over the running app, reached from the host at
// the app's published port (the same address Traefik proxies to).
type penpotAPI struct {
	cl *appclient.Client
}

func newAPI(f configurator.HTTPFactory, baseURLFn func() string) *penpotAPI {
	return &penpotAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// waitConfig polls until the frontend serves its rendered config and returns
// the body. The default WaitPolicy (3 minutes) is the bound, inside the
// framework's PostStart ceiling, so the wait expires on its own terms.
func (a *penpotAPI) waitConfig(ctx context.Context) (string, error) {
	var body string
	ready := func(status int, raw []byte) bool {
		if status != http.StatusOK {
			return false
		}
		body = string(raw)
		return true
	}
	err := a.cl.GET(configJSPath).
		Wait(ready).
		Interval(2 * time.Second).
		Within(2 * time.Minute).
		Do(ctx)
	if err != nil {
		return "", err
	}
	return body, nil
}

// waitServing polls the boot-critical RPC until the backend answers it, which
// is the only outside-visible proof that the services Penpot itself depends on
// are up. See bootProbePath.
func (a *penpotAPI) waitServing(ctx context.Context) error {
	return a.cl.GET(bootProbePath).
		Wait(func(status int, _ []byte) bool { return status == http.StatusOK }).
		Interval(3 * time.Second).
		Within(2 * time.Minute).
		Do(ctx)
}

// flagsIn reports the flag tokens the served config declares.
//
// The frontend's entrypoint writes `var penpotFlags = "...";` into config.js
// from PENPOT_FLAGS, so reading them back is a read of what the running app
// told the browser, not of what Bloud wrote on disk.
func flagsIn(configJS string) map[string]bool {
	flags := map[string]bool{}
	for _, line := range strings.Split(configJS, "\n") {
		if !strings.Contains(line, "penpotFlags") {
			continue
		}
		for _, field := range strings.Fields(line) {
			field = strings.Trim(field, `";=`)
			if strings.HasPrefix(field, "enable-") || strings.HasPrefix(field, "disable-") {
				flags[field] = true
			}
		}
	}
	return flags
}
