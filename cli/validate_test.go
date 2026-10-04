// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"reflect"
	"testing"
)

func TestRunCommandsPreservesManifestOrder(t *testing.T) {
	root := t.TempDir()
	cmds := []manifestCommand{
		{ID: "heavy-a", Cwd: ".", Run: "echo a", Heavy: true},
		{ID: "light-b", Cwd: ".", Run: "echo b"},
		{ID: "light-c", Cwd: ".", Run: "echo c"},
		{ID: "heavy-d", Cwd: ".", Run: "echo d", Heavy: true},
		{ID: "light-fail", Cwd: ".", Run: "sh -c 'exit 7'"},
	}
	result := &ValidateResult{Tier: "fast"}

	// json mode keeps the run quiet; the point is the ordering and the exit
	// code, not the console output.
	code := runCommands(root, cmds, result, validateFlags{json: true})

	if code != 1 {
		t.Fatalf("runCommands exit = %d, want 1 (light-fail fails)", code)
	}
	if len(result.Commands) != len(cmds) {
		t.Fatalf("ledger rows = %d, want %d", len(result.Commands), len(cmds))
	}
	for i, want := range []string{"heavy-a", "light-b", "light-c", "heavy-d", "light-fail"} {
		if result.Commands[i].ID != want {
			t.Errorf("ledger row %d = %q, want %q (ledger must keep manifest order)", i, result.Commands[i].ID, want)
		}
	}
	if result.Commands[4].ExitCode != 7 {
		t.Errorf("light-fail exit code = %d, want 7", result.Commands[4].ExitCode)
	}
}

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
