// SPDX-License-Identifier: AGPL-3.0-only

package system

import (
	"context"
	"errors"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
)

// fakeProber is the ContainerProber test double.
type fakeProber struct {
	containers []podman.Container
	listErr    error
	execFn     func(container string, cmd []string) ([]byte, error)
	calls      []string
}

func (f *fakeProber) ListContainers(context.Context) ([]podman.Container, error) {
	return f.containers, f.listErr
}

func (f *fakeProber) ExecWithEnv(_ context.Context, container string, _ map[string]string, cmd []string) ([]byte, error) {
	f.calls = append(f.calls, container+" "+strings.Join(cmd, " "))
	if f.execFn == nil {
		return nil, errors.New("exec not configured")
	}
	return f.execFn(container, cmd)
}

// newDiagnostics builds the checker with a resolved host and one running
// Traefik container, unless the test overrides the list.
func newDiagnostics(prober ContainerProber, publicURL string, lookup func(context.Context, string) ([]string, error)) *DNSDiagnostics {
	return &DNSDiagnostics{
		client:    prober,
		publicURL: func() string { return publicURL },
		lookup:    lookup,
		maxProbes: 3,
	}
}

func resolvesTo(addrs ...string) func(context.Context, string) ([]string, error) {
	return func(context.Context, string) ([]string, error) { return addrs, nil }
}

// resolvConfExec answers the two execs a probe makes: the cat of resolv.conf
// and the resolver script.
func resolvConfExec(nameservers, resolution string) func(string, []string) ([]byte, error) {
	return func(_ string, cmd []string) ([]byte, error) {
		if len(cmd) > 0 && cmd[0] == "cat" {
			return []byte("search internal\nnameserver " + nameservers + "\n"), nil
		}
		return []byte(resolution + "\n"), nil
	}
}

func runningTraefik() []podman.Container {
	return []podman.Container{{Names: []string{"/apps-traefik"}, State: "running"}}
}

// TestDNSDiagnostics_DivergenceIsReported is the AFFiNE scenario: the host
// resolves the public host and the container, whose resolver was captured with
// public fallbacks, does not.
func TestDNSDiagnostics_DivergenceIsReported(t *testing.T) {
	prober := &fakeProber{
		containers: runningTraefik(),
		execFn:     resolvConfExec("8.8.8.8", "UNRESOLVED"),
	}
	d := newDiagnostics(prober, "https://thebloud.org", resolvesTo("10.0.0.21"))

	got := d.Check(context.Background())
	if !got.HostResolves {
		t.Error("host should have resolved")
	}
	if !got.ContainerChecked || got.ContainerResolves {
		t.Errorf("container = checked %v resolves %v, want checked and unresolved", got.ContainerChecked, got.ContainerResolves)
	}
	if !got.Diverged {
		t.Error("expected Diverged for host-resolves/container-does-not")
	}
	if got.Container != "apps-traefik" {
		t.Errorf("container = %q, want apps-traefik", got.Container)
	}
	if len(got.ContainerNameservers) != 1 || got.ContainerNameservers[0] != "8.8.8.8" {
		t.Errorf("nameservers = %v, want [8.8.8.8]", got.ContainerNameservers)
	}
	if !strings.Contains(got.Detail, "not inside apps-traefik") {
		t.Errorf("detail = %q, want it to name the divergence", got.Detail)
	}
}

// TestDNSDiagnostics_AlignedIsNotDiverged: both sides resolve, so there is
// nothing to warn about.
func TestDNSDiagnostics_AlignedIsNotDiverged(t *testing.T) {
	prober := &fakeProber{
		containers: runningTraefik(),
		execFn:     resolvConfExec("10.0.0.1", "RESOLVED"),
	}
	d := newDiagnostics(prober, "https://bloud.example.com", resolvesTo("10.0.0.21"))

	got := d.Check(context.Background())
	if got.Diverged {
		t.Error("aligned resolvers must not diverge")
	}
	if !got.ContainerChecked || !got.ContainerResolves {
		t.Errorf("container = checked %v resolves %v, want both true", got.ContainerChecked, got.ContainerResolves)
	}
}

// TestDNSDiagnostics_NoResolverToolIsNotAGuiltyVerdict: an image without
// getent or nslookup answers NO_RESOLVER, which must read as "could not check",
// never as "resolution failed".
func TestDNSDiagnostics_NoResolverToolIsNotAGuiltyVerdict(t *testing.T) {
	prober := &fakeProber{
		containers: runningTraefik(),
		execFn:     resolvConfExec("8.8.8.8", "NO_RESOLVER"),
	}
	d := newDiagnostics(prober, "https://thebloud.org", resolvesTo("10.0.0.21"))

	got := d.Check(context.Background())
	if got.ContainerChecked {
		t.Error("NO_RESOLVER must leave ContainerChecked false")
	}
	if got.Diverged {
		t.Error("an inconclusive probe must not diverge")
	}
}

// TestDNSDiagnostics_HostFailureIsNotDivergence: if the host cannot resolve the
// name either, the container failing to resolve it says nothing about the
// sandbox.
func TestDNSDiagnostics_HostFailureIsNotDivergence(t *testing.T) {
	prober := &fakeProber{
		containers: runningTraefik(),
		execFn:     resolvConfExec("8.8.8.8", "UNRESOLVED"),
	}
	lookupErr := func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") }
	d := newDiagnostics(prober, "https://thebloud.org", lookupErr)

	got := d.Check(context.Background())
	if got.HostResolves {
		t.Error("host should not have resolved")
	}
	if got.Diverged {
		t.Error("divergence needs the host to resolve first")
	}
}

