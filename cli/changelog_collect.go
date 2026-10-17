// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// changelogCmd runs a tool and returns its trimmed stdout. It is a field rather
// than a direct exec call so the collection path is testable without a git
// history and a GitHub token.
type changelogCmd func(name string, args ...string) (string, error)

// runTool is the real changelogCmd. A failure carries the tool's stderr, because
// `gh` explains an expired token or a missing repository there and nowhere else.
func runTool(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// prFromSubjectRe finds the pull-request number a squash merge appends to the
// subject of the commit it creates.
var prFromSubjectRe = regexp.MustCompile(`\(#([0-9]+)\)$`)

// newestVersionTag returns the highest release tag, or "" when the repository
// has none yet. The filter is done here rather than with a git glob because this
// repository carries other tag families that start with the same letter, and a
// tag named `vm-images` is not version 1 of anything.
func newestVersionTag(run changelogCmd, root string) (string, error) {
	out, err := run("git", "-C", root, "tag", "--list")
	if err != nil {
		return "", err
	}
	best := ""
	for _, line := range strings.Split(out, "\n") {
		tag := strings.TrimSpace(line)
		if !semverTagRe.MatchString(tag) {
			continue
		}
		if best == "" || semverGreater(tag, best) {
			best = tag
		}
	}
	return best, nil
}

// semverTagRe matches the only tag shape this process treats as a release.
var semverTagRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// mergedPRNumbers returns the pull-request numbers merged since `since`, newest
// first. The commit subjects are the record: this repository squash-merges, so
// every commit on main names the pull request it came from.
func mergedPRNumbers(run changelogCmd, root, since string) ([]int, error) {
	revision := "HEAD"
	if since != "" {
		revision = since + "..HEAD"
	}
	out, err := run("git", "-C", root, "log", "--format=%s", revision)
	if err != nil {
		return nil, err
	}
	var (
		numbers []int
		seen    = map[int]bool{}
	)
	for _, line := range strings.Split(out, "\n") {
		m := prFromSubjectRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || seen[n] {
			continue
		}
		seen[n] = true
		numbers = append(numbers, n)
	}
	return numbers, nil
}

// repoSlug reads `owner/repo` from the origin remote, so `gh` addresses the right
// repository on a fork or a mirror without a flag.
func repoSlug(run changelogCmd, root string) (string, error) {
	url, err := run("git", "-C", root, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return parseRepoSlug(url)
}

// parseRepoSlug takes the owner and repository out of an HTTPS remote URL or an
// SSH one in the scp-like form git accepts.
func parseRepoSlug(url string) (string, error) {
	path := strings.TrimSpace(url)
	if i := strings.Index(path, "://"); i >= 0 {
		path = path[i+3:]
	} else if i := strings.Index(path, "@"); i >= 0 {
		path = path[i+1:]
	}
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("cannot read owner and repository from the remote URL %q", url)
	}
	// The scp-like form separates the host from the path with a colon instead of a
	// slash, so the host sits on the front of the owner segment.
	if i := strings.Index(parts[0], ":"); i >= 0 {
		parts[0] = parts[0][i+1:]
	}
	owner := parts[len(parts)-2]
	repo := parts[len(parts)-1]
	if owner == "" || repo == "" {
		return "", fmt.Errorf("cannot read owner and repository from the remote URL %q", url)
	}
	return owner + "/" + repo, nil
}

// prBodyReader fetches one pull request body. It is a function field so a test
// can supply bodies from fixtures instead of a GitHub token.
type prBodyReader func(number int) (string, error)

// ghPRBody returns a reader that takes each body through the gh CLI.
func ghPRBody(run changelogCmd, repo string) prBodyReader {
	return func(n int) (string, error) {
		out, err := run("gh", "pr", "view", strconv.Itoa(n), "--repo", repo, "--json", "body")
		if err != nil {
			return "", err
		}
		var payload struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			return "", fmt.Errorf("read the gh response for PR #%d: %w", n, err)
		}
		return payload.Body, nil
	}
}

