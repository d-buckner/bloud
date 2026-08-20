// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package netutil

import (
	"net"
	"net/url"
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
// It works by connecting a UDP socket to an external address — no traffic is sent.
func GetPrimaryIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// BuildBaseURLs returns a list of base URLs: the configured base URL first,
// then an http://<ip>[:port] URL for each detected local IP.
//
// The port is extracted from configuredBaseURL using net/url.Parse. If the
// configured URL has an explicit port (e.g. "http://bloud.local:8080"),
// the same port is used for IP-based URLs. If there's no explicit port
// (e.g. "http://bloud.local"), IP-based URLs omit the port too.
func BuildBaseURLs(configuredBaseURL string) []string {
	urls := []string{configuredBaseURL}

	parsed, err := url.Parse(configuredBaseURL)
	if err != nil {
		return urls
	}

	// Extract explicit port from the configured URL.
	// url.Port() returns "" when no port is specified (e.g. "http://bloud.local").
	port := parsed.Port()

	ips := DetectLocalIPs()
	for _, ip := range ips {
		u := &url.URL{
			Scheme: parsed.Scheme,
			Host:   ip,
		}
		if port != "" {
			u.Host = net.JoinHostPort(ip, port)
		}
		urls = append(urls, u.String())
	}

	return urls
}
