// SPDX-License-Identifier: AGPL-3.0-only

package hostset

import (
	"reflect"
	"strings"
	"testing"
)

// This is the derivation table for the "Bloud sits behind a TLS-terminating
// proxy" problem. Every externally visible URL in Bloud comes out of HostSet, so
// this is where the whole class is pinned: if a value here is wrong, the OIDC
// issuer, the registered redirect URIs, the launch URLs, or the outpost's
// browser URL is wrong in the deployed system.
//
// Each row is a topology an operator can actually end up with, not a synthetic
// input. The columns are the values that get handed to a browser, to an app
// container, or to Authentik.
type derivationCase struct {
	name string
	in   Input

	wantPublicScheme   Scheme
	wantPrimaryBaseURL string
	wantIssuerBaseURL  string
	wantIssuerExtra    string
	wantBaseURLs       []string

	// tlsAtTraefik is the deployability input: does TLS terminate where the
	// container's host-gateway pin actually lands?
	tlsAtTraefik bool
	// wantIssueCodes are the Deployability codes expected. Empty means the
	// derived set is deployable.
	wantIssueCodes []string
}

func derivationCases() []derivationCase {
	return []derivationCase{
		{
			// The dev loop and every e2e run. The :8080 here is a dev VM port
			// forward, not a TLS statement, which is why it survives only under
			// plain http.
			name:               "dev vm, localhost primary, nothing in front",
			in:                 Input{},
			wantPublicScheme:   SchemeHTTP,
			wantPrimaryBaseURL: "http://localhost:8080",
			wantIssuerBaseURL:  "http://sso.localhost:8080",
			wantIssuerExtra:    "sso.localhost:host-gateway",
			wantBaseURLs:       []string{"http://localhost:8080", "http://bloud.local"},
		},
		{
			// Today's production default: a real domain, plain HTTP, no proxy.
			name:               "direct, real domain primary, plain http",
			in:                 Input{BaseDomain: "bloud.example.com"},
			wantPublicScheme:   SchemeHTTP,
			wantPrimaryBaseURL: "http://bloud.example.com",
			wantIssuerBaseURL:  "http://bloud.example.com",
			wantIssuerExtra:    "bloud.example.com:host-gateway",
			wantBaseURLs:       []string{"http://bloud.example.com", "http://localhost:8080", "http://bloud.local"},
		},
		{
			// The reported failure. Before the scheme was representable, this
			// row was unreachable: BaseURLFor returned http:// no matter what.
			name:               "TLS-terminating proxy, https via env, no stored hosts",
			in:                 Input{BaseDomain: "bloud.example.com", PublicScheme: "https"},
			wantPublicScheme:   SchemeHTTPS,
			wantPrimaryBaseURL: "https://bloud.example.com",
			wantIssuerBaseURL:  "https://bloud.example.com",
			wantIssuerExtra:    "bloud.example.com:host-gateway",
			wantBaseURLs:       []string{"https://bloud.example.com", "http://localhost:8080", "http://bloud.local"},
			// Correct derivation, still not deployable: the container hop is
			// plain HTTP, so the https issuer cannot match on both hops.
			wantIssueCodes: []string{"https_issuer_no_tls_at_gateway"},
		},
		{
			// The Layer 2 gap. This is the path a real operator takes: they add
			// the domain in Settings, which makes stored hosts win over env and
			// kills the legacy BLOUD_SSO_BASE_URL override. Before PublicScheme
			// there was no way to express https here at all.
			name: "TLS-terminating proxy, https with stored admin hosts",
			in: Input{
				Stored:       []StoredHost{{Hostname: "bloud.example.com", Primary: true}},
				PublicScheme: "https",
			},
			wantPublicScheme:   SchemeHTTPS,
			wantPrimaryBaseURL: "https://bloud.example.com",
			wantIssuerBaseURL:  "https://bloud.example.com",
			wantIssuerExtra:    "bloud.example.com:host-gateway",
			wantBaseURLs:       []string{"https://bloud.example.com", "http://localhost:8080", "http://bloud.local"},
			wantIssueCodes:     []string{"https_issuer_no_tls_at_gateway"},
		},
		{
			// Same as above but with TLS where the container lands: the pair is
			// now consistent and the set is deployable.
			name: "https proxy with TLS at Traefik",
			in: Input{
				Stored:       []StoredHost{{Hostname: "bloud.example.com", Primary: true}},
				PublicScheme: "https",
			},
			tlsAtTraefik:       true,
			wantPublicScheme:   SchemeHTTPS,
			wantPrimaryBaseURL: "https://bloud.example.com",
			wantIssuerBaseURL:  "https://bloud.example.com",
			wantIssuerExtra:    "bloud.example.com:host-gateway",
			wantBaseURLs:       []string{"https://bloud.example.com", "http://localhost:8080", "http://bloud.local"},
		},
		{
			// A proxy on a non-standard port. Only the legacy full-URL override
			// can carry a port, so this row also pins that path. Note the
			// extra_hosts entry carries no port: podman's --extra-hosts is
			// hostname:IP only. The port reaches the container through the
			// issuer URL, not through the host mapping.
			name:               "proxy on :8443 via the legacy URL override",
			in:                 Input{SSOBaseURL: "https://bloud.example.com:8443"},
			wantPublicScheme:   SchemeHTTPS,
			wantPrimaryBaseURL: "https://bloud.example.com:8443",
			wantIssuerBaseURL:  "https://bloud.example.com:8443",
			wantIssuerExtra:    "bloud.example.com:host-gateway",
			wantBaseURLs:       []string{"https://bloud.example.com:8443", "http://localhost:8080", "http://bloud.local"},
			wantIssueCodes:     []string{"https_issuer_no_tls_at_gateway"},
		},
	}
}

