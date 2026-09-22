// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShippedCatalog_ContractDeclarationsAreComplete checks the shipped catalog
// for the pairing no compiler sees: every app that integrates with another under
// a contract carrying a payload must find that provider declaring the same
// contract.
//
// A missing declaration is not an error anywhere: the consumer receives an empty
// payload, and an empty payload is also the legitimate "the provider has not
// published yet" state. So the stack would keep running with the link silently
// unwired and a warning per pass, which is exactly the failure a test has to
// catch. Contracts that carry only an address (proxy, downloadClient, database)
// need no declaration: the address is the edge.
func TestShippedCatalog_ContractDeclarationsAreComplete(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	checked := 0
	for consumerID, consumer := range apps {
		for contract, integration := range consumer.Integrations {
			spec, known := ContractFor(contract)
			if !known || (len(spec.Secrets) == 0 && len(spec.Values) == 0) {
				// An address-only contract, or a label whose contract is not
				// part of the framework's vocabulary yet.
				continue
			}
			for _, compatible := range integration.Compatible {
				provider, shipped := apps[compatible.App]
				if !shipped {
					continue
				}
				checked++
				_, declared := provider.Provides[contract]
				assert.True(t, declared,
					"%s integrates with %s as %q, but %s does not declare provides.%s",
					consumerID, compatible.App, contract, compatible.App, contract)
			}
		}
	}
	assert.NotZero(t, checked, "the shipped catalog should contain at least one payload-carrying contract")
}
