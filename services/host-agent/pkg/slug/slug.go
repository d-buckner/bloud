// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package slug provides URL-safe slug generation for subdomain routing.
// This is the single canonical implementation — the frontend has a mirrored
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
