// SPDX-License-Identifier: AGPL-3.0-only

package opengym

import (
	"context"
	"encoding/json"
	"time"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// healthPath is openGym's own health route, served by the API and proxied by the web
// container under the same prefix a browser uses.
const healthPath = "/api/health"

// webAPI asks the app the way a visitor's browser does: through the published port,
// through nginx, and on to the API container.
type webAPI struct {
	cl *appclient.Client
}

// newWebAPI builds the probe client against the address the app publishes.
func newWebAPI(f configurator.HTTPFactory, baseURLFn func() string) *webAPI {
	return &webAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// waitServing polls the published health path until the API answers its own health
// document, or the budget runs out.
//
// The budget is short because both containers have already passed their own health
// checks by the time this runs. What is left to wait for is a front end that has not
// yet resolved the API name, not a cold start: nginx re-resolves on a 10s timer, so
// one interval plus a cold container is what this has to cover.
func (a *webAPI) waitServing(ctx context.Context) error {
	return a.cl.GET(healthPath).
		Wait(healthIsOK).
		Interval(2 * time.Second).
		Within(90 * time.Second).
		Do(ctx)
}

// healthIsOK reports whether the body is openGym's own health answer, and a healthy
// one.
//
// A status code cannot answer this. The web container answers 200 for the static app
// whatever the API does, and a catch-all that served index.html in place of the API
// would answer 200 with HTML: upstream's own troubleshooting names that exact
// signature. Reading the document separates "the API answered through the proxy" from
// "something on this port is up", and `writable` is the half a TCP probe never sees,
// because a data directory the app cannot write is a server that answers and saves
// nothing.
func healthIsOK(_ int, body []byte) bool {
	var health struct {
		OK       *bool `json:"ok"`
		Writable *bool `json:"writable"`
	}
	if err := json.Unmarshal(body, &health); err != nil {
		return false
	}
	return health.OK != nil && *health.OK && health.Writable != nil && *health.Writable
}
