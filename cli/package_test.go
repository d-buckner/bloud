// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

// withEnv must override an inherited key (not append a duplicate, which a
// child process would resolve ambiguously) while leaving other keys untouched.
func TestWithEnvOverridesAndAdds(t *testing.T) {
	got := withEnv([]string{"PATH=/bin", "GOOS=darwin"}, "GOOS=linux", "GOTOOLCHAIN=auto")

	seen := map[string]int{}
	for _, e := range got {
		key, value, _ := strings.Cut(e, "=")
		seen[key]++
		switch key {
		case "PATH":
			if value != "/bin" {
				t.Fatalf("PATH = %q, want /bin", value)
			}
		case "GOOS":
			if value != "linux" {
				t.Fatalf("GOOS = %q, want linux", value)
			}
		case "GOTOOLCHAIN":
			if value != "auto" {
				t.Fatalf("GOTOOLCHAIN = %q, want auto", value)
			}
		}
	}

	for _, key := range []string{"PATH", "GOOS", "GOTOOLCHAIN"} {
		if seen[key] != 1 {
			t.Fatalf("%s appears %d times, want 1: %v", key, seen[key], got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("env = %v, want 3 entries", got)
	}
}

// renderedDepends renders packaging/nfpm.yaml.tmpl exactly as runPackage does
// and returns the Depends list the built .deb would carry.
func renderedDepends(t *testing.T) []string {
	t.Helper()

	root, err := getProjectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "packaging", "nfpm.yaml.tmpl"))
	if err != nil {
		t.Fatalf("read package template: %v", err)
	}

	tmpl, err := template.New("nfpm").Parse(string(raw))
	if err != nil {
		t.Fatalf("parse package template: %v", err)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, struct{ Version, Arch string }{"9.9.9", "amd64"}); err != nil {
		t.Fatalf("execute package template: %v", err)
	}

	var control struct {
		Depends []string `yaml:"depends"`
	}
	if err := yaml.Unmarshal([]byte(buf.String()), &control); err != nil {
		t.Fatalf("rendered control is not valid YAML: %v", err)
	}
	if len(control.Depends) == 0 {
		t.Fatal("package template declares no depends")
	}
	return control.Depends
}

// depAlternatives splits one Depends entry into its alternative package names,
// dropping any version constraint: "passt | slirp4netns" and
// "podman (>= 5.0.0)" both come back as plain names.
func depAlternatives(dep string) []string {
	parts := strings.Split(dep, "|")
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		name := strings.TrimSpace(strings.SplitN(p, "(", 2)[0])
		names = append(names, name)
	}
	return names
}

// depConstraint returns the version constraint of one Depends entry without
// the surrounding parentheses, or "" when the entry is unversioned.
func depConstraint(dep string) string {
	open := strings.Index(dep, "(")
	closeIdx := strings.Index(dep, ")")
	if open < 0 || closeIdx < open {
		return ""
	}
	return strings.TrimSpace(dep[open+1 : closeIdx])
}

func dependsForPackage(depends []string, pkg string) []string {
	var out []string
	for _, dep := range depends {
		for _, name := range depAlternatives(dep) {
			if name == pkg {
				out = append(out, dep)
			}
		}
	}
	return out
}

// The podman floor is not decoration: internal/podman/client.go addresses the
// libpod API as /v5.0.0/..., which podman 4.x (Debian 12) does not serve, so
// an unversioned dependency would install a runtime that fails every call.
func TestDependsPinPodmanToTheLibpodAPIFloor(t *testing.T) {
	matches := dependsForPackage(renderedDepends(t), "podman")
	if len(matches) != 1 {
		t.Fatalf("podman declared %d times, want exactly 1: %v", len(matches), matches)
	}

	constraint := depConstraint(matches[0])
	if constraint != ">= 5.0.0" {
		t.Fatalf("podman constraint = %q, want \">= 5.0.0\" (the libpod API the host agent calls)", constraint)
	}
}

