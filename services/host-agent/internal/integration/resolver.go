package integration

import (
	"fmt"
	"sort"
)

// Resolve calculates deterministic integration bindings from declared requirements,
// persisted provider bindings, and installed providers.
func Resolve(input ResolutionInput) (Bindings, error) {
	types := integrationTypes(input)
	bindings := make(Bindings)

	for _, integrationType := range types {
		requirement, declared := input.Requirements[integrationType]

		if provider, bound := input.BoundProviders[integrationType]; bound {
			if !declared {
				return nil, fmt.Errorf("binding for undeclared integration %s", integrationType)
			}
			if !isCompatible(provider, requirement.Compatible) {
				return nil, fmt.Errorf(
					"incompatible provider %s for %s",
					provider,
					integrationType,
				)
			}
			bindings[integrationType] = provider
			continue
		}

		if requirement.Required {
			return nil, fmt.Errorf("required integration %s has no binding", integrationType)
		}

		for _, provider := range requirement.Compatible {
			if _, installed := input.Installed[provider]; installed {
				bindings[integrationType] = provider
				break
			}
		}
	}

	return bindings, nil
}

func integrationTypes(input ResolutionInput) []Type {
	seen := make(map[Type]bool)
	types := make([]Type, 0, len(input.Requirements)+len(input.BoundProviders))

	for integrationType := range input.Requirements {
		if !seen[integrationType] {
			seen[integrationType] = true
			types = append(types, integrationType)
		}
	}
	for integrationType := range input.BoundProviders {
		if !seen[integrationType] {
			seen[integrationType] = true
			types = append(types, integrationType)
		}
	}

	sort.Slice(types, func(i, j int) bool {
		return types[i] < types[j]
	})
	return types
}

func isCompatible(provider AppID, compatible []AppID) bool {
	for _, candidate := range compatible {
		if candidate == provider {
			return true
		}
	}
	return false
}
