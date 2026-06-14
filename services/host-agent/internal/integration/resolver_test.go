package integration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve_EmptyInput(t *testing.T) {
	instances, err := Resolve(ResolutionInput{Consumer: "immich"})

	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestResolve_BoundRequiredProvider(t *testing.T) {
	instances, err := Resolve(ResolutionInput{
		Consumer: "immich",
		Requirements: map[Type]Requirement{
			"database": {Required: true, Compatible: []AppID{"postgres"}},
		},
		BoundProviders: map[Type]AppID{"database": "postgres"},
	})

	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, Instance{
		Consumer: "immich",
		Provider: "postgres",
		Type:     "database",
		Required: true,
		Source:   ResolutionBound,
	}, instances[0])
}

func TestResolve_BoundProviderWithoutRequirementPreservesLegacyBinding(t *testing.T) {
	instances, err := Resolve(ResolutionInput{
		Consumer:       "legacy-app",
		BoundProviders: map[Type]AppID{"database": "postgres"},
	})

	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, AppID("postgres"), instances[0].Provider)
	assert.Equal(t, ResolutionBound, instances[0].Source)
}

func TestResolve_BoundCompatibleProviderNeedNotBeInstalled(t *testing.T) {
	instances, err := Resolve(ResolutionInput{
		Consumer: "immich",
		Requirements: map[Type]Requirement{
			"database": {Required: true, Compatible: []AppID{"postgres"}},
		},
		BoundProviders: map[Type]AppID{"database": "postgres"},
		Installed:      map[AppID]bool{},
	})

	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, AppID("postgres"), instances[0].Provider)
}

func TestResolve_BoundIncompatibleProviderReturnsError(t *testing.T) {
	_, err := Resolve(ResolutionInput{
		Consumer: "immich",
		Requirements: map[Type]Requirement{
			"database": {Required: true, Compatible: []AppID{"postgres"}},
		},
		BoundProviders: map[Type]AppID{"database": "mariadb"},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "incompatible provider mariadb")
}

func TestResolve_BoundProviderTakesPrecedenceOverOptionalDiscovery(t *testing.T) {
	instances, err := Resolve(ResolutionInput{
		Consumer: "app",
		Requirements: map[Type]Requirement{
			"database": {Compatible: []AppID{"postgres", "mariadb"}},
		},
		BoundProviders: map[Type]AppID{"database": "mariadb"},
		Installed:      map[AppID]bool{"postgres": true, "mariadb": true},
	})

	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, AppID("mariadb"), instances[0].Provider)
	assert.Equal(t, ResolutionBound, instances[0].Source)
}

func TestResolve_OptionalInstalledProvider(t *testing.T) {
	instances, err := Resolve(ResolutionInput{
		Consumer: "immich",
		Requirements: map[Type]Requirement{
			"sso": {Compatible: []AppID{"authentik"}},
		},
		Installed: map[AppID]bool{"authentik": true},
	})

	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, AppID("authentik"), instances[0].Provider)
	assert.Equal(t, ResolutionOptional, instances[0].Source)
}

func TestResolve_OptionalAbsentProvider(t *testing.T) {
	instances, err := Resolve(ResolutionInput{
		Consumer: "immich",
		Requirements: map[Type]Requirement{
			"sso": {Compatible: []AppID{"authentik"}},
		},
	})

	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestResolve_OptionalProviderUsesCatalogOrder(t *testing.T) {
	instances, err := Resolve(ResolutionInput{
		Consumer: "app",
		Requirements: map[Type]Requirement{
			"database": {Compatible: []AppID{"postgres", "mariadb"}},
		},
		Installed: map[AppID]bool{"postgres": true, "mariadb": true},
	})

	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, AppID("postgres"), instances[0].Provider)
}

func TestResolve_RequiredProviderWithoutBindingDoesNotResolve(t *testing.T) {
	instances, err := Resolve(ResolutionInput{
		Consumer: "immich",
		Requirements: map[Type]Requirement{
			"database": {Required: true, Compatible: []AppID{"postgres"}},
		},
		Installed: map[AppID]bool{"postgres": true},
	})

	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestResolve_OutputOrderIsDeterministic(t *testing.T) {
	input := ResolutionInput{
		Consumer: "immich",
		Requirements: map[Type]Requirement{
			"sso":      {Compatible: []AppID{"authentik"}},
			"cache":    {Required: true, Compatible: []AppID{"redis"}},
			"database": {Required: true, Compatible: []AppID{"postgres"}},
		},
		BoundProviders: map[Type]AppID{
			"database": "postgres",
			"cache":    "redis",
		},
		Installed: map[AppID]bool{"authentik": true},
	}

	instances, err := Resolve(input)

	require.NoError(t, err)
	require.Len(t, instances, 3)
	assert.Equal(t, []Type{"cache", "database", "sso"}, []Type{
		instances[0].Type,
		instances[1].Type,
		instances[2].Type,
	})
}

func TestLegacyMap_DeduplicatesAndSortsProviders(t *testing.T) {
	legacy := LegacyMap([]Instance{
		{Type: "pvr", Provider: "sonarr"},
		{Type: "pvr", Provider: "radarr"},
		{Type: "pvr", Provider: "sonarr"},
		{Type: "database", Provider: "postgres"},
	})

	assert.Equal(t, map[string][]string{
		"database": {"postgres"},
		"pvr":      {"radarr", "sonarr"},
	}, legacy)
}
