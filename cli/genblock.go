// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// docTargetPath resolves a --target value against the project root.
func docTargetPath(root, target string) string {
	if filepath.IsAbs(target) {
		return target
	}
	return filepath.Join(root, target)
}

// loadCatalog reads the app catalog every generated-doc command renders from.
// An empty catalog is reported as an error rather than generating an empty
// block, because an empty block would silently erase the list it replaced.
func loadCatalog(root string) (map[string]*AppMetadata, bool) {
	appsDir := filepath.Join(root, "apps")
	apps, err := loadAppMetadata(appsDir)
	if err != nil {
		errorf("Failed to load app metadata: %v", err)
		return nil, false
	}
	if len(apps) == 0 {
		errorf("No apps with a metadata.yaml found under %s", appsDir)
		return nil, false
	}
	return apps, true
}

// relOrAbs labels a path for output: relative to the repo root when it is
// inside it, absolute otherwise.
func relOrAbs(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// generatedBlock is one span of a document that a command owns outright:
// everything between the two markers is generated from the catalog, and
// everything outside them is hand-written and left byte-for-byte alone.
//
// This is the mechanism `bloud depgraph` introduced for the text form of the
// graph, generalized so `bloud catalogdoc` can own two blocks in the same
// file. A block is always replaced whole, never merged line by line, so
// generated output can never accumulate against a previous version of
// itself.
type generatedBlock struct {
	// Begin and End are the HTML comment markers that delimit the block.
	// They are comments rather than a fence so the block is invisible in the
	// rendered document.
	Begin string
	End   string
	// Label names the block in messages ("dependency graph", "catalog
	// list"), so a stale document says what is stale rather than which
	// marker pair failed to match.
	Label string
}

// locate returns the byte offsets of the whole block, markers included.
func (b generatedBlock) locate(content string) (start, stop int, ok bool) {
	start = strings.Index(content, b.Begin)
	if start < 0 {
		return 0, 0, false
	}
	rel := strings.Index(content[start:], b.End)
	if rel < 0 {
		return 0, 0, false
	}
	return start, start + rel + len(b.End), true
}

// Present reports whether the document carries this block at all.
func (b generatedBlock) Present(content string) bool {
	_, _, ok := b.locate(content)
	return ok
}

// Extract returns the block as the document holds it, markers included.
func (b generatedBlock) Extract(content string) (string, bool) {
	start, stop, ok := b.locate(content)
	if !ok {
		return "", false
	}
	return content[start:stop], true
}

// Splice swaps the document's block for `generated` and leaves the rest of
// the document byte-for-byte alone.
//
// `generated` carries its own markers, the same shape the renderers produce.
// The replacement is that text with its trailing newline dropped: the newline
// that terminates the end-marker line belongs to the document, not to the
// block, and it sits outside the replaced span. Splicing the rendered bytes
// verbatim therefore leaves both behind, and every write grows the file by
// one blank line. Trimming the generated tail makes the write a fixed point:
// writing the same block twice changes nothing, which is what lets a
// merge-to-main refresh commit only when something actually moved.
func (b generatedBlock) Splice(content, generated string) (string, bool) {
	start, stop, ok := b.locate(content)
	if !ok {
		return content, false
	}
	return content[:start] + strings.TrimRight(generated, "\n") + content[stop:], true
}

// Same compares the document's block with what the catalog produces now,
// ignoring trailing whitespace so the comparison is about content rather
// than about how the file ends.
func (b generatedBlock) Same(existing, generated string) bool {
	return strings.TrimSpace(existing) == strings.TrimSpace(generated)
}

// ReportDifference prints the first line where the committed block and the
// current one diverge, which is usually all it takes to see what a metadata
// change did.
func (b generatedBlock) ReportDifference(existing, generated string) {
	existingLines := strings.Split(strings.TrimSpace(existing), "\n")
	generatedLines := strings.Split(strings.TrimSpace(generated), "\n")
	limit := len(existingLines)
	if len(generatedLines) < limit {
		limit = len(generatedLines)
	}
	for i := 0; i < limit; i++ {
		if existingLines[i] != generatedLines[i] {
			fmt.Fprintf(os.Stderr, "  %s: first difference at block line %d:\n    committed: %s\n    expected:  %s\n",
				b.Label, i+1, strings.TrimSpace(existingLines[i]), strings.TrimSpace(generatedLines[i]))
			return
		}
	}
	fmt.Fprintf(os.Stderr, "  %s: block length differs: committed %d lines, expected %d lines\n",
		b.Label, len(existingLines), len(generatedLines))
}

// missingBlockMessage explains that the target document has no block for
// this label, and what to add by hand. A write never appends a block that is
// not there, so a typo in the markers cannot produce a second copy of the
// generated content at the end of the file.
func (b generatedBlock) missingBlockMessage(path string) string {
	return fmt.Sprintf("%s has no generated %s block. Add these two lines where it belongs:\n  %s\n  %s",
		path, b.Label, b.Begin, b.End)
}
