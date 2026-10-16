// SPDX-License-Identifier: AGPL-3.0-only

package hindsight

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
)

// The gate is the app's front door, in the literal sense: it is the container that
// publishes the app's port, so Traefik's generated service points here and
// nothing reaches the server around it.
//
// It exists because this app has two surfaces and Bloud routes an app on one port.
// The memory API serves the MCP endpoint on 8888 and the Control Plane serves the
// web UI on 9999, in one container, and Bloud publishes neither. A proxy that
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
)

// GateConfigurator owns the apps-hindsight-gate node: write the nginx config,
// then prove the endpoint a consumer actually calls.
type GateConfigurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
	api     *mcpAPI
}

// NewGateConfigurator builds the gate node's configurator.
func NewGateConfigurator(port int, deps configurator.Deps) *GateConfigurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &GateConfigurator{
		port:    port,
		secrets: deps.Secrets,
		logger:  logger.With("app", appName, "node", gateNodeName),
	}
	if deps.HTTP != nil {
		c.api = newAPI(deps.HTTP, func() string {
			return fmt.Sprintf("http://localhost:%d", c.port)
		})
	}
	return c
}

// Name returns the graph node this configurator owns.
func (c *GateConfigurator) Name() string { return gateNodeName }

// Port is the app port this node was constructed with, which is the catalog
// `port:` when registration passed 0. The conformance harness compares it against
// metadata.yaml, because this is the node that routes traffic.
func (c *GateConfigurator) Port() int { return c.port }

// PreStart writes the nginx config the gate container mounts. It is rewritten
// whenever either credential changes, which is also the only time the gate needs a
// recreate: nginx reads its config at start and never again.
func (c *GateConfigurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	bearer, accessKey, err := ensureCredentials(c.secrets, c.logger)
	if err != nil {
		return configurator.NoRestart(), err
	}
	content := renderNginxConfig(c.port, bearer, accessKey)

	confPath := filepath.Join(state.DataPath, configDir, nginxFileName)
	if err := os.MkdirAll(filepath.Dir(confPath), 0o755); err != nil {
		return configurator.NoRestart(), fmt.Errorf("creating the %s config directory: %w", appName, err)
	}
	changed, err := managedfile.Write(confPath, []byte(content), managedfile.ModeSharedConfig)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing the %s gate config: %w", appName, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}
	c.logger.Info("wrote hindsight gate config", "path", confPath,
		"mcp_bearer", bearer != "", "access_key", accessKey != "")
	return configurator.MustRestart("hindsight gate config rewritten"), nil
}

// PostStart proves the endpoint a consumer calls: a real MCP `initialize` at the
// published path, through the gate, carrying the published bearer.
//
// Every weaker probe lies in a different direction. The gate's own health route
// says nginx is up and says nothing about the server behind it. The server's health
// route is unreachable from here, because it publishes no port, and says nothing
// about the gate. Only the composed path can answer the question a consumer is
// about to ask, and presenting the bearer is part of what the probe proves: an
// unauthenticated probe would report a healthy node that every real consumer is
// about to be refused by.
//
// Because PostStart also runs on every resync pass, a path that breaks later,
// including one that breaks because a container moved, is surfaced rather than left
// silently up.
func (c *GateConfigurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if c.api == nil {
		return nil
	}
	bearer, _ := currentCredentials(c.secrets)
	if bearer == "" {
		// Nothing to present, so nothing to prove: the gate is locked on this path
		// and says so. Reporting an error here would hold the node below RUNNING
		// for a condition the operator has not had the chance to act on.
		c.logger.Warn("the hindsight MCP endpoint is locked until its bearer exists")
		return nil
	}
	if err := c.api.waitServing(ctx, bearer); err != nil {
		return fmt.Errorf("waiting for the hindsight MCP endpoint: %w", err)
	}
	c.logger.Info("hindsight is serving MCP through the gate")
	return nil
}

// --- nginx config rendering ---

// nginxTemplate is the whole nginx configuration, not a snippet: the container
// starts nginx with -c on the rendered file, so there is no stock config for it to
// be included by.
//
// Three locations, and nothing else exists:
//
//   - `/healthz`, answered here, so a server mid-restart is not a dead gate.
//   - the MCP prefix, proxied to the memory API.
//   - everything else, proxied to the Control Plane.
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

// mcpLocation is the one route that reaches the memory API.
//
// The match is a regex rather than a prefix because the endpoint is both `/mcp` and
// `/mcp/<bank>/`, and a plain prefix location would also claim `/mcpsomething` that
// no upstream serves.
//
// A variable upstream plus a resolver is what makes nginx re-resolve instead of
// pinning the address it read at config load. Without it, a recreate of the server
// container leaves this gate answering 502 until it is restarted too.
//
// proxy_buffering stays off because a streamable-HTTP server answers with a
// server-sent-events stream it holds open; buffering would hold the response until
// the upstream finished, which is never.
func mcpLocation() string {
	return fmt.Sprintf(`        location ~ ^/mcp(/|$) {
            resolver %s valid=10s ipv6=off;
            set $hindsight_api %s;
            proxy_pass http://$hindsight_api:%d;

            proxy_http_version 1.1;
            proxy_buffering off;
            proxy_read_timeout 3600s;
            proxy_send_timeout 3600s;
        }
`, nginxResolverPort, nodeName, apiPort)
}

// mcpLocationLocked is what the MCP path answers before the bearer exists:
// refusal, and no route to the API at all. Rendering the proxy with an empty bearer
// would be the one configuration worse than this, because the API would then be
// reachable with no credential in front of it.
func mcpLocationLocked() string {
	return `        location ~ ^/mcp(/|$) { return 401; }
`
}

// uiLocation is everything that is not MCP: the Control Plane.
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
            # Off for the same reason as the MCP route: the Control Plane reports
            # ingestion progress as it happens, and a buffered proxy turns a live
            # view into a page that fills in when the request ends.
            proxy_buffering off;
            proxy_read_timeout 300s;
        }
`, nginxResolverPort, nodeName, defaultPort)
}

// uiLocationLocked is what every non-MCP path answers before the access key
// exists. 503 rather than 401: the UI has no way to present a key it does not have
// yet, and "not ready" is the honest answer.
func uiLocationLocked() string {
	return `        location / { return 503; }
`
}

// renderNginxConfig renders the gate's configuration for one pair of credentials.
// Each half locks independently, so a rotation of one never opens the other.
func renderNginxConfig(port int, bearer, accessKey string) string {
	mcp, ui := mcpLocation(), uiLocation()
	if bearer == "" {
		mcp = mcpLocationLocked()
	}
	if accessKey == "" {
		ui = uiLocationLocked()
	}
	return fmt.Sprintf(nginxTemplate, port, mcp, ui)
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
