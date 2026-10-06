// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The edge cases below call validateClientAccess directly rather than going
// through validateProvides. The reason is reachability, not convenience:
// validateContractSecrets pins an offer's secrets to exactly the set its
// contract names, so an offer under `clientPassword` can never publish zero
// secrets or two of them without failing on the secrets rule first. Those two
// shapes are still worth failing loudly for the contracts that get added later,
// and the unit is where they can be exercised.

// TestClientAccessRequiresPublishedSecrets guards against a reveal surface
// that would print nothing.
//
// A clientAccess block on an offer that publishes no secrets describes a
// credential that does not exist. The reveal surface would render an empty
// value, which a user reads as a broken UI rather than as a provider that has
// nothing to give, and the provider author gets no signal at load time that
// their declaration is hollow.
func TestClientAccessRequiresPublishedSecrets(t *testing.T) {
	err := validateClientAccess("clientPassword", ContractProvides{
		ClientAccess: &ClientAccess{
			Reveal:  ClientRevealOnce,
			Reaches: "this web UI as one shared account",
		},
	})
	require.Error(t, err, "a client credential needs a published secret")
	assert.Contains(t, err.Error(), "publishes no secrets")
}

// TestClientAccessOnAKeylessContractFailsViaTheLoader proves the same rule on
// the path a real catalog takes.
//
// `caldav` carries no credential by design: a DAV server authenticates the
// person, not a machine. Declaring a client credential on it is therefore a
// provider asserting a secret it has no way to publish, and the load has to
// say so rather than ship a reveal surface with an empty field.
func TestClientAccessOnAKeylessContractFailsViaTheLoader(t *testing.T) {
	app := &App{
		CatalogID:   "davthing",
		DisplayName: "DAV Thing",
		Description: "A thing that serves DAV",
		Category:    "productivity",
		Port:        8080,
		Provides: Provides{"caldav": ContractProvides{
			Values:       map[string]string{"path": "/dav"},
			ClientAccess: &ClientAccess{Reveal: ClientRevealOnce, Reaches: "the calendars"},
		}},
	}

	err := validateProvides(app)
	require.Error(t, err, "a credential-less contract cannot carry a client credential")
	assert.Contains(t, err.Error(), "publishes no secrets")
}

// TestClientAccessMustNameItsSecretWhenThereAreSeveral pins the ambiguity
// guard.
//
// With one published secret the block can mean only that one. With several, a
// block that names none leaves the reveal surface to guess which credential it
// is about to print, and a wrong guess prints a credential the provider never
// meant to hand to a human.
func TestClientAccessMustNameItsSecretWhenThereAreSeveral(t *testing.T) {
	err := validateClientAccess("clientPassword", ContractProvides{
		Secrets:      []string{"apiKey", "password"},
		ClientAccess: &ClientAccess{Reveal: ClientRevealOnce, Reaches: "the app"},
	})
	require.Error(t, err, "several secrets and no `secret:` is ambiguous")
	assert.Contains(t, err.Error(), "secret:")

	// Naming one resolves the ambiguity.
	require.NoError(t, validateClientAccess("clientPassword", ContractProvides{
		Secrets:      []string{"apiKey", "password"},
		ClientAccess: &ClientAccess{Secret: "password", Reveal: ClientRevealOnce, Reaches: "the app"},
	}))
}

