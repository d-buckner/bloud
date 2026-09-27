// SPDX-License-Identifier: AGPL-3.0-only

package hostset

import (
	"net/url"
	"strings"
	"testing"
)

// Part E of the proxied-scheme plan (docs/plans/proxied-scheme-urls.md).
//
// A reproduction test that passes both with and without the fix is worse than no
// test: it manufactures confidence and hides the bug it was written to catch. The
// canaries here exist to make the A and B suites falsifiable, and the signature
// table makes a red run name one layer instead of three.
//
// Read these as: "if you change the code so that A and B can no longer fail, one
// of these must fail instead."

func mustResolve(t *testing.T, in Input) HostSet {
	t.Helper()
	hs, err := Resolve(in)
	if err != nil {
		t.Fatalf("Resolve(%+v) failed: %v", in, err)
	}
	return hs
}

func schemeOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("%q is not a parseable URL: %v", raw, err)
	}
	return u.Scheme
}

// Canary 1: the mutation guard.
//
// If the scheme stops reaching the derivation, every https assertion in the A
// table would still compile and the derived URLs would silently revert to the
// hardcoded literal this change exists to remove. This fails if setting the scheme
// changes nothing.
func TestCanary_SchemeActuallyChangesDerivedURLs(t *testing.T) {
	plain := New([]string{"a.test"}, "a.test")
	tls := plain.WithScheme("a.test", SchemeHTTPS)

	pairs := []struct {
		field            string
		without, withTLS string
	}{
		{"BaseURLFor", plain.BaseURLFor("a.test"), tls.BaseURLFor("a.test")},
		{"PrimaryBaseURL", plain.PrimaryBaseURL(), tls.PrimaryBaseURL()},
		{"IssuerBaseURL", plain.IssuerBaseURL(), tls.IssuerBaseURL()},
	}
	for _, p := range pairs {
		if p.without == p.withTLS {
			t.Fatalf("%s returns %q under both http and https: the scheme is not reaching the derivation",
				p.field, p.without)
		}
		if schemeOf(t, p.withTLS) != "https" {
			t.Errorf("%s under https = %q, want an https scheme", p.field, p.withTLS)
		}
	}
}

// Canary 2: Deployability must be two-sided.
//
// A Deployability that always returns nil is worse than no diagnostic at all,
// because it reads as "checked and fine". A Deployability that always fires is
// equally useless and gets ignored. Both directions are pinned.
func TestCanary_DeployabilityFiresOnlyWhenUndeployable(t *testing.T) {
	tlsNoGateway := New([]string{"a.test"}, "a.test").WithScheme("a.test", SchemeHTTPS)
	if len(tlsNoGateway.Deployability(false)) == 0 {
		t.Fatal("a https issuer with no TLS at the host gateway must be reported")
	}
	if len(tlsNoGateway.Deployability(true)) != 0 {
		t.Fatal("a https issuer with TLS at the host gateway is deployable and must not be reported")
	}

	plain := New([]string{"a.test"}, "a.test")
	if len(plain.Deployability(false)) != 0 {
		t.Fatal("a plain-http deployment must not be reported as broken")
	}
	if len(plain.Deployability(true)) != 0 {
		t.Fatal("a plain-http deployment must not be reported as broken regardless of TLS placement")
	}
}

// Canary 3: the wrong fix is closed off.
//
// The tempting way to make the Layer 3 contradiction go away is to quietly
// downgrade the issuer to http so the container's plain-HTTP hop matches. That
// breaks the browser half, which is the half the user is standing in front of.
// This pins the issuer to the declared scheme; paired with the sso blueprint
// tests, a change has to satisfy both halves or fail somewhere.
func TestCanary_HTTPSSchemeNeverSilentlyDowngradesTheIssuer(t *testing.T) {
	hs := mustResolve(t, Input{
		Stored:       []StoredHost{{Hostname: "bloud.example.com", Primary: true}},
		PublicScheme: "https",
	})

	if got := schemeOf(t, hs.IssuerBaseURL()); got != "https" {
		t.Errorf("IssuerBaseURL() = %q (scheme %q): downgrading the issuer to make the container hop work breaks the browser half",
			hs.IssuerBaseURL(), got)
	}
	if got := schemeOf(t, hs.PrimaryBaseURL()); got != "https" {
		t.Errorf("PrimaryBaseURL() = %q (scheme %q), want https", hs.PrimaryBaseURL(), got)
	}
	// The primary must appear in the registered base URLs as https. Built-ins
	// are http by design and are not this assertion's subject.
	var found bool
	for _, u := range hs.BaseURLs() {
		if strings.Contains(u, "bloud.example.com") {
			found = true
			if schemeOf(t, u) != "https" {
				t.Errorf("registered base URL %q must be https", u)
			}
		}
	}
	if !found {
		t.Error("the primary host is missing from BaseURLs(); the redirect URI would never be registered")
	}
}

