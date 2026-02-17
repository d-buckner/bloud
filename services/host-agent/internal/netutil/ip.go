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
