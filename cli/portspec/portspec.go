// SPDX-License-Identifier: AGPL-3.0-only

// Package portspec derives the debug port forwards a dev runtime exposes.
//
// The same port number used to live in five places: an app's metadata.yaml,
// each backend's port map, the QEMU launch arguments, dev/lima.yaml, and
// AGENTS.md. Every copy was internally consistent, so no check could catch a
// copy that was wrong. Two of them were. The QEMU launch list omitted guest
// 8000 and 8222 while the port map advertised both, so those debug ports were
// unreachable and the map was the reason nobody noticed, and the four MCP
// wrappers added later were in no forward list at all.
//
// This package is the one place that answer lives. The catalog is the source:
// `port:` is the port an app serves, and `containers[].ports[].host` is every
// other port an app publishes. The backends, the generated dev/*.yaml blocks,
// and the generated AGENTS.md table all read the list from here, so a new app
// gets its debug port the same moment it gets a metadata.yaml.
package portspec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kind selects the backend shape the list is built for. The app ports are the
// same everywhere; what differs is the access a backend already has by other
// means.
type Kind int

const (
	// KindLima is a Lima VM. Lima forwards SSH itself, so the list carries no
	// SSH entry.
	KindLima Kind = iota
	// KindQEMU is a hand-launched QEMU process. Its only path to the guest is
	// the slirp forward list, so SSH is entry one.
	KindQEMU
)

// One is one guest port to publish on the host.
type One struct {
	// Guest is the port inside the runtime. It is never remapped.
	Guest int
	// Host is the port the host binds. It defaults to Guest, and the QEMU
	// backend lets BLOUD_QEMU_FWD_<guest> override it.
	Host int
	// App is the catalog id the port belongs to, empty for instance
	// infrastructure.
	App string
	// Label says what answers on the port, for the generated documents.
	Label string
	// Audience names who dials the port. The public entrypoint is the one port
	// an end user dials; everything else is an operator or a debugging port.
	Audience string
}

// Skipped records a declared port that gets no forward, and why. A port that
// silently disappears is the failure mode this package exists to end, so the
// reason is returned rather than dropped.
type Skipped struct {
	Guest  int
	App    string
	Reason string
}

// List is the derived answer for one backend.
type List struct {
	Forwards []One
	Skipped  []Skipped
}

// appMetadata is the slice of an app's metadata.yaml this package reads. The
// field names mirror the catalog's own struct; everything else is irrelevant
// to a port list.
type appMetadata struct {
	Name        string `yaml:"name"`
	DisplayName string `yaml:"displayName"`
	Port        int    `yaml:"port"`
	Containers  []struct {
		Name  string `yaml:"name"`
		Ports []struct {
			Host int `yaml:"host"`
		} `yaml:"ports"`
	} `yaml:"containers"`
}

// declared is one port one app declares, before host-port collisions are
// resolved. A zero host means "publish it on its own number".
type declared struct {
	guest    int
	host     int
	app      string
	label    string
	audience string
}

// Derive reads the catalog under appsDir and returns the debug forwards for
// one backend shape, in display order: infrastructure first, then every app
// port ascending.
//
// A declared port whose host port is already taken is skipped and reported.
// The case that happens today is Traefik's own dashboard on guest 8080: the
// public entrypoint (guest 80) is already published on host 8080, and two
// forwards on one host port is a QEMU startup error rather than a warning.
func Derive(appsDir string, kind Kind) (List, error) {
	apps, err := readCatalog(appsDir)
	if err != nil {
		return List{}, err
	}

	list := List{}
	owner := map[int]string{}
	for _, c := range append(infra(kind), apps...) {
		host := c.host
		if host == 0 {
			host = c.guest
		}
		if held, taken := owner[host]; taken {
			list.Skipped = append(list.Skipped, Skipped{
				Guest:  c.guest,
				App:    c.app,
				Reason: fmt.Sprintf("host port %d already forwards %s", host, held),
			})
			continue
		}
		owner[host] = c.label
		list.Forwards = append(list.Forwards, One{
			Guest:    c.guest,
			Host:     host,
			App:      c.app,
			Label:    c.label,
			Audience: c.audience,
		})
	}
	return list, nil
}

// infra returns the ports the runtime itself owns, in the order they are
// listed. The public entrypoint is guest 80 published on host 8080: a real
// deployment serves :80 directly, and the dev runtimes and the e2e suite stay
// on http://localhost:8080 so the two agree.
func infra(kind Kind) []declared {
	out := []declared{}
	if kind == KindQEMU {
		out = append(out, declared{guest: 22, host: 2222, label: "SSH to the guest", audience: "ops"})
	}
	return append(out,
		declared{guest: 3000, label: "host-agent API", audience: "ops"},
		declared{guest: 80, host: 8080, label: "Traefik: the user-facing port", audience: "end users"},
	)
}

// readCatalog returns every declared app port, ascending by guest port and
// then by app name, so the list is the same on every machine and in every
// generated document.
func readCatalog(appsDir string) ([]declared, error) {
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		return nil, fmt.Errorf("read apps dir %s: %w", appsDir, err)
	}

	var out []declared
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		appPorts, err := readApp(filepath.Join(appsDir, entry.Name(), "metadata.yaml"), entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, appPorts...)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].guest != out[j].guest {
			return out[i].guest < out[j].guest
		}
		return out[i].app < out[j].app
	})
	return out, nil
}

// readApp returns the ports one app declares: its own `port:`, plus every
// container host port that is not that same number. A directory with no
// metadata.yaml is not an app and contributes nothing; deciding whether a
// declaration is malformed is the catalog loader's job, so this package only
// reports what it cannot read at all.
func readApp(path, dirName string) ([]declared, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var m appMetadata
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	name := m.Name
	if name == "" {
		name = dirName
	}
	display := m.DisplayName
	if display == "" {
		display = name
	}

	out := []declared{}
	if m.Port > 0 {
		out = append(out, declared{guest: m.Port, app: name, label: display, audience: "debugging"})
	}
	for _, c := range m.Containers {
		for _, p := range c.Ports {
			if p.Host <= 0 || p.Host == m.Port {
				continue
			}
			label := display
			if word := containerWord(c.Name, name); word != "" {
				label = fmt.Sprintf("%s (%s)", display, word)
			}
			out = append(out, declared{
				guest:    p.Host,
				app:      name,
				label:    label,
				audience: "debugging",
			})
		}
	}
	return out, nil
}

// containerWord turns a container node name into the short phrase that follows
// an app name in a port table: "ldap" rather than "apps-authentik-ldap". A node
// name is a container name, not a label, so the app's own prefix is trimmed off
// and the rest is shown. When nothing distinct is left, the container is the app
// itself and the port needs no qualifier.
func containerWord(node, app string) string {
	trimmed := strings.TrimPrefix(node, "apps-")
	trimmed = strings.TrimPrefix(trimmed, app+"-")
	if trimmed == "" || trimmed == node || trimmed == app {
		return ""
	}
	return strings.ReplaceAll(trimmed, "-", " ")
}
