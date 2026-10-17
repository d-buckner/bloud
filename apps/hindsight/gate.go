// SPDX-License-Identifier: AGPL-3.0-only

package hindsight

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/mcpwrapper"
)

// The gate is the app's front door, in the literal sense: it is the container that
// publishes the app's port, so Traefik's generated service points here and
// nothing reaches the server around it.
//
// It exists because this app has two surfaces and Bloud routes an app on one port.
// The memory API serves the REST API on 8888 and the Control Plane serves the web
// UI on 9999, in one container, and Bloud publishes neither. A proxy that
// branches on a path prefix is what lets one published port carry both. See
// metadata.yaml for why `extraPorts` is the wrong answer here.
//
// The gate performs no authentication of its own. Both surfaces behind it check
// their own credential: the API refuses anything that does not carry the published
// bearer, and the Control Plane refuses anything that has not presented the access
// key. What the gate does own is the locked shape: while either credential is
// missing it serves neither surface, so "not configured yet" is a refusal rather
// than an open door.
const (
	nginxFileName = "nginx.conf"

	// nginxResolverPort is the one value the gate container has to fill in itself.
	//
	// nginx resolves a variable upstream through a `resolver` directive, and the
	// address of that resolver is a property of the container's own network, not of
	// the host that wrote this file. So the rendered file keeps the token and the
	// container substitutes it at start, from its own /etc/resolv.conf. A host-side
	// read of /etc/resolv.conf would name the machine's resolver, which answers
	// nothing inside a podman user-defined network.
	nginxResolverPort = "@RESOLVER@"

	// nginxMountPath is where the gate container reads the rendered template, and
	// nginxRuntimePath is where it writes the finished config it starts nginx from.
	nginxMountPath   = "/etc/nginx/bloud.conf.template"
	nginxRuntimePath = "/tmp/bloud.conf"

	// apiPathPrefix is the first of the two paths this app publishes through the
	// `memory` contract. Every route the memory API serves below its versioned root
	// starts with it, and a memory client appends the rest itself.
	apiPathPrefix = "/v1"

	// apiVersionPath is the second published path, and the one that is not under the
	// prefix: the client's own version probe asks `/version`. A gate that routed only
	// `/v1` would leave a configured client unable to introduce itself to the server.
	apiVersionPath = "/version"

	// apiProbePath is where the readiness probe reads, and it is deliberately not
	// `/version`. That route answers without a credential, so a probe against it
	// would call a gate healthy while proving nothing about the bearer the contract
	// publishes. The bank list needs the bearer and answers 401 without it.
	apiProbePath = "/v1/default/banks"
)

// NewGateConfigurator builds the gate node's configurator. The shared half
// (write the config, report the recreate, probe the endpoint) lives in
// pkg/mcpwrapper.Gate; what is here is the app-specific half: the nginx
// rendering that splits one published port across the two surfaces.
func NewGateConfigurator(port int, deps configurator.Deps) *mcpwrapper.Gate {
	if port == 0 {
		port = defaultPort
	}
	return mcpwrapper.NewGate(port, deps, mcpwrapper.GateConfig{
		App:      appName,
		Node:     gateNodeName,
		TokenKey: httpTokenKey,
		Render: func(secrets configurator.AppSecretsProvider, logger *slog.Logger) (string, error) {
			bearer, accessKey, err := ensureCredentials(secrets, logger)
			if err != nil {
				return "", err
			}
			return renderNginxConfig(port, bearer, accessKey), nil
		},
		Probe: func(ctx context.Context, cl *appclient.Client, bearer string) error {
			// 45s is the same budget the MCP probe over this port used, and it
			// covers a server that is still running its first-boot migrations.
			// Written as a literal because the wait-budget harness reads the
			// expression at every Within() call site and fails one it cannot
			// evaluate.
			return cl.GET(apiProbePath).
				Header("Authorization", "Bearer "+bearer).
				Wait(bankListOK).Within(45 * time.Second).Do(ctx)
		},
	})
}

