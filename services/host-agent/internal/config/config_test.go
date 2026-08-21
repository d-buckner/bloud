// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package config

import (
	"reflect"
	"testing"
)

func TestSplitNets(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"10.0.2.0/24", []string{"10.0.2.0/24"}},
		{"10.0.2.2", []string{"10.0.2.2"}},
		{"10.0.2.0/24, 10.0.3.2", []string{"10.0.2.0/24", "10.0.3.2"}},
		{"bad, 10.0.2.2, not-a-cidr", []string{"10.0.2.2"}},
		{" , 10.0.2.0/24 , ", []string{"10.0.2.0/24"}},
	}
	for _, tc := range cases {
		got := splitNets(tc.raw)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("splitNets(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
