// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"regexp"
	"strings"
)

// changelogSectionHeading is the pull-request template section that carries the
// entry. The match is case-insensitive and takes any heading level, so a template
// that moves the section under a different level still parses.
var changelogSectionHeading = regexp.MustCompile(`(?i)^#{1,6}[ \t]*changelog[ \t]*$`)

// headingRe matches any ATX heading and captures its text.
var headingRe = regexp.MustCompile(`^#{1,6}[ \t]+`)

// changelogBulletRe matches a top-level bullet and captures its text. A nested
// bullet is rejected rather than flattened: an indented line under a bullet is
// detail about that bullet, and dropping it would lose the author's text.
var changelogBulletRe = regexp.MustCompile(`^[-*][ \t]+(.+)$`)

// prRefRe matches the `(#123)` suffix a squash merge appends to a subject, and
// the same suffix an author may add by hand to a bullet. Any sentence punctuation
// that follows it is kept: a bullet ends with a period, and dropping it would
// make the entry read worse for the number being removed.
var prRefRe = regexp.MustCompile(`[ \t]*\([#!][0-9]+\)([.!?,;:]?)[ \t]*$`)

// changelogCategoryPrefixRe splits a bullet into its category and its text.
var changelogCategoryPrefixRe = regexp.MustCompile(`^([A-Za-z]+):[ \t]*(.*)$`)

// htmlCommentRe matches an HTML comment, including one that spans lines. The
// pull request template carries its instructions as comments, so the parser
// removes them before it reads what the author wrote.
var htmlCommentRe = regexp.MustCompile(`(?s)<!--[\s\S]*?-->`)

// extractChangelogSection returns the body of the pull request's Changelog
// section, and whether the section exists at all. The section ends at the next
// heading of the same level or higher.
func extractChangelogSection(body string) (string, bool) {
	lines := strings.Split(body, "\n")
	start, level := -1, 0
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if changelogSectionHeading.MatchString(trimmed) {
			start = i + 1
			level = len(headingRe.FindString(trimmed))
			break
		}
	}
	if start < 0 {
		return "", false
	}
	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimRight(lines[i], " \t")
		if !headingRe.MatchString(trimmed) {
			continue
		}
		if len(headingRe.FindString(trimmed)) <= level {
			return strings.Join(lines[start:i], "\n"), true
		}
	}
	return strings.Join(lines[start:], "\n"), true
}

// parsePREntries reads the Changelog section of a pull request body. It returns
// the entries the PR contributes, which is empty for a PR that answers `n/a`.
//
// The rules are strict because this text is the only copy of the entry: the
// squash-merge commit body on main is empty, so a section that is missing or
// malformed loses the entry with no other record of it.
func parsePREntries(body string) ([]changelogEntry, error) {
	raw, ok := extractChangelogSection(body)
	if !ok {
		return nil, fmt.Errorf("the pull request body has no `## Changelog` section. Add one, list the user-visible changes as bullets, or write %q when there are none", changelogNoEntry)
	}
	section := htmlCommentRe.ReplaceAllString(raw, "")
	if strings.EqualFold(strings.TrimSpace(section), changelogNoEntry) {
		return nil, nil
	}
	var entries []changelogEntry
	for i, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "" {
			continue
		}
		if isContinuation(line) {
			if len(entries) == 0 {
				return nil, fmt.Errorf("line %d of the Changelog section: an indented line continues no bullet", i+1)
			}
			entries[len(entries)-1].Text += " " + strings.TrimSpace(line)
			continue
		}
		m := changelogBulletRe.FindStringSubmatch(trimmed)
		if m == nil {
			return nil, fmt.Errorf("line %d of the Changelog section: %q is not a bullet. Write one bullet per user-visible change, or %q when there are none", i+1, trimmed, changelogNoEntry)
		}
		entry, err := parseEntryBullet(m[1])
		if err != nil {
			return nil, fmt.Errorf("line %d of the Changelog section: %w", i+1, err)
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("the `## Changelog` section is empty. Write one bullet per user-visible change, or %q when there are none", changelogNoEntry)
	}
	return entries, nil
}

// parseEntryBullet reads one bullet: a category, a colon, and the text.
func parseEntryBullet(text string) (changelogEntry, error) {
	m := changelogCategoryPrefixRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return changelogEntry{}, fmt.Errorf("%q does not start with a category. Start the bullet with one of %s followed by a colon", strings.TrimSpace(text), strings.Join(changelogCategories, ", "))
	}
	idx := categoryIndex(m[1])
	if idx < 0 {
		return changelogEntry{}, fmt.Errorf("unknown category %q (use one of %s)", m[1], strings.Join(changelogCategories, ", "))
	}
	body := strings.TrimSpace(prRefRe.ReplaceAllString(m[2], "$1"))
	if body == "" {
		return changelogEntry{}, fmt.Errorf("the %q entry has no text", m[1])
	}
	return changelogEntry{Category: changelogCategories[idx], Text: body}, nil
}

// withPRRef appends the pull-request number to an entry, so a reader of the
// release notes can open the change that produced it. An entry that already
// names a number keeps its own.
func withPRRef(entry changelogEntry, number int) changelogEntry {
	if prRefRe.MatchString(entry.Text) || number <= 0 {
		return entry
	}
	entry.Text = fmt.Sprintf("%s (#%d)", entry.Text, number)
	return entry
}

// changelogHelpForPR explains the section to whoever wrote the pull request.
// It is printed on failure, so it carries the format rather than pointing at a
// document the reader has to leave the terminal to find.
func changelogHelpForPR() string {
	return "The Changelog section takes one bullet per user-visible change:\n\n" +
		"  ## Changelog\n\n" +
		"  - Added: A searchable time zone picker under Settings.\n" +
		"  - Fixed: The waiting page no longer hides an app's own 503.\n\n" +
		"The text after the colon is a sentence: it appears in the release notes under\n" +
		"that category. Write " + changelogNoEntry + " on its own line when the change has no user-visible effect."
}
