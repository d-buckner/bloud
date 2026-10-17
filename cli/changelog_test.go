// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"
)

const changelogFixture = `# Changelog

The changes Bloud publishes, newest first.

## [v0.2.0] - 2026-02-10

### Added

- Add a searchable time zone picker (#365).

### Fixed

- Stop the waiting page from swallowing an app's own 503 (#311).

## [v0.1.0] - 2026-01-31

### Added

- Ship the instance as a Debian package, and install.sh installs the newest
  published one.
`

func TestParseChangelog(t *testing.T) {
	doc, err := parseChangelog(changelogFixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(doc.Header, "newest first") {
		t.Fatalf("header = %q, want the hand-written intro", doc.Header)
	}
	if len(doc.Releases) != 2 {
		t.Fatalf("releases = %d, want 2", len(doc.Releases))
	}
	head := doc.Releases[0]
	if head.Version != "v0.2.0" || head.Date != "2026-02-10" {
		t.Fatalf("head = %+v, want v0.2.0 2026-02-10", head)
	}
	if len(head.Entries) != 2 {
		t.Fatalf("head entries = %d, want 2", len(head.Entries))
	}
	if head.Entries[0].Category != "Added" || head.Entries[1].Category != "Fixed" {
		t.Fatalf("categories = %q, want Added then Fixed", []string{head.Entries[0].Category, head.Entries[1].Category})
	}
}

// A wrapped bullet is one entry, not two: the document wraps at a column count
// the same way every other tracked Markdown file does.
func TestParseChangelogJoinsWrappedEntry(t *testing.T) {
	doc, err := parseChangelog(changelogFixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	last := doc.Releases[1].Entries[0]
	want := "Ship the instance as a Debian package, and install.sh installs the newest published one."
	if last.Text != want {
		t.Fatalf("entry = %q, want %q", last.Text, want)
	}
}

func TestParseChangelogErrors(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "release heading without a date",
			content: "## [v1.0.0]\n\n### Added\n\n- x\n",
			want:    "is not a release heading",
		},
		{
			name:    "unknown group",
			content: "## [v1.0.0] - 2026-01-31\n\n### Tweaked\n\n- x\n",
			want:    "unknown group",
		},
		{
			name:    "group out of order",
			content: "## [v1.0.0] - 2026-01-31\n\n### Fixed\n\n- x\n\n### Added\n\n- y\n",
			want:    "repeats or is out of order",
		},
		{
			name:    "group repeated",
			content: "## [v1.0.0] - 2026-01-31\n\n### Added\n\n- x\n\n### Added\n\n- y\n",
			want:    "repeats or is out of order",
		},
		{
			name:    "entry with no group",
			content: "## [v1.0.0] - 2026-01-31\n\n- x\n",
			want:    "under no ### group",
		},
		{
			name:    "prose inside a release",
			content: "## [v1.0.0] - 2026-01-31\n\n### Added\n\n- x\n\nSome prose.\n",
			want:    "is not a release heading, a group heading, or an entry",
		},
		{
			name:    "indented line with nothing to continue",
			content: "## [v1.0.0] - 2026-01-31\n\n### Added\n\n  orphan\n",
			want:    "continues no entry",
		},
		{
			name:    "release heading missing its brackets",
			content: "## v1.0.0 - 2026-01-31\n\n### Added\n\n- x\n",
			want:    "is not a release heading",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseChangelog(tc.content)
			if err == nil {
				t.Fatalf("parse succeeded, want an error naming %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestValidateChangelog(t *testing.T) {
	doc, err := parseChangelog(changelogFixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if problems := validateChangelog(doc, "v0.2.0"); len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}

	empty := &changelogDoc{}
	if problems := validateChangelog(empty, ""); len(problems) == 0 {
		t.Fatal("an empty changelog validated clean")
	} else if !strings.Contains(problems[0], "no release section") {
		t.Fatalf("problems = %v, want the missing-section message", problems)
	}

	noEntries := &changelogDoc{Releases: []changelogRelease{{Version: "v1.0.0", Date: "2026-01-31"}}}
	if problems := validateChangelog(noEntries, ""); len(problems) == 0 {
		t.Fatal("a section with no entries validated clean")
	}

	outOfOrder := &changelogDoc{Releases: []changelogRelease{
		{Version: "v0.1.0", Date: "2026-01-31", Entries: []changelogEntry{{Category: "Added", Text: "x"}}},
		{Version: "v0.2.0", Date: "2026-02-10", Entries: []changelogEntry{{Category: "Added", Text: "y"}}},
	}}
	if problems := validateChangelog(outOfOrder, ""); len(problems) == 0 {
		t.Fatal("releases in the wrong order validated clean")
	}

	if problems := validateChangelog(doc, "v0.3.0"); len(problems) == 0 {
		t.Fatal("a tag with no section validated clean")
	} else if !strings.Contains(problems[0], "v0.3.0") {
		t.Fatalf("problems = %v, want it to name v0.3.0", problems)
	}
}

func TestRenderReleaseNotes(t *testing.T) {
	doc, err := parseChangelog(changelogFixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	head, _ := doc.newest()
	notes := renderReleaseNotes(head)
	if strings.Contains(notes, "v0.2.0") {
		t.Fatalf("notes carry the version heading, which the release title already prints:\n%s", notes)
	}
	if !strings.HasPrefix(notes, "### Added\n") {
		t.Fatalf("notes = %q, want them to open with the first group", notes)
	}
	if strings.Index(notes, "### Added") > strings.Index(notes, "### Fixed") {
		t.Fatalf("groups are out of order:\n%s", notes)
	}
}

// renderChangelog must be a fixed point: writing the file twice changes nothing,
// which is what lets `collect` run twice without growing the document.
func TestRenderChangelogIsAFixedPoint(t *testing.T) {
	doc, err := parseChangelog(changelogFixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	once := renderChangelog(doc)
	again, err := parseChangelog(once)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if renderChangelog(again) != once {
		t.Fatal("rendering the rendered document changed it")
	}
	if !strings.HasPrefix(once, "# Changelog\n") {
		t.Fatalf("rendered document lost its header:\n%s", once)
	}
}

func TestNextVersion(t *testing.T) {
	empty := &changelogDoc{}
	if v, err := nextVersion(empty, "patch"); err != nil || v != "v0.1.0" {
		t.Fatalf("first version = %q, %v; want v0.1.0", v, err)
	}
	doc, err := parseChangelog(changelogFixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := []struct{ bump, want string }{
		{"patch", "v0.2.1"},
		{"minor", "v0.3.0"},
		{"major", "v1.0.0"},
	}
	for _, tc := range cases {
		got, err := nextVersion(doc, tc.bump)
		if err != nil || got != tc.want {
			t.Fatalf("bump %s = %q, %v; want %q", tc.bump, got, err, tc.want)
		}
	}
	if _, err := nextVersion(doc, "huge"); err == nil {
		t.Fatal("an unknown bump succeeded")
	}
}

func TestSemverGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.1.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.1.1", false},
		{"vm-images", "v0.1.0", false},
	}
	for _, tc := range cases {
		if got := semverGreater(tc.a, tc.b); got != tc.want {
			t.Fatalf("semverGreater(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestInsertRelease(t *testing.T) {
	doc, err := parseChangelog(changelogFixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rel := changelogRelease{Version: "v0.3.0", Date: "2026-03-01", Entries: []changelogEntry{{Category: "Added", Text: "New thing."}}}
	out := insertRelease(doc, rel)
	if len(out.Releases) != 3 || out.Releases[0].Version != "v0.3.0" {
		t.Fatalf("insert did not put the new section on top: %v", out.Releases)
	}
	if len(doc.Releases) != 2 {
		t.Fatal("insertRelease mutated the document it was given")
	}
	if problems := validateChangelog(out, "v0.3.0"); len(problems) != 0 {
		t.Fatalf("problems after insert = %v", problems)
	}
}

func TestDebianVersion(t *testing.T) {
	cases := []struct{ describe, want string }{
		{"v0.1.0", "0.1.0"},
		{"v0.1.0-5-gabc1234", "0.1.0-5-gabc1234"},
		{"deb-20261017-175224", "0.0.0+deb-20261017-175224"},
		{"f1a025d", "0.0.0+f1a025d"},
		{"", "0.1.0"},
	}
	for _, tc := range cases {
		if got := debianVersion(tc.describe); got != tc.want {
			t.Fatalf("debianVersion(%q) = %q, want %q", tc.describe, got, tc.want)
		}
	}
}
