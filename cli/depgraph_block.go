// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"strings"
)

// writeGraphBlock replaces the generated block in the target file. It fails
// rather than appending when the markers are missing, so a typo in the target
// can never produce a second copy of the diagram.
func writeGraphBlock(root, target, generated string) int {
	path := docTargetPath(root, target)
	content, err := os.ReadFile(path)
	if err != nil {
		errorf("Could not read %s: %v", path, err)
		return 1
	}

	updated, ok := spliceGraphBlock(string(content), generated)
	if !ok {
		errorf("%s", graphBlock.missingBlockMessage(path))
		return 1
	}
	if updated == string(content) {
		log(relOrAbs(root, path) + " dependency graph is already current")
		return 0
	}
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		errorf("Could not write %s: %v", path, err)
		return 1
	}
	log("Wrote the dependency graph to " + relOrAbs(root, path))
	return 0
}

// checkGraphBlock compares the target file's block against what the catalog
// produces now. A stale README describes a graph that no longer exists, so
// this is the gate that keeps the two together.
func checkGraphBlock(root, target, generated string) int {
	path := docTargetPath(root, target)
	content, err := os.ReadFile(path)
	if err != nil {
		errorf("Could not read %s: %v", path, err)
		return 1
	}

	existing, ok := extractGraphBlock(string(content))
	if !ok {
		errorf("%s has no generated dependency graph block. Run: ./bloud depgraph --write", relOrAbs(root, path))
		return 1
	}
	if normalizeGraph(existing) == normalizeGraph(generated) {
		log(relOrAbs(root, path) + " dependency graph is up to date")
		return 0
	}

	errorf("%s dependency graph is stale. Run: ./bloud depgraph --write (then commit the result)", relOrAbs(root, path))
	reportFirstGraphDifference(normalizeGraph(existing), normalizeGraph(generated))
	return 1
}

// graphBlock is the generated span of the graph document. The marker pair,
// the splice semantics, and the difference report all live in the shared
// generatedBlock (genblock.go), which is what lets `bloud catalogdoc` own a
// second generated block in the same file without copying the mechanism.
var graphBlock = generatedBlock{
	Begin: graphBeginMarker,
	End:   graphEndMarker,
	Label: "dependency graph",
}

// extractGraphBlock returns the generated block, markers included.
func extractGraphBlock(content string) (string, bool) {
	return graphBlock.Extract(content)
}

// spliceGraphBlock swaps the target's generated block for a new one and
// leaves the rest of the document byte-for-byte alone. The generated tail is
// trimmed so the write is a fixed point; see generatedBlock.Splice.
func spliceGraphBlock(content, generated string) (string, bool) {
	return graphBlock.Splice(content, generated)
}

// normalizeGraph trims the trailing newline and surrounding blank lines so a
// comparison is about content, not about how the file ends.
func normalizeGraph(s string) string {
	return strings.TrimSpace(s)
}

// reportFirstGraphDifference prints the first line where the committed
// diagram and the current one diverge.
func reportFirstGraphDifference(existing, generated string) {
	graphBlock.ReportDifference(existing, generated)
}
