// SPDX-License-Identifier: AGPL-3.0-only

package inference

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Settings keys. Declared next to the type that decodes them, so what a key
// means is this package's business rather than a string the callers each
// remember independently.
const (
	// SettingUpstreams holds the JSON-encoded upstream list.
	SettingUpstreams = "ai_upstreams"
	// SettingDefaultModel holds the instance default model id: the model a
	// consumer uses where it has not chosen its own.
	SettingDefaultModel = "ai_default_model"
)

// SecretScope is the secrets-manager scope the upstream credential lives under.
// It is not a catalog app: the instance owns this credential, and putting it in
// the settings row would echo it into every API response that reads settings.
const SecretScope = "ai"

// SecretAPIKey is the secret name holding the active upstream's API key.
//
// One name for one upstream. A multi-upstream Settings UI needs a key per
// upstream, and that is a deliberate change to this constant rather than an
// accident of the current shape.
const SecretAPIKey = "apiKey"

// Upstream is one configured OpenAI-compatible server.
type Upstream struct {
	// ID is a stable identifier, used to recognize the entry across saves so
	// renaming it does not read as remove-plus-add.
	ID string `json:"id"`
	// Name is the operator-facing label.
	Name string `json:"name"`
	// BaseURL is the OpenAI-compatible endpoint as entered, path included.
	BaseURL string `json:"baseUrl"`
	// Models is the last discovered model list. It is a display convenience,
	// not authority: the upstream owns which models exist, and Bloud does not
	// gate anything on this list.
	Models []string `json:"models,omitempty"`
	// Enabled reports whether the upstream is active. Disabled entries are
	// kept rather than deleted so toggling is reversible.
	Enabled bool `json:"enabled"`
}

// Settings is the decoded AI settings: the upstream list and the instance
// default model.
type Settings struct {
	Upstreams    []Upstream
	DefaultModel string
}

// DecodeSettings parses the two settings keys into one value. An empty upstreams
// payload is not an error: "no AI configured" is a normal state, and every
// consumer treats the resulting empty binding the way it treats an uninstalled
// provider.
func DecodeSettings(upstreamsJSON, defaultModel string) (Settings, error) {
	out := Settings{DefaultModel: strings.TrimSpace(defaultModel)}
	upstreamsJSON = strings.TrimSpace(upstreamsJSON)
	if upstreamsJSON == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(upstreamsJSON), &out.Upstreams); err != nil {
		return Settings{}, fmt.Errorf("decode %s: %w", SettingUpstreams, err)
	}
	return out, nil
}

// EncodeUpstreams renders the upstream list for storage.
func EncodeUpstreams(upstreams []Upstream) (string, error) {
	if len(upstreams) == 0 {
		return "", nil
	}
	b, err := json.Marshal(upstreams)
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", SettingUpstreams, err)
	}
	return string(b), nil
}

// ActiveUpstream returns the first enabled upstream, and whether one exists.
//
// v1 resolves to a single upstream. Returning "the first enabled" rather than
// "all of them" keeps the promotion rule unambiguous about which source a
// consumer gets: there is exactly one answer, and a multi-upstream design
// changes this function rather than every caller.
func (s Settings) ActiveUpstream() (Upstream, bool) {
	for _, u := range s.Upstreams {
		if u.Enabled {
			return u, true
		}
	}
	return Upstream{}, false
}

// Endpoint returns the active upstream's parsed endpoint. The second result is
// false when nothing is configured or the stored value no longer parses; the
// caller logs the parse failure rather than treating it as unconfigured, because
// "configured and broken" and "not configured" need different diagnoses.
func (s Settings) Endpoint() (Endpoint, bool, error) {
	u, ok := s.ActiveUpstream()
	if !ok {
		return Endpoint{}, false, nil
	}
	ep, err := ParseEndpoint(u.BaseURL)
	if err != nil {
		return Endpoint{}, false, fmt.Errorf("upstream %q: %w", u.ID, err)
	}
	return ep, true, nil
}