// collectOptions says which version to cut, what to collect since, and where to
// read the entries from.
type collectOptions struct {
	root    string
	run     changelogCmd
	version string
	since   string
	now     time.Time
	read    prBodyReader
}

// collectedRelease is the outcome of a collection run: the section to publish,
// and the pull requests that owed an entry and gave none.
type collectedRelease struct {
	Release changelogRelease
	// Missing names the pull requests with no Changelog section, so the failure
	// says which PRs to go back and fix rather than only that something is wrong.
	Missing []int
}

// collectRelease gathers the Changelog sections of every pull request merged
// since the last release tag.
//
// A pull request with no section is a failure, not a skip. The squash-merge
// commit body on main is empty, so the section is the only copy of the entry:
// skipping it would publish release notes that quietly omit a change.
func collectRelease(opts collectOptions) (collectedRelease, error) {
	repo, err := repoSlug(opts.run, opts.root)
	if err != nil {
		return collectedRelease{}, err
	}
	if opts.read == nil {
		opts.read = ghPRBody(opts.run, repo)
	}
	numbers, err := mergedPRNumbers(opts.run, opts.root, opts.since)
	if err != nil {
		return collectedRelease{}, err
	}
	out := collectedRelease{}
	byCategory := map[string][]string{}
	for _, n := range numbers {
		body, err := opts.read(n)
		if err != nil {
			return collectedRelease{}, fmt.Errorf("read PR #%d: %w", n, err)
		}
		entries, err := parsePREntries(body)
		if err != nil {
			out.Missing = append(out.Missing, n)
			continue
		}
		for _, e := range entries {
			e = withPRRef(e, n)
			byCategory[e.Category] = append(byCategory[e.Category], e.Text)
		}
	}
	if len(out.Missing) > 0 {
		return out, fmt.Errorf("%d merged pull request(s) have no usable Changelog section: %s. Add the section to each one, then run this again",
			len(out.Missing), joinNumbers(out.Missing))
	}
	release := changelogRelease{Version: opts.version, Date: opts.now.UTC().Format("2006-01-02")}
	for _, category := range changelogCategories {
		for _, text := range byCategory[category] {
			release.Entries = append(release.Entries, changelogEntry{Category: category, Text: text})
		}
	}
	if len(release.Entries) == 0 {
		return out, fmt.Errorf("no pull request merged since %s carries an entry, so this release would publish empty notes", labelOrHead(opts.since))
	}
	out.Release = release
	return out, nil
}

// labelOrHead names the revision a collection started from in a message.
func labelOrHead(since string) string {
	if since == "" {
		return "the beginning of the history"
	}
	return since
}

// joinNumbers formats a list of pull-request numbers for one message.
func joinNumbers(numbers []int) string {
	parts := make([]string, 0, len(numbers))
	for _, n := range numbers {
		parts = append(parts, "#"+strconv.Itoa(n))
	}
	return strings.Join(parts, ", ")
}

// nextVersion applies a bump to the newest version in the document. With no
// release yet, every bump starts at v0.1.0: a first release is a feature
// release, not a patch.
func nextVersion(doc *changelogDoc, bump string) (string, error) {
	if bump != "major" && bump != "minor" && bump != "patch" {
		return "", fmt.Errorf("unknown bump %q (use major, minor, or patch)", bump)
	}
	head, ok := doc.newest()
	if !ok {
		return "v0.1.0", nil
	}
	parts, err := semverParts(head.Version)
	if err != nil {
		return "", err
	}
	switch bump {
	case "major":
		parts[0]++
		parts[1], parts[2] = 0, 0
	case "minor":
		parts[1]++
		parts[2] = 0
	case "patch":
		parts[2]++
	}
	return fmt.Sprintf("v%d.%d.%d", parts[0], parts[1], parts[2]), nil
}

// insertRelease puts a new section above every existing one and returns the
// document. The header stays byte-for-byte what the author wrote.
func insertRelease(doc *changelogDoc, rel changelogRelease) *changelogDoc {
	out := *doc
	out.Releases = append([]changelogRelease{rel}, doc.Releases...)
	return &out
}
