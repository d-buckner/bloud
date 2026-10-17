// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// changelogMode is what a `bloud changelog` run does.
type changelogMode int

const (
	changelogPrint changelogMode = iota
	changelogCheck
	changelogNotes
	changelogCheckPR
	changelogCollect
	changelogHelp
)

// changelogFlags holds the resolved options of one run.
type changelogFlags struct {
	mode     changelogMode
	version  string
	bump     string
	since    string
	out      string
	bodyFile string
}

func cmdChangelog(args []string) int {
	if len(args) > 0 && args[0] == "collect" {
		return runChangelogCollect(args[1:])
	}
	flags, err := parseChangelogFlags(args)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	if flags.mode == changelogHelp {
		printChangelogUsage()
		return 0
	}
	if flags.mode == changelogCheckPR {
		return runChangelogCheckPR(flags.bodyFile)
	}
	root, err := getProjectRoot()
	if err != nil {
		errorf("could not find project root: %v", err)
		return 1
	}
	doc, err := readChangelog(root)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	switch flags.mode {
	case changelogCheck:
		return runChangelogCheck(root, doc)
	case changelogNotes:
		return runChangelogNotes(doc, flags)
	default:
		return runChangelogPrint(doc)
	}
}

// parseChangelogFlags reads the flags of a non-collect run.
func parseChangelogFlags(args []string) (changelogFlags, error) {
	flags := changelogFlags{mode: changelogPrint, bump: "patch"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--check":
			flags.mode = changelogCheck
		case "--notes":
			flags.mode = changelogNotes
			flags.version = nextValue(args, &i)
		case "--out":
			flags.out = nextValue(args, &i)
		case "--check-pr":
			flags.bodyFile = nextValue(args, &i)
			flags.mode = changelogCheckPR
		case "--help", "-h":
			flags.mode = changelogHelp
		default:
			return flags, fmt.Errorf("unknown flag %q (run `./bloud changelog --help`)", args[i])
		}
	}
	return flags, nil
}

// nextValue returns the value that follows a flag, and moves the caller's index
// onto it. It returns "" when the flag is the last argument: a flag that takes a
// value and gets one somewhere else is a mistake the caller can name.
func nextValue(args []string, i *int) string {
	if *i+1 >= len(args) {
		return ""
	}
	*i++
	return args[*i]
}

// readChangelog loads and parses the changelog. A missing file is an error with
// the fix in it, because the release workflow reads this file and an absent one
// means no release can be published.
func readChangelog(root string) (*changelogDoc, error) {
	path := filepath.Join(root, changelogFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s is missing. It is the source of every release's notes; see docs/operations/releases.md", changelogFile)
		}
		return nil, err
	}
	return parseChangelog(string(raw))
}

// runChangelogPrint prints the newest release section.
func runChangelogPrint(doc *changelogDoc) int {
	rel, ok := doc.newest()
	if !ok {
		errorf("%s has no release section", changelogFile)
		return 1
	}
	fmt.Print(rel.markdown())
	return 0
}

// runChangelogCheck is the gate the fast tier runs. It fails a changelog the
// release workflow could not publish from, and a changelog that falls behind the
// newest tag, which is the shape of the mistake that produces placeholder notes.
func runChangelogCheck(root string, doc *changelogDoc) int {
	tag, err := newestVersionTag(runTool, root)
	if err != nil {
		errorf("could not read the version tags: %v", err)
		return 1
	}
	problems := validateChangelog(doc, tag)
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		errorf("%s is not ready to publish from (%d problem(s))", changelogFile, len(problems))
		return 1
	}
	head, _ := doc.newest()
	log(fmt.Sprintf("%s: %d release section(s), newest %s", changelogFile, len(doc.Releases), head.Version))
	return 0
}

// runChangelogNotes writes the release notes for one version. The version
// defaults to the newest section, which is the one a tag push publishes.
func runChangelogNotes(doc *changelogDoc, flags changelogFlags) int {
	rel, ok := chooseRelease(doc, flags.version)
	if !ok {
		if flags.version == "" {
			errorf("%s has no release section to render notes from", changelogFile)
		} else {
			errorf("%s has no section for %s. Cut it with `./bloud changelog collect` before the tag.", changelogFile, flags.version)
		}
		return 1
	}
	notes := strings.TrimRight(renderReleaseNotes(rel), "\n") + "\n"
	if strings.TrimSpace(notes) == "" {
		errorf("the section for %s renders no notes", rel.Version)
		return 1
	}
	if flags.out == "" {
		fmt.Print(notes)
		return 0
	}
	if err := os.WriteFile(flags.out, []byte(notes), 0644); err != nil {
		errorf("write %s: %v", flags.out, err)
		return 1
	}
	log(fmt.Sprintf("Wrote %s for %s (%d bytes)", flags.out, rel.Version, len(notes)))
	return 0
}

// chooseRelease picks the section to render: a named version, or the newest.
func chooseRelease(doc *changelogDoc, version string) (changelogRelease, bool) {
	if version == "" {
		return doc.newest()
	}
	return doc.find(version)
}

