// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// changelogFile is the document this command owns. Every published release gets
// one section here, and the release workflow renders that section as the GitHub
// release notes. The file is therefore both the history a visitor reads and the
// text a release page shows, which is the reason its format is strict.
const changelogFile = "CHANGELOG.md"

// changelogCategories are the Keep a Changelog groups, in the order a release
// section prints them. The set is closed on purpose: a free-form group name is
// a second way to spell an existing one, and the notes then sort nothing into a
// useful order.
var changelogCategories = []string{"Added", "Changed", "Deprecated", "Removed", "Fixed", "Security"}

// changelogNoEntry is the one answer a pull request may give when the change has
// no user-visible effect. It is data, not a comment: a reviewer can tell "the
// author considered this and found nothing" apart from "the author skipped the
// section".
const changelogNoEntry = "n/a"

// The line shapes a release section may hold. Anything else is a format error
// rather than prose to be preserved, because a machine reads this file to build
// release notes and silently ignores what it cannot parse.
var (
	changelogReleaseRe = regexp.MustCompile(`^## \[(v[0-9]+\.[0-9]+\.[0-9]+)\] - ([0-9]{4}-[0-9]{2}-[0-9]{2})$`)
	changelogGroupRe   = regexp.MustCompile(`^### ([A-Za-z]+)$`)
	changelogEntryRe   = regexp.MustCompile(`^[-*] (.+)$`)
)

// isContinuation reports whether a line continues the entry above it. Markdown
// renders an indented line as part of the bullet, so a document that wraps at a
// column count can hold one long entry on several lines.
func isContinuation(line string) bool {
	return line != "" && (line[0] == ' ' || line[0] == '\t')
}

// changelogEntry is one bullet: a category and the text after it.
type changelogEntry struct {
	Category string
	Text     string
}

// changelogRelease is one released version, its date, and its entries.
type changelogRelease struct {
	Version string
	Date    string
	Entries []changelogEntry
}

// changelogDoc is the whole file: the hand-written header above the first
// release, and the releases newest first.
type changelogDoc struct {
	Header   string
	Releases []changelogRelease
}

// categoryIndex returns the position of a group in the canonical order, or -1
// when the name is not a group this format allows.
func categoryIndex(name string) int {
	for i, c := range changelogCategories {
		if strings.EqualFold(c, name) {
			return i
		}
	}
	return -1
}

// changelogParser carries the state one pass over the release sections keeps:
// the section being filled, and which group its entries fall under.
type changelogParser struct {
	doc       *changelogDoc
	cur       *changelogRelease
	lastGroup int
}

// parseChangelog reads the document. It reports the first format problem with
// its line number, because a changelog that does not parse produces no release
// notes, and a silent empty release is the failure this file exists to prevent.
func parseChangelog(content string) (*changelogDoc, error) {
	lines := strings.Split(content, "\n")
	if err := checkReleaseHeadings(lines); err != nil {
		return nil, err
	}
	doc := &changelogDoc{}
	first := indexOfFirstRelease(lines)
	doc.Header = strings.Join(lines[:first], "\n")

	p := &changelogParser{doc: doc, lastGroup: -1}
	for i := first; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		if line == "" {
			continue
		}
		if err := p.line(line, i+1); err != nil {
			return nil, err
		}
	}
	p.flush()
	return doc, nil
}

// line sends one non-blank line to the case that owns its shape.
func (p *changelogParser) line(line string, num int) error {
	switch {
	case strings.HasPrefix(line, "## "):
		return p.release(line, num)
	case strings.HasPrefix(line, "### "):
		return p.group(line, num)
	case changelogEntryRe.MatchString(line):
		return p.entry(line, num)
	case isContinuation(line):
		return p.continuation(line, num)
	default:
		return fmt.Errorf("line %d: %q is not a release heading, a group heading, or an entry", num, line)
	}
}

// release starts one section and closes the section before it.
func (p *changelogParser) release(line string, num int) error {
	rel, err := parseReleaseHeader(line, num)
	if err != nil {
		return err
	}
	p.flush()
	p.cur = rel
	p.lastGroup = -1
	return nil
}

