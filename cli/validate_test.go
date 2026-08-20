// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"reflect"
	"testing"
)

func TestSplitShellWords(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "simple command",
			in:   "test -f /tmp/file",
			want: []string{"test", "-f", "/tmp/file"},
		},
		{
			name: "double-quoted argument with spaces",
			in:   `grep -F "foo bar" file.txt`,
			want: []string{"grep", "-F", "foo bar", "file.txt"},
		},
		{
			name: "single-quoted argument with spaces",
			in:   `test -f 'my notes.txt'`,
			want: []string{"test", "-f", "my notes.txt"},
		},
		{
			name: "backslash-escaped space",
			in:   `test -f my\ file.txt`,
			want: []string{"test", "-f", "my file.txt"},
		},
		{
			name: "quoted path with trailing content",
			in:   `grep "a b"`,
			want: []string{"grep", "a b"},
		},
		{
			name: "empty string",
			in:   ``,
			want: nil,
		},
		{
			name: "only whitespace",
			in:   "  \t  ",
			want: nil,
		},
		{
			name: "mixed quotes and escapes",
			in:   `echo 'a b' "c \"quoted\" d" "e\\f" single\ token`,
			want: []string{"echo", "a b", `c "quoted" d`, `e\f`, "single token"},
		},
		{
			name: "adjacent quoted segments form one word",
			in:   `foo"bar baz"'qux'`,
			want: []string{`foobar bazqux`},
		},
		{
			name: "unterminated single quote consumes rest",
			in:   `test 'still one`,
			want: []string{"test", "still one"},
		},
		{
			name: "unterminated double quote consumes rest",
			in:   `test "still one`,
			want: []string{"test", "still one"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitShellWords(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitShellWords(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
