// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"fmt"
	"net/url"
	"strconv"
)

// endpoint is where Seerr dials a provider: the host, port, and TLS flag its
// settings want.
//
// Both halves of Seerr's configuration (the media server login and the DVR
// entries) take host/port/useSSL rather than a URL, so something has to split
// one into the other. It reads them off the binding's BaseURL rather than off
// Node and Port because that is the one field correct from both vantages: a
// container Bloud put on its own network and a server three rooms away both
// have a URL that reaches them, while only the first has a container name.
//
// That distinction is not cosmetic. An external provider's Node is empty and
// its Port is 0, so a settings body built from them reads `http://:0`, which
// Seerr accepts, stores, and then fails to reach. The address Bloud writes has
// to come from the place the operator actually typed it.
type endpoint struct {
	host   string
	port   int
	useSSL bool
}

// splitEndpoint parses a provider's BaseURL into the fields Seerr stores. A
// URL with no explicit port gets its scheme's default, because Seerr keeps the
// number rather than the scheme and "https with no port" has to mean 443.
func splitEndpoint(baseURL string) (endpoint, error) {
	if baseURL == "" {
		return endpoint{}, fmt.Errorf("the provider binding carries no address")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return endpoint{}, fmt.Errorf("parsing the provider address %q: %w", baseURL, err)
	}
	if parsed.Hostname() == "" {
		return endpoint{}, fmt.Errorf("the provider address %q has no host", baseURL)
	}
	ep := endpoint{host: parsed.Hostname(), useSSL: parsed.Scheme == "https"}
	portText := parsed.Port()
	switch {
	case portText != "":
		// nothing to fill; parsed.Port() already validated the digits
	case ep.useSSL:
		portText = "443"
	case parsed.Scheme == "http":
		portText = "80"
	default:
		return endpoint{}, fmt.Errorf("the provider address %q has an unsupported scheme", baseURL)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return endpoint{}, fmt.Errorf("the provider address %q has a port that is not a number", baseURL)
	}
	ep.port = port
	return ep, nil
}