// group opens one category inside the current section.
func (p *changelogParser) group(line string, num int) error {
	if p.cur == nil {
		return fmt.Errorf("line %d: a group heading appears before the first release", num)
	}
	idx, err := parseGroupHeader(line, num, p.lastGroup)
	if err != nil {
		return err
	}
	p.lastGroup = idx
	return nil
}

// entry adds one bullet to the current group.
func (p *changelogParser) entry(line string, num int) error {
	if p.cur == nil {
		return fmt.Errorf("line %d: an entry appears before the first release", num)
	}
	if p.lastGroup < 0 {
		return fmt.Errorf("line %d: an entry appears under no ### group", num)
	}
	text := strings.TrimSpace(changelogEntryRe.FindStringSubmatch(line)[1])
	if text == "" {
		return fmt.Errorf("line %d: an entry has no text", num)
	}
	p.cur.Entries = append(p.cur.Entries, changelogEntry{
		Category: changelogCategories[p.lastGroup],
		Text:     text,
	})
	return nil
}

// continuation appends an indented line to the entry above it.
func (p *changelogParser) continuation(line string, num int) error {
	if p.cur == nil || len(p.cur.Entries) == 0 {
		return fmt.Errorf("line %d: an indented line continues no entry", num)
	}
	last := len(p.cur.Entries) - 1
	p.cur.Entries[last].Text += " " + strings.TrimSpace(line)
	return nil
}

// flush closes the section that was being filled.
func (p *changelogParser) flush() {
	if p.cur == nil {
		return
	}
	p.doc.Releases = append(p.doc.Releases, *p.cur)
	p.cur = nil
}

// changelogNearMissRe matches a `## ` heading that is trying to be a release
// heading: a bracketed one, or one that starts with a v and a digit. A heading
// like `## Version history` is prose and stays out of it.
var changelogNearMissRe = regexp.MustCompile(`^## (\[|v[0-9])`)

// checkReleaseHeadings reports a version heading that is almost right. Without
// this pass a heading with a typo makes indexOfFirstRelease find no releases, and
// the whole file reads as a header, so the report says "no release section" about
// a document that plainly has one.
func checkReleaseHeadings(lines []string) error {
	for i, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		if changelogReleaseRe.MatchString(line) || !changelogNearMissRe.MatchString(line) {
			continue
		}
		return fmt.Errorf("line %d: %q is not a release heading of the form `## [v1.2.3] - 2026-01-31`", i+1, line)
	}
	return nil
}

// indexOfFirstRelease returns the line the releases start on, so everything
// above it stays the hand-written header.
func indexOfFirstRelease(lines []string) int {
	for i, line := range lines {
		if changelogReleaseRe.MatchString(strings.TrimRight(line, " \t")) {
			return i
		}
	}
	return len(lines)
}

// parseReleaseHeader reads one `## [v1.2.3] - 2026-01-31` line.
func parseReleaseHeader(line string, num int) (*changelogRelease, error) {
	m := changelogReleaseRe.FindStringSubmatch(line)
	if m == nil {
		return nil, fmt.Errorf("line %d: %q is not a release heading of the form `## [v1.2.3] - 2026-01-31`", num, line)
	}
	return &changelogRelease{Version: m[1], Date: m[2]}, nil
}

// parseGroupHeader reads one `### Added` line and enforces the canonical order:
// a group may appear once, and only after the group before it. The rule keeps
// every section readable in the same shape, which is what lets a reader scan
// three releases without re-learning the layout.
func parseGroupHeader(line string, num, lastGroup int) (int, error) {
	m := changelogGroupRe.FindStringSubmatch(line)
	if m == nil {
		return 0, fmt.Errorf("line %d: %q is not a group heading of the form `### Added`", num, line)
	}
	idx := categoryIndex(m[1])
	if idx < 0 {
		return 0, fmt.Errorf("line %d: unknown group %q (use one of %s)", num, m[1], strings.Join(changelogCategories, ", "))
	}
	if idx <= lastGroup {
		return 0, fmt.Errorf("line %d: group %q repeats or is out of order (groups go: %s)", num, m[1], strings.Join(changelogCategories, " / "))
	}
	return idx, nil
}

