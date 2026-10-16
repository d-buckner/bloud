// SPDX-License-Identifier: AGPL-3.0-only

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

// AppExternalURL returns the app's public URL from the base URL the state
// carries. It is the AppState-shaped counterpart of the package function, so a
// configurator reads the URL inside PreStart or PostStart instead of threading
// a base-URL function through its constructor.
func (s *AppState) AppExternalURL(appName string) string {
	if s == nil {
		return fallbackAppURL(appName)
	}
	return AppExternalURL(s.PrimaryBaseURL, appName)
}

// BaseURL returns the current public base URL, or "" when there is no source.
// A nil PrimaryBaseURL is the CLI/test shape, and the empty answer lets a
// caller render the "not ready" form instead of guessing.
func (s *AppState) BaseURL() string {
	if s == nil || s.PrimaryBaseURL == nil {
		return ""
	}
	return s.PrimaryBaseURL()
}
