// SPDX-License-Identifier: AGPL-3.0-only

package netutil

import (
	"net"
	"strconv"
)

// DetectLocalIPs returns all non-loopback IPv4 addresses on the host.
func DetectLocalIPs() []string {
	var ips []string

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		ips = append(ips, ip.String())
	}

	return ips
}

// GetPrimaryIP returns the IP address used for outbound traffic to the internet.
// This is the host's primary non-loopback IP (i.e. eth0, not container bridges).
// It works by connecting a UDP socket to an external address; no traffic is sent.
func GetPrimaryIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// PortSuffix renders an entrypoint port for a URL, or "" when the port is the
// http default. 0 means "no statement about the port" and also renders as "",
// so a caller that does not know the port still produces a valid URL rather
// than a bare ":".
func PortSuffix(port int) string {
	if port == 0 || port == 80 {
		return ""
	}
	return ":" + strconv.Itoa(port)
}

// LANBaseURLs returns one plain-http base URL per detected non-loopback IPv4
// address, on the port the public entrypoint actually serves.
//
// The scheme is always http. A client using one of these URLs is on the same
// network reaching this machine's own address directly: there is no TLS
// terminator in that path, Bloud ships no certificate at the gateway, and no
// CA issues a certificate for a bare address. Deriving these from the primary
// host's URL instead was the bug: with a https primary, http://10.0.0.210:8080
// redirected its OAuth login to https://10.0.0.210, a scheme and port nothing
// answers on, and LAN access became unreachable for anyone not using the domain.
//
// port is the port the public entrypoint listens on (config.TraefikPort), not
// the port carried by the primary host's URL. Those are different numbers in
// every deployment that does not serve 80: the primary's port describes the
// public origin, which behind a TLS terminator is 443, while a LAN client
// reaches the socket Bloud actually opened. 0 and 80 both mean the http
// default and are left off the URL.
func LANBaseURLs(port int) []string {
	var urls []string
	for _, ip := range DetectLocalIPs() {
		urls = append(urls, "http://"+ip+PortSuffix(port))
	}
	return urls
}
