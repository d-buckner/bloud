// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

// Hermes owns its gateway, and the credential is the part of that ownership
// that has to survive contact with the image.
//
// The gateway's api_server refuses to start with a key shorter than 16
// characters, and the image's own stage2 hook mints one into
// $HERMES_HOME/.env when nothing is there. A key that exists only inside a
// file Bloud cannot read back is a key Bloud cannot publish, so the
// direction here is the other way: the instance mints the credential,
// seeds it into the file the app reads, and publishes the same value under
// the `agentGateway` contract for whatever front end is allowed to ask.
//
// The window for that seed is narrow and measured. The stage2 hook chowns
// $HERMES_HOME/.env to the container's runtime user and chmods it 0600 on
// *every* start, unconditionally ("so a host-mounted .env that was
// world-readable gets tightened"). Under rootless podman that runtime user
// is a host uid in the subuid range the agent is not, so from the second
// boot on the host can neither read nor write the file. PreStart on a fresh
// install is the last moment it can; everything after that goes through the
// running container.
const (
	// gatewayTokenSecret is the name this app publishes its gateway
	// credential under. It is the single secret the `agentGateway` contract
	// carries, so this one string has to agree with the contract registry,
	// the `provides:` entry in metadata.yaml, and the lookup a consumer's
	// binding goes through.
	gatewayTokenSecret = "httpToken"

	// gatewayEnvVar is the name Hermes' api_server reads its key from.
	gatewayEnvVar = "API_SERVER_KEY"

	// gatewayEnvFile is $HERMES_HOME/.env relative to this app's data dir.
	// Hermes loads it with override=True on every start, which is what makes
	// a value written here win over anything the image would generate.
	gatewayEnvFile = ".env"

	// gatewayTokenBytes is the entropy of a minted key, rendered as twice
	// that many hex characters. Well clear of the 16-character guard rather
	// than sitting on its boundary.
	gatewayTokenBytes = 32

	// gatewayEnvContainerPath is gatewayEnvFile as the running container
	// addresses it: the target of the through-the-container write.
	gatewayEnvContainerPath = containerHome + "/" + gatewayEnvFile

	// gatewayAbsentExitCode is what the container-side read exits with when
	// there is no .env yet. A dedicated code rather than a message match,
	// because Deps.Exec reports failure as a string and "No such file or
	// directory" is text that can change with locale while an exit status
	// cannot.
	gatewayAbsentExitCode = 44
)

// gatewayToken returns the credential this instance uses for the Hermes
// gateway, minting and persisting one the first time it is asked.
//
// It is stored rather than derived so every consumer of the contract and the
// gateway itself agree on one value that survives a restart, a reinstall of
// the app, and the deletion of the file the app keeps it in.
func (c *Configurator) gatewayToken() (string, error) {
	if existing := c.secrets.GetAppSecret(appName, gatewayTokenSecret); existing != "" {
		return existing, nil
	}

	key, err := mintGatewayToken()
	if err != nil {
		return "", err
	}
	if err := c.secrets.SetAppSecret(appName, gatewayTokenSecret, key); err != nil {
		return "", fmt.Errorf("publishing the Hermes gateway credential: %w", err)
	}
	c.logger.Info("minted the Hermes gateway credential", "secret", gatewayTokenSecret)
	return key, nil
}

// canOwnGatewayCredential reports whether this process has a secrets store to
// mint and publish into. The real runtime always does; a zero-value Deps is
// what a CLI or unit-test context produces, and there is no instance there to
// own a credential. Skipping is the same shape Hermes Web UI uses for an
// absent asset installer: report it, do not fail a pass that has no runtime
// behind it.
func (c *Configurator) canOwnGatewayCredential() bool {
	return c.secrets != nil
}

