// SPDX-License-Identifier: AGPL-3.0-only

package penpot

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// fakeSecrets records what the configurator publishes so the MCP contract can
// be asserted as a set of writes rather than as a log line.
type fakeSecrets struct {
	appValues   map[string]string
	contractVal map[string]string
	setErr      error
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{
		appValues:   map[string]string{},
		contractVal: map[string]string{},
	}
}

func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) { return "", nil }

func (f *fakeSecrets) GetAppSecret(_, key string) string { return f.appValues[key] }

func (f *fakeSecrets) SetAppSecret(_, key, value string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.appValues[key] = value
	return nil
}

func (f *fakeSecrets) contractKey(contract, key string) string { return contract + "/" + key }

func (f *fakeSecrets) SetAppContractValue(_, contract, key, value string) error {
	f.contractVal[f.contractKey(contract, key)] = value
	return nil
}

func (f *fakeSecrets) GetAppContractValue(_, contract, key string) string {
	return f.contractVal[f.contractKey(contract, key)]
}

func (f *fakeSecrets) DeleteAppSecrets(string) error { return nil }

func oidcState() *configurator.AppState {
	return &configurator.AppState{
		SSOEnabled: true,
		OIDC: &configurator.OIDCOutput{
			ClientID:     "penpot-client",
			ClientSecret: "penpot-secret",
			IssuerURL:    "http://sso.localhost:8080/application/o/penpot/",
			RedirectURI:  "http://penpot.localhost:8080/api/auth/oidc/callback",
		},
	}
}

// envLines parses a rendered env file the way the runtime does: comments and
// blanks skipped, one KEY=value per line, one layer of quotes stripped.
func envLines(t *testing.T, content string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for i, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		require.Truef(t, ok, "line %d is not KEY=value: %q", i+1, line)
		out[strings.TrimSpace(key)] = strings.Trim(value, `"'`)
	}
	return out
}

func TestRenderEnvWithSSO(t *testing.T) {
	content, err := renderEnv(publicSettings{
		publicURL: "http://penpot.localhost:8080",
		secretKey: "shared-signing-key",
		oidc:      oidcState().OIDC,
	})
	require.NoError(t, err)

	env := envLines(t, content)
	assert.Equal(t, "http://penpot.localhost:8080", env["PENPOT_PUBLIC_URI"])
	assert.Equal(t, "shared-signing-key", env["PENPOT_SECRET_KEY"])
	assert.Equal(t, "false", env["PENPOT_TELEMETRY_ENABLED"])

	for _, flag := range []string{
		"disable-registration", "enable-oidc-registration", "enable-login-with-oidc",
		"disable-email-verification", "enable-mcp", "enable-admin-console",
		"disable-onboarding",
	} {
		assert.Contains(t, env["PENPOT_FLAGS"], flag, "PENPOT_FLAGS must carry "+flag)
	}

	// The addresses Penpot's own entrypoints fall back to when a URI is unset.
	// A silent fallback to "penpot-backend" would point the install at a
	// container Bloud never created.
	assert.Equal(t, "http://apps-penpot-backend:6060", env["PENPOT_BACKEND_URI"])
	assert.Equal(t, "http://apps-penpot-exporter:6061", env["PENPOT_EXPORTER_URI"])
	assert.Equal(t, "http://apps-penpot-mcp:4401", env["PENPOT_MCP_URI"])
	assert.Equal(t, "http://apps-penpot-mcp:4402", env["PENPOT_MCP_URI_WS"])
	assert.Equal(t, "http://apps-penpot-admin-console:3000", env["PENPOT_ADMIN_CONSOLE_URI"])
	assert.Equal(t, "http://apps-penpot-frontend:8080", env["PENPOT_INTERNAL_URI"])
	assert.Equal(t, "redis://apps-penpot-valkey/0", env["PENPOT_REDIS_URI"])

	assert.Equal(t, "penpot-client", env["PENPOT_OIDC_CLIENT_ID"])
	assert.Equal(t, "penpot-secret", env["PENPOT_OIDC_CLIENT_SECRET"])
	// The issuer, not its origin: Penpot joins the discovery path onto this.
	assert.Equal(t, "http://sso.localhost:8080/application/o/penpot/", env["PENPOT_OIDC_BASE_URI"])
	assert.Contains(t, env["PENPOT_OIDC_SCOPES"], "email")
	// Penpot's SSRF guard refuses private addresses, and a localhost install's
	// issuer is one by design.
	assert.Equal(t, "sso.localhost", env["PENPOT_SSRF_ALLOWED_HOSTS"])
}

