// SPDX-License-Identifier: AGPL-3.0-only

package servarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const (
	// apiKeyHeader is the header every Servarr API request authenticates with
	// (?apikey= works too; the header keeps the key out of URLs and logs).
	apiKeyHeader = "X-Api-Key"

	// authMethodField and authRequiredField name the /config/host JSON fields.
	// The API is camel-case with string enums, unlike the PascalCase enum names
	// config.xml stores.
	authMethodField   = "authenticationMethod"
	authRequiredField = "authenticationRequired"

	// fieldAuthExternal and fieldAuthEnabled are the JSON spellings of the
	// states config.xml calls "External" and "Enabled".
	fieldAuthExternal = "external"
	fieldAuthEnabled  = "enabled"
)

// Client is the X-Api-Key HTTP surface Bloud uses against one instance.
type Client struct {
	name    string
	apiPath string
	api     *appclient.Client
}

// NewClient builds the client. apiPath is the versioned API root without
// slashes: "api/v3" (Sonarr/Radarr) or "api/v1" (Prowlarr).
func NewClient(f configurator.ClientFactory, name, apiPath string, baseURLFn func() string) *Client {
	return &Client{
		name:    name,
		apiPath: strings.Trim(apiPath, "/"),
		api:     f.New(appclient.Spec{Name: name, BaseURLFn: baseURLFn}),
	}
}

// hostConfigPath is the /config/host resource under this instance's API root.
// The leading slash matters: appclient joins the base URL and the path by
// concatenation.
func (c *Client) hostConfigPath() string {
	return "/" + c.apiPath + "/config/host"
}

// EnsureExternalAuth verifies through the instance's own API that external
// authentication is in effect and repairs it when it is not (a UI edit can
// rewrite config.xml). Returns whether it changed anything.
//
// The repair is a read-modify-write of the whole /config/host document: only
// the two auth fields are overwritten and everything else the app reported is
// echoed back, so no field of the resource has to be modelled here.
func (c *Client) EnsureExternalAuth(ctx context.Context, apiKey string) (bool, error) {
	host, err := c.getHostConfig(ctx, apiKey)
	if err != nil {
		return false, err
	}
	if isExternal(host) {
		return false, nil
	}

	host[authMethodField] = fieldAuthExternal
	host[authRequiredField] = fieldAuthEnabled
	// PUT /config/host rejects a null AllowedHosts and an empty Branch; both
	// come back populated from the GET above, which is why the full document
	// is round-tripped instead of posting a minimal body.
	err = c.api.PUT(c.hostConfigPath()).
		Header(apiKeyHeader, apiKey).
		JSON(host).
		OK(http.StatusOK).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("%s: enabling external authentication: %w", c.name, err)
	}

	// Confirm through the API rather than trusting the write: the app keeps
	// reporting the old mode when it rejects the change.
	after, err := c.getHostConfig(ctx, apiKey)
	if err != nil {
		return false, err
	}
	if !isExternal(after) {
		return false, fmt.Errorf("%s: %s still reports %s=%q after enabling external authentication",
			c.name, c.hostConfigPath(), authMethodField, observedAuthMethod(after))
	}
	return true, nil
}

// getHostConfig reads /config/host. A non-2xx (401/403 when the API key is
// wrong, 5xx while the app boots) surfaces as an appclient HTTPError naming
// the app and status; an unparseable body is reported with the same framing.
func (c *Client) getHostConfig(ctx context.Context, apiKey string) (map[string]any, error) {
	body, err := c.api.GET(c.hostConfigPath()).
		Header(apiKeyHeader, apiKey).
		OK(http.StatusOK).
		Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: reading %s: %w", c.name, c.hostConfigPath(), err)
	}

	var host map[string]any
	if err := json.Unmarshal(body, &host); err != nil {
		// The GET accepted exactly one status, so a decode failure is always
		// attributable to that 200 response.
		return nil, fmt.Errorf("%s: reading %s → %d: decode JSON: %w",
			c.name, c.hostConfigPath(), http.StatusOK, err)
	}
	if host == nil {
		return nil, fmt.Errorf("%s: reading %s → %d: empty JSON document",
			c.name, c.hostConfigPath(), http.StatusOK)
	}
	return host, nil
}

// isExternal reports whether the host config declares external auth. Servarr
// parses the enum case-insensitively, so the comparison does too.
func isExternal(host map[string]any) bool {
	return strings.EqualFold(observedAuthMethod(host), fieldAuthExternal)
}

// observedAuthMethod renders the mode the app reported, for error messages. A
// missing field is named explicitly rather than rendered as "".
func observedAuthMethod(host map[string]any) string {
	mode, ok := host[authMethodField].(string)
	if !ok || mode == "" {
		return "(unset)"
	}
	return mode
}

// TransientFailure reports whether err is the kind of HTTP failure that is
// worth retrying on the next reconciliation instead of failing the node: a
// transport failure (the app's container is restarting, or its name does not
// resolve yet) or a status the app itself marks retryable (408, 425, 429, 5xx).
//
// It exists because ERROR is terminal in the orchestrator (a node in ERROR is
// skipped on every later pass until its status is explicitly reset), so a
// consumer that fails its PostStart because a *sibling* hiccuped stays "failed"
// until an operator reinstalls it or restarts host-agent, while the link it was
// wiring would have been created by the next pass. Anything a caller cannot
// classify (a 4xx: a rejected key, an invalid document) is a real fault and is
// reported as such.
func TransientFailure(err error) bool {
	var httpErr *appclient.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.IsTransient()
	}
	return false
}