func TestDerivationTable(t *testing.T) {
	for _, tc := range derivationCases() {
		t.Run(tc.name, func(t *testing.T) {
			hs, err := Resolve(tc.in)
			if err != nil {
				t.Fatalf("Resolve(%+v) error = %v", tc.in, err)
			}

			if got := hs.PublicScheme(); got != tc.wantPublicScheme {
				t.Errorf("PublicScheme() = %q, want %q", got, tc.wantPublicScheme)
			}
			if got := hs.PrimaryBaseURL(); got != tc.wantPrimaryBaseURL {
				t.Errorf("PrimaryBaseURL() = %q, want %q", got, tc.wantPrimaryBaseURL)
			}
			if got := hs.IssuerBaseURL(); got != tc.wantIssuerBaseURL {
				t.Errorf("IssuerBaseURL() = %q, want %q", got, tc.wantIssuerBaseURL)
			}
			if got := hs.IssuerExtraHost(); got != tc.wantIssuerExtra {
				t.Errorf("IssuerExtraHost() = %q, want %q", got, tc.wantIssuerExtra)
			}
			if got := hs.BaseURLs(); !reflect.DeepEqual(got, tc.wantBaseURLs) {
				t.Errorf("BaseURLs() = %v, want %v", got, tc.wantBaseURLs)
			}

			want := tc.wantIssueCodes
			if want == nil {
				want = []string{}
			}
			if got := issueCodes(hs.Deployability(tc.tlsAtTraefik)); !reflect.DeepEqual(got, want) {
				t.Errorf("Deployability(tlsAtTraefik=%v) codes = %v, want %v",
					tc.tlsAtTraefik, got, want)
			}
		})
	}
}

func issueCodes(issues []Issue) []string {
	codes := make([]string, 0, len(issues))
	for _, i := range issues {
		codes = append(codes, i.Code)
	}
	return codes
}

