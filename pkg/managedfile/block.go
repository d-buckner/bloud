// SPDX-License-Identifier: AGPL-3.0-only

package managedfile

import (
	"os"
	"strings"
)

// Marker delimits a Bloud-managed region inside a user-owned file.
type Marker struct {
	// Begin prefixes the first line of the managed region.
	Begin string
	// End prefixes the last line of the managed region.
	End string
}

// Render writes deterministic rendered content to path; changed=false when the
// rendered content equals what is already on disk.
func Render(path string, mode os.FileMode, render func() (string, error)) (bool, error) {
	content, err := render()
	if err != nil {
		return false, err
	}
	return Write(path, []byte(content), mode)
}

// Block owns one marker-delimited region inside a user-owned file: it replaces
// the region with the rendered block, preserving everything outside it. The
// file is created if absent. changed=false when the file content is unchanged.
//
// A lone Begin with no End is treated as an unterminated region and replaced
// to EOF (the same edge case the per-app merge handled).
func Block(path string, m Marker, mode os.FileMode, render func() string) (bool, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	desired := mergeBlock(string(existing), m, render())
	return Write(path, []byte(desired), mode)
}

// RemoveBlock deletes the marker-delimited region (including an unterminated
// one, to EOF). changed=false when there was no region to remove. The file's
// existing mode is preserved.
func RemoveBlock(path string, m Marker) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	removed, changed := removeBlock(string(existing), m)
	if !changed || removed == string(existing) {
		return false, nil
	}
	return Write(path, []byte(removed), info.Mode().Perm())
}

// mergeBlock returns existing with the marker region replaced by block
// (appended when no region exists).
func mergeBlock(existing string, m Marker, block string) string {
	blockLines := strings.Split(block, "\n")
	lines := []string{}
	if existing != "" {
		lines = strings.Split(existing, "\n")
	}
	begin, end := -1, -1
	for i, line := range lines {
		if strings.HasPrefix(line, m.Begin) {
			if begin == -1 {
				begin = i
			}
			if strings.HasPrefix(line, m.End) {
				end = i
			}
		} else if begin != -1 && strings.HasPrefix(line, m.End) {
			end = i
		}
	}
	switch {
	case begin == -1:
		if strings.TrimSpace(existing) == "" {
			return block + "\n"
		}
		return strings.TrimRight(existing, "\n") + "\n\n" + block + "\n"
	case end == -1 || end < begin:
		out := append(append([]string{}, lines[:begin]...), blockLines...)
		return joinLines(out)
	default:
		out := append(append([]string{}, lines[:begin]...), blockLines...)
		out = append(out, lines[end+1:]...)
		return joinLines(out)
	}
}

// removeBlock deletes the marker region (including an unterminated one).
func removeBlock(existing string, m Marker) (string, bool) {
	if existing == "" {
		return "", false
	}
	lines := strings.Split(existing, "\n")
	begin, end := -1, len(lines)-1
	for i, line := range lines {
		if strings.HasPrefix(line, m.Begin) && begin == -1 {
			begin = i
		}
		if begin != -1 && strings.HasPrefix(line, m.End) {
			end = i
			break
		}
	}
	if begin == -1 {
		return existing, false
	}
	out := append(append([]string{}, lines[:begin]...), lines[end+1:]...)
	trimmed := strings.TrimRight(joinLines(out), "\n")
	if trimmed == "" {
		return "", true
	}
	return trimmed + "\n", true
}

func joinLines(lines []string) string {
	out := strings.Join(lines, "\n")
	if out == "" {
		return out
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}
