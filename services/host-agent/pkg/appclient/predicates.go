// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appclient

import (
	"bytes"
	"encoding/json"
)

// StatusIn returns a ReadyFunc true when the status is one of codes.
func StatusIn(codes ...int) ReadyFunc {
	return func(status int, _ []byte) bool { return intIn(status, codes) }
}

// StatusIs returns a ReadyFunc true when the status equals code.
func StatusIs(code int) ReadyFunc {
	return func(status int, _ []byte) bool { return status == code }
}

// StatusNot returns a ReadyFunc true when the status is not code.
func StatusNot(code int) ReadyFunc {
	return func(status int, _ []byte) bool { return status != code }
}

// StatusLT returns a ReadyFunc true when the status is below code: "anything
// under 500 means the listener is up".
func StatusLT(code int) ReadyFunc {
	return func(status int, _ []byte) bool { return status < code }
}

// DecodeInto returns a ReadyFunc that unmarshals the body into out and then
// evaluates cond. A decode error means "not ready".
func DecodeInto(out any, cond func() bool) ReadyFunc {
	return func(_ int, body []byte) bool {
		if len(body) == 0 {
			return false
		}
		if err := json.Unmarshal(body, out); err != nil {
			return false
		}
		return cond()
	}
}

// JSONHas returns a ReadyFunc true when the body is a JSON object containing
// key (e.g. a discovery document with a "url" field proving a provider is
// live).
func JSONHas(key string) ReadyFunc {
	return func(_ int, body []byte) bool {
		if len(body) == 0 {
			return false
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			return false
		}
		_, ok := m[key]
		return ok
	}
}

// JSONValid returns a ReadyFunc true when the body is valid JSON. For example, a
// JSON endpoint that answers HTML while an app is still booting is "not ready".
func JSONValid(_ int, body []byte) bool { return json.Valid(bytes.TrimSpace(body)) }