// mintGatewayToken draws a new key. Hex rather than base64 so the result is
// shell-safe and quote-free wherever it ends up, including inside a command
// line on the way into a container.
func mintGatewayToken() (string, error) {
	buf := make([]byte, gatewayTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("drawing entropy for the Hermes gateway credential: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// mergeGatewayEnvKey returns env carrying exactly one API_SERVER_KEY line,
// set to key.
//
// Every other line is preserved verbatim and in place. The .env is Hermes'
// file and Bloud is a guest in it: the app reads other keys out of the same
// document, so this replaces the one line this app claims and nothing else.
// A duplicate line is collapsed rather than appended-to because python-dotenv
// takes the last match, and a file that grows a new API_SERVER_KEY on every
// pass would silently change which credential is effective.
//
// A file that already carries the right value comes back byte-identical, so
// a pass with nothing to change writes nothing.
func mergeGatewayEnvKey(existing []byte, key string) []byte {
	lines := strings.Split(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n")
	// Drop the trailing blank lines the input's own final newline produced,
	// before the key is appended rather than after. Stripping afterwards
	// would find none, because the key line is the last thing there is, and
	// the blank element would end up sitting above it instead.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	out := make([]string, 0, len(lines)+1)
	placed := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), gatewayEnvVar+"=") {
			if !placed {
				out = append(out, gatewayEnvVar+"="+key)
				placed = true
			}
			continue
		}
		out = append(out, line)
	}
	if !placed {
		out = append(out, gatewayEnvVar+"="+key)
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

// gatewayEnvPath resolves this app's copy of $HERMES_HOME/.env on the host.
func gatewayEnvPath(state *configurator.AppState) string {
	return filepath.Join(state.DataPath, "data", gatewayEnvFile)
}

// seedGatewayCredential writes the gateway credential into Hermes' own .env
// while the host still has the right to write that file, and reports whether
// it changed anything.
//
// This is the PreStart half of the ownership story: on a fresh install the
// host owns the whole app tree, so seeding here is what makes the container
// boot with the instance's key already in place and the image's own
// generation path a no-op.
//
// A file the host can no longer read is not a failure. It means the app has
// already started and reclaimed the file, which is the state every pass
// after the first meets; convergeGatewayEnv owns that case against the live
// container.
func (c *Configurator) seedGatewayCredential(state *configurator.AppState) (bool, error) {
	if state == nil || state.DataPath == "" {
		return false, nil
	}
	if !c.canOwnGatewayCredential() {
		c.logger.Warn("no secrets provider available; the Hermes gateway credential cannot be minted or seeded")
		return false, nil
	}
	key, err := c.gatewayToken()
	if err != nil {
		return false, err
	}

	path := gatewayEnvPath(state)
	existing, readErr := os.ReadFile(path)
	switch {
	case readErr == nil:
	case errors.Is(readErr, fs.ErrNotExist):
		existing = nil
	case errors.Is(readErr, fs.ErrPermission):
		c.logger.Debug("the gateway env file is owned by the container; seeding from the host is done",
			"path", path)
		return false, nil
	default:
		return false, fmt.Errorf("reading %s: %w", path, readErr)
	}

	want := mergeGatewayEnvKey(existing, key)
	if bytes.Equal(existing, want) {
		return false, nil
	}
	// ModeHostOnly, not ModeSharedConfig: this file is a bag of credentials,
	// and the app that reads it takes ownership of it at boot anyway, so
	// there is no cross-uid reader to accommodate with a world-readable mode.
	changed, err := managedfile.Write(path, want, managedfile.ModeHostOnly)
	if err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	if changed {
		c.logger.Info("seeded the Hermes gateway credential", "path", path)
	}
	return changed, nil
}

// convergeGatewayEnv re-asserts the credential against the running
// container, which is the only process with a right to write its own .env
// once the first boot has chowned it.
//
// Without this the published credential could quietly stop matching reality:
// delete the file after first boot and the image mints its own on the next
// start, while the contract keeps handing consumers the value in the
// secrets store. Re-asserting through the container is what makes the
// published value the effective one rather than merely the intended one.
func (c *Configurator) convergeGatewayEnv(ctx context.Context) error {
	if c.exec == nil || !c.canOwnGatewayCredential() {
		return nil
	}
	key, err := c.gatewayToken()
	if err != nil {
		return err
	}

	current, err := c.readGatewayEnvThroughContainer(ctx)
	if err != nil {
		return err
	}
	want := mergeGatewayEnvKey(current, key)
	if bytes.Equal(current, want) {
		return nil
	}
	if err := c.writeGatewayEnvThroughContainer(ctx, want); err != nil {
		return fmt.Errorf("writing %s through %s: %w", gatewayEnvFile, nodeName, err)
	}
	c.logger.Info("re-asserted the Hermes gateway credential through the running container",
		"path", gatewayEnvContainerPath)
	return nil
}

// readGatewayEnvThroughContainer reads the app's .env from inside the
// container, base64-encoded so a stray warning on the combined stdout+stderr
// channel fails the decode instead of landing inside the file on the way back
// out. Absent is a distinct answer, not an error: the container-side test is
// what makes it one.
func (c *Configurator) readGatewayEnvThroughContainer(ctx context.Context) ([]byte, error) {
	script := fmt.Sprintf("if [ -f %s ]; then base64 %s; else exit %d; fi",
		gatewayEnvContainerPath, gatewayEnvContainerPath, gatewayAbsentExitCode)
	out, err := c.exec(ctx, nodeName, nil, []string{"sh", "-c", script})
	if err != nil {
		if strings.Contains(err.Error(), fmt.Sprintf("exit status %d", gatewayAbsentExitCode)) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s inside %s: %w", gatewayEnvFile, nodeName, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, fmt.Errorf("decoding %s read from %s: %w", gatewayEnvFile, nodeName, err)
	}
	return decoded, nil
}

// writeGatewayEnvThroughContainer replaces the whole file with `want`,
// base64-encoded so no byte of it can reach the shell.
//
// The mode is set explicitly after the write. A shell redirection does not
// change the mode of a file that already exists, it only truncates, and
// leaving the result dependent on whatever mode the file happened to have is
// how a credential file ends up readable by something that should not read
// it. 0600 is the mode the image itself enforces on every start.
func (c *Configurator) writeGatewayEnvThroughContainer(ctx context.Context, want []byte) error {
	encoded := base64.StdEncoding.EncodeToString(want)
	script := fmt.Sprintf("umask 077; printf %%s '%s' | base64 -d > %s; chmod %o %s",
		encoded, gatewayEnvContainerPath, 0o600, gatewayEnvContainerPath)
	_, err := c.exec(ctx, nodeName, nil, []string{"sh", "-c", script})
	return err
}
