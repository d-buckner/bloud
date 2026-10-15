// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/cli/portspec"
)

func samplePortsList() portspec.List {
	return portspec.List{
		Forwards: []portspec.One{
			{Guest: 3000, Host: 3000, Label: "host-agent API", Audience: "ops"},
			{Guest: 80, Host: 8080, Label: "Traefik: the user-facing port", Audience: "end users"},
			{Guest: 8096, Host: 8096, App: "jellyfin", Label: "Jellyfin", Audience: "debugging"},
		},
		Skipped: []portspec.Skipped{
			{Guest: 8080, App: "traefik", Reason: "host port 8080 already forwards Traefik"},
		},
	}
}

// The YAML block has to be valid input for the VM spec it lands in: a real
// `portForwards:` mapping, and `hostPort` only where the host port differs,
// because a redundant hostPort is one more line to keep true.
func TestRenderPortsYAML(t *testing.T) {
	block := generatedBlock{Begin: portsYAMLBeginMarker, End: portsYAMLEndMarker, Label: "debug port forwards"}
	out := renderPortsYAML(samplePortsList(), block)

	for _, want := range []string{
		portsYAMLBeginMarker,
		"portForwards:",
		"- guestPort: 3000",
		"- guestPort: 80",
		"hostPort: 8080",
		"# Not forwarded: 8080 (traefik: host port 8080 already forwards Traefik)",
		portsYAMLEndMarker,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("YAML block missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hostPort: 3000") {
		t.Errorf("YAML block states a hostPort that equals the guest port:\n%s", out)
	}
	if strings.Index(out, portsYAMLEndMarker) < strings.Index(out, "portForwards:") {
		t.Error("end marker precedes the mapping it closes")
	}
}

func TestRenderPortsTable(t *testing.T) {
	block := generatedBlock{Begin: portsTableBeginMarker, End: portsTableEndMarker, Label: "debug port table"}
	out := renderPortsTable(samplePortsList(), block)

	for _, want := range []string{
		portsTableBeginMarker,
		"| Port | What | Audience |",
		"| 3000 | host-agent API | ops |",
		"| 80 on host 8080 | Traefik: the user-facing port | end users |",
		"| 8096 | Jellyfin | debugging |",
		"Not forwarded: 8080 (traefik:",
		portsTableEndMarker,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

// A skipped port is only useful if it is named. An empty skip list must not
// leave a dangling heading in the document.
func TestRenderPortsSkippedOnlyWhenThereIsOne(t *testing.T) {
	block := generatedBlock{Begin: portsTableBeginMarker, End: portsTableEndMarker, Label: "debug port table"}
	out := renderPortsTable(portspec.List{Forwards: []portspec.One{{Guest: 1, Host: 1, Label: "x"}}}, block)
	if strings.Contains(out, "Not forwarded") {
		t.Errorf("table reports skips it does not have:\n%s", out)
	}
}

func TestSelectPortsTarget(t *testing.T) {
	targets := portsTargets()

	got, err := selectPortsTarget(targets, "dev/lima.yaml")
	if err != nil {
		t.Fatalf("selectPortsTarget() error = %v", err)
	}
	if len(got) != 1 || got[0].path != "dev/lima.yaml" {
		t.Errorf("selected %v, want only dev/lima.yaml", got)
	}
	if _, err := selectPortsTarget(targets, "README.md"); err == nil {
		t.Error("selectPortsTarget() accepted a file this command does not own")
	}
}

// The three documents are the ones that carried hand-written copies of the
// port list. If one loses its block, the command must say so rather than
// quietly write nothing.
func TestPortsTargetsAreTheDriftSites(t *testing.T) {
	got := map[string]portspec.Kind{}
	for _, tgt := range portsTargets() {
		got[tgt.path] = tgt.kind
	}
	for path, want := range map[string]portspec.Kind{
		"dev/lima.yaml": portspec.KindLima,
		"dev/qemu.yaml": portspec.KindQEMU,
		"AGENTS.md":     portspec.KindLima,
	} {
		if kind, ok := got[path]; !ok {
			t.Errorf("no target for %s", path)
		} else if kind != want {
			t.Errorf("%s kind = %v, want %v", path, kind, want)
		}
	}
	if len(got) != 3 {
		t.Errorf("targets = %v, want the three files that held copies", got)
	}
}

func TestParsePortsArgs(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		wantMode   portsMode
		wantTarget string
	}{
		{nil, portsPrint, ""},
		{[]string{"--write"}, portsWrite, ""},
		{[]string{"--check"}, portsCheck, ""},
		{[]string{"--check", "--target", "AGENTS.md"}, portsCheck, "AGENTS.md"},
		{[]string{"--target"}, portsHelp, ""},
		{[]string{"--nope"}, portsHelp, ""},
	} {
		mode, target := parsePortsArgs(tc.args)
		if mode != tc.wantMode || target != tc.wantTarget {
			t.Errorf("parsePortsArgs(%v) = (%v, %q), want (%v, %q)", tc.args, mode, target, tc.wantMode, tc.wantTarget)
		}
	}
}

// A write is a fixed point: writing the same list twice changes nothing, and
// `--check` after `--write` is green. That is what lets the fast tier gate the
// documents without a merge job to refresh them.
func TestPortsWriteThenCheck(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "doc.md")
	body := "before\n\n" + portsTableBeginMarker + "\nstale\n" + portsTableEndMarker + "\nafter\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	target := portsTarget{
		path:   "doc.md",
		block:  generatedBlock{Begin: portsTableBeginMarker, End: portsTableEndMarker, Label: "debug port table"},
		render: renderPortsTable,
	}
	generated := renderPortsTable(samplePortsList(), target.block)

	if checkPortsBlock(root, target, generated) {
		t.Error("checkPortsBlock() passed a stale block")
	}
	if !writePortsBlock(root, target, generated) {
		t.Fatal("writePortsBlock() failed")
	}
	if !checkPortsBlock(root, target, generated) {
		t.Error("checkPortsBlock() failed after a write")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, want := range []string{"before\n", "after\n"} {
		if !strings.Contains(string(content), want) {
			t.Errorf("write disturbed the hand-written text %q", want)
		}
	}

	// A second write of the same list is a no-op, byte for byte.
	before := string(content)
	if !writePortsBlock(root, target, generated) {
		t.Fatal("second writePortsBlock() failed")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if before != string(after) {
		t.Error("writing the same block twice changed the file")
	}
}

// A document with no markers is an error, not a silent append: a typo in the
// markers must not grow a second copy of the list at the end of the file.
func TestPortsWriteRefusesADocumentWithoutABlock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "doc.md")
	if err := os.WriteFile(path, []byte("nothing here\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	target := portsTarget{
		path:   "doc.md",
		block:  generatedBlock{Begin: portsTableBeginMarker, End: portsTableEndMarker, Label: "debug port table"},
		render: renderPortsTable,
	}
	if writePortsBlock(root, target, "x") {
		t.Error("writePortsBlock() succeeded without markers")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(content) != "nothing here\n" {
		t.Errorf("file was modified: %q", content)
	}
}

// The committed documents must be what the catalog produces. This is the same
// assertion `./bloud ports --check` makes, wired into `go test` so a metadata
// change cannot land without its documents.
func TestCommittedPortDocumentsAreCurrent(t *testing.T) {
	root := filepath.Join("..", "..")
	appsDir := filepath.Join(root, "apps")
	if _, err := os.Stat(appsDir); err != nil {
		t.Skipf("no catalog at %s", appsDir)
	}
	for _, target := range portsTargets() {
		list, err := portspec.Derive(appsDir, target.kind)
		if err != nil {
			t.Fatalf("Derive() error = %v", err)
		}
		if !checkPortsBlock(root, target, target.render(list, target.block)) {
			t.Errorf("%s is not what the catalog produces; run ./bloud ports --write", target.path)
		}
	}
}