// The default path must stay byte-identical to a build with no scheme concept.
// Every existing deployment lands here, so a change to this row is a change to
// what already-installed instances derive, not a new capability.
func TestDefaultDerivationIsUnchanged(t *testing.T) {
	hs, err := Resolve(Input{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"localhost":   "http://localhost:8080",
		"bloud.local": "http://bloud.local",
	}
	for host, wantURL := range want {
		if got := hs.BaseURLFor(host); got != wantURL {
			t.Errorf("BaseURLFor(%q) = %q, want %q (default derivation must not drift)", host, got, wantURL)
		}
	}
	if hs.PublicScheme() != SchemeHTTP {
		t.Errorf("PublicScheme() = %q, want http for an unset scheme", hs.PublicScheme())
	}
	if issues := hs.Deployability(false); len(issues) != 0 {
		t.Errorf("Deployability() = %v, want no issues for the plain-http default", issues)
	}
}

// A host that already carries an explicit base-URL override is the more specific
// statement of intent. A broad public scheme must not silently contradict it, or
// a redirect ends up on a protocol the operator never asked for.
func TestPublicSchemeDoesNotClobberExplicitOverride(t *testing.T) {
	hs := New([]string{"a.test", "b.test"}, "a.test").
		WithURLOverride("a.test", "http://pinned.test:9999").
		WithPublicScheme(SchemeHTTPS)

	if got := hs.BaseURLFor("a.test"); got != "http://pinned.test:9999" {
		t.Errorf("BaseURLFor(a.test) = %q, want the pinned override to win", got)
	}
	// A proxied host with no override still gets the public scheme.
	if got := hs.BaseURLFor("b.test"); got != "https://b.test" {
		t.Errorf("BaseURLFor(b.test) = %q, want https://b.test", got)
	}
	// The override carries its own scheme, so SchemeFor must read it rather
	// than report the broad public scheme.
	if got := hs.SchemeFor("a.test"); got != SchemeHTTP {
		t.Errorf("SchemeFor(a.test) = %q, want http as the pinned override declares", got)
	}
}

// The built-in hosts are reached directly, not through the proxy the public
// scheme describes, so they must stay plain HTTP. Flipping them would break the
// LAN and dev paths that are the only reason those names exist.
func TestPublicSchemeLeavesBuiltinsAlone(t *testing.T) {
	hs, err := Resolve(Input{
		Stored:       []StoredHost{{Hostname: "bloud.example.com", Primary: true}},
		PublicScheme: "https",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := hs.BaseURLFor("localhost"); got != "http://localhost:8080" {
		t.Errorf("BaseURLFor(localhost) = %q, want the builtin left on plain http", got)
	}
	if got := hs.BaseURLFor("bloud.local"); got != "http://bloud.local" {
		t.Errorf("BaseURLFor(bloud.local) = %q, want the builtin left on plain http", got)
	}
	if got := hs.BaseURLFor("bloud.example.com"); got != "https://bloud.example.com" {
		t.Errorf("BaseURLFor(bloud.example.com) = %q, want the proxied host on https", got)
	}
}

// The localhost :8080 convention is a dev VM port forward, not a rule about
// TLS, so it must not follow an https host.
func TestLocalhostPortConventionIsHTTPOnly(t *testing.T) {
	hs := New([]string{"localhost"}, "localhost").WithScheme("localhost", SchemeHTTPS)
	if got := hs.BaseURLFor("localhost"); got != "https://localhost" {
		t.Errorf("BaseURLFor(localhost) under https = %q, want https://localhost with no :8080", got)
	}
}

func TestNormalizeScheme(t *testing.T) {
	cases := map[string]Scheme{
		"http":    SchemeHTTP,
		"HTTP":    SchemeHTTP,
		" https ": SchemeHTTPS,
		"HTTPS":   SchemeHTTPS,
		"ftp":     "",
		"":        "",
		"htp":     "",
	}
	for in, want := range cases {
		if got := NormalizeScheme(in); got != want {
			t.Errorf("NormalizeScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

// An unrecognized public scheme must be rejected at Resolve rather than silently
// ignored, because a silently ignored scheme produces a deployment that looks
// configured and is not.
func TestResolveRejectsBadPublicScheme(t *testing.T) {
	_, err := Resolve(Input{BaseDomain: "a.test", PublicScheme: "sftp"})
	if err == nil {
		t.Fatal("Resolve() error = nil, want rejection of a non-http(s) scheme")
	}
	if !strings.Contains(err.Error(), "BLOUD_PUBLIC_SCHEME") {
		t.Errorf("error = %q, want it to name BLOUD_PUBLIC_SCHEME", err)
	}
}

// WithScheme must not corrupt the set when handed junk: the safe behavior is to
// leave the scheme at its http default rather than mint a nonsense protocol.
func TestWithSchemeIgnoresUnrecognizedValue(t *testing.T) {
	hs := New([]string{"a.test"}, "a.test").WithScheme("a.test", Scheme("gopher"))
	if got := hs.BaseURLFor("a.test"); got != "http://a.test" {
		t.Errorf("BaseURLFor = %q, want the http default preserved", got)
	}
}

// The diagnostic must name the concrete thing an operator has to go fix, not
// just "misconfigured". A generic message here is what turns a stalled login
// into an afternoon of guessing.
func TestDeployabilityMessageNamesTheConcreteCause(t *testing.T) {
	hs, err := Resolve(Input{
		Stored:       []StoredHost{{Hostname: "bloud.example.com", Primary: true}},
		PublicScheme: "https",
	})
	if err != nil {
		t.Fatal(err)
	}
	issues := hs.Deployability(false)
	if len(issues) != 1 {
		t.Fatalf("Deployability() = %v, want exactly one issue", issues)
	}
	msg := issues[0].Message
	for _, must := range []string{
		"https://bloud.example.com", // the issuer that cannot be matched
		"host-gateway",              // the pin the container actually dials
		"plain HTTP",                // what is served there
	} {
		if !strings.Contains(msg, must) {
			t.Errorf("message %q must name %q so the cause is identifiable", msg, must)
		}
	}
}
