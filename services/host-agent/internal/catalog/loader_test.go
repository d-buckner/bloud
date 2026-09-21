// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeMetadataApp writes one app directory holding a metadata.yaml into a
// fresh temp apps dir and returns that dir.
func writeMetadataApp(t *testing.T, metadata string) string {
	t.Helper()

	dir := t.TempDir()
	appDir := filepath.Join(dir, "sso-app")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "metadata.yaml"), []byte(metadata), 0o644))
	return dir
}

// TestValidateApp_LoopbackIssuerRequiresHostNetwork pins the coupling between
// the loopback issuer and host networking: the issuer is
// http://localhost:<Traefik port>, which reaches Traefik only from inside the
// host network namespace. Without that, a mis-authored app would register its
// provider and pass its health check, failing only at login when discovery
// cannot reach the issuer.
func TestValidateApp_LoopbackIssuerRequiresHostNetwork(t *testing.T) {
	const tmpl = `name: sso-app
displayName: SSO App
description: An app
category: productivity
sso:
  strategy: native-oidc
  loopbackIssuer: true
containers:
  - name: apps-sso-app
    image: example/sso-app:1.0
%s
`

	cases := []struct {
		name    string
		network string
		wantErr bool
	}{
		{name: "network field", network: "    network: host"},
		{name: "networks list", network: "    networks: [host]"},
		{name: "private network", network: "    network: apps-net", wantErr: true},
		{name: "no network", network: "", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeMetadataApp(t, fmt.Sprintf(tmpl, tc.network))
			apps, err := NewLoader(dir).LoadAll()
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "sso.loopbackIssuer requires a container with network: host")
				return
			}
			require.NoError(t, err)
			assert.Contains(t, apps, "sso-app")
		})
	}
}

// TestLoader_LoadAll_ShippedMetadataValid runs the runtime load path over the
// real apps/ directory. LoadAll is what the catalog cache uses, and it is where
// validateApp runs, so a mis-authored shipped app fails here instead of at
// install time.
func TestLoader_LoadAll_ShippedMetadataValid(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	hermes, ok := apps["hermes"]
	require.True(t, ok, "hermes should be in the shipped catalog")
	assert.True(t, hermes.SSO.LoopbackIssuer, "hermes is the app that needs the loopback issuer")
	assert.True(t, hermes.HasHostNetworkedContainer(), "and the host-networked container that makes it reachable")
}