// Canary 4: the failure-signature table.
//
// Each row is one misconfiguration and the single layer it should be blamed on.
// This is the thing that turns "SSO does not work behind my proxy" into a
// one-line answer.
func TestCanary_ConsistencyNamesTheRightLayer(t *testing.T) {
	nets := []string{"192.168.1.7"}

	cases := []struct {
		name         string
		scheme       string
		proxyNets    []string
		tlsAtTraefik bool
		wantCodes    []string
	}{
		{
			name:         "all three layers set right",
			scheme:       "https",
			proxyNets:    nets,
			tlsAtTraefik: true,
			wantCodes:    nil,
		},
		{
			// Layer 1. The scheme is right and the dial plan is right, but
			// Traefik was never told to trust the upstream, so it rewrites
			// the proto and Authentik sees http on an https page.
			name:         "https but Traefik trusts nobody (layer 1)",
			scheme:       "https",
			proxyNets:    nil,
			tlsAtTraefik: true,
			wantCodes:    []string{"https_without_trusted_proxy_nets"},
		},
		{
			// Layer 2. The proxy is trusted, but the deployment still
			// describes itself as http, so every derived URL is http.
			name:         "proxy trusted but scheme still http (layer 2)",
			scheme:       "http",
			proxyNets:    nets,
			tlsAtTraefik: true,
			wantCodes:    []string{"proxy_present_but_scheme_is_http"},
		},
		{
			// Layer 3. Trust scope and derivation are both right; the
			// container hop cannot match the issuer string.
			name:         "https and trusted proxy, but no TLS at the gateway (layer 3)",
			scheme:       "https",
			proxyNets:    nets,
			tlsAtTraefik: false,
			wantCodes:    []string{"https_issuer_no_tls_at_gateway"},
		},
		{
			// Layers 1 and 3 wrong together, and both must be reported. A
			// diagnostic that stops at the first issue sends the operator to
			// fix one thing and come back for the other.
			name:         "layers 1 and 3 wrong at once",
			scheme:       "https",
			proxyNets:    nil,
			tlsAtTraefik: false,
			wantCodes:    []string{"https_without_trusted_proxy_nets", "https_issuer_no_tls_at_gateway"},
		},
		{
			// The plain-http no-proxy deployment is a valid configuration,
			// not a misconfiguration. Reporting anything here would make the
			// diagnostic noise.
			name:         "plain http with no proxy: nothing to report",
			scheme:       "http",
			proxyNets:    nil,
			tlsAtTraefik: false,
			wantCodes:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := mustResolve(t, Input{
				Stored:       []StoredHost{{Hostname: "bloud.example.com", Primary: true}},
				PublicScheme: tc.scheme,
			})
			got := issueCodes(hs.ProxyConsistency(tc.proxyNets, tc.tlsAtTraefik))

			want := tc.wantCodes
			if want == nil {
				want = []string{}
			}
			if len(got) != len(want) {
				t.Fatalf("ProxyConsistency() codes = %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("code[%d] = %q, want %q", i, got[i], want[i])
				}
			}
		})
	}
}

// Every reported issue must name the setting the operator has to change. A
// diagnostic that says "inconsistent" without a setting name is a second bug.
func TestCanary_EveryIssueNamesAnActionableSetting(t *testing.T) {
	hs := mustResolve(t, Input{
		Stored:       []StoredHost{{Hostname: "bloud.example.com", Primary: true}},
		PublicScheme: "https",
	})
	issues := hs.ProxyConsistency(nil, false)
	if len(issues) < 2 {
		t.Fatalf("expected the both-layers-wrong case to report at least 2 issues, got %d", len(issues))
	}
	for _, is := range issues {
		if is.Code == "" {
			t.Errorf("issue %q has no code", is.Message)
		}
		namesSetting := strings.Contains(is.Message, "BLOUD_TRUSTED_PROXY_NETS") ||
			strings.Contains(is.Message, "Traefik") ||
			strings.Contains(is.Message, "host-gateway")
		if !namesSetting {
			t.Errorf("issue %s (%q) must name the setting or component to change", is.Code, is.Message)
		}
	}
}
