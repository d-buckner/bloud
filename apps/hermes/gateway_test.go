// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// errExit builds the error shape Deps.Exec produces for a non-zero exit, so a
// test can raise the script's dedicated "no such file" status rather than a
// generic failure.
func errExit(code int) error {
	return fmt.Errorf("exec: exit status %d", code)
}

// fakeSecrets implements configurator.AppSecretsProvider. Only the app-secret
// pair is exercised here; the contract-value half exists to satisfy the
// interface and is asserted to stay untouched, because a gateway credential
// that leaked into the runtime-value channel would be published to consumers
// that never asked for it.
type fakeSecrets struct {
	appSecrets  map[string]string
	writes      []string
	contractSet []string
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{appSecrets: map[string]string{}}
}

func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) { return "", nil }

func (f *fakeSecrets) GetAppSecret(app, key string) string {
	return f.appSecrets[app+"/"+key]
}

func (f *fakeSecrets) SetAppSecret(app, key, value string) error {
	f.writes = append(f.writes, app+"/"+key)
	f.appSecrets[app+"/"+key] = value
	return nil
}

func (f *fakeSecrets) SetAppContractValue(app, contract, key, value string) error {
	f.contractSet = append(f.contractSet, app+"/"+contract+"/"+key+"="+value)
	return nil
}

func (f *fakeSecrets) GetAppContractValue(string, string, string) string { return "" }

func gatewayTestConfigurator(t *testing.T, secrets configurator.AppSecretsProvider, exec configurator.ExecFunc) *Configurator {
	t.Helper()
	return NewConfigurator(0, configurator.Deps{Logger: quietLogger(), Secrets: secrets, Exec: exec})
}

func TestMergeGatewayEnvKey(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		want     string
	}{
		{
			name:     "empty file gets the line",
			existing: "",
			want:     "API_SERVER_KEY=k1\n",
		},
		{
			name:     "other keys are kept in place",
			existing: "OTHER=1\nmore=2\n",
			want:     "OTHER=1\nmore=2\nAPI_SERVER_KEY=k1\n",
		},
		{
			name:     "a differing value is replaced in position",
			existing: "FIRST=1\nAPI_SERVER_KEY=old\nLAST=2\n",
			want:     "FIRST=1\nAPI_SERVER_KEY=k1\nLAST=2\n",
		},
		{
			name:     "duplicate lines collapse to one",
			existing: "API_SERVER_KEY=a\nAPI_SERVER_KEY=b\n",
			want:     "API_SERVER_KEY=k1\n",
		},
		{
			name:     "a differently named key is not ours",
			existing: "API_SERVER_KEY_FILE=x\n",
			want:     "API_SERVER_KEY_FILE=x\nAPI_SERVER_KEY=k1\n",
		},
		{
			name:     "CRLF input normalises to LF",
			existing: "OTHER=1\r\nAPI_SERVER_KEY=old\r\n",
			want:     "OTHER=1\nAPI_SERVER_KEY=k1\n",
		},
		{
			name:     "trailing blank lines are normalised",
			existing: "OTHER=1\n\n\n",
			want:     "OTHER=1\nAPI_SERVER_KEY=k1\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeGatewayEnvKey([]byte(tc.existing), "k1")
			if string(got) != tc.want {
				t.Fatalf("merge = %q, want %q", got, tc.want)
			}
			// The merge has to be a fixed point: a second pass over its own
			// output changes nothing, which is what lets a steady-state
			// reconciliation cycle write nothing at all.
			again := mergeGatewayEnvKey(got, "k1")
			if string(again) != tc.want {
				t.Fatalf("re-merge = %q, want %q (merge is not a fixed point)", again, tc.want)
			}
		})
	}
}

func TestMintGatewayTokenClearsTheListenerGuard(t *testing.T) {
	// The image's api_server refuses to start under 16 characters. A mint
	// that landed near that boundary would fail the listener rather than the
	// install, so the margin is asserted here rather than left to arithmetic.
	key, err := mintGatewayToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) < 32 {
		t.Fatalf("minted key length = %d, want at least 32", len(key))
	}
	if strings.ContainsAny(key, "'\"$`\\ \n") {
		t.Fatalf("minted key contains shell-significant characters: %q", key)
	}
	other, err := mintGatewayToken()
	if err != nil {
		t.Fatal(err)
	}
	if key == other {
		t.Fatal("two mints produced the same credential")
	}
}

func TestGatewayTokenMintsOnceAndReuses(t *testing.T) {
	secrets := newFakeSecrets()
	c := gatewayTestConfigurator(t, secrets, nil)

	first, err := c.gatewayToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == "" {
		t.Fatal("first call minted nothing")
	}
	if len(secrets.writes) != 1 || secrets.writes[0] != appName+"/"+gatewayTokenSecret {
		t.Fatalf("writes = %v, want one publish of %s", secrets.writes, gatewayTokenSecret)
	}

	second, err := c.gatewayToken()
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("second call returned %q, want the minted %q", second, first)
	}
	if len(secrets.writes) != 1 {
		t.Fatalf("second call re-published: %v", secrets.writes)
	}
}

