// SPDX-License-Identifier: AGPL-3.0-only

package system

import (
	"context"
	"net"
	"net/url"
	"sort"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
)

// ContainerProber is the subset of the podman client the DNS diagnostic needs:
// list the running containers and run a command inside one.
type ContainerProber interface {
	ListContainers(ctx context.Context) ([]podman.Container, error)
	ExecWithEnv(ctx context.Context, containerName string, env map[string]string, cmd []string) ([]byte, error)
}

// DNSDiagnostic is the result of one container DNS check.
//
// It exists because podman's rootless network sandbox captures the host
// resolv.conf once, when the sandbox is first created after boot. If that
// capture races DHCP, the sandbox bakes in public fallback resolvers and
// forwards every non-local query to them for the sandbox's lifetime. A name
// that resolves only through the local router (an apex with an override but no
// public record, for example) then resolves on the host and fails inside every
// container, and the only symptom Bloud showed was the app that happened to
// need the name parking in error. This diagnostic names the condition instead.
type DNSDiagnostic struct {
	// Host is the hostname resolved from the instance's configured public URL.
	Host string `json:"host"`
	// Skipped is true when the public address is not a DNS name containers
	// would resolve (localhost, an IP literal, a built-in `.local` name), so
	// there is nothing to compare.
	Skipped bool `json:"skipped"`
	// HostResolves is whether the host can resolve Host.
	HostResolves bool `json:"hostResolves"`
	// Container is the managed container that was probed.
	Container string `json:"container,omitempty"`
	// ContainerChecked is true when a resolver tool ran inside Container and
	// gave a definitive answer. False means the probe was inconclusive (no
	// `getent`/`nslookup` in the image, or no running container), never that
	// resolution failed.
	ContainerChecked bool `json:"containerChecked"`
	// ContainerResolves is whether the resolver inside Container resolved Host.
	// It is only meaningful when ContainerChecked is true.
	ContainerResolves bool `json:"containerResolves"`
	// ContainerNameservers is what the probed container's /etc/resolv.conf
	// lists, the "observed sandbox upstreams" the diagnosis needs.
	ContainerNameservers []string `json:"containerNameservers,omitempty"`
	// Diverged is the condition worth a warning: the host resolves Host and the
	// container definitively does not.
	Diverged bool `json:"diverged"`
	// Detail is a one-line human-readable summary.
	Detail string `json:"detail,omitempty"`
}

// DNSDiagnostics runs the container DNS check. The zero value is not usable;
// build one with NewDNSDiagnostics.
type DNSDiagnostics struct {
	client    ContainerProber
	publicURL func() string
	lookup    func(ctx context.Context, host string) ([]string, error)
	// maxProbes bounds how many containers are tried before giving up on a
	// definitive answer, so a fleet of images without a resolver tool cannot
	// make the endpoint walk every container.
	maxProbes int
}

// NewDNSDiagnostics builds the check against the live podman client and a
// getter for the instance's configured public URL. publicURL is a function so
// an address change made in Settings is picked up on the next check.
func NewDNSDiagnostics(client ContainerProber, publicURL func() string) *DNSDiagnostics {
	return &DNSDiagnostics{
		client:    client,
		publicURL: publicURL,
		lookup:    net.DefaultResolver.LookupHost,
		maxProbes: 3,
	}
}

// Check resolves the configured public host on the host and inside a running
// managed container and reports whether the two disagree.
func (d *DNSDiagnostics) Check(ctx context.Context) DNSDiagnostic {
	rawURL := ""
	if d.publicURL != nil {
		rawURL = d.publicURL()
	}
	out := DNSDiagnostic{Host: hostnameOf(rawURL)}
	if d.client == nil {
		out.Detail = "no container runtime is wired"
		return out
	}
	if out.Host == "" || isLocalHostname(out.Host) {
		out.Skipped = true
		out.Detail = "the public address is not a hostname containers resolve"
		return out
	}

	out.HostResolves = d.hostResolves(ctx, out.Host)
	var detail string
	out.Container, out.ContainerNameservers, out.ContainerChecked, out.ContainerResolves, detail = d.probeManaged(ctx, out.Host)
	if detail != "" {
		out.Detail = detail
		return out
	}
	out.Diverged = out.HostResolves && out.ContainerChecked && !out.ContainerResolves
	out.Detail = describeDNS(out)
	return out
}

// hostResolves reports whether the host itself can resolve host.
func (d *DNSDiagnostics) hostResolves(ctx context.Context, host string) bool {
	if d.lookup == nil {
		return false
	}
	addrs, err := d.lookup(ctx, host)
	return err == nil && len(addrs) > 0
}

