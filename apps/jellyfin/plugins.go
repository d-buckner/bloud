// SPDX-License-Identifier: AGPL-3.0-only

package jellyfin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appasset"
)

// ensureLDAPPlugin installs the LDAP-Auth plugin via the shared asset
// installer: fetch (retried) → sha256 verify → stage → atomic commit. The
// sentinel makes it a no-op (no network) when already installed; the Verify
// callback asserts the archive actually contains the expected DLL before
// anything lands in the destination.
func (c *Configurator) ensureLDAPPlugin(ctx context.Context, dataPath string) (bool, error) {
	pluginDir := filepath.Join(dataPath, "config", "plugins", "LDAP-Auth")
	return c.assets.Install(ctx, appasset.Asset{
		Name:     "jellyfin-ldap-auth",
		Dest:     pluginDir,
		Source:   appasset.URL(c.pluginURL),
		Kind:     appasset.Zip,
		SHA256:   c.pluginSHA256,
		Sentinel: "LDAP-Auth.dll",
		Verify:   requireFile("LDAP-Auth.dll"),
	})
}

// requireFile returns a staged-tree Verify func asserting rel exists.
func requireFile(rel string) func(staging string) error {
	return func(staging string) error {
		if _, err := os.Stat(filepath.Join(staging, rel)); err != nil {
			return fmt.Errorf("archive did not contain %s", rel)
		}
		return nil
	}
}