// runChangelogCheckPR is the per-pull-request gate. The workflow writes the body
// from the event payload and hands it over, so the check needs no API call.
func runChangelogCheckPR(bodyFile string) int {
	body, err := readPRBody(bodyFile)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	entries, err := parsePREntries(body)
	if err != nil {
		errorf("%v", err)
		fmt.Println()
		fmt.Println(changelogHelpForPR())
		return 1
	}
	if len(entries) == 0 {
		log("Changelog: " + changelogNoEntry + " (no user-visible change)")
		return 0
	}
	log(fmt.Sprintf("Changelog: %d entr%s", len(entries), pluralEntries(len(entries))))
	for _, e := range entries {
		fmt.Printf("  %s: %s\n", e.Category, e.Text)
	}
	return 0
}

// pluralEntries completes the count message without a plural that reads wrong.
func pluralEntries(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// readPRBody reads the pull-request body from a file, or from stdin when the
// path is "-" or absent.
func readPRBody(path string) (string, error) {
	if path == "" || path == "-" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read the pull request body from stdin: %w", err)
		}
		return string(raw), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the pull request body from %s: %w", path, err)
	}
	return string(raw), nil
}

// parseCollectFlags reads the flags of a `collect` run.
func parseCollectFlags(args []string) (changelogFlags, error) {
	flags := changelogFlags{bump: "patch"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--bump":
			flags.bump = nextValue(args, &i)
		case "--version":
			flags.version = nextValue(args, &i)
		case "--since":
			flags.since = nextValue(args, &i)
		default:
			return flags, fmt.Errorf("unknown flag %q (usage: ./bloud changelog collect [--bump major|minor|patch] [--version v1.2.3] [--since <ref>])", args[i])
		}
	}
	return flags, nil
}

// resolveCollectVersion decides which version `collect` cuts, and refuses one
// the document cannot take.
func resolveCollectVersion(doc *changelogDoc, flags changelogFlags) (string, error) {
	version := flags.version
	if version == "" {
		return nextVersion(doc, flags.bump)
	}
	if !semverTagRe.MatchString(version) {
		return "", fmt.Errorf("%q is not a release version (use the form v1.2.3)", version)
	}
	if _, taken := doc.find(version); taken {
		return "", fmt.Errorf("%s already has a section for %s", changelogFile, version)
	}
	// A section below the newest one would sit under it and fail `--check`, so the
	// run says so now rather than writing a document nobody can publish from.
	if head, ok := doc.newest(); ok && !semverGreater(version, head.Version) {
		return "", fmt.Errorf("%s is not newer than %s, the newest section (pass a higher version, or a different --bump)", version, head.Version)
	}
	return version, nil
}

// runChangelogCollect cuts the next release section from the pull requests
// merged since the last tag and writes it into the changelog.
func runChangelogCollect(args []string) int {
	flags, err := parseCollectFlags(args)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	root, err := getProjectRoot()
	if err != nil {
		errorf("could not find project root: %v", err)
		return 1
	}
	doc, err := readChangelog(root)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	version, err := resolveCollectVersion(doc, flags)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	since := flags.since
	if since == "" {
		since, err = newestVersionTag(runTool, root)
		if err != nil {
			errorf("could not read the version tags: %v", err)
			return 1
		}
	}
	result, err := collectRelease(collectOptions{
		root:    root,
		run:     runTool,
		version: version,
		since:   since,
		now:     time.Now(),
	})
	if err != nil {
		errorf("%v", err)
		return 1
	}
	if err := writeChangelog(root, insertRelease(doc, result.Release)); err != nil {
		errorf("%v", err)
		return 1
	}
	log(fmt.Sprintf("Added [%s] to %s from the pull requests merged since %s", version, changelogFile, labelOrHead(since)))
	fmt.Println()
	fmt.Print(result.Release.markdown())
	fmt.Println()
	log("Commit this file, then tag it and push both to publish the release.")
	return 0
}

// writeChangelog renders the document back to disk.
func writeChangelog(root string, doc *changelogDoc) error {
	return os.WriteFile(filepath.Join(root, changelogFile), []byte(renderChangelog(doc)), 0644)
}

func printChangelogUsage() {
	for _, line := range []string{
		"./bloud changelog [flags]",
		"",
		"  (no flag)          Print the newest release section",
		"  --check            Exit 1 when the changelog cannot be published from",
		"  --notes [v]        Print the release notes for a version (default: newest)",
		"  --out FILE         Write the notes to FILE instead of stdout",
		"  --check-pr FILE    Check a pull request body for its Changelog section (- reads stdin)",
		"",
		"./bloud changelog collect [flags]",
		"",
		"  --bump major|minor|patch   The version to cut (default: patch)",
		"  --version v1.2.3           The version to cut, instead of a bump",
		"  --since <ref>              Collect since a ref (default: the newest v tag)",
	} {
		fmt.Println(line)
	}
}