func TestRenderEnvWithoutSSO(t *testing.T) {
	content, err := renderEnv(publicSettings{publicURL: "https://bloud.example.com", secretKey: "k"})
	require.NoError(t, err)

	env := envLines(t, content)
	for key := range env {
		assert.False(t, strings.HasPrefix(key, "PENPOT_OIDC_"), "no OIDC block without a provider: "+key)
	}
	_, hasSSRF := env["PENPOT_SSRF_ALLOWED_HOSTS"]
	assert.False(t, hasSSRF, "no SSRF bypass without a provider to reach")
	assert.Equal(t, "https://bloud.example.com", env["PENPOT_PUBLIC_URI"])
}

func TestRenderEnvRejectsUnquotableValue(t *testing.T) {
	// The runtime's env parser strips quotes rather than escaping them, so a
	// value carrying one must fail the render instead of shipping a file the
	// app reads as something else.
	_, err := renderEnv(publicSettings{publicURL: `http://a"b`, secretKey: "k"})
	require.Error(t, err)
}

func TestSecretKeyIsReusedAcrossRenders(t *testing.T) {
	first, err := secretKey(nil)
	require.NoError(t, err)
	require.NotEmpty(t, first)

	second, err := secretKey(nil)
	require.NoError(t, err)
	assert.NotEqual(t, first, second, "a fresh install generates a new key")

	content, err := renderEnv(publicSettings{publicURL: "http://x", secretKey: first})
	require.NoError(t, err)
	reused, err := secretKey([]byte(content))
	require.NoError(t, err)
	assert.Equal(t, first, reused, "the key in the file is the key that stays")
}

func TestPreStartWritesEnvFileAndConverges(t *testing.T) {
	for _, tc := range []struct {
		name string
		node func(configurator.Deps) configurator.NodeLifecycle
	}{
		{"backend", func(d configurator.Deps) configurator.NodeLifecycle { return newEnvWriter(d) }},
		{"frontend", func(d configurator.Deps) configurator.NodeLifecycle { return newFrontendConfigurator(d) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			state := &configurator.AppState{DataPath: dir}
			cfg := tc.node(configurator.Deps{})

			first, err := cfg.PreStart(context.Background(), state)
			require.NoError(t, err)
			assert.True(t, first.RestartNeeded, "a first write must recreate the container")

			raw, err := os.ReadFile(filepath.Join(dir, "config", envFileName))
			require.NoError(t, err)
			assert.Contains(t, string(raw), "PENPOT_PUBLIC_URI=http://penpot.localhost:8080")

			admin, err := os.ReadFile(filepath.Join(dir, "config", adminEnvFileName))
			require.NoError(t, err, "the admin console's container is created from the second file")
			assert.Contains(t, string(admin), "PENPOT_PUBLIC_URI=http://penpot.localhost:8080")

			second, err := cfg.PreStart(context.Background(), state)
			require.NoError(t, err)
			assert.False(t, second.RestartNeeded, "a steady-state pass must touch nothing")
		})
	}
}

