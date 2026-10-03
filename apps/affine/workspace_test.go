// SPDX-License-Identifier: AGPL-3.0-only

package affine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// The first pass on an instance with no workspaces creates the shared
// workspace, seeds it with the root document AFFiNE's editor needs to render,
// and publishes its id to appApi companions. This is the whole workspace
// contract in one pass; AFFiNE's own MCP server is no longer exposed.
func TestEnsureSharedWorkspace_CreatesAndPublishes(t *testing.T) {
	fake := newFakeAffine()
	secrets := newStoreSecrets("owner-pass")
	c := apiConfigurator(t, fake, secrets)

	c.ensureSharedWorkspace(context.Background())

	require.Len(t, fake.workspaces, 1, "an instance with no workspaces gets one created")
	assert.Equal(t, workspaceInitDoc, fake.createInitBytes(),
		"the workspace is seeded with a root document so the editor initializes")
	assert.Equal(t, fake.workspaces[0], secrets.GetAppContractValue("affine", "appApi", "workspaceId"),
		"the settled workspace is published to appApi companions so they default to it")
}

// A workspace the operator already has is adopted instead of a second copy
// being created.
func TestEnsureSharedWorkspace_AdoptsExistingWorkspace(t *testing.T) {
	fake := newFakeAffine("operator-ws")
	secrets := newStoreSecrets("owner-pass")
	c := apiConfigurator(t, fake, secrets)

	c.ensureSharedWorkspace(context.Background())

	assert.Equal(t, []string{"operator-ws"}, fake.workspaces, "no workspace is created beside an existing one")
	assert.Equal(t, "operator-ws", secrets.GetAppContractValue("affine", "appApi", "workspaceId"))
}

// Once Bloud has published a workspace, a workspace added later must not move
// the wiring: the companion is pinned to the published scope, and silently
// repointing it would change which library an agent reads.
func TestEnsureSharedWorkspace_PublishedWorkspaceIsSticky(t *testing.T) {
	fake := newFakeAffine("ws-1")
	secrets := newStoreSecrets("owner-pass")
	c := apiConfigurator(t, fake, secrets)

	c.ensureSharedWorkspace(context.Background())
	published := secrets.GetAppContractValue("affine", "appApi", "workspaceId")
	require.Equal(t, "ws-1", published)

	fake.mu.Lock()
	fake.workspaces = append([]string{"newer-first"}, fake.workspaces...)
	fake.mu.Unlock()

	c.ensureSharedWorkspace(context.Background())
	assert.Equal(t, published, secrets.GetAppContractValue("affine", "appApi", "workspaceId"),
		"the previously published workspace is kept even when another sorts first")
}

// A steady-state pass creates nothing: the workspace exists and the published
// id is unchanged, so a reconcile loop must not spawn a workspace per pass.
func TestEnsureSharedWorkspace_Idempotent(t *testing.T) {
	fake := newFakeAffine("ws-1")
	secrets := newStoreSecrets("owner-pass")
	c := apiConfigurator(t, fake, secrets)

	c.ensureSharedWorkspace(context.Background())
	for i := 0; i < 3; i++ {
		c.ensureSharedWorkspace(context.Background())
	}

	assert.Equal(t, []string{"ws-1"}, fake.workspaces)
	assert.Equal(t, "ws-1", secrets.GetAppContractValue("affine", "appApi", "workspaceId"))
}

// The GraphQL write must carry the CSRF token the sign-in issued: AFFiNE
// rejects one that does not, so a configurator that skipped it would never
// create the shared workspace.
func TestEnsureSharedWorkspace_SendsCSRFToken(t *testing.T) {
	fake := newFakeAffine()
	fake.requireCSRF = true
	secrets := newStoreSecrets("owner-pass")
	c := apiConfigurator(t, fake, secrets)

	c.ensureSharedWorkspace(context.Background())

	require.Len(t, fake.workspaces, 1, "the create succeeded against a server that demands a CSRF token")
	assert.Equal(t, fake.workspaces[0], secrets.GetAppContractValue("affine", "appApi", "workspaceId"))
}

// A missing secrets store must not panic: the node is the knowledge base itself
// and the value is only meaningful to a companion.
func TestEnsureSharedWorkspace_NilSecretsSafe(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	assert.NotPanics(t, func() { c.ensureSharedWorkspace(context.Background()) })
}

// A sign-in fault is logged and swallowed, publishing nothing rather than a
// half-written scope. The companion reads an absent scope as "not pinned".
func TestEnsureSharedWorkspace_SignInFailurePublishesNothing(t *testing.T) {
	secrets := newStoreSecrets("owner-pass")
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	c.baseURL = "http://127.0.0.1:1"

	assert.NotPanics(t, func() { c.ensureSharedWorkspace(context.Background()) })
	assert.Empty(t, secrets.GetAppContractValue("affine", "appApi", "workspaceId"))
}
