// SPDX-License-Identifier: AGPL-3.0-only

package apps

import (
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/apps/configtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two trees Hermes and hermes-webui share beyond the attachment inbox. Each
// pair of declarations (this file, both metadata.yaml files, and the Go constants
// in both packages) has to agree, because the path the agent writes and the path
// the web UI serves are the same path only while they do.
const (
	sharedWorkspaceSource = "{{dataDir}}/shared-trees/hermes/workspace"
	sharedCacheSource     = "{{dataDir}}/shared-trees/hermes/cache"
)

// mountBySource returns the one mount of a shared tree declared by a container.
//
// It fails on zero or two matches rather than returning the first: a second mount
// of the same tree at a second destination is precisely the drift this guards, and
// a lenient lookup would let it pass unnoticed.
func mountBySource(t *testing.T, dir, containerName, source string) configtest.MetadataVolume {
	t.Helper()

	md := configtest.LoadMetadata(t, dir)
	var found []configtest.MetadataVolume
	for _, c := range md.Containers {
		if c.Name != containerName {
			continue
		}
		for _, v := range c.Volumes {
			if v.Source == source {
				found = append(found, v)
			}
		}
	}
	require.Len(t, found, 1, "%s/%s must mount %s exactly once", dir, containerName, source)
	return found[0]
}

// TestSharedWorkspaceMountsAgree pins the fact the whole workspace fix rests on:
// the directory the agent works in is the directory the web UI browses and serves.
//
// The path is the web UI's own convention on purpose. The app trusts a path under
// its boot default and already stores /workspace as its last workspace, so mounting
// here keeps every earlier session valid. A path elsewhere leaves new chats failing
// to start with "Path is outside the user home directory".
func TestSharedWorkspaceMountsAgree(t *testing.T) {
	agent := mountBySource(t, "hermes", "apps-hermes", sharedWorkspaceSource)
	web := mountBySource(t, "hermes-webui", "apps-hermes-webui", sharedWorkspaceSource)

	assert.Equal(t, "/workspace", agent.Destination,
		"the workspace must stay at the web UI's own convention, or an existing "+
			"install's stored workspace stops being trusted and new chats fail to start")
	assert.Equal(t, agent.Destination, web.Destination,
		"hermes-webui mounts the workspace at %q but Hermes mounts it at %q; the web UI "+
			"then browses a tree the agent never writes",
		web.Destination, agent.Destination)
	assert.NotContains(t, web.Options, "ro",
		"the web UI edits the same workspace files the agent edits, so its mount must be writable")
}

// TestAgentWorkspaceIsInsideTheWriteSafeRoot: Hermes confines its file tools to
// HERMES_WRITE_SAFE_ROOT, so a workspace outside it is readable by the agent and
// unwritable by it. The image sets /opt/data alone, and metadata must widen the
// list to include the shared workspace.
func TestAgentWorkspaceIsInsideTheWriteSafeRoot(t *testing.T) {
	md := configtest.LoadMetadata(t, "hermes")
	var env map[string]string
	for _, c := range md.Containers {
		if c.Name == "apps-hermes" {
			env = c.Environment
		}
	}
	require.NotEmpty(t, env, "apps-hermes declares no environment")

	workspace := mountBySource(t, "hermes", "apps-hermes", sharedWorkspaceSource).Destination
	assert.Equal(t, workspace, env["TERMINAL_CWD"],
		"the agent's working directory must be the shared workspace")
	roots := strings.Split(env["HERMES_WRITE_SAFE_ROOT"], ":")
	assert.Contains(t, roots, workspace,
		"the agent's working directory must be inside HERMES_WRITE_SAFE_ROOT, or its "+
			"file tools refuse to write its own workspace")
}

// TestSharedCacheMountsAgree pins the media-cache pairing: tool-generated media is
// written under $HERMES_HOME/cache and delivered as a MEDIA: tag naming that
// absolute path, so the web UI can serve it only when the bytes exist at the same
// path in its own container.
//
// The read-only flag is the whole ownership statement: the agent writes the cache,
// the web UI only reads it, and the flag keeps this app's own delete, rename, and
// move APIs away from files the agent owns.
func TestSharedCacheMountsAgree(t *testing.T) {
	agent := mountBySource(t, "hermes", "apps-hermes", sharedCacheSource)
	web := mountBySource(t, "hermes-webui", "apps-hermes-webui", sharedCacheSource)

	assert.Equal(t, "/opt/data/cache", agent.Destination,
		"the cache must mount where Hermes writes it, or its MEDIA: tags name a path "+
			"that exists in neither container")
	assert.Equal(t, agent.Destination, web.Destination,
		"hermes-webui mounts the cache at %q but Hermes mounts it at %q; every MEDIA: "+
			"tag the tools emit then resolves to nothing",
		web.Destination, agent.Destination)
	assert.NotContains(t, agent.Options, "ro",
		"the agent writes its own media cache")
	assert.Contains(t, web.Options, "ro",
		"the web UI only reads the cache, so its mount must stay read-only")
}

// TestBrowserToolsWriteIntoTheSharedCache: the browser harness writes its default
// screenshot to BH_TMP_DIR, which defaults to $HOME/.config/browser-harness/tmp.
// That path is outside every tree the web UI may serve, so a screenshot the agent
// takes and puts in its reply answers 403. metadata.yaml must point the tmp
// directory into the shared cache, and the bundled agent-browser skill at the same
// directory.
func TestBrowserToolsWriteIntoTheSharedCache(t *testing.T) {
	md := configtest.LoadMetadata(t, "hermes")
	var env map[string]string
	for _, c := range md.Containers {
		if c.Name == "apps-hermes" {
			env = c.Environment
		}
	}
	require.NotEmpty(t, env, "apps-hermes declares no environment")

	tmp := env["BH_TMP_DIR"]
	require.NotEmpty(t, tmp,
		"BH_TMP_DIR must be set, or a browser screenshot lands outside the shared trees")
	assert.Equal(t, tmp, env["AGENT_BROWSER_SCREENSHOT_DIR"],
		"the browser harness and the agent-browser skill must write to one shared directory")
	assert.Equal(t, "1", env["BH_TMP_DIR_SHARED"],
		"concurrent sessions share the directory, so a filename must carry the session name")

	cache := mountBySource(t, "hermes", "apps-hermes", sharedCacheSource).Destination
	assert.True(t, strings.HasPrefix(tmp, cache+"/"),
		"BH_TMP_DIR (%s) must be inside the shared cache mount (%s), which the web UI serves", tmp, cache)
}

// TestWebuiWorkspaceAndMediaRootsNameTheMounts closes the loop between the mounts
// and the two settings that make them useful. A mount the app is never told about
// fixes nothing: HERMES_WEBUI_DEFAULT_WORKSPACE must name the workspace mount, or
// the app falls back to its own /workspace, and MEDIA_ALLOWED_ROOTS must name both
// mounts, or /api/media answers 403 for a file the app can see.
func TestWebuiWorkspaceAndMediaRootsNameTheMounts(t *testing.T) {
	md := configtest.LoadMetadata(t, "hermes-webui")
	var env map[string]string
	for _, c := range md.Containers {
		if c.Name == "apps-hermes-webui" {
			env = c.Environment
		}
	}
	require.NotEmpty(t, env, "apps-hermes-webui declares no environment")

	workspace := mountBySource(t, "hermes-webui", "apps-hermes-webui", sharedWorkspaceSource).Destination
	cache := mountBySource(t, "hermes-webui", "apps-hermes-webui", sharedCacheSource).Destination

	assert.Equal(t, workspace, env["HERMES_WEBUI_DEFAULT_WORKSPACE"],
		"HERMES_WEBUI_DEFAULT_WORKSPACE must name the workspace mount")

	roots := strings.Split(env["MEDIA_ALLOWED_ROOTS"], ":")
	assert.Contains(t, roots, workspace,
		"MEDIA_ALLOWED_ROOTS must include the workspace, or an agent-written file is refused")
	assert.Contains(t, roots, cache,
		"MEDIA_ALLOWED_ROOTS must include the cache, or a screenshot or MCP image is refused")
}