// probeManaged resolves host inside the first running managed container that
// has a resolver tool. The first probed container's nameservers are returned
// even when no container gives a definitive answer, so the observed sandbox
// upstreams are still reported. detail is non-empty when no probe was possible
// at all (no container, or the list failed).
func (d *DNSDiagnostics) probeManaged(ctx context.Context, host string) (container string, nameservers []string, checked, resolves bool, detail string) {
	containers, err := d.runningManaged(ctx)
	if err != nil {
		return "", nil, false, false, "listing containers: " + err.Error()
	}
	if len(containers) == 0 {
		return "", nil, false, false, "no running managed container to probe"
	}
	for i, name := range containers {
		if i >= d.maxProbes {
			break
		}
		probe := d.probeContainer(ctx, name, host)
		if container == "" {
			container, nameservers = name, probe.nameservers
		}
		if probe.checked {
			return name, probe.nameservers, true, probe.resolves, ""
		}
	}
	return container, nameservers, false, false, ""
}

// describeDNS renders the one-line summary for a completed check.
func describeDNS(out DNSDiagnostic) string {
	switch {
	case out.Diverged:
		return "the public host resolves on the host but not inside " + out.Container +
			"; the container resolver may have captured a stale upstream when the podman sandbox was created"
	case out.ContainerChecked && out.ContainerResolves:
		return "the public host resolves on the host and inside the container"
	case out.ContainerChecked:
		return "the public host does not resolve on the host or inside the container"
	default:
		return "no resolver tool (getent, nslookup) in the probed container"
	}
}

// runningManaged returns the names of the running Bloud-managed containers,
// with Traefik first: it is the system proxy, so it is up whenever any app is.
func (d *DNSDiagnostics) runningManaged(ctx context.Context) ([]string, error) {
	containers, err := d.client.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(containers))
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		name := containerName(c.Names)
		if strings.HasPrefix(name, "apps-") {
			names = append(names, name)
		}
	}
	sort.SliceStable(names, func(i, j int) bool {
		return names[i] == "apps-traefik" && names[j] != "apps-traefik"
	})
	return names, nil
}

// containerProbe is one container's answer.
type containerProbe struct {
	nameservers []string
	checked     bool
	resolves    bool
}

// resolveScript picks the first resolver tool the image has and prints a single
// unambiguous token. It exists so the check never has to read an exit status
// out of Exec's combined output: `getent` and `nslookup` both exit non-zero for
// "not found" and for "not installed", which are different answers.
const resolveScript = `for t in getent nslookup; do ` +
	`command -v "$t" >/dev/null 2>&1 && { "$t" "$1" >/dev/null 2>&1 && echo RESOLVED || echo UNRESOLVED; exit 0; }; ` +
	`done; echo NO_RESOLVER`

func (d *DNSDiagnostics) probeContainer(ctx context.Context, name, host string) containerProbe {
	var p containerProbe
	if out, err := d.client.ExecWithEnv(ctx, name, nil, []string{"cat", "/etc/resolv.conf"}); err == nil {
		p.nameservers = ParseNameservers(out)
	}
	out, err := d.client.ExecWithEnv(ctx, name, nil, []string{"sh", "-c", resolveScript, "sh", host})
	if err != nil {
		return p
	}
	p.checked, p.resolves = parseResolution(out)
	return p
}

// ParseNameservers extracts the resolver addresses from a resolv.conf body.
func ParseNameservers(raw []byte) []string {
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			out = append(out, fields[1])
		}
	}
	return out
}

// parseResolution reads the single token resolveScript prints. It scans from
// the end so noise on the combined stream cannot shadow the answer.
func parseResolution(out []byte) (checked, resolves bool) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		switch strings.TrimSpace(lines[i]) {
		case "RESOLVED":
			return true, true
		case "UNRESOLVED":
			return true, false
		case "NO_RESOLVER":
			return false, false
		}
	}
	return false, false
}

// containerName returns the bare container name from podman's leading-slash
// form.
func containerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// hostnameOf reads the host out of a public origin. A value that is not a URL
// is returned trimmed of any port, so a bare configured hostname still works.
func hostnameOf(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	if host, _, err := net.SplitHostPort(rawURL); err == nil {
		return host
	}
	return rawURL
}

// isLocalHostname reports whether host is not something containers resolve
// through DNS: loopback, an IP literal, or a built-in `.local` name.
func isLocalHostname(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if strings.HasSuffix(host, ".local") {
		return true
	}
	return net.ParseIP(host) != nil
}
