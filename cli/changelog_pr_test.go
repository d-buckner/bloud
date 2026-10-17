// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParsePREntries(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []changelogEntry
		err  string
	}{
		{
			name: "two bullets",
			body: "## What changes\n\nx\n\n## Changelog\n\n- Added: a time zone picker.\n- Fixed: the waiting page.\n\n## How\n",
			want: []changelogEntry{{Category: "Added", Text: "a time zone picker."}, {Category: "Fixed", Text: "the waiting page."}},
		},
		{
			name: "template instructions are comments, not entries",
			body: "## Changelog\n\n<!-- One bullet per user-visible change. -->\n<!-- The categories are Added, Changed, Fixed. -->\n\nn/a\n",
			want: nil,
		},
		{
			name: "wrapped bullet is one entry",
			body: "## Changelog\n\n- Added: a picker, so an operator on a laptop no longer\n  types a city name from memory.\n",
			want: []changelogEntry{{Category: "Added", Text: "a picker, so an operator on a laptop no longer types a city name from memory."}},
		},
		{
			name: "heading level and case do not matter",
			body: "### changelog\n\n- Fixed: a route.\n",
			want: []changelogEntry{{Category: "Fixed", Text: "a route."}},
		},
		{
			name: "a later heading ends the section",
			body: "## Changelog\n\n- Fixed: a route.\n\n## How this was verified\n\n- Not a bullet you should read.\n",
			want: []changelogEntry{{Category: "Fixed", Text: "a route."}},
		},
		{
			name: "no section at all",
			body: "## What changes\n\nx\n",
			err:  "has no `## Changelog` section",
		},
		{
			name: "section left as the template left it",
			body: "## Changelog\n\n<!-- One bullet per user-visible change. -->\n\n## How\n",
			err:  "section is empty",
		},
		{
			name: "prose instead of a bullet",
			body: "## Changelog\n\nThis changes the time zone picker.\n",
			err:  "is not a bullet",
		},
		{
			name: "bullet with no category",
			body: "## Changelog\n\n- a time zone picker\n",
			err:  "does not start with a category",
		},
		{
			name: "bullet with an unknown category",
			body: "## Changelog\n\n- Tweaked: a time zone picker\n",
			err:  "unknown category",
		},
		{
			name: "category with no text",
			body: "## Changelog\n\n- Added:\n",
			err:  "has no text",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePREntries(tc.body)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("entries = %d (%v), want %d", len(got), got, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("entry %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestWithPRRef(t *testing.T) {
	e := changelogEntry{Category: "Added", Text: "a picker."}
	if got := withPRRef(e, 365); got.Text != "a picker. (#365)" {
		t.Fatalf("got %q", got.Text)
	}
	kept := changelogEntry{Category: "Added", Text: "a picker (#12)."}
	if got := withPRRef(kept, 365); got.Text != "a picker (#12)." {
		t.Fatalf("an entry that names a number was rewritten: %q", got.Text)
	}
	if got := withPRRef(e, 0); got.Text != "a picker." {
		t.Fatalf("an unknown number was appended: %q", got.Text)
	}
}

func TestParseRepoSlug(t *testing.T) {
	cases := []struct{ url, want string }{
		{"https://github.com/d-buckner/bloud.git", "d-buckner/bloud"},
		{"https://github.com/d-buckner/bloud", "d-buckner/bloud"},
		{"git@github.com:d-buckner/bloud.git", "d-buckner/bloud"},
	}
	for _, tc := range cases {
		got, err := parseRepoSlug(tc.url)
		if err != nil || got != tc.want {
			t.Fatalf("parseRepoSlug(%q) = %q, %v; want %q", tc.url, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "nonsense"} {
		if _, err := parseRepoSlug(bad); err == nil {
			t.Fatalf("parseRepoSlug(%q) succeeded, want an error", bad)
		}
	}
}

// fakeTool answers the git calls the collection path makes, so the tests need no
// repository and no GitHub token.
func fakeTool(tags, subjects string) changelogCmd {
	return func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "tag --list"):
			return tags, nil
		case strings.Contains(joined, "log --format=%s"):
			return subjects, nil
		case strings.Contains(joined, "remote get-url"):
			return "https://github.com/d-buckner/bloud.git", nil
		}
		return "", fmt.Errorf("unexpected call: %s %s", name, joined)
	}
}

func TestNewestVersionTagIgnoresOtherTagFamilies(t *testing.T) {
	// The repository carries deb-<stamp> and iso-<stamp> tags, and one tag whose
	// name starts with the same letter as a version. None of them is a release.
	tags := "deb-20261017-175224\niso-20260314-044639\nvm-images\nv0.1.2\nv0.9.0\nv0.10.0\n"
	got, err := newestVersionTag(fakeTool(tags, ""), "/repo")
	if err != nil {
		t.Fatalf("newestVersionTag: %v", err)
	}
	if got != "v0.10.0" {
		t.Fatalf("newest tag = %q, want v0.10.0", got)
	}
	if got, _ := newestVersionTag(fakeTool("deb-1\n", ""), "/repo"); got != "" {
		t.Fatalf("newest tag = %q, want none", got)
	}
}

func TestMergedPRNumbers(t *testing.T) {
	// A revert names the commit it reverted, not a pull request, so it contributes
	// no number. The author of a revert writes the entry in their own pull request.
	subjects := "feat(web): a command module (#370)\nhermes: share the cache (#369)\nRevert \"hermes: keep the bank (#368)\"\nreadme: refresh a block\nfeat(web): a command module (#370)"
	got, err := mergedPRNumbers(fakeTool("", subjects), "/repo", "v0.1.0")
	if err != nil {
		t.Fatalf("mergedPRNumbers: %v", err)
	}
	want := []int{370, 369}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("numbers = %v, want %v", got, want)
	}
}

func TestCollectRelease(t *testing.T) {
	bodies := map[int]string{
		370: "## Changelog\n\n- Added: a command module.\n",
		369: "## Changelog\n\nn/a\n",
		368: "## Changelog\n\n- Fixed: the waiting page\n  swallows an app 503.\n",
	}
	result, err := collectRelease(collectOptions{
		root:    "/repo",
		run:     fakeTool("v0.1.0\n", "a command module (#370)\nshare the cache (#369)\nkeep the bank (#368)"),
		version: "v0.2.0",
		since:   "v0.1.0",
		now:     time.Date(2026, 2, 10, 13, 0, 0, 0, time.UTC),
		read: func(n int) (string, error) {
			body, ok := bodies[n]
			if !ok {
				return "", fmt.Errorf("no such PR: %d", n)
			}
			return body, nil
		},
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	rel := result.Release
	if rel.Version != "v0.2.0" || rel.Date != "2026-02-10" {
		t.Fatalf("release = %+v", rel)
	}
	if len(rel.Entries) != 2 {
		t.Fatalf("entries = %v, want two", rel.Entries)
	}
	if rel.Entries[0].Category != "Added" || !strings.HasSuffix(rel.Entries[0].Text, "(#370)") {
		t.Fatalf("first entry = %+v, want the Added entry tagged with #370", rel.Entries[0])
	}
	if rel.Entries[1].Category != "Fixed" || rel.Entries[1].Text != "the waiting page swallows an app 503. (#368)" {
		t.Fatalf("second entry = %+v", rel.Entries[1])
	}
}

// A merged pull request with no section stops the run: the section is the only
// copy of the entry, so skipping it would publish notes that omit a change.
func TestCollectReleaseFailsOnAMissingSection(t *testing.T) {
	_, err := collectRelease(collectOptions{
		root:    "/repo",
		run:     fakeTool("v0.1.0\n", "a command module (#370)\nnothing recorded (#369)"),
		version: "v0.2.0",
		since:   "v0.1.0",
		now:     time.Date(2026, 2, 10, 13, 0, 0, 0, time.UTC),
		read: func(n int) (string, error) {
			if n == 369 {
				return "## What changes\n\nnothing recorded\n", nil
			}
			return "## Changelog\n\n- Added: a command module.\n", nil
		},
	})
	if err == nil {
		t.Fatal("collect succeeded with a pull request that owes an entry")
	}
	if !strings.Contains(err.Error(), "#369") {
		t.Fatalf("error = %q, want it to name the pull request at fault", err)
	}
}

func TestCollectReleaseRefusesEmptyNotes(t *testing.T) {
	_, err := collectRelease(collectOptions{
		root:    "/repo",
		run:     fakeTool("v0.1.0\n", "#370"),
		version: "v0.2.0",
		since:   "v0.1.0",
		now:     time.Date(2026, 2, 10, 13, 0, 0, 0, time.UTC),
		read:    func(int) (string, error) { return "## Changelog\n\nn/a\n", nil },
	})
	if err == nil || !strings.Contains(err.Error(), "empty notes") {
		t.Fatalf("error = %v, want the empty-notes refusal", err)
	}
}