// TestBothNodesAgreeOnTheFile is the reason two nodes write one file: the second
// writer must converge on the first one's bytes, or the install oscillates
// between two configurations and recreates containers forever.
func TestBothNodesAgreeOnTheFile(t *testing.T) {
	dir := t.TempDir()
	state := oidcState()
	state.DataPath = dir

	backend := newEnvWriter(configurator.Deps{})
	frontend := newFrontendConfigurator(configurator.Deps{HTTP: appclient.Factory{}})

	_, err := backend.PreStart(context.Background(), state)
	require.NoError(t, err)
	before, err := os.ReadFile(filepath.Join(dir, "config", envFileName))
	require.NoError(t, err)

	res, err := frontend.PreStart(context.Background(), state)
	require.NoError(t, err)
	// The frontend still recreates its own container: it has never been created
	// from these bytes, whatever the file's own history is. What must not happen
	// is a second render. The oscillation this test guards is two nodes
	// disagreeing about the bytes, not each node restarting once for its own
	// container.
	assert.True(t, res.RestartNeeded, "each node recreates from its own stamp, not from the file's")

	after, err := os.ReadFile(filepath.Join(dir, "config", envFileName))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
	assert.Contains(t, string(after), "PENPOT_OIDC_CLIENT_ID=penpot-client")
}

// TestAdminEnvFileIsTheNarrowOne guards the split. The admin console reads
// PENPOT_ADMIN_CONSOLE_URI as the path it must serve itself under, not as the
// address others dial it on, so the shared file's value makes it exit(1) with
// "Admin Console must be served under /admin-console/, but
// PENPOT_ADMIN_CONSOLE_URI puts it at /". A dead admin console is not a missing
// admin page: the profile RPC calls it, so every request, the login page's
// first one included, answers 503 {:type :nitrate-unavailable}.
// TestEveryNodeRestartsWhenTheBytesChange pins the bug a live install produced.
// Two nodes write one env file, and managedfile.Write tells whoever writes second
// that nothing changed. Reading that as "nothing to do" is what left the frontend
// serving the previous flags forever after a configuration change: every later
// pass also found the file up to date, so nothing ever corrected it, and the only
// thing that noticed was the frontend's own readiness probe disagreeing with the
// app. The recreate decision therefore belongs to the node, not to the file.
func TestEveryNodeRestartsWhenTheBytesChange(t *testing.T) {
	dir := t.TempDir()
	state := &configurator.AppState{DataPath: dir}
	const url = "http://penpot.localhost:8080"

	for _, node := range []string{backendNode, frontendNode} {
		res, err := writeEnvFile(node, state, url, discardLogger())
		require.NoError(t, err)
		assert.True(t, res.RestartNeeded, "%s must recreate on its first pass", node)
	}

	// Steady state, twice over: a resync that changes nothing must restart nothing
	// for either node, however often it runs (invariant 2).
	for pass := 1; pass <= 2; pass++ {
		for _, node := range []string{backendNode, frontendNode} {
			res, err := writeEnvFile(node, state, url, discardLogger())
			require.NoError(t, err)
			assert.False(t, res.RestartNeeded, "%s on steady-state pass %d", node, pass)
		}
	}

	// The address moves, so the file moves. The backend writes it and reports the
	// change; the frontend then finds the file already correct, which is the exact
	// answer that used to be read as "nothing to do".
	for _, node := range []string{backendNode, frontendNode} {
		res, err := writeEnvFile(node, state, "https://penpot.example.com", discardLogger())
		require.NoError(t, err)
		assert.True(t, res.RestartNeeded,
			"%s must recreate after a config change even when the other node wrote the file first", node)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "config", envFileName))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "PENPOT_PUBLIC_URI=https://penpot.example.com")
}

func TestAdminEnvFileIsTheNarrowOne(t *testing.T) {
	settings := publicSettings{
		publicURL: "http://penpot.localhost:8080",
		secretKey: "the-signing-key",
		oidc: &configurator.OIDCOutput{
			ClientID:     "penpot-client",
			ClientSecret: "penpot-secret",
			IssuerURL:    "http://sso.localhost:8080/application/o/penpot/",
		},
	}

	full, err := renderEnv(settings)
	require.NoError(t, err)
	admin, err := renderAdminEnv(settings)
	require.NoError(t, err)

	wide, narrow := envLines(t, full), envLines(t, admin)
	assert.Equal(t, "http://"+adminConsoleNode+":3000", wide["PENPOT_ADMIN_CONSOLE_URI"])

	for _, key := range []string{
		"PENPOT_ADMIN_CONSOLE_URI", "PENPOT_BACKEND_URI", "PENPOT_MCP_URI",
		"PENPOT_FLAGS", "PENPOT_OIDC_CLIENT_SECRET", "PENPOT_SSRF_ALLOWED_HOSTS",
	} {
		assert.NotContains(t, narrow, key, "the console must not be told where it lives")
	}
	for _, key := range adminEnvKeys {
		assert.NotEmpty(t, narrow[key], "the console needs %s", key)
	}

	// The console derives the shared secret the backend authenticates to it with
	// out of this key, so the two files cannot disagree about it.
	assert.Equal(t, wide["PENPOT_SECRET_KEY"], narrow["PENPOT_SECRET_KEY"])
	assert.Equal(t, wide["PENPOT_PUBLIC_URI"], narrow["PENPOT_PUBLIC_URI"])
}

