// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// loginSessionDuration is how long a Bloud login lasts. Upstream authentik
// ships the login stage with seconds=0, which ends the session the moment the
// browser closes; Bloud is a home server you check from the phone in your
// pocket, so a login is meant to outlive a browser restart by months.
const loginSessionDuration = "days=90"

// loginStageModel is the authentik model whose session_duration decides how long
// the session behind every SSO round trip lives. The stage is what calls
// request.session.set_expiry(), so this is the one knob that answers "how long
// am I logged in for".
const loginStageModel = "authentik_stages_user_login.userloginstage"

// TestAuthFlowBlueprintLoginSessionDuration pins the session duration Bloud
// declares on the login stage of the authentication flow it mounts at
// /blueprints/default/flow-default-authentication-flow.yaml.
//
// The blueprint is the mechanism: authentik's worker re-applies it whenever the
// file hash changes (watchdog) and on its hourly discovery schedule, so the
// value converges without the host agent policing it. Verified against a live
// instance: a session_duration patched away from the blueprint's value is put
// back by the next apply.
func TestAuthFlowBlueprintLoginSessionDuration(t *testing.T) {
	attrs := loginStageAttrs(t)

	raw, ok := attrs["session_duration"]
	require.True(t, ok, "the login stage must declare session_duration; without it "+
		"authentik keeps its seconds=0 default and the login dies with the browser")
	assert.Equal(t, loginSessionDuration, raw)

	// The value has to be valid in authentik's own syntax, not just equal to the
	// string we wrote: an unparseable duration is a serializer error at apply
	// time, which fails the blueprint silently rather than shortening the login.
	assert.Equal(t, 90*24*time.Hour, mustParseAuthentikDuration(t, raw))
}

// TestAuthFlowBlueprintLeavesRememberMeOff pins the two durations that decide
// whether the login form shows a "remember me" toggle. Both stay zero on
// purpose: session_duration alone carries the whole 90 days, so there is nothing
// for a toggle to extend and no second, shorter lifetime for a user to
// accidentally pick.
func TestAuthFlowBlueprintLeavesRememberMeOff(t *testing.T) {
	attrs := loginStageAttrs(t)

	for _, key := range []string{"remember_me_offset"} {
		if v, present := attrs[key]; present {
			assert.Equal(t, 0, int(mustParseAuthentikDuration(t, v)),
				"%s must stay zero so the remember-me toggle is not shown", key)
		}
	}
}

// loginStageAttrs returns the attrs of the user login stage entry in the
// authentication flow blueprint.
func loginStageAttrs(t *testing.T) map[string]string {
	t.Helper()

	path := filepath.Join(".", "auth.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err, "read the auth flow blueprint")

	var doc struct {
		Entries []struct {
			Model       string    `yaml:"model"`
			Identifiers yaml.Node `yaml:"identifiers"`
			Attrs       yaml.Node `yaml:"attrs"`
			ID          string    `yaml:"id"`
		} `yaml:"entries"`
	}
	// The blueprint carries custom tags (!KeyOf, !Find) that only appear in
	// other entries' values, so the attrs we read are decoded as nodes and
	// unwrapped by hand instead of into a plain map.
	require.NoError(t, yaml.Unmarshal(data, &doc))

	for _, entry := range doc.Entries {
		if entry.Model != loginStageModel {
			continue
		}
		if name := scalarValue(entry.Identifiers, "name"); name != "default-authentication-login" {
			continue
		}
		return mappingValues(entry.Attrs)
	}
	t.Fatalf("no %s entry named default-authentication-login in %s", loginStageModel, path)
	return nil
}

// scalarValue returns the value of key inside a mapping node, or "" when the key
// is absent.
func scalarValue(node yaml.Node, key string) string {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1].Value
		}
	}
	return ""
}

// mappingValues flattens a mapping node of scalar values. A non-mapping node
// yields an empty map, which reads as "this entry declared no attrs".
func mappingValues(node yaml.Node) map[string]string {
	out := map[string]string{}
	if node.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		out[node.Content[i].Value] = node.Content[i+1].Value
	}
	return out
}

// mustParseAuthentikDuration parses authentik's duration syntax ("days=90",
// "hours=12;minutes=30") over the unit set authentik's timedelta_from_string
// accepts, so a value the identity provider would reject fails here first.
func mustParseAuthentikDuration(t *testing.T, s string) time.Duration {
	t.Helper()

	units := map[string]time.Duration{
		"microseconds": time.Microsecond,
		"milliseconds": time.Millisecond,
		"seconds":      time.Second,
		"minutes":      time.Minute,
		"hours":        time.Hour,
		"days":         24 * time.Hour,
		"weeks":        7 * 24 * time.Hour,
	}

	var total time.Duration
	parsed := 0
	for _, pair := range strings.Split(s, ";") {
		key, val, ok := strings.Cut(pair, "=")
		require.True(t, ok, "%q is not a key=value duration pair", pair)
		unit, known := units[strings.ToLower(strings.TrimSpace(key))]
		require.True(t, known, "%q is not a unit authentik understands", key)
		n, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		require.NoError(t, err, "%q is not a duration number", val)
		total += time.Duration(n * float64(unit))
		parsed++
	}
	require.Positive(t, parsed, "%q has no valid duration units", s)
	return total
}
