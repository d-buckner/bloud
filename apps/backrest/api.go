// SPDX-License-Identifier: AGPL-3.0-only

package backrest

import (
	"context"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// getConfigPath is Backrest's Connect RPC for reading its config. Connect serves
// a unary call as a POST with a JSON body, and the empty object is the request
// message (GetConfig takes google.protobuf.Empty).
const getConfigPath = "/v1.Backrest/GetConfig"

// configResponse is the slice of Backrest's config this app reads back: the
// identity and the ids of the repositories and plans it wrote. Decoding only
// these keeps the client independent of the parts of the config schema it does
// not use.
type configResponse struct {
	Instance string `json:"instance"`
	Repos    []struct {
		ID string `json:"id"`
	} `json:"repos"`
	Plans []struct {
		ID string `json:"id"`
	} `json:"plans"`
}

// backrestAPI is the typed surface over Backrest's HTTP API. Transport, retry,
// and timeouts live in appclient.
type backrestAPI struct {
	cl *appclient.Client
}

func newAPI(factory configurator.ClientFactory, baseURL func() string) *backrestAPI {
	return &backrestAPI{cl: factory.New(appclient.Spec{Name: appName, BaseURLFn: baseURL})}
}

// getConfig reads the running config. With Backrest's own login disabled the
// call needs no credential; with it enabled (the operator's choice) the call
// answers 401, which the caller treats as a valid state rather than a failure.
func (a *backrestAPI) getConfig(ctx context.Context) (configResponse, error) {
	var out configResponse
	err := a.cl.POST(getConfigPath).
		JSON(struct{}{}).
		OK(http.StatusOK).
		DoInto(ctx, &out)
	return out, err
}
