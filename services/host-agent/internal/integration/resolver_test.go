package integration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve_EmptyInput(t *testing.T) {
	bindings, err := Resolve(ResolutionInput{})

	require.NoError(t, err)
	assert.Empty(t, bindings)
}

func TestResolve_BoundRequiredProvider(t *testing.T) {
	bindings, err := Resolve(ResolutionInput{
		Requirements: map[Type]Requirement{
			"database": {Required: true, Compatible: []AppID{"postgres"}},
		},
		BoundProviders: map[Type]AppID{"database": "postgres"},
	})

	require.NoError(t, err)
	require.Len(t, bindings, 1)
	assert.Equal(t, AppID("postgres"), bindings["database"])
}

func TestResolve_BoundProviderWithoutRequirementReturnsError(t *testing.T) {
	_, err := Resolve(ResolutionInput{
		BoundProviders: map[Type]AppID{"database": "postgres"},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "binding for undeclared integration database")
}

func TestResolve_BoundCompatibleProviderNeedNotBeInstalled(t *testing.T) {
	bindings, err := Resolve(ResolutionInput{
		Requirements: map[Type]Requirement{
			"database": {Required: true, Compatible: []AppID{"postgres"}},
		},
		BoundProviders: map[Type]AppID{"database": "postgres"},
		Installed:      map[AppID]struct{}{},
	})

	require.NoError(t, err)
	require.Len(t, bindings, 1)
	assert.Equal(t, AppID("postgres"), bindings["database"])
}

func TestResolve_BoundIncompatibleProviderReturnsError(t *testing.T) {
	_, err := Resolve(ResolutionInput{
		Requirements: map[Type]Requirement{
			"database": {Required: true, Compatible: []AppID{"postgres"}},
		},
		BoundProviders: map[Type]AppID{"database": "mariadb"},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "incompatible provider mariadb")
}

func TestResolve_BoundProviderTakesPrecedenceOverOptionalDiscovery(t *testing.T) {
	bindings, err := Resolve(ResolutionInput{
		Requirements: map[Type]Requirement{
			"database": {Compatible: []AppID{"postgres", "mariadb"}},
		},
		BoundProviders: map[Type]AppID{"database": "mariadb"},
		Installed:      appSet("postgres", "mariadb"),
	})

	require.NoError(t, err)
	require.Len(t, bindings, 1)
	assert.Equal(t, AppID("mariadb"), bindings["database"])
}

func TestResolve_OptionalInstalledProvider(t *testing.T) {
	bindings, err := Resolve(ResolutionInput{
		Requirements: map[Type]Requirement{
			"sso": {Compatible: []AppID{"authentik"}},
		},
		Installed: appSet("authentik"),
	})

	require.NoError(t, err)
	require.Len(t, bindings, 1)
	assert.Equal(t, AppID("authentik"), bindings["sso"])
}

func TestResolve_OptionalAbsentProvider(t *testing.T) {
	bindings, err := Resolve(ResolutionInput{
		Requirements: map[Type]Requirement{
			"sso": {Compatible: []AppID{"authentik"}},
		},
	})

	require.NoError(t, err)
	assert.Empty(t, bindings)
}

func TestResolve_OptionalProviderUsesCatalogOrder(t *testing.T) {
	bindings, err := Resolve(ResolutionInput{
		Requirements: map[Type]Requirement{
			"database": {Compatible: []AppID{"postgres", "mariadb"}},
		},
		Installed: appSet("postgres", "mariadb"),
	})

	require.NoError(t, err)
	require.Len(t, bindings, 1)
	assert.Equal(t, AppID("postgres"), bindings["database"])
}

func TestResolve_RequiredProviderWithoutBindingReturnsError(t *testing.T) {
	_, err := Resolve(ResolutionInput{
		Requirements: map[Type]Requirement{
			"database": {Required: true, Compatible: []AppID{"postgres"}},
		},
		Installed: appSet("postgres"),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "required integration database has no binding")
}

func TestResolve_AllBindings(t *testing.T) {
	input := ResolutionInput{
		Requirements: map[Type]Requirement{
			"sso":      {Compatible: []AppID{"authentik"}},
			"cache":    {Required: true, Compatible: []AppID{"redis"}},
			"database": {Required: true, Compatible: []AppID{"postgres"}},
		},
		BoundProviders: map[Type]AppID{
			"database": "postgres",
			"cache":    "redis",
		},
		Installed: appSet("authentik"),
	}

	bindings, err := Resolve(input)

	require.NoError(t, err)
	require.Len(t, bindings, 3)
	assert.Equal(t, Bindings{
		"cache":    "redis",
		"database": "postgres",
		"sso":      "authentik",
	}, bindings)
}

func appSet(apps ...AppID) map[AppID]struct{} {
	set := make(map[AppID]struct{}, len(apps))
	for _, app := range apps {
		set[app] = struct{}{}
	}
	return set
}
