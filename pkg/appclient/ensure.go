// SPDX-License-Identifier: AGPL-3.0-only

package appclient

import (
	"context"
	"encoding/json"
	"fmt"
)

// EnsureSpec describes a compare-and-apply (get → diff → set) unit.
type EnsureSpec[T any] struct {
	// Name identifies the unit in logs.
	Name string
	// Current reads the live value.
	Current func(ctx context.Context) (T, error)
	// Desired is the target value.
	Desired T
	// Equal compares current to desired. nil → canonical-JSON equality.
	Equal func(cur, want T) bool
	// Apply writes the desired value.
	Apply func(ctx context.Context, want T) error
}

// Ensure reads current, compares to desired, and applies only when different.
// It returns changed=true only when Apply ran. This is the dominant write
// pattern across configurators (jellyfin LDAP config, the authentik Ensure*
// methods, navidrome user sync) and gives one place to log "already converged"
// vs "applied".
func Ensure[T any](ctx context.Context, s EnsureSpec[T]) (bool, error) {
	cur, err := s.Current(ctx)
	if err != nil {
		return false, fmt.Errorf("ensure %s: read current: %w", s.Name, err)
	}
	equal := s.Equal
	if equal == nil {
		equal = jsonEqual[T]
	}
	if equal(cur, s.Desired) {
		return false, nil
	}
	if err := s.Apply(ctx, s.Desired); err != nil {
		return false, fmt.Errorf("ensure %s: apply: %w", s.Name, err)
	}
	return true, nil
}

// jsonEqual compares two values by canonical JSON. Used as the default Equal.
func jsonEqual[T any](a, b T) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ja) == string(jb)
}
