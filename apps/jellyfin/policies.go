// SPDX-License-Identifier: AGPL-3.0-only

package jellyfin

import (
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// Jellyfin's first-run behaviour needs specific poll shapes; each is declared
// once here rather than hand-rolled per loop. Jitter is off so the cadence
// matches the previous fixed sleeps (keeps cold-start timing predictable).
const (
	pollInterval = 2 * time.Second
)

// systemInfoWaitPolicy bounds the "wait for the API to answer" probe.
var systemInfoWaitPolicy = appclient.RetryPolicy{
	MaxAttempts: 10, Initial: pollInterval, MaxInterval: pollInterval,
}

// wizardCheckPolicy bounds the short wizard-completion re-check. Persistent
// 503s fall through after this cap (the caller treats it as non-fatal).
var wizardCheckPolicy = appclient.RetryPolicy{
	MaxAttempts: 5, Initial: pollInterval, MaxInterval: pollInterval,
}

// wizardReadyPolicy bounds the wait for the wizard endpoint itself (~60s).
var wizardReadyPolicy = appclient.RetryPolicy{
	MaxAttempts: 60, Initial: time.Second, MaxInterval: time.Second,
}

// startupUserPolicy bounds the wait for the async initial user to appear.
var startupUserPolicy = appclient.RetryPolicy{
	MaxAttempts: 10, Initial: 500 * time.Millisecond, MaxInterval: 500 * time.Millisecond,
}