func TestGatewayTokenPicksUpAStoredCredential(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.appSecrets[appName+"/"+gatewayTokenSecret] = "already-published"
	c := gatewayTestConfigurator(t, secrets, nil)

	got, err := c.gatewayToken()
	if err != nil {
		t.Fatal(err)
	}
	if got != "already-published" {
		t.Fatalf("gatewayToken() = %q, want the stored value", got)
	}
	if len(secrets.writes) != 0 {
		t.Fatalf("a stored credential was overwritten: %v", secrets.writes)
	}
}

func TestSeedGatewayCredentialWritesTheKeyIntoTheEnvFile(t *testing.T) {
	dir := t.TempDir()
	state := &configurator.AppState{DataPath: dir}
	secrets := newFakeSecrets()
	c := gatewayTestConfigurator(t, secrets, nil)

	changed, err := c.seedGatewayCredential(state)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first seed reported no change")
	}

	raw, err := os.ReadFile(filepath.Join(dir, "data", gatewayEnvFile))
	if err != nil {
		t.Fatal(err)
	}
	want := gatewayEnvVar + "=" + secrets.GetAppSecret(appName, gatewayTokenSecret) + "\n"
	if string(raw) != want {
		t.Fatalf("env file = %q, want %q", raw, want)
	}
	// The credential must not have gone out through the non-secret channel.
	if len(secrets.contractSet) != 0 {
		t.Fatalf("credential published as a contract value: %v", secrets.contractSet)
	}
}

func TestSeedGatewayCredentialIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	state := &configurator.AppState{DataPath: dir}
	c := gatewayTestConfigurator(t, newFakeSecrets(), nil)

	if _, err := c.seedGatewayCredential(state); err != nil {
		t.Fatal(err)
	}
	changed, err := c.seedGatewayCredential(state)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second seed changed the file; a steady-state pass would restart the container forever")
	}
}