// TestDNSDiagnostics_SkipsNonDNSAddresses: localhost, IP literals, and built-in
// `.local` names are not resolved through container DNS, so the check is not
// applicable.
func TestDNSDiagnostics_SkipsNonDNSAddresses(t *testing.T) {
	for _, publicURL := range []string{
		"http://localhost:8080",
		"http://127.0.0.1:3000",
		"http://10.0.0.21",
		"http://bloud.local",
		"http://foo.localhost",
		"",
	} {
		prober := &fakeProber{containers: runningTraefik()}
		d := newDiagnostics(prober, publicURL, resolvesTo("10.0.0.21"))
		got := d.Check(context.Background())
		if !got.Skipped {
			t.Errorf("%q: Skipped = false, want true", publicURL)
		}
		if got.Diverged {
			t.Errorf("%q: must not diverge when skipped", publicURL)
		}
	}
}

// TestDNSDiagnostics_PrefersTraefik: the system proxy is up whenever any app
// is, so it is probed before an arbitrary app container.
func TestDNSDiagnostics_PrefersTraefik(t *testing.T) {
	prober := &fakeProber{
		containers: []podman.Container{
			{Names: []string{"/apps-jellyfin"}, State: "running"},
			{Names: []string{"/apps-traefik"}, State: "running"},
		},
		execFn: resolvConfExec("10.0.0.1", "RESOLVED"),
	}
	d := newDiagnostics(prober, "https://bloud.example.com", resolvesTo("10.0.0.21"))

	got := d.Check(context.Background())
	if got.Container != "apps-traefik" {
		t.Errorf("container = %q, want apps-traefik", got.Container)
	}
	if anyCallFor(prober.calls, "apps-jellyfin") {
		t.Errorf("jellyfin was probed before traefik: %v", prober.calls)
	}
}

// TestDNSDiagnostics_IgnoresStoppedAndUnmanagedContainers: only running
// `apps-` containers are candidates.
func TestDNSDiagnostics_IgnoresStoppedAndUnmanagedContainers(t *testing.T) {
	prober := &fakeProber{
		containers: []podman.Container{
			{Names: []string{"/apps-jellyfin"}, State: "exited"},
			{Names: []string{"/other"}, State: "running"},
		},
		execFn: resolvConfExec("10.0.0.1", "RESOLVED"),
	}
	d := newDiagnostics(prober, "https://bloud.example.com", resolvesTo("10.0.0.21"))

	got := d.Check(context.Background())
	if got.Container != "" {
		t.Errorf("container = %q, want none", got.Container)
	}
	if !strings.Contains(got.Detail, "no running managed container") {
		t.Errorf("detail = %q, want the no-container explanation", got.Detail)
	}
}

// TestDNSDiagnostics_BoundsProbes: images without a resolver tool must not make
// the endpoint walk the whole container list.
func TestDNSDiagnostics_BoundsProbes(t *testing.T) {
	containers := []podman.Container{
		{Names: []string{"/apps-a"}, State: "running"},
		{Names: []string{"/apps-b"}, State: "running"},
		{Names: []string{"/apps-c"}, State: "running"},
		{Names: []string{"/apps-d"}, State: "running"},
	}
	prober := &fakeProber{containers: containers, execFn: resolvConfExec("8.8.8.8", "NO_RESOLVER")}
	d := newDiagnostics(prober, "https://bloud.example.com", resolvesTo("10.0.0.21"))

	d.Check(context.Background())
	probed := map[string]bool{}
	for _, call := range prober.calls {
		probed[strings.Fields(call)[0]] = true
	}
	if len(probed) > 3 {
		t.Errorf("probed %d containers, want at most 3", len(probed))
	}
}

// TestDNSDiagnostics_SkipsToTheContainerWithAResolver: a first container
// without a resolver tool must not end the search when a later one can answer.
func TestDNSDiagnostics_SkipsToTheContainerWithAResolver(t *testing.T) {
	prober := &fakeProber{
		containers: []podman.Container{
			{Names: []string{"/apps-minimal"}, State: "running"},
			{Names: []string{"/apps-chatty"}, State: "running"},
		},
		execFn: func(container string, cmd []string) ([]byte, error) {
			if cmd[0] == "cat" {
				return []byte("nameserver 10.0.0.1\n"), nil
			}
			if container == "apps-minimal" {
				return []byte("NO_RESOLVER\n"), nil
			}
			return []byte("UNRESOLVED\n"), nil
		},
	}
	d := newDiagnostics(prober, "https://thebloud.org", resolvesTo("10.0.0.21"))

	got := d.Check(context.Background())
	if got.Container != "apps-chatty" {
		t.Errorf("container = %q, want apps-chatty", got.Container)
	}
	if !got.Diverged {
		t.Error("the definitive container answer should drive Diverged")
	}
}

func TestParseNameservers(t *testing.T) {
	raw := []byte("# Generated\nsearch internal\nnameserver 169.254.1.1\nnameserver\t10.0.0.1 # router\noptions ndots:1\n")
	got := ParseNameservers(raw)
	want := []string{"169.254.1.1", "10.0.0.1"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ParseNameservers = %v, want %v", got, want)
	}
}

func TestHostnameOf(t *testing.T) {
	cases := map[string]string{
		"https://bloud.example.com:8443": "bloud.example.com",
		"bloud.example.com":              "bloud.example.com",
		"bloud.example.com:8443":         "bloud.example.com",
		"":                               "",
	}
	for in, want := range cases {
		if got := hostnameOf(in); got != want {
			t.Errorf("hostnameOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func anyCallFor(calls []string, name string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, name+" ") {
			return true
		}
	}
	return false
}
