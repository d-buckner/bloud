// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
)

// nonConsumerPayloads lists the payload-bearing contracts that are deliberately
// not bound by contractBinders, with the reason. The test fails an entry that
// no longer guards anything (the contract lost its payload, gained a binder, or
// vanished), so the list cannot rot.
var nonConsumerPayloads = map[string]string{
	"clientPassword": "revealed to a human (reveal/rotate), never bound to a consumer app",
	"inference":      "resolved by resolveInference, not the contractBinders table",
}

// TestContractBindersComplete proves the binder table and the contract registry
// agree: every binder names a declared contract, and every payload-bearing
// contract has a binder unless it is explicitly non-consumer. A forgotten arm
// would otherwise be only a WARN at runtime, and a stale exception would sit in
// the list guarding nothing.
func TestContractBindersComplete(t *testing.T) {
	for name := range contractBinders {
		if _, ok := catalog.ContractFor(name); !ok {
			t.Errorf("contractBinders[%q] has no catalog contract", name)
		}
	}

	for _, c := range catalog.AllContracts() {
		if len(c.Secrets) == 0 && len(c.Values) == 0 {
			continue // address-only: no payload to bind
		}
		reason, isException := nonConsumerPayloads[c.Name]
		if isException {
			if _, hasBinder := contractBinders[c.Name]; hasBinder {
				t.Errorf("contract %q is in nonConsumerPayloads (%s) but also has a binder; drop the exception", c.Name, reason)
			}
			continue
		}
		if _, ok := contractBinders[c.Name]; !ok {
			t.Errorf("payload-bearing contract %q has no binder in contractBinders", c.Name)
		}
	}

	for name, reason := range nonConsumerPayloads {
		c, ok := catalog.ContractFor(name)
		if !ok {
			t.Errorf("nonConsumerPayloads[%q] names no catalog contract (%s)", name, reason)
			continue
		}
		if len(c.Secrets) == 0 && len(c.Values) == 0 {
			t.Errorf("nonConsumerPayloads[%q] guards nothing: the contract has no payload (%s)", name, reason)
		}
	}
}
