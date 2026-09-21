// SPDX-License-Identifier: AGPL-3.0-only

package appclient

import (
	"math/rand"
	"time"
)

// logAttempt emits one Debug record per attempt. The client owns the trace so
// app packages never carry inline DBG-style logging.
func (x *Call) logAttempt(statusText string, status int, attempt int, label string) {
	if x.c == nil || x.c.logger == nil {
		return
	}
	x.c.logger.Debug("appclient call",
		"method", x.method,
		"path", x.path,
		"status", status,
		"status_text", statusText,
		"attempt", attempt,
		"outcome", label,
	)
}

// randFloat returns a pseudo-random float in [0,1). Kept as a package-level
// seam so jitter is testable and does not depend on the global source.
var randFloat = func() float64 { return rand.Float64() }

// nowFunc is the clock seam for Retry-After date parsing.
var nowFunc = time.Now