// TestClientAccessSecretMustBePublished rejects a block pointing at a
// credential the offer does not carry.
func TestClientAccessSecretMustBePublished(t *testing.T) {
	err := validateClientAccess("clientPassword", ContractProvides{
		Secrets:      []string{"password"},
		ClientAccess: &ClientAccess{Secret: "apiToken", Reveal: ClientRevealOnce, Reaches: "the app"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apiToken")
}

// TestClientAccessRequiresReachesWhenRevealable pins the disclosure rule,
// which is the field this whole design depends on.
//
// A reveal surface that shows a secret without saying what the secret opens
// trains the person holding it to click through the warning. The disclosure
// is required for every revealable policy and is not required when nothing can
// be shown, so a provider can use the block for `rotate` alone.
func TestClientAccessRequiresReachesWhenRevealable(t *testing.T) {
	for _, reveal := range []ClientReveal{ClientRevealOnce, ClientRevealAlways} {
		t.Run(string(reveal), func(t *testing.T) {
			err := validateClientAccess("clientPassword", ContractProvides{
				Secrets:      []string{"password"},
				ClientAccess: &ClientAccess{Reveal: reveal, Reaches: "   "},
			})
			require.Error(t, err, "%q can be revealed, so it needs a disclosure", reveal)
			assert.Contains(t, err.Error(), "reaches")
		})
	}

	t.Run("never reveal needs no disclosure", func(t *testing.T) {
		require.NoError(t, validateClientAccess("clientPassword", ContractProvides{
			Secrets:      []string{"password"},
			ClientAccess: &ClientAccess{Reveal: ClientRevealNever, Rotate: ClientRotateBloud},
		}))
	})
}

// TestClientAccessRejectsUnknownEnumValues fails a typo rather than letting it
// read as the safe default.
//
// A misspelled policy that silently became "never" would look like a deliberate
// restriction, and the provider author would spend the debugging time on the
// reveal surface instead of on the six letters they got wrong.
func TestClientAccessRejectsUnknownEnumValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ClientAccess)
		want   string
	}{
		{"reveal typo", func(c *ClientAccess) { c.Reveal = "sometime" }, "reveal"},
		{"rotate typo", func(c *ClientAccess) { c.Rotate = "sometimes" }, "rotate"},
		{"snippet typo", func(c *ClientAccess) { c.Snippet = "paste-this" }, "snippet"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			access := &ClientAccess{
				Reveal:  ClientRevealOnce,
				Rotate:  ClientRotateBloud,
				Snippet: SnippetURLAndPassword,
				Reaches: "the app",
			}
			tc.mutate(access)
			err := validateClientAccess("clientPassword", ContractProvides{
				Secrets:      []string{"password"},
				ClientAccess: access,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestClientAccessDefaultsAreTheSafeOnes pins the zero-value behaviour, which
// is what every contract that predates this field gets.
func TestClientAccessDefaultsAreTheSafeOnes(t *testing.T) {
	var nilBlock *ClientAccess
	assert.False(t, nilBlock.Revealable(), "no block is not revealable")
	assert.False(t, nilBlock.RotateAllowed(), "no block cannot rotate")
	assert.Equal(t, ClientRevealNever, nilBlock.EffectiveReveal())
	assert.Equal(t, ClientRotateNone, nilBlock.EffectiveRotate())

	empty := &ClientAccess{}
	assert.False(t, empty.Revealable(), "an empty policy is never, not always")
	assert.False(t, empty.RotateAllowed())

	once := &ClientAccess{Reveal: ClientRevealOnce, Rotate: ClientRotateBloud}
	assert.True(t, once.Revealable())
	assert.True(t, once.RotateAllowed())

	// Provider-owned rotation is still rotation: Bloud asks, the app performs.
	providerOwned := &ClientAccess{Reveal: ClientRevealOnce, Rotate: ClientRotateProvider}
	assert.True(t, providerOwned.RotateAllowed())

	// A fixed credential is not rotatable even when it is revealable.
	fixed := &ClientAccess{Reveal: ClientRevealAlways, Rotate: ClientRotateNone}
	assert.True(t, fixed.Revealable())
	assert.False(t, fixed.RotateAllowed())
}

// TestClientAccessAbsentIsNotAnError proves the field is opt-in.
func TestClientAccessAbsentIsNotAnError(t *testing.T) {
	require.NoError(t, validateClientAccess("clientPassword", ContractProvides{
		Secrets: []string{"password"},
	}))
}

// TestClientPasswordContractIsRegistered pins the contract the pilot declares.
func TestClientPasswordContractIsRegistered(t *testing.T) {
	spec, known := ContractFor("clientPassword")
	require.True(t, known, "clientPassword must be in the contract registry")
	assert.Equal(t, []string{"password"}, spec.Secrets,
		"a client credential is one password and nothing else")
	assert.Empty(t, spec.Values,
		"the URL is the instance address, not a provider-declared value")
	assert.Empty(t, spec.SatisfiedBy,
		"nothing can stand in for a credential the user copies by hand")
}

// TestClientAccessWellFormedLoads is the positive control: the exact shape the
// pilot declares has to pass end to end through the loader.
func TestClientAccessWellFormedLoads(t *testing.T) {
	app := &App{
		CatalogID:   "thing",
		DisplayName: "Thing",
		Description: "A thing",
		Category:    "productivity",
		Port:        8080,
		Provides: Provides{"clientPassword": ContractProvides{
			Secrets: []string{"password"},
			ClientAccess: &ClientAccess{
				Secret:  "password",
				Reveal:  ClientRevealOnce,
				Rotate:  ClientRotateBloud,
				Label:   "Mobile client password",
				Reaches: "this web UI as one shared account, with the same access as any signed-in user",
				Snippet: SnippetURLAndPassword,
			},
		}},
	}
	require.NoError(t, validateProvides(app))
}
