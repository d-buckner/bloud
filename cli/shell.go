// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"strings"
)

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// --- Helpers ---

// splitShellWords splits a command string into arguments, respecting single
// quotes, double quotes, and backslash escapes. It is a minimal shell-like
// tokenizer used to turn manifest command strings into exec.Command args so
// that quoted arguments containing spaces survive intact.

// splitShellWords splits a command string into arguments, respecting single
// quotes, double quotes, and backslash escapes. It is a minimal shell-like
// tokenizer used to turn manifest command strings into exec.Command args so
// that quoted arguments containing spaces survive intact.
func splitShellWords(s string) []string {
	var words []string
	for i := 0; i < len(s); {
		for i < len(s) && isShellSpace(s[i]) {
			i++
		}
		if i == len(s) {
			break
		}

		var word strings.Builder
		i = readShellWord(s, i, &word)
		if word.Len() > 0 {
			words = append(words, word.String())
		}
	}
	return words
}

// isShellSpace reports whether b is a word-separating whitespace byte.

// isShellSpace reports whether b is a word-separating whitespace byte.
func isShellSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// readShellWord reads one shell word starting at i (a non-space byte), writing
// its unquoted contents into word. It returns the index just past the word.

// readShellWord reads one shell word starting at i (a non-space byte), writing
// its unquoted contents into word. It returns the index just past the word.
func readShellWord(s string, i int, word *strings.Builder) int {
	for i < len(s) {
		switch {
		case isShellSpace(s[i]):
			return i
		case s[i] == '\'':
			i = readSingleQuoted(s, i, word)
		case s[i] == '"':
			i = readDoubleQuoted(s, i, word)
		case s[i] == '\\':
			if i+1 < len(s) {
				word.WriteByte(s[i+1])
				i += 2
			} else {
				i++
			}
		default:
			word.WriteByte(s[i])
			i++
		}
	}
	return i
}

// readSingleQuoted copies the literal contents of a single-quoted section
// starting at the opening quote at i, and returns the index just past it. An
// unterminated quote consumes the rest of the string.

// readSingleQuoted copies the literal contents of a single-quoted section
// starting at the opening quote at i, and returns the index just past it. An
// unterminated quote consumes the rest of the string.
func readSingleQuoted(s string, i int, word *strings.Builder) int {
	close := strings.IndexByte(s[i+1:], '\'')
	if close < 0 {
		word.WriteString(s[i+1:])
		return len(s)
	}
	word.WriteString(s[i+1 : i+1+close])
	return i + close + 2
}

// readDoubleQuoted copies the contents of a double-quoted section starting at
// the opening quote at i, honoring backslash escapes, and returns the index
// just past it. An unterminated quote consumes the rest of the string.

// readDoubleQuoted copies the contents of a double-quoted section starting at
// the opening quote at i, honoring backslash escapes, and returns the index
// just past it. An unterminated quote consumes the rest of the string.
func readDoubleQuoted(s string, i int, word *strings.Builder) int {
	for i++; i < len(s); i++ {
		switch {
		case s[i] == '"':
			return i + 1
		case s[i] == '\\' && i+1 < len(s):
			switch s[i+1] {
			case '"', '\\', '$', '`':
				word.WriteByte(s[i+1])
			default:
				word.WriteByte('\\')
				word.WriteByte(s[i+1])
			}
			i++
		default:
			word.WriteByte(s[i])
		}
	}
	return i
}
