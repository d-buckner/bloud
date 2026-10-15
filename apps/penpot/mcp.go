// SPDX-License-Identifier: AGPL-3.0-only

package penpot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// mcpStreamPath is the frontend's proxied MCP endpoint. Penpot's own nginx maps
// /mcp/stream to the MCP server's streamable-HTTP listener and passes the query
// string through, and the credential travels as the `userToken` query parameter
// rather than a header: that is the shape Penpot's Integrations page hands the
// user, and the shape its in-app plugin dials with.
const mcpStreamPath = "/mcp/stream?userToken="

// mcpKeyQuery reads the operator's MCP key out of Penpot's own database.
//
// Why a read and not a mint: the key is Penpot's, and only Penpot can hand it to
// the half of the integration that lives in the browser. Penpot mints one MCP key
// per user (creating a second deletes the first), shows it once, and gives it to
// the plugin through its own UI; the plugin and the MCP client then meet at the
// MCP server under that string. A key Bloud invented would authenticate against
// nothing and would not be what the operator's browser tab dials with, so the
// only correct move is to read the one the operator created.
//
// The read is scoped to the operator's own profile by email, and to rows of type
// 'mcp' that have not expired: it cannot return a normal access token, and it
// cannot return another person's key. An operator who has not enabled MCP in
// Penpot has no row, which is the intended answer, not a failure.
//
// The %s is the operator's email, filled in by MCPKeyQuery. psql does not
// interpolate -v variables into a -c command string (it does when the same text
// arrives as a script on stdin, which is the distinction that made `:'email'`
// fail with "syntax error at or near :" on every pass until a container log
// caught it), and the exec channel carries no stdin, so the literal is built
// here. lookupableEmail is what makes that safe rather than clever.
const mcpKeyQuery = `SELECT t.token FROM access_token t
JOIN profile p ON p.id = t.profile_id
WHERE p.email = '%s' AND t.type = 'mcp'
  AND (t.expires_at IS NULL OR t.expires_at > now())
ORDER BY t.created_at DESC LIMIT 1`

// MCPKeyQuery fills the operator's email into the query. Callers must have
// checked lookupableEmail first; that check is the whole injection defense, so
// this function refuses to build a query for an address it would not vouch for.
//
// It is exported so the integration tier can run this exact text against a real
// Penpot database. A unit test can pin the query's shape but cannot say whether
// Postgres accepts it, and a version of this query that psql could not parse
// failed as a warning in a log rather than as a failed test.
func MCPKeyQuery(email string) string {
	if !lookupableEmail(email) {
		return ""
	}
	return fmt.Sprintf(mcpKeyQuery, email)
}

// mcpPublisher keeps the install's `mcp` contract offer in step with what Penpot
// itself currently has.
type mcpPublisher struct {
	secrets configurator.AppSecretsProvider
	exec    configurator.ExecFunc
	running configurator.ContainerRunningFunc
	email   string
	logger  *slog.Logger
}

// sync reads the operator's MCP key and publishes or unpublishes the contract.
//
// Every failure path logs and returns nil. Penpot is a working design tool with
// or without this wiring, and PostStart errors are terminal for the node, so a
// database that is briefly unreachable would take the app out of the dashboard
// to report a missing tool namespace. The periodic reconciliation pass re-runs
// this, which is what makes "not now" a fine answer: the operator enabling MCP in
// Penpot's UI is an outside-world change with no intent behind it, and the resync
// is how Bloud notices that class of thing at all.
func (p *mcpPublisher) sync(ctx context.Context) {
	if p.secrets == nil || p.exec == nil {
		p.logger.Debug("cannot read Penpot's MCP key: no secrets store or container exec")
		return
	}
	email := strings.TrimSpace(p.email)
	if email == "" {
		p.logger.Debug("no operator email; Penpot MCP stays unpublished")
		return
	}
	if !lookupableEmail(email) {
		p.logger.Warn("operator email is not usable as a database lookup key; Penpot MCP stays unpublished")
		return
	}
	if p.running != nil {
		up, err := p.running(ctx, postgresNode)
		if err != nil {
			p.logger.Warn("cannot tell whether Penpot's database is up; MCP key read deferred", "error", err)
			return
		}
		if !up {
			p.logger.Debug("Penpot database is not running; MCP key read deferred")
			return
		}
	}

	token, err := p.lookupKey(ctx, email)
	if err != nil {
		p.logger.Warn("reading Penpot MCP key failed; leaving the published value as it is", "error", err)
		return
	}

	published := p.secrets.GetAppSecret(appName, "httpToken")
	if token == published {
		return
	}
	if err := p.secrets.SetAppSecret(appName, "httpToken", token); err != nil {
		p.logger.Warn("publishing Penpot MCP key failed", "error", err)
		return
	}
	// The path carries the same string because that is how Penpot's endpoint
	// takes it. Clearing both together is what makes "no key" a single state a
	// consumer can see: an empty Token already means "not ready", and a stale
	// path with a token in it would be a half-published offer.
	path := ""
	if token != "" {
		path = mcpStreamPath + token
	}
	if err := p.secrets.SetAppContractValue(appName, "mcp", "path", path); err != nil {
		p.logger.Warn("publishing Penpot MCP endpoint failed", "error", err)
		return
	}

	switch {
	case token != "":
		p.logger.Info("published Penpot MCP endpoint for the operator's account", "email", email)
	case published != "":
		p.logger.Info("Penpot MCP key is gone from the app; unpublished the endpoint")
	}
}

// lookupKey runs the read inside Penpot's own postgres container.
//
// The email reaches the query as a quoted literal built by MCPKeyQuery,
// which is only callable with an address lookupableEmail vouches for. It travels
// in the command line rather than the environment because the exec channel has
// neither stdin nor a psql variable channel, and an email address is not the
// secret here; the secret is what the query returns, and that leaves only
// through stdout.
func (p *mcpPublisher) lookupKey(ctx context.Context, email string) (string, error) {
	query := MCPKeyQuery(email)
	if query == "" {
		return "", fmt.Errorf("refusing to look up MCP key for %q", email)
	}
	out, err := p.exec(ctx, postgresNode, nil, []string{
		"psql", "-U", "penpot", "-d", "penpot", "-At",
		"-v", "ON_ERROR_STOP=1",
		"-c", query,
	})
	if err != nil {
		return "", fmt.Errorf("query penpot database: %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", nil
	}
	// One line, no whitespace: anything else means the read did not return what
	// the query was written to return, and publishing a mangled key would show
	// up as an MCP client that cannot connect rather than as a fault here.
	if strings.ContainsAny(token, "\n\r\t ") {
		return "", fmt.Errorf("penpot returned an unusable MCP key")
	}
	return token, nil
}

// lookupableEmail reports whether an address is safe to place inside a SQL
// string literal. Emails that contain whitespace or quotes are legal in the spec
// and would need escaping to survive interpolation; they are rare enough that
// declining the lookup is better than building a quoting layer for them.
func lookupableEmail(email string) bool {
	return !strings.ContainsAny(email, " \t\n\r'\"\\")
}
