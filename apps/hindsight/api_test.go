// SPDX-License-Identifier: AGPL-3.0-only

package hindsight

import (
	"testing"

	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
)

// fakeHTTP satisfies configurator.HTTPFactory with a real client, so a gate
// built with it has a probe to exercise. The factory exists so a test can tell
// "no probe available" from "probe available"; it is not a mock of the app.
type fakeHTTP struct{}

func (fakeHTTP) New(spec appclient.Spec) *appclient.Client { return appclient.New(spec) }

// bankListResult is the body the real server returns for the probe's request.
// Captured from ghcr.io/vectorize-io/hindsight:1.0.0-slim through the gate's
// route, with the tenant extension loaded and the published bearer presented.
const bankListResult = `{"banks":[{"bank_id":"main","name":"main",` +
	`"disposition":{"skepticism":2,"literalism":3,"empathy":4},` +
	`"mission":"You are the long-term memory for an always-on personal assistant."}]}`

// TestBankListAcceptsTheRealResponse is the positive case against the shape the
// image actually sends: a JSON object carrying a bank array.
func TestBankListAcceptsTheRealResponse(t *testing.T) {
	require.True(t, bankListOK(200, []byte(bankListResult)))

	require.True(t, bankListOK(200, []byte(`{"banks":[]}`)),
		"a server with no bank yet has answered; the empty list is the answer")
}

// TestBankListRefusesEverythingThatIsNotAnAnswer is the reason the probe reads
// the body instead of the status code. Each of these is "something is listening"
// and none of them is "this app serves its memory API to this credential".
func TestBankListRefusesEverythingThatIsNotAnAnswer(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"locked gate":          {status: 401, body: ""},
		"gate up, server gone": {status: 502, body: "Bad Gateway"},
		"empty body":           {status: 200, body: ""},
		"login page":           {status: 200, body: "<!DOCTYPE html><html lang=\"en\">…</html>"},
		"json error envelope":  {status: 200, body: `{"detail":"Not authenticated"}`},
		"an array, not an object": {
			status: 200,
			body:   `[{"bank_id":"main"}]`,
		},
		"banks is null": {status: 200, body: `{"banks":null}`},
		"truncated body": {
			status: 200,
			body:   `{"banks":[{"bank_id":"main"`,
		},
	}
	for name, tc := range cases {
		require.Falsef(t, bankListOK(tc.status, []byte(tc.body)), name)
	}
}
