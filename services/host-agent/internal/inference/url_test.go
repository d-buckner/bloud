// SPDX-License-Identifier: AGPL-3.0-only

package inference

import (
	"strings"
	"testing"
)

// TestModelsURLNeverInventsAPrefix: the probe has to test the base the client
// appends /chat/completions to, which is the endpoint as entered. Falling back to
// origin+"/v1/models" when the entered path is not already /v1 would let an
// endpoint typed without its API base path pass the settings check while every
// real call the app makes 404s, reporting on a URL nothing would ever dial.
func TestModelsURLNeverInventsAPrefix(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://inference.example.com/v1", "https://inference.example.com/v1/models"},
		{"https://inference.example.com/v1/", "https://inference.example.com/v1/models"},
		// The path-less case: resolved at the origin, NOT quietly rewritten
		// to /v1/models. This is what makes a bad endpoint fail the check.
		{"https://inference.example.com", "https://inference.example.com/models"},
		{"https://inference.example.com:8443/v1", "https://inference.example.com:8443/v1/models"},
		// A gateway serving under its own prefix is probed there, not at /v1.
		{"https://gw.example.com/llm/openai", "https://gw.example.com/llm/openai/models"},
	}
	for _, c := range cases {
		ep, err := ParseEndpoint(c.in)
		if err != nil {
			t.Fatalf("ParseEndpoint(%q): %v", c.in, err)
		}
		if got := ep.ModelsURL(); got != c.want {
			t.Errorf("ModelsURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestMissingAPIPrefixHintOnlyFiresWithoutAPath: the hint is advice about a
// missing prefix, so it must stay silent once the operator has supplied one.
func TestMissingAPIPrefixHintOnlyFiresWithoutAPath(t *testing.T) {
	bare, err := ParseEndpoint("https://inference.example.com")
	if err != nil {
		t.Fatal(err)
	}
	hint := bare.MissingAPIPrefixHint()
	if !strings.Contains(hint, "/v1") || !strings.Contains(hint, "chat/completions") {
		t.Errorf("hint = %q, want it to name the /v1 prefix and why", hint)
	}
	if !strings.Contains(hint, "https://inference.example.com/v1") {
		t.Errorf("hint = %q, want a concrete corrected example", hint)
	}

	withPath, err := ParseEndpoint("https://inference.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	if got := withPath.MissingAPIPrefixHint(); got != "" {
		t.Errorf("hint for a pathed endpoint = %q, want empty", got)
	}
}

func TestParseEndpointPreservesPathAndCanonicalizes(t *testing.T) {
	ep, err := ParseEndpoint("HTTPS://Inference.Example.COM:443//v1//")
	if err != nil {
		t.Fatalf("ParseEndpoint: %v", err)
	}
	if ep.Scheme != "https" || ep.Host != "inference.example.com" || ep.Port != 443 {
		t.Errorf("parsed = %+v", ep)
	}
	if ep.Path != "/v1" {
		t.Errorf("Path = %q, want /v1", ep.Path)
	}
	if ep.SchemeAssumed {
		t.Error("scheme was explicit, must not be reported as assumed")
	}
}

func TestParseEndpointRejectsHalfUnderstoodURLs(t *testing.T) {
	for _, in := range []string{
		"",
		"ftp://host/v1",
		"https://user:pass@host/v1",
		"https://host/v1?key=1",
		"https://host/v1#frag",
		"https://host:99999/v1",
	} {
		if _, err := ParseEndpoint(in); err == nil {
			t.Errorf("ParseEndpoint(%q) = nil error, want rejection", in)
		}
	}
}

// TestParseEndpointAssumesHTTPOnlyWhenNoScheme: the assumption is surfaced
// rather than hidden, so the UI can show what it filled in.
func TestParseEndpointAssumesHTTPOnlyWhenNoScheme(t *testing.T) {
	ep, err := ParseEndpoint("inference.example.com/v1")
	if err != nil {
		t.Fatalf("ParseEndpoint: %v", err)
	}
	if !ep.SchemeAssumed {
		t.Error("schemeless input must report the assumed http scheme")
	}
	if ep.String() != "http://inference.example.com/v1" {
		t.Errorf("String() = %q", ep.String())
	}
}
