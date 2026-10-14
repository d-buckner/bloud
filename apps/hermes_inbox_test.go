// SPDX-License-Identifier: AGPL-3.0-only

package apps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/apps/configtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedInboxSource is the one host directory both Hermes containers mount as the
// attachment inbox, and sharedInboxDestination is where both mount it.
//
// The inbox exists because hermes-webui hands the agent a literal absolute path:
// it writes the upload, then puts that path in the message text, and the agent
// opens it with its own file tools. Two containers agree on such a path only if
// they mount the same directory at the same destination, and that agreement is
// the entire content of this file. Get it wrong on either side and every upload
// reads as ENOENT inside the agent, which is the bug this tree fixes.
const (
	sharedInboxSource      = "{{dataDir}}/shared-trees/hermes/inbox"
	sharedInboxDestination = "/bloud-hermes/inbox"
)

// inboxMount returns the shared inbox mount declared by one app's container.
//
// It fails on zero or two matches rather than returning the first: a second mount
// of the same tree at a second destination is precisely the drift this guards, and
// a lenient lookup would let it pass unnoticed.
func inboxMount(t *testing.T, dir, containerName string) configtest.MetadataVolume {
	t.Helper()

	md := configtest.LoadMetadata(t, dir)
	var found []configtest.MetadataVolume
	for _, c := range md.Containers {
		if c.Name != containerName {
			continue
		}
		for _, v := range c.Volumes {
			if v.Source == sharedInboxSource {
				found = append(found, v)
			}
		}
	}
	require.Len(t, found, 1, "%s/%s must mount the shared inbox exactly once", dir, containerName)
	return found[0]
}

// TestSharedInboxMountsAgree pins the one fact the whole mechanism rests on: the
// path hermes-webui writes is a path Hermes can open.
//
// Nothing else about the two apps has to match, so this is the only place the
// pairing can be asserted. Each app's own configurator test pins its Go constant
// against its own metadata, and together the two kinds of test close the loop.
func TestSharedInboxMountsAgree(t *testing.T) {
	agent := inboxMount(t, "hermes", "apps-hermes")
	web := inboxMount(t, "hermes-webui", "apps-hermes-webui")

	assert.Equal(t, sharedInboxDestination, agent.Destination,
		"the agent's inbox destination drifted from the shared constant")
	assert.Equal(t, agent.Destination, web.Destination,
		"hermes-webui mounts the inbox at %q but Hermes mounts it at %q; the web UI "+
			"writes the first path and the agent is asked to open the second",
		web.Destination, agent.Destination)
}

// TestWebuiAttachmentDirIsTheInboxMount keeps the app's own notion of where uploads
// go equal to the mount. Left unset the app defaults to
// $HERMES_WEBUI_STATE_DIR/attachments, which is /data/attachments: this container's
// private state directory, which Hermes has never seen. A mount that the app is
// never told about would fix nothing.
func TestWebuiAttachmentDirIsTheInboxMount(t *testing.T) {
	md := configtest.LoadMetadata(t, "hermes-webui")
	var env map[string]string
	for _, c := range md.Containers {
		if c.Name == "apps-hermes-webui" {
			env = c.Environment
		}
	}
	require.NotEmpty(t, env, "apps-hermes-webui declares no environment")

	dest := inboxMount(t, "hermes-webui", "apps-hermes-webui").Destination
	assert.Equal(t, dest, env["HERMES_WEBUI_ATTACHMENT_DIR"],
		"HERMES_WEBUI_ATTACHMENT_DIR must name the inbox mount destination")
}