// Each of these is invoked directly by host-agent or the maintainer scripts.
// None of them is guaranteed by another package: they are podman's own
// Recommends, which an --no-install-recommends install drops.
func TestDependsCoverTheRootlessRuntime(t *testing.T) {
	depends := renderedDepends(t)

	required := []string{
		"podman",            // container runtime, CLI and libpod socket
		"uidmap",            // newuidmap/newgidmap for the user namespace
		"dbus-user-session", // rootless podman's user session bus
		"ca-certificates",   // TLS trust for registry pulls
		"adduser",           // postinst/postrm user lifecycle
		"systemd",           // user manager, linger, sysctl drop-in
	}
	for _, pkg := range required {
		if len(dependsForPackage(depends, pkg)) == 0 {
			t.Errorf("depends is missing %s (declared: %v)", pkg, depends)
		}
	}

	// Rootless networking: pasta (passt) or the slirp4netns fallback. One
	// alternative dependency, so either name satisfies the runtime.
	if len(dependsForPackage(depends, "passt")) == 0 && len(dependsForPackage(depends, "slirp4netns")) == 0 {
		t.Errorf("depends declares no rootless network provider (passt or slirp4netns): %v", depends)
	}
}

// A malformed dependency string is a broken package on every user's box, and
// nfpm writes these entries into the control file verbatim.
func TestDependsAreWellFormedDebianRelationships(t *testing.T) {
	nameRe := regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)
	versionRe := regexp.MustCompile(`^[A-Za-z0-9.+~:-]+$`)
	ops := map[string]bool{"=": true, ">=": true, "<=": true, ">>": true, "<<": true}

	for _, dep := range renderedDepends(t) {
		for _, alt := range strings.Split(dep, "|") {
			alt = strings.TrimSpace(alt)
			name := strings.TrimSpace(strings.SplitN(alt, "(", 2)[0])
			if !nameRe.MatchString(name) {
				t.Errorf("%q: package name %q is not a valid Debian package name", dep, name)
				continue
			}

			constraint := depConstraint(alt)
			if constraint == "" {
				continue
			}
			op, version, found := strings.Cut(constraint, " ")
			if !found || !ops[op] || !versionRe.MatchString(version) {
				t.Errorf("%q: constraint %q is not \"<op> <version>\" with a Debian comparison operator", dep, constraint)
			}
			if strings.Count(alt, "(") > 1 {
				t.Errorf("%q: more than one version constraint on one alternative", dep)
			}
		}
	}
}

// Duplicate names across Depends entries are a control-file defect: dpkg
// reports them as separate relationships and apt dedupes silently, which
// hides a second, conflicting constraint on the same package.
func TestDependsHaveNoDuplicatePackages(t *testing.T) {
	seen := map[string]int{}
	for _, dep := range renderedDepends(t) {
		for _, name := range depAlternatives(dep) {
			seen[name]++
		}
	}
	for name, count := range seen {
		if count > 1 {
			t.Errorf("package %s declared %d times in depends, want 1", name, count)
		}
	}
}

// The template must keep rendering with the fields runPackage passes it; a
// renamed placeholder fails at package time otherwise.
func TestPackageTemplateRendersResolvedVersionAndArch(t *testing.T) {
	root, err := getProjectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "packaging", "nfpm.yaml.tmpl"))
	if err != nil {
		t.Fatalf("read package template: %v", err)
	}
	tmpl := template.Must(template.New("nfpm").Parse(string(raw)))

	var buf strings.Builder
	if err := tmpl.Execute(&buf, struct{ Version, Arch string }{"1.2.3", "arm64"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var control struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
		Arch    string `yaml:"arch"`
	}
	if err := yaml.Unmarshal([]byte(buf.String()), &control); err != nil {
		t.Fatalf("rendered control is not valid YAML: %v", err)
	}
	if control.Name != "bloud" {
		t.Errorf("name = %q, want bloud", control.Name)
	}
	if got := fmt.Sprintf("%s/%s", control.Version, control.Arch); got != "1.2.3/arm64" {
		t.Errorf("rendered version/arch = %s, want 1.2.3/arm64", got)
	}
}
