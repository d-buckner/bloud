// SPDX-License-Identifier: AGPL-3.0-only

// Package timezone holds the instance clock setting: the IANA zone name the
// operator picks in Settings, and the live value the reconciler reads when it
// builds a container spec.
//
// The zone reaches a container as the TZ environment variable, which is what
// the C library and most language runtimes read. Bloud states no zone until the
// operator sets one, so every image keeps its own default until then.
package timezone

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	// The embedded zone database is the fallback for a machine that runs
	// host-agent without /usr/share/zoneinfo. Without it a real zone name would
	// be rejected on a slim host, even though the container images that need it
	// each carry the zone themselves.
	_ "time/tzdata"
)

// EnvVar names the environment variable a container reads to pick its zone.
const EnvVar = "TZ"

// maxNameLen bounds a zone name. The longest IANA name is well under it, so a
// longer string is a typo or an attack rather than a zone.
const maxNameLen = 64

// localZone is the one name time.LoadLocation answers that names no zone. It
// means "this machine's own clock", which is not a choice the operator made.
const localZone = "Local"

// ErrEmpty reports that no zone was given. The caller decides whether that is a
// rejection or a request to clear the setting, so the package does not guess.
var ErrEmpty = errors.New("no timezone given")

// Parse validates an IANA zone name and returns it with surrounding space
// removed.
//
// It is the single gate on what may become the instance clock, in the same way
// hostset.ParsePublicURL is the gate on the address. The API calls it so a
// mistake is a 400 that names the problem, and the orchestrator calls it again
// because the intent queue is reachable from outside the API.
func Parse(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", ErrEmpty
	}
	if len(trimmed) > maxNameLen {
		return "", fmt.Errorf("timezone must be %d characters or fewer", maxNameLen)
	}
	// The shape is checked before the lookup. time.LoadLocation resolves a name
	// against the host's zone directory when one exists, so a name that carries
	// path syntax must never reach it.
	if !validName(trimmed) {
		return "", fmt.Errorf("%q is not a timezone name", name)
	}
	if trimmed == localZone {
		return "", fmt.Errorf("%q names the container's own clock, not a timezone", localZone)
	}
	if _, err := time.LoadLocation(trimmed); err != nil {
		return "", fmt.Errorf("unknown timezone %q", name)
	}
	return trimmed, nil
}

// validName reports whether name uses only the characters an IANA zone name
// uses: letters, digits, and the three marks in names such as Etc/GMT+2 and
// America/Port-au-Prince. Every separator must be a slash, and no segment may be
// empty, which is what rejects an absolute path, a trailing slash, and "..".
func validName(name string) bool {
	for _, segment := range strings.Split(name, "/") {
		if segment == "" {
			return false
		}
		for _, r := range segment {
			switch {
			case r >= 'a' && r <= 'z':
			case r >= 'A' && r <= 'Z':
			case r >= '0' && r <= '9':
			case r == '_' || r == '+' || r == '-':
			default:
				return false
			}
		}
	}
	return true
}

// State holds the live zone a convergence pass reads. The settings store holds
// the durable copy; this is the copy the reconciler reads on every container it
// builds, so a spec build never needs a database query.
//
// A nil *State is a valid empty state, which is what lets a hand-built
// orchestrator run without a clock setting at all.
type State struct {
	mu   sync.RWMutex
	zone string
}

// NewState returns a State that holds zone. An empty zone means Bloud states
// nothing and every image keeps its own default.
func NewState(zone string) *State {
	return &State{zone: zone}
}

// Get returns the current zone, or "" when none is set.
func (s *State) Get() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.zone
}

// Set replaces the zone. An empty zone clears it.
func (s *State) Set(zone string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.zone = zone
}
