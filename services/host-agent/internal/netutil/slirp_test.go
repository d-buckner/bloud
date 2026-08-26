// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package netutil

import "testing"

const routeHeader = "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT"

func TestRouteTableOnSlirp(t *testing.T) {
	tests := []struct {
		name  string
		table string
		want  bool
	}{
		{
			name: "slirp default route",
			table: routeHeader + "\n" +
				"eth0\t00000000\t0202020A\t00010003\t0\t0\t0\t00000000\t0\t0\t0\n",
			want: true,
		},
		{
			name: "slirp non-default route",
			table: routeHeader + "\n" +
				"eth0\t00D00F0A\t0202020A\t00010003\t0\t0\t0\t00FFFFFF\t0\t0\t0\n",
			want: true,
		},
		{
			name: "regular LAN gateway",
			table: routeHeader + "\n" +
				"eth0\t00000000\t0101A8C0\t00010003\t0\t0\t0\t00000000\t0\t0\t0\n",
			want: false,
		},
		{
			name: "loopback route only",
			table: routeHeader + "\n" +
				"lo\t00000000\t01000000\t00010003\t0\t0\t0\t00000000\t0\t0\t0\n",
			want: false,
		},
		{
			name: "loopback carries slirp hex (must not match)",
			table: routeHeader + "\n" +
				"lo\t00000000\t0202020A\t00010003\t0\t0\t0\t00000000\t0\t0\t0\n",
			want: false,
		},
		{
			name:  "empty table",
			table: "",
			want:  false,
		},
		{
			name:  "header only",
			table: routeHeader,
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RouteTableOnSlirp([]byte(tt.table)); got != tt.want {
				t.Errorf("RouteTableOnSlirp() = %v, want %v", got, tt.want)
			}
		})
	}
}