// markdown renders one release section, groups in the canonical order. The
// blank line after each heading is what the hand-written document uses, so a
// section `collect` writes and a section a person wrote read the same.
func (r changelogRelease) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## [%s] - %s\n", r.Version, r.Date)
	for _, group := range changelogCategories {
		entries := r.entriesIn(group)
		if len(entries) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n", group)
		for _, e := range entries {
			fmt.Fprintf(&b, "- %s\n", e)
		}
	}
	return b.String()
}

// entriesIn returns the text of every entry in one group, in file order.
func (r changelogRelease) entriesIn(group string) []string {
	var out []string
	for _, e := range r.Entries {
		if e.Category == group {
			out = append(out, e.Text)
		}
	}
	return out
}

// renderChangelog rebuilds the document: the header verbatim, then every release
// in order, separated by one blank line.
func renderChangelog(doc *changelogDoc) string {
	var b strings.Builder
	header := strings.TrimRight(doc.Header, "\n")
	if header != "" {
		b.WriteString(header)
		b.WriteString("\n")
	}
	for _, rel := range doc.Releases {
		b.WriteString("\n")
		b.WriteString(rel.markdown())
	}
	return b.String()
}

// renderReleaseNotes renders the body of the GitHub release notes for one
// release: the groups, with no version heading. The release page carries the
// version in its title, so repeating it here would put the same string on the
// page twice.
func renderReleaseNotes(rel changelogRelease) string {
	section := rel.markdown()
	body := strings.TrimPrefix(section, strings.SplitN(section, "\n", 2)[0]+"\n")
	return strings.TrimLeft(body, "\n")
}

// newest returns the topmost release, which is the newest by the ordering rule
// validateChangelog enforces.
func (d *changelogDoc) newest() (changelogRelease, bool) {
	if len(d.Releases) == 0 {
		return changelogRelease{}, false
	}
	return d.Releases[0], true
}

// find returns the section for one version.
func (d *changelogDoc) find(version string) (changelogRelease, bool) {
	for _, rel := range d.Releases {
		if rel.Version == version {
			return rel, true
		}
	}
	return changelogRelease{}, false
}

// semverParts splits `v1.2.3` into its three numbers.
func semverParts(version string) ([3]int, error) {
	parts := [3]int{}
	for i, p := range strings.SplitN(strings.TrimPrefix(version, "v"), ".", 3) {
		n, err := strconv.Atoi(p)
		if err != nil {
			return parts, fmt.Errorf("%q is not a numeric version part", p)
		}
		parts[i] = n
	}
	return parts, nil
}

// semverGreater reports whether a sorts after b.
func semverGreater(a, b string) bool {
	pa, ea := semverParts(a)
	pb, eb := semverParts(b)
	if ea != nil || eb != nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

// validateChangelog reports every way the document fails to be a changelog the
// release workflow can publish from. It returns messages rather than one error
// so a run lists all of them: a file with two problems should cost one edit,
// not three runs.
func validateChangelog(doc *changelogDoc, newestTag string) []string {
	var problems []string
	if len(doc.Releases) == 0 {
		problems = append(problems, changelogFile+" has no release section. Cut one with `./bloud changelog collect --bump patch`.")
	}
	for i, rel := range doc.Releases {
		if len(rel.Entries) == 0 {
			problems = append(problems, fmt.Sprintf("%s has no entries under [%s]. An empty section publishes empty release notes.", rel.Version, rel.Version))
		}
		if i > 0 && !semverGreater(doc.Releases[i-1].Version, rel.Version) {
			problems = append(problems, fmt.Sprintf("%s must come before %s: releases go newest first.", rel.Version, doc.Releases[i-1].Version))
		}
	}
	if newestTag != "" {
		head, ok := doc.newest()
		if ok && head.Version != newestTag {
			problems = append(problems, fmt.Sprintf("the newest tag is %s but %s has no section for it. The release notes come from this file, so cut the section before the tag.", newestTag, changelogFile))
		}
	}
	return problems
}