func TestSeedGatewayCredentialPreservesOperatorLines(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dataDir, gatewayEnvFile)
	if err := os.WriteFile(envPath, []byte("OPERATOR_THING=keep\nAPI_SERVER_KEY=stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets := newFakeSecrets()
	c := gatewayTestConfigurator(t, secrets, nil)

	if _, err := c.seedGatewayCredential(appState(dir)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "OPERATOR_THING=keep\n") {
		t.Fatalf("operator line lost: %q", raw)
	}
	if !strings.Contains(string(raw), gatewayEnvVar+"="+secrets.GetAppSecret(appName, gatewayTokenSecret)) {
		t.Fatalf("credential not written: %q", raw)
	}
	if strings.Contains(string(raw), "stale") {
		t.Fatalf("stale credential left behind: %q", raw)
	}
}

func TestSeedGatewayCredentialWithoutASecretsProviderSkips(t *testing.T) {
	dir := t.TempDir()
	c := gatewayTestConfigurator(t, nil, nil)

	changed, err := c.seedGatewayCredential(&configurator.AppState{DataPath: dir})
	if err != nil {
		t.Fatalf("a CLI-shaped Deps must not fail the pass: %v", err)
	}
	if changed {
		t.Fatal("changed without a secrets store")
	}
	if _, err := os.Stat(filepath.Join(dir, "data", gatewayEnvFile)); !os.IsNotExist(err) {
		t.Fatalf("wrote an env file with nothing to mint from: %v", err)
	}
}

func TestSeedGatewayCredentialWhenTheContainerOwnsTheFile(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	envPath := filepath.Join(dataDir, gatewayEnvFile)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte(gatewayEnvVar+"=from-container\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadableByHost(t, envPath)

	c := gatewayTestConfigurator(t, newFakeSecrets(), nil)
	changed, err := c.seedGatewayCredential(&configurator.AppState{DataPath: dir})
	if err != nil {
		t.Fatalf("a container-owned env file is the normal steady state, not an error: %v", err)
	}
	if changed {
		t.Fatal("reported a change it could not have made")
	}
}

func TestConvergeGatewayEnvWritesThroughTheContainerWhenItDiffers(t *testing.T) {
	fx := &recordingExec{out: b64("OPERATOR_THING=keep\nAPI_SERVER_KEY=stale\n") + "\n"}
	secrets := newFakeSecrets()
	c := gatewayTestConfigurator(t, secrets, fx.fn)

	if err := c.convergeGatewayEnv(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fx.calls) != 2 {
		t.Fatalf("exec calls = %d, want a read and a write", len(fx.calls))
	}
	writeCmd := fx.calls[1]
	if !strings.Contains(writeCmd, gatewayEnvContainerPath) {
		t.Fatalf("write did not target the container path: %q", writeCmd)
	}
	if strings.Contains(writeCmd, "stale") {
		t.Fatalf("the stale value was echoed into the write: %q", writeCmd)
	}
	// The credential travels encoded, so no byte of it can reach the shell.
	if !strings.Contains(writeCmd, "base64 -d") {
		t.Fatalf("write is not the encoded form: %q", writeCmd)
	}
	if !strings.Contains(writeCmd, "chmod 600") {
		t.Fatalf("write does not set the mode the image enforces: %q", writeCmd)
	}
}

func TestConvergeGatewayEnvWritesNothingWhenTheContainerAlreadyMatches(t *testing.T) {
	secrets := newFakeSecrets()
	key := "0123456789abcdef0123456789abcdef"
	secrets.appSecrets[appName+"/"+gatewayTokenSecret] = key
	fx := &recordingExec{out: b64("OPERATOR_THING=keep\n"+gatewayEnvVar+"="+key+"\n") + "\n"}
	c := gatewayTestConfigurator(t, secrets, fx.fn)

	if err := c.convergeGatewayEnv(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fx.calls) != 1 {
		t.Fatalf("exec calls = %d, want only the read", len(fx.calls))
	}
}

func TestConvergeGatewayEnvTreatsAnAbsentFileAsAbsent(t *testing.T) {
	// The container-side read has to distinguish "no such file" from "read
	// failed". Without that distinction a first converge pass errors on a
	// file that simply has not been created yet.
	fx := &recordingExec{err: errExit(gatewayAbsentExitCode)}
	c := gatewayTestConfigurator(t, newFakeSecrets(), fx.fn)

	got, err := c.readGatewayEnvThroughContainer(context.Background())
	if err != nil {
		t.Fatalf("absent must not be an error: %v", err)
	}
	if got != nil {
		t.Fatalf("absent returned %q, want nil", got)
	}
}

func TestConvergeGatewayEnvSurfacesARealReadFailure(t *testing.T) {
	fx := &recordingExec{err: errors.New("exec: exit status 1")}
	c := gatewayTestConfigurator(t, newFakeSecrets(), fx.fn)

	if err := c.convergeGatewayEnv(context.Background()); err == nil {
		t.Fatal("a failed container read must surface, not be read as an empty file")
	}
}

func TestConvergeGatewayEnvWithoutExecIsANoOp(t *testing.T) {
	c := gatewayTestConfigurator(t, newFakeSecrets(), nil)
	if err := c.convergeGatewayEnv(context.Background()); err != nil {
		t.Fatalf("no runtime must not fail the pass: %v", err)
	}
}

func TestPreStartSeedsTheGatewayCredentialAlongsideConfig(t *testing.T) {
	dir := t.TempDir()
	secrets := newFakeSecrets()
	c := gatewayTestConfigurator(t, secrets, nil)

	res, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !res.RestartNeeded {
		t.Fatal("seeding the credential on a fresh install must ask for the container to come up with it")
	}
	if !strings.Contains(res.Reason, "gateway") {
		t.Fatalf("restart reason = %q, want it to name the gateway", res.Reason)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", gatewayEnvFile)); err != nil {
		t.Fatalf("env file not written by PreStart: %v", err)
	}
}

func TestPreStartGatewayOnlyChangeDoesNotRewriteConfig(t *testing.T) {
	dir := t.TempDir()
	secrets := newFakeSecrets()
	c := gatewayTestConfigurator(t, secrets, nil)
	// SSO on so the first pass has something to write into config.yaml.
	// Without it the config half converges to an empty document and never
	// creates the file, which would make "config.yaml was not rewritten" a
	// vacuous assertion about a file that does not exist.
	state := &configurator.AppState{
		DataPath:   dir,
		SSOEnabled: true,
		OIDC: &configurator.OIDCOutput{
			ClientID:  "hermes-client",
			IssuerURL: "http://sso.localhost:8080/application/o/hermes/",
		},
	}

	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	// Delete only the env file: the config on disk is already converged, so
	// the next pass must re-seed the credential and leave config.yaml alone.
	envPath := filepath.Join(dir, "data", gatewayEnvFile)
	if err := os.Remove(envPath); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "data", configFileName)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	res, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !res.RestartNeeded {
		t.Fatal("a missing credential must be re-seeded, not ignored")
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("config.yaml was rewritten by a gateway-only pass")
	}
	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf("the credential was not re-seeded: %v", err)
	}
}

func TestPreStartIsQuietOnceTheCredentialIsSeeded(t *testing.T) {
	dir := t.TempDir()
	c := gatewayTestConfigurator(t, newFakeSecrets(), nil)
	state := &configurator.AppState{DataPath: dir}

	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	res, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if res.RestartNeeded {
		t.Fatalf("a converged install still asks for a restart: %q", res.Reason)
	}
}

// appState is a small helper so the tests read as app state rather than as
// path plumbing.
func appState(dir string) *configurator.AppState { return &configurator.AppState{DataPath: dir} }