// TestWebuiUmaskMakesUploadsReadable guards the half of the fix that is not a
// mount. The image's init runs the server with umask 0077, so an upload lands as a
// 0700 directory holding a 0600 file. Hermes runs as a different uid with no shared
// group, so it could neither traverse that directory nor open that file even with
// the inbox mounted at the identical path.
//
// Setting the umask in the container command does not work: the init script sets
// `umask 0077` itself, again after it drops privileges, so the command has to run a
// patched copy of the script. That makes the command itself the thing that can
// rot, so these run it. The three absolute paths in the real command body are
// rewritten onto a fixture init script and the body is executed, which is the only
// way to catch a patch that silently stops applying.
func TestWebuiUmaskMakesUploadsReadable(t *testing.T) {
	// One bare `umask 0077`, then report the umask the process actually ended up
	// with. The patched copy is what gets exec'd, so this prints the answer.
	const fixture = "#!/bin/bash\numask 0077\nprintf 'UMASK=%s\\n' \"$(umask)\"\n"

	out, err := runWebuiInitPatch(t, fixture)
	require.NoError(t, err, "the container command failed against a matching fixture:\n%s", out)
	assert.Contains(t, out, "UMASK=0022",
		"the server must end up with a umask that leaves uploads world-readable, or "+
			"the agent cannot open them whatever the mount says:\n%s", out)
}

// TestWebuiUmaskPatchStopsWhenTheLineMoves is why the guard checks the original
// rather than the output. An image bump that reworded or indented the init's umask
// line would leave a sed that matches nothing, and a guard that only looked for
// `umask 0022` in the result would sail past it: uploads quietly back to 0600,
// every test green.
func TestWebuiUmaskPatchStopsWhenTheLineMoves(t *testing.T) {
	out, err := runWebuiInitPatch(t, "#!/bin/bash\n  umask 0027\n")
	require.Error(t, err, "a fixture the patch cannot apply to must stop the container:\n%s", out)
	assert.Contains(t, out, "stale", "the failure must say the patch is stale:\n%s", out)
}

// TestWebuiUmaskPatchStopsOnAnUnexpectedCount pins the count guard. A second bare
// `umask 0077` upstream would be widened by the same sed, and nothing here can
// tell whether that second one guards something that must stay private. An
// unexpected shape is a stop, not a guess.
func TestWebuiUmaskPatchStopsOnAnUnexpectedCount(t *testing.T) {
	out, err := runWebuiInitPatch(t, "#!/bin/bash\numask 0077\numask 0077\n")
	require.Error(t, err, "two matching lines must stop the container:\n%s", out)
	assert.Contains(t, out, "stale", "the failure must say the patch is stale:\n%s", out)
}

// TestWebuiPatchedInitIsNotInAServedRoot keeps the patched copy out of the trees
// the app serves. /api/file/raw serves /tmp among other roots, so a copy written
// there is readable by every signed-in user. It carries no secrets, but there is
// no reason to put it where the app can hand it out.
func TestWebuiPatchedInitIsNotInAServedRoot(t *testing.T) {
	body := webuiCommandBody(t)
	assert.NotContains(t, body, "/tmp/",
		"the patched init copy must not land in /tmp: the app serves that root")
}

// webuiCommandBody returns the shell body of the hermes-webui container command.
func webuiCommandBody(t *testing.T) string {
	t.Helper()

	md := configtest.LoadMetadata(t, "hermes-webui")
	var command []string
	for _, c := range md.Containers {
		if c.Name == "apps-hermes-webui" {
			command = c.Command
		}
	}
	require.NotEmpty(t, command, "apps-hermes-webui declares no command")
	require.Len(t, command, 3, "expected [interpreter, flag, body]")
	return command[2]
}

// runWebuiInitPatch executes the real container command body against a fixture
// standing in for the image's init script, and returns its combined output.
func runWebuiInitPatch(t *testing.T, fixture string) (string, error) {
	t.Helper()

	dir := t.TempDir()
	src := filepath.Join(dir, "hermeswebui_init.bash")
	if err := os.WriteFile(src, []byte(fixture), 0o755); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	body := webuiCommandBody(t)
	body = strings.ReplaceAll(body, "/hermeswebui_init.bash", src)
	body = strings.ReplaceAll(body, "init=/run/", "init="+filepath.Join(dir, "run")+"/")
	body = strings.ReplaceAll(body, "/config/bloud.env", filepath.Join(dir, "absent.env"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", body)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
