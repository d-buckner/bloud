// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"reflect"
	"testing"
)

func TestAptPackagesFor(t *testing.T) {
	tests := []struct {
		name    string
		missing []prereq
		want    []string
	}{
		{
			name:    "none missing",
			missing: nil,
			want:    nil,
		},
		{
			name:    "single tool",
			missing: []prereq{{"go", "Go"}},
			want:    []string{"golang-go"},
		},
		{
			name:    "tool with multiple packages",
			missing: []prereq{{"node", "Node.js"}},
			want:    []string{"nodejs", "npm"},
		},
		{
			name:    "multiple tools, sorted and deduped",
			missing: []prereq{{"podman", "Podman"}, {"go", "Go"}, {"node", "Node.js"}},
			want:    []string{"golang-go", "nodejs", "npm", "podman"},
		},
		{
			name:    "unknown tool has no apt mapping",
			missing: []prereq{{"limactl", "Lima"}},
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := aptPackagesFor(tt.missing)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("aptPackagesFor(%v) = %v, want %v", tt.missing, got, tt.want)
			}
		})
	}
}
