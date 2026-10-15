// SPDX-License-Identifier: AGPL-3.0-only

// Package slug provides URL-safe slug generation for subdomain routing.
// This is the single canonical implementation; the frontend has a mirrored
// version in web/src/lib/utils/appUrl.ts that must produce identical output.
package slug

import "strings"

// Slugify converts a string to a URL-safe slug suitable for subdomain routing.
// It lowercases the input, replaces runs of non-alphanumeric characters with a
// single dash, and trims leading/trailing dashes.
func Slugify(s string) string {
	s = strings.ToLower(s)
	var result []byte
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			result = append(result, byte(r))
		} else if len(result) > 0 && result[len(result)-1] != '-' {
			result = append(result, '-')
		}
	}
	return strings.Trim(string(result), "-")
}

// IsPathSegment reports whether s is safe to use verbatim as one URL path
// segment: non-empty, no separator, no whitespace, no traversal, and nothing
// that would have to be percent-encoded to appear in a path.
//
// This is the guard a value needs when a declaration becomes a *location*
// rather than a label. Slugify rewrites anything into a slug; this answers a
// question about a string somebody chose, because a value that gets
// concatenated into a path has to be checked rather than normalized.
// Normalizing `../..` down to `etc` would hide the fact that somebody
// declared a traversal; rejecting it names the app that did.
func IsPathSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}