// configJSServer serves the file the frontend image renders at start, plus the
// boot-critical RPC the routed node's PostStart waits on.
func configJSServer(flags string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case configJSPath:
			_, _ = fmt.Fprintf(w, "var penpotFlags = %q;\n", flags)
		case bootProbePath:
			_, _ = w.Write([]byte(`["~:id","~u00000000-0000-0000-0000-000000000000"]`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestPostStartWaitsForTheBackendToServe(t *testing.T) {
	// The failure the first CI boot actually produced: nginx serving a correct
	// config.js, and get-profile answering 503 because the admin console is not
	// answering yet. The browser gets "temporarily unavailable"; PostStart has to
	// say so instead of promoting the node.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == configJSPath {
			_, _ = fmt.Fprintf(w, "var penpotFlags = %q;\n",
				"disable-registration enable-mcp enable-admin-console "+
					"enable-login-with-oidc enable-oidc-registration")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"type":"nitrate-unavailable"}`))
	}))
	defer server.Close()

	c := newFrontendConfigurator(configurator.Deps{HTTP: appclient.Factory{}})
	c.baseURL = server.URL

	// Bounded so the test proves the gate rejects without sitting out the wait
	// budget; the deadline expiring is the same code path as the budget doing so.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := c.PostStart(ctx, oidcState())
	require.Error(t, err)
	assert.Contains(t, err.Error(), bootProbePath)
}

func TestPostStartVerifiesServedFlags(t *testing.T) {
	t.Run("all required flags present", func(t *testing.T) {
		server := configJSServer("disable-email-verification disable-registration " +
			"enable-oidc-registration enable-login-with-oidc enable-mcp enable-admin-console")
		defer server.Close()

		c := newFrontendConfigurator(configurator.Deps{HTTP: appclient.Factory{}})
		c.baseURL = server.URL
		require.NoError(t, c.PostStart(context.Background(), oidcState()))
	})

	t.Run("missing flag is a fault", func(t *testing.T) {
		// The env file said enable-mcp; the app is not serving it, so the
		// container was created from something else.
		server := configJSServer("disable-registration enable-login-with-oidc enable-oidc-registration")
		defer server.Close()

		c := newFrontendConfigurator(configurator.Deps{HTTP: appclient.Factory{}})
		c.baseURL = server.URL
		err := c.PostStart(context.Background(), oidcState())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "enable-mcp")
	})

	t.Run("oidc flags only required with a provider", func(t *testing.T) {
		server := configJSServer("disable-registration enable-mcp enable-admin-console")
		defer server.Close()

		c := newFrontendConfigurator(configurator.Deps{HTTP: appclient.Factory{}})
		c.baseURL = server.URL
		require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{}))
	})
}

func TestFlagsIn(t *testing.T) {
	flags := flagsIn(`var penpotFlags = "enable-mcp disable-registration other";`)
	assert.True(t, flags["enable-mcp"])
	assert.True(t, flags["disable-registration"])
	assert.False(t, flags["other"], "only flag-shaped tokens are read out")
	assert.Empty(t, flagsIn("var penpotPublicURI = \"http://x\";"))
}

// execFunc is the Deps.Exec seam.
func execFunc(fn func(container string, env map[string]string, cmd []string) ([]byte, error)) configurator.ExecFunc {
	return func(_ context.Context, container string, env map[string]string, cmd []string) ([]byte, error) {
		return fn(container, env, cmd)
	}
}

func TestMCPPublisherPublishsOperatorKey(t *testing.T) {
	secrets := newFakeSecrets()
	var gotContainer string
	var gotCmd []string
	p := &mcpPublisher{
		secrets: secrets,
		email:   "operator@example.com",
		logger:  discardLogger(),
		exec: execFunc(func(container string, _ map[string]string, cmd []string) ([]byte, error) {
			gotContainer, gotCmd = container, cmd
			return []byte("  mcp-token-value\n"), nil
		}),
	}

	p.sync(context.Background())

	assert.Equal(t, "mcp-token-value", secrets.appValues["httpToken"])
	assert.Equal(t, mcpStreamPath+"mcp-token-value", secrets.contractVal["mcp/path"])
	assert.Equal(t, postgresNode, gotContainer)
	assert.Contains(t, strings.Join(gotCmd, " "), "operator@example.com")
	assert.NotContains(t, strings.Join(gotCmd, " "), "mcp-token-value",
		"the key comes back on stdout; it never goes in on the command line")
}

func TestMCPPublisherUnpublishesWhenKeyIsGone(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.appValues["httpToken"] = "stale"
	secrets.contractVal["mcp/path"] = mcpStreamPath + "stale"

	p := &mcpPublisher{
		secrets: secrets,
		email:   "operator@example.com",
		logger:  discardLogger(),
		exec:    execFunc(func(string, map[string]string, []string) ([]byte, error) { return []byte("\n"), nil }),
	}

	p.sync(context.Background())

	assert.Equal(t, "", secrets.appValues["httpToken"])
	assert.Equal(t, "", secrets.contractVal["mcp/path"])
}

func TestMCPPublisherLeavesPublishedValueOnFailure(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.appValues["httpToken"] = "still-good"

	p := &mcpPublisher{
		secrets: secrets,
		email:   "operator@example.com",
		logger:  discardLogger(),
		exec: execFunc(func(string, map[string]string, []string) ([]byte, error) {
			return nil, fmt.Errorf("database is starting")
		}),
	}

	p.sync(context.Background())
	assert.Equal(t, "still-good", secrets.appValues["httpToken"],
		"a failed read must not be read as a revoked key")
}

func TestMCPPublisherSkipsWithoutAUsableEmail(t *testing.T) {
	for name, email := range map[string]string{
		"empty":        "  ",
		"needs escape": `we"ird@example.com`,
	} {
		t.Run(name, func(t *testing.T) {
			secrets := newFakeSecrets()
			called := false
			p := &mcpPublisher{
				secrets: secrets,
				email:   email,
				logger:  discardLogger(),
				exec: execFunc(func(string, map[string]string, []string) ([]byte, error) {
					called = true
					return []byte("nope\n"), nil
				}),
			}
			p.sync(context.Background())
			assert.False(t, called, "no lookup, so no key can be published")
			assert.Empty(t, secrets.appValues)
		})
	}
}

func TestMCPPublisherToleratesMissingDeps(t *testing.T) {
	// The zero Deps a CLI or a test supplies has neither a secrets store nor a
	// container runtime; sync must be a no-op rather than a panic.
	p := &mcpPublisher{email: "operator@example.com", logger: discardLogger()}
	p.sync(context.Background())
}

func TestMCPKeyQueryIsScopedToTheOperator(t *testing.T) {
	q := strings.ToLower(MCPKeyQuery("operator@example.com"))
	require.NotEmpty(t, q, "a plain address must produce a query")
	assert.Contains(t, q, "t.type = 'mcp'", "never return a general access token")
	assert.Contains(t, q, "p.email = 'operator@example.com'", "the operator's own profile, quoted as a literal")
	assert.Contains(t, q, "expires_at", "an expired key is not published")
	assert.Contains(t, q, "limit 1")
}

// TestMCPKeyQueryRefusesAnUnquotableEmail pins the injection defense. The address
// is placed inside a SQL string literal, so an address that cannot be quoted
// produces no query at all rather than an escaped one: the lookup is optional and
// declining it is a logged no-op.
func TestMCPKeyQueryRefusesAnUnquotableEmail(t *testing.T) {
	for _, email := range []string{`a'b@example.com`, "a b@example.com", `a\b@example.com`, `a"b@example.com`} {
		assert.Empty(t, MCPKeyQuery(email), "an address that cannot be quoted must produce no query")
	}
}

// TestMetadataAgreesWithCode pins the two halves of the app together: the
// container names and env file path metadata.yaml declares are the ones the Go
// constants use, and the MCP offer is the shape the publisher writes.
func TestMetadataAgreesWithCode(t *testing.T) {
	raw, err := os.ReadFile("metadata.yaml")
	require.NoError(t, err)

	var md struct {
		Name string `yaml:"name"`
		Port int    `yaml:"port"`
		SSO  struct {
			Strategy     string `yaml:"strategy"`
			CallbackPath string `yaml:"callbackPath"`
		} `yaml:"sso"`
		Provides map[string]struct {
			Secrets       []string          `yaml:"secrets"`
			RuntimeValues []string          `yaml:"runtimeValues"`
			Values        map[string]string `yaml:"values"`
		} `yaml:"provides"`
		Containers []struct {
			Name    string `yaml:"name"`
			EnvFile string `yaml:"envFile"`
			Ports   []struct {
				Host      int `yaml:"host"`
				Container int `yaml:"container"`
			} `yaml:"ports"`
		} `yaml:"containers"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &md))

	assert.Equal(t, appName, md.Name)
	assert.Equal(t, defaultPort, md.Port)
	assert.Equal(t, "native-oidc", md.SSO.Strategy)

	nodes := map[string]string{}
	for _, c := range md.Containers {
		nodes[c.Name] = c.EnvFile
	}
	for _, node := range []string{
		backendNode, frontendNode, exporterNode, mcpNode, adminConsoleNode, postgresNode, valkeyNode,
	} {
		_, declared := nodes[node]
		assert.Truef(t, declared, "%s is not a container metadata.yaml declares", node)
	}
	require.Len(t, md.Containers, 7, "every container is accounted for by a constant")

	// The last declared container is the one the orchestrator treats as the
	// app's entry point (primaryContainerNode), and that is the node ensureSSO
	// provisions the OIDC client on, before that node's own container starts.
	// Penpot's backend reads the client once, at JVM start, so it must be that
	// node; reordering this file for readability would silently take SSO away
	// from the first boot.
	assert.Equalf(t, backendNode, md.Containers[len(md.Containers)-1].Name,
		"%s must stay last: it is the node Bloud provisions this app's SSO on", backendNode)

	// The containers that read Penpot's environment must name the file the
	// configurator writes; the ones that do not must not declare it, or a
	// container is created asking for a file nothing writes. The admin console
	// names the second file on purpose (adminEnvFileName).
	assert.Equalf(t, "{{appDataDir}}/config/"+adminEnvFileName, nodes[adminConsoleNode],
		"%s must read the narrow env file", adminConsoleNode)
	for _, node := range []string{backendNode, frontendNode, exporterNode} {
		assert.Equalf(t, "{{appDataDir}}/config/"+envFileName, nodes[node],
			"%s must read the generated env file", node)
	}
	for _, node := range []string{postgresNode, valkeyNode, mcpNode} {
		assert.Emptyf(t, nodes[node], "%s declares no env file", node)
	}

	require.Len(t, md.Containers, 7)
	for _, c := range md.Containers {
		if c.Name != frontendNode {
			continue
		}
		require.Len(t, c.Ports, 1)
		assert.Equal(t, defaultPort, c.Ports[0].Host, "Traefik routes to the published host port")
	}

	offer := md.Provides["mcp"]
	assert.Equal(t, []string{"httpToken"}, offer.Secrets)
	assert.Equal(t, []string{"path"}, offer.RuntimeValues)
	assert.Equal(t, "penpot", offer.Values["serverName"])
	assert.NotContains(t, offer.Values, "path",
		"the path is minted at runtime; a static one would be published before any key exists")
}
