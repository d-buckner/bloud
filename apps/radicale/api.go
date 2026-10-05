// SPDX-License-Identifier: AGPL-3.0-only

package radicale

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// probePath is a top-level collection path asked about without credentials.
// Radicale resolves any top-level segment as a user's collection tree, so this
// needs nothing to exist: the answer comes from the auth layer before the
// storage layer is consulted. A real account named `bloud-probe` changes
// nothing, because the challenge happens before ownership is considered.
const probePath = "/bloud-probe/"

// davCallTimeout bounds a single DAV round trip the configurator makes. These
// are container-network calls that either answer or they do not; the long
// budget belongs to the orchestrator's pass, not to one property read.
const davCallTimeout = 15 * time.Second

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

// collectionExists reports whether a collection is present, as a DAV client
// finds out: PROPFIND with Depth 0 on the path. 207 means it is there, 404
// means it is not, and anything else is a real answer the caller has to see
// rather than a silent "no".
func (a *radicaleAPI) collectionExists(ctx context.Context, path, user, password string) (bool, error) {
	status := 0
	_, err := a.cl.Method("PROPFIND", path).
		Anonymous().
		Header("Authorization", basicAuthHeader(user, password)).
		Header("Depth", "0").
		Timeout(davCallTimeout).
		AlreadyDoneFunc(func(s int, _ []byte) bool {
			status = s
			return true
		}).
		Do(ctx)
	switch status {
	case http.StatusMultiStatus:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	case 0:
		return false, fmt.Errorf("propfind %s: %w", path, err)
	default:
		return false, fmt.Errorf("propfind %s: unexpected status %d", path, status)
	}
}

// createCalendar issues MKCALENDAR as the given principal. The body carries
// only the display name; Radicale supplies the rest, and an empty property set
// is accepted, so this cannot fail on a body shape the server dislikes.
func (a *radicaleAPI) createCalendar(ctx context.Context, path, user, password, displayName string) error {
	body := fmt.Sprintf(
		`<?xml version="1.0" encoding="utf-8" ?>`+
			`<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">`+
			`<D:set><D:prop><D:displayname>%s</D:displayname></D:prop></D:set>`+
			`</C:mkcalendar>`, xmlEscape(displayName))
	return a.cl.Method("MKCALENDAR", path).
		Anonymous().
		Header("Authorization", basicAuthHeader(user, password)).
		Body([]byte(body), "application/xml").
		Timeout(davCallTimeout).
		OK(http.StatusCreated).
		AlreadyDone(http.StatusConflict).
		Exec(ctx)
}

// basicAuthHeader builds the Authorization value for a DAV call made with an
// account's own Bloud password. The client's token machinery does not apply
// here: Radicale authenticates every principal with the Basic credential its
// client sends, verified against the identity provider over LDAP, and the
// configurator is acting as a client of that account rather than as Bloud.
func basicAuthHeader(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}

// xmlEscape covers the five predefined XML entities. The display names that go
// through here are Bloud's own constants rather than user input, but a
// collection name is a place to be strict about, not to assume.
func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
