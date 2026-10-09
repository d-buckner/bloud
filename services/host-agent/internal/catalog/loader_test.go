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

// writeAppDir drops one named app into an existing apps directory, so a test
// can build a catalog of more than one app. The name goes in the directory,
// which is what the loader enumerates.
func writeAppDir(t *testing.T, dir, name, metadata string) {
	t.Helper()
	appDir := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "metadata.yaml"), []byte(metadata), 0o644))
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

// TestValidateApp_ShmSizeMustParse pins that a container's declared shmSize is
// checked at load. Podman's create API ignores a field it does not understand
// rather than failing the create, so an unreadable size would otherwise ship as
// a container quietly left on the 64MB /dev/shm default. That is the condition
// #267 turns into a SIGBUS crash loop, so the typo has to be caught here, on
// the file that was written.
func TestValidateApp_ShmSizeMustParse(t *testing.T) {
	const tmpl = `name: sso-app
displayName: SSO App
description: An app
category: productivity
containers:
  - name: apps-sso-app
    image: example/sso-app:1.0
    shmSize: "%s"
`

	cases := []struct {
		name    string
		size    string
		wantErr bool
	}{
		{name: "suffix form", size: "256m"},
		{name: "byte count", size: "268435456"},
		{name: "unknown suffix", size: "256q", wantErr: true},
		{name: "zero", size: "0", wantErr: true},
		{name: "negative", size: "-1m", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeMetadataApp(t, fmt.Sprintf(tmpl, tc.size))
			_, err := NewLoader(dir).LoadAll()
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), `container "apps-sso-app": shmSize:`)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestShippedMetadata_AuthentikRaisesShmSize guards the fix for #267 from
// quietly going away. Authentik's gunicorn workers mmap /dev/shm, and at
// podman's 64MB default a full tmpfs sends each worker SIGBUS the moment it
// writes to a mapped page. The worker dies, the master boots a replacement, the
// replacement dies the same way, and the container never exits, so
// `restartPolicy: always` never fires and SSO stays down for every app on the
// install.
func TestShippedMetadata_AuthentikRaisesShmSize(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	authentik, ok := apps["authentik"]
	require.True(t, ok, "authentik should be in the shipped catalog")

	for _, name := range []string{"apps-authentik-server", "apps-authentik-worker"} {
		def := findContainerByName(t, authentik, name)
		size, err := def.ShmSizeBytes()
		require.NoError(t, err)
		assert.GreaterOrEqual(t, size, int64(256<<20),
			"%s needs at least 256MB of /dev/shm: the 64MB default is what filled up", name)
	}
}

func findContainerByName(t *testing.T, app *App, name string) ContainerDef {
	t.Helper()
	for _, c := range app.Containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("app %q has no container %q", app.CatalogID, name)
	return ContainerDef{}
}
