// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package netutil

import (
	"os"
	"strings"
)

// slirpGatewayHex is slirp's fixed host address 10.0.2.2 as it appears in the
// gateway column of /proc/net/route on the little-endian architectures Bloud
// supports (x86_64, arm64): the address bytes in reversed order.
const slirpGatewayHex = "0202020A"

// OnSlirp reports whether this host sits on QEMU's user-mode (slirp) network,
// i.e. some non-loopback route uses slirp's fixed gateway 10.0.2.2. Multicast
// never leaves the VM through slirp, and the guest's own port-5353 multicast
// traffic corrupts slirp's UDP hostfwd state (see the mDNS publisher), so
// services that rely on LAN multicast must fall back to unicast there.
func OnSlirp() bool {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return false
	}
	return RouteTableOnSlirp(data)
}

// RouteTableOnSlirp reports whether any non-loopback route in a
// /proc/net/route table uses slirp's gateway.
func RouteTableOnSlirp(table []byte) bool {
	for _, line := range strings.Split(string(table), "\n") {
		fields := strings.Fields(line)
		// Header row or short row: iface, destination, gateway, ...
		if len(fields) < 3 || fields[0] == "lo" {
			continue
		}
		if fields[2] == slirpGatewayHex {
			return true
		}
	}
	return false
}
