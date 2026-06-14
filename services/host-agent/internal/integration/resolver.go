package integration

import (
	"fmt"
	"sort"
)

// Resolve calculates deterministic integration instances from declared requirements,
// persisted provider bindings, and installed providers.
func Resolve(input ResolutionInput) ([]Instance, error) {
	types := integrationTypes(input)
	instances := make([]Instance, 0, len(types))

	for _, integrationType := range types {
		requirement, declared := input.Requirements[integrationType]

		if provider, bound := input.BoundProviders[integrationType]; bound {
			if declared && !isCompatible(provider, requirement.Compatible) {
				return nil, fmt.Errorf(
					"%s is bound to incompatible provider %s for %s",
					input.Consumer,
					provider,
					integrationType,
				)
			}
			instances = append(instances, Instance{
				Consumer: input.Consumer,
				Provider: provider,
				Type:     integrationType,
				Required: requirement.Required,
				Source:   ResolutionBound,
			})
			continue
		}

		if !declared || requirement.Required {
			continue
		}

		for _, provider := range requirement.Compatible {
			if input.Installed[provider] {
				instances = append(instances, Instance{
					Consumer: input.Consumer,
					Provider: provider,
					Type:     integrationType,
					Required: false,
					Source:   ResolutionOptional,
				})
				break
			}
		}
	}

	return instances, nil
}

// LegacyMap converts typed instances to the configurator interface used by the
// current implementation. Providers are sorted and deduplicated for stability.
func LegacyMap(instances []Instance) map[string][]string {
	result := make(map[string][]string)

	for _, instance := range instances {
		key := string(instance.Type)
		provider := string(instance.Provider)
		if contains(result[key], provider) {
			continue
		}
		result[key] = append(result[key], provider)
	}

	for key := range result {
		sort.Strings(result[key])
	}

	return result
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

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