// bankListOK reports whether a response is the memory API answering with the bank
// list instead of something that merely listens on the port. The status alone
// cannot answer it: the Control Plane's login page answers 200 with HTML for a
// path it does not serve, and an nginx answering for a dead upstream answers 502.
// Reading the body separates "this app serves the API to this credential" from
// "something is listening".
func bankListOK(status int, body []byte) bool {
	if status != http.StatusOK {
		return false
	}
	var decoded struct {
		Banks []json.RawMessage `json:"banks"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return false
	}
	return decoded.Banks != nil
}

// --- nginx config rendering ---

// nginxTemplate is the whole nginx configuration, not a snippet: the container
// starts nginx with -c on the rendered file, so there is no stock config for it to
// be included by.
//
// Four locations, and nothing else exists:
//
//   - `/healthz`, answered here, so a server mid-restart is not a dead gate.
//   - `/v1` and `/version`, proxied to the memory API.
//   - everything else, proxied to the Control Plane.
//
// The memory API also serves `/health` and `/metrics`. Neither is routed: they
// answer without a credential, no consumer reads them, and a second unauthenticated
// path onto the server is surface the gate does not need to own. The gate answers
// `/healthz` for its own health check instead.
const nginxTemplate = `# Generated by Bloud; rewritten on every reconciliation.
worker_processes 1;
error_log /dev/stderr warn;
pid /tmp/nginx.pid;

events { worker_connections 256; }

http {
    # On, not off. This container's whole job is deciding which of two upstreams a
    # path belongs to, and a front that silently 502s is undebuggable: the access
    # log is where "the server container moved" becomes visible without exec'ing
    # into a container.
    access_log /dev/stdout;

    server {
        listen %d;
        server_name _;

        # Uploads arrive at the Control Plane, which accepts documents up to its own
        # HINDSIGHT_CP_MAX_UPLOAD_SIZE (100 MB by default). nginx would refuse at
        # its 1 MB default first, and the app would never see the file or get to
        # say why, so the two limits are set to agree.
        client_max_body_size 100m;

        # The container health check. Deliberately not proxied.
        location = /healthz { return 200 "ok"; }

%s
%s    }
}
`

// apiLocations are the routes that reach the memory API. Two entries for one
// upstream, because a memory client uses two: the versioned root for everything
// it does, and the unversioned `/version` it asks to introduce the server.
//
// Both matches are anchored and complete. A plain prefix location on `/v1` would
// also claim `/v123` that no upstream serves, and the exact match on `/version`
// keeps a future `/version/notes` from arriving here by accident.
//
// A variable upstream plus a resolver is what makes nginx re-resolve instead of
// pinning the address it read at config load. Without it, a recreate of the server
// container leaves this gate answering 502 until it is restarted too.
//
// proxy_buffering stays off because the API serves attachment bodies as whole
// files: a buffered proxy would hold one in memory before relaying it, and a
// long report would arrive all at once instead of as it is produced.
func apiLocations() string {
	return fmt.Sprintf(`        location ~ ^%s(/|$) {
            resolver %s valid=10s ipv6=off;
            set $hindsight_api %s;
            proxy_pass http://$hindsight_api:%d;

            proxy_http_version 1.1;
            proxy_buffering off;
            proxy_read_timeout 300s;
            proxy_send_timeout 300s;
        }

        location = %s {
            resolver %s valid=10s ipv6=off;
            set $hindsight_api %s;
            proxy_pass http://$hindsight_api:%d;

            proxy_http_version 1.1;
            proxy_read_timeout 300s;
        }
`, apiPathPrefix, nginxResolverPort, nodeName, apiPort,
		apiVersionPath, nginxResolverPort, nodeName, apiPort)
}

// apiLocationsLocked is what the memory paths answer before the bearer exists:
// refusal, and no route to the API at all. Rendering the proxy with an empty bearer
// would be the one configuration worse than this, because the API would then be
// reachable with no credential in front of it.
func apiLocationsLocked() string {
	return fmt.Sprintf(`        location ~ ^%s(/|$) { return 401; }

        location = %s { return 401; }
`, apiPathPrefix, apiVersionPath)
}

// uiLocation is everything that is not the memory API: the Control Plane.
//
// Host and X-Forwarded-* are left alone on purpose. nginx forwards the client's
// request headers to the upstream by default, and Traefik has already set the ones
// the Next.js server uses to build its own URLs. Overwriting them with this hop's
// local values would tell the app it is served over plain http on a local port,
// which is true of this hop and false of the one the browser saw.
func uiLocation() string {
	return fmt.Sprintf(`        location / {
            resolver %s valid=10s ipv6=off;
            set $hindsight_ui %s;
            proxy_pass http://$hindsight_ui:%d;

            proxy_http_version 1.1;
            # Off for the same reason as the API routes: the Control Plane reports
            # ingestion progress as it happens, and a buffered proxy turns a live
            # view into a page that fills in when the request ends.
            proxy_buffering off;
            proxy_read_timeout 300s;
        }
`, nginxResolverPort, nodeName, defaultPort)
}

// uiLocationLocked is what every other path answers before the access key
// exists. 503 rather than 401: the UI has no way to present a key it does not have
// yet, and "not ready" is the honest answer.
func uiLocationLocked() string {
	return `        location / { return 503; }
`
}

// renderNginxConfig renders the gate's configuration for one pair of credentials.
// Each half locks independently, so a rotation of one never opens the other.
func renderNginxConfig(port int, bearer, accessKey string) string {
	api, ui := apiLocations(), uiLocation()
	if bearer == "" {
		api = apiLocationsLocked()
	}
	if accessKey == "" {
		ui = uiLocationLocked()
	}
	return fmt.Sprintf(nginxTemplate, port, api, ui)
}

// nginxResolverScript is the container-side half of nginxResolverPort: the command
// the gate runs, which fills the token from its own resolv.conf before starting
// nginx.
//
// It writes the finished config to a container path rather than editing the mounted
// file. The mounted file is Bloud's, and Bloud compares its bytes to decide whether
// anything changed: a container that rewrote it would make every reconciliation pass
// see a diff, restart the gate, and never settle.
func nginxResolverScript() string {
	return strings.Join([]string{
		"set -e",
		`resolver="$(awk '/^nameserver/{print $2; exit}' /etc/resolv.conf)"`,
		`test -n "$resolver"`,
		`sed "s|` + nginxResolverPort + `|$resolver|" ` +
			nginxMountPath + " > " + nginxRuntimePath,
		"exec nginx -g 'daemon off;' -c " + nginxRuntimePath,
	}, "\n")
}
