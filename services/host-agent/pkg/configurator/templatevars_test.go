// SPDX-License-Identifier: AGPL-3.0-only

package configurator

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTemplateVars_copiesInput(t *testing.T) {
	input := map[string]string{"postgresPassword": "pw"}
	vars := NewTemplateVars(input)

	// Mutating the caller's map must not change what the store renders.
	input["postgresPassword"] = "rotated"
	got, ok := vars.Resolve("postgresPassword")
	require.True(t, ok)
	assert.Equal(t, "pw", got)
}

func TestTemplateVars_ldapOutpostToken(t *testing.T) {
	vars := NewTemplateVars(map[string]string{"postgresPassword": "pw"})

	// Unissued: the name is not resolvable, so a template leaves it alone
	// rather than substituting an empty token.
	_, ok := vars.Resolve(LDAPOutpostTokenVar)
	assert.False(t, ok, "an unissued token must not resolve")
	assert.Empty(t, vars.LDAPOutpostToken())

	vars.SetLDAPOutpostToken("tok-123")
	got, ok := vars.Resolve(LDAPOutpostTokenVar)
	require.True(t, ok)
	assert.Equal(t, "tok-123", got)
	assert.Equal(t, "tok-123", vars.LDAPOutpostToken())
}

func TestTemplateVars_snapshotIncludesToken(t *testing.T) {
	vars := NewTemplateVars(map[string]string{"authentikSecretKey": "sk"})

	snap := vars.Snapshot()
	assert.Equal(t, map[string]string{"authentikSecretKey": "sk"}, snap)

	vars.SetLDAPOutpostToken("tok")
	snap = vars.Snapshot()
	assert.Equal(t, map[string]string{
		"authentikSecretKey": "sk",
		LDAPOutpostTokenVar:  "tok",
	}, snap)
}

// Snapshot hands back a private copy: writing to it must not reach the store.
func TestTemplateVars_snapshotIsNotShared(t *testing.T) {
	vars := NewTemplateVars(map[string]string{"a": "1"})
	snap := vars.Snapshot()
	snap["a"] = "tampered"
	snap["injected"] = "yes"

	got, _ := vars.Resolve("a")
	assert.Equal(t, "1", got)
	_, ok := vars.Resolve("injected")
	assert.False(t, ok)
}

// The whole point of the store: a configurator writing the LDAP token while
// the orchestrator renders is not a data race. Run with -race.
func TestTemplateVars_concurrentWriteAndSnapshot(t *testing.T) {
	vars := NewTemplateVars(map[string]string{"postgresPassword": "pw"})

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			vars.SetLDAPOutpostToken("token")
		}()
		go func() {
			defer wg.Done()
			snap := vars.Snapshot()
			_, _ = snap["postgresPassword"]
			_, _ = vars.Resolve(LDAPOutpostTokenVar)
		}()
	}
	wg.Wait()

	assert.Equal(t, "token", vars.LDAPOutpostToken())
}

// A nil store snapshots to an empty map: a build with no template variables
// configured is legitimate and the renderer must not panic on it.
func TestTemplateVars_nilStoreIsSafe(t *testing.T) {
	var nilVars *TemplateVars
	assert.Empty(t, nilVars.Snapshot())
}
