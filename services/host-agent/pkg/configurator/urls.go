// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package configurator

import (
	"net/url"
	"strings"
)

// AppExternalURL derives an app's public URL from the Bloud base URL by
// prepending the app name as a subdomain, the same way the host-agent derives
// routes and OIDC redirect URIs. baseURL is read on every call, so an admin host
// change takes effect without re-registering the configurator.
//
// It returns "http://<appName>.localhost:8080" when baseURL is nil, empty, or
// unparseable: the localhost dev default the SSO apps fall back to.
func AppExternalURL(baseURL func() string, appName string) string {
	raw := ""
	if baseURL != nil {
		raw = baseURL()
	}
	if raw == "" {
		return fallbackAppURL(appName)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fallbackAppURL(appName)
	}
	parsed.Host = appName + "." + parsed.Host
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return strings.TrimSuffix(parsed.String(), "/")
}

func fallbackAppURL(appName string) string {
	return "http://" + appName + ".localhost:8080"
}
