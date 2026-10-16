// SPDX-License-Identifier: AGPL-3.0-only

package configurator

import (
	"os"

	"codeberg.org/d-buckner/bloud/pkg/managedfile"
)

// WriteConfig writes content to path and reports a recreate only when the
// bytes changed. It is the shared tail of every generated-file PreStart: a
// steady-state resync is a read-only diff that asks for no recreate.
func WriteConfig(path string, content []byte, mode os.FileMode, reason string) (PreStartResult, error) {
	changed, err := managedfile.Write(path, content, mode)
	if err != nil {
		return NoRestart(), err
	}
	return RestartIf(changed, reason), nil
}
