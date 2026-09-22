// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateApp_Provides pins the provider-side declaration against the
// contract registry. Everything checked here is a cross-file agreement no
// compiler sees: a provider's `provides.pvr.secrets` must carry the key the PVR
// contract names, an MCP endpoint's path is concatenated onto an address, and a
// contract name no consumer can use is a typo. Each has to fail the load rather
// than reach a consumer as a binding that is silently half-empty.
func TestValidateApp_Provides(t *testing.T) {
	const tmpl = `name: sso-app
displayName: SSO App
description: An app
category: productivity
port: %s
provides:
%s
containers:
  - name: apps-sso-app
    image: example/sso-app:1.0
`

	cases := []struct {
		name     string
		port     string
		provides string
		wantErr  string
	}{
		{
			name:     "an address-only contract needs no declaration",
			port:     "9696",
			provides: "  proxy: {}",
		},
		{
			name:     "pvr with the key this contract carries",
			port:     "8989",
			provides: "  pvr:\n    secrets: [apiKey]",
		},
		{
			name:     "pvr with a name the contract does not carry",
			port:     "8989",
			provides: "  pvr:\n    secrets: [somethingElse]",
			wantErr:  `provides.pvr publishes "somethingElse", which this contract does not carry`,
		},
		{
			name:     "pvr with no secrets at all",
			port:     "8989",
			provides: "  pvr: {}",
			wantErr:  `provides.pvr must publish the "apiKey" secret this contract carries`,
		},
		{
			name:     "a contract no consumer can declare",
			port:     "8989",
			provides: "  pvrr:\n    secrets: [apiKey]",
			wantErr:  "provides.pvrr is not a known contract",
		},
		{
			name:     "a secret name that is a value",
			port:     "8989",
			provides: `  pvr:` + "\n    secrets: [\"api key\"]",
			wantErr:  "provides.pvr.secrets entries must be single non-empty names",
		},
		{
			name:     "the same secret twice",
			port:     "8989",
			provides: "  pvr:\n    secrets: [apiKey, apiKey]",
			wantErr:  "provides.pvr.secrets lists a name twice",
		},
		{
			name: "mcp with the endpoint and token this contract carries",
			port: "3011",
			provides: "  mcp:\n    secrets: [httpToken]\n" +
				"    values: {path: /mcp, serverName: affine}",
		},
		{
			name:     "mcp without a server name",
			port:     "3011",
			provides: "  mcp:\n    secrets: [httpToken]\n    values: {path: /mcp}",
			wantErr:  `provides.mcp.values must declare "serverName"`,
		},
		{
			name:     "mcp with a relative path",
			port:     "3011",
			provides: "  mcp:\n    secrets: [httpToken]\n    values: {path: mcp, serverName: affine}",
			wantErr:  "provides.mcp.values.path must be an absolute path",
		},
		{
			name:     "a secret the contract does not carry",
			port:     "8989",
			provides: "  pvr:\n    secrets: [apiKey, somethingElse]",
			wantErr:  `provides.pvr publishes "somethingElse", which this contract does not carry`,
		},
		{
			name:     "mcp on an app with no port to build the address from",
			port:     "0",
			provides: "  mcp:\n    secrets: [httpToken]\n    values: {path: /mcp, serverName: affine}",
			wantErr:  "provides.mcp declares values that are resolved against the app's address",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apps, err := NewLoader(writeMetadataApp(t, fmt.Sprintf(tmpl, tc.port, tc.provides))).LoadAll()
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, apps, "sso-app")
		})
	}
}

// TestValidateApp_ProvidesAcceptsNoDeclaration keeps the common case quiet: most
// apps offer nothing to a consumer beyond being reachable.
func TestValidateApp_ProvidesAcceptsNoDeclaration(t *testing.T) {
	const metadata = `name: sso-app
displayName: SSO App
description: An app
category: productivity
integrations: {}
containers:
  - name: apps-sso-app
    image: example/sso-app:1.0
`
	apps, err := NewLoader(writeMetadataApp(t, metadata)).LoadAll()
	require.NoError(t, err)
	assert.Empty(t, apps["sso-app"].Provides)
}

// TestValidateApp_Requires pins the consumer side of least privilege: an app
// declares which secrets it reads out of a contract's payload, and a name the
// contract does not carry fails the load. Without that, a typo would leave the
// app with an empty field it reads as "the provider has not published yet".
func TestValidateApp_Requires(t *testing.T) {
	const tmpl = `name: sso-app
displayName: SSO App
description: An app
category: productivity
integrations:
%s
containers:
  - name: apps-sso-app
    image: example/sso-app:1.0
`

	cases := []struct {
		name         string
		integrations string
		wantErr      string
	}{
		{
			name:         "a secret the contract carries",
			integrations: "  sso:\n    compatible: [{app: authentik}]\n    requires: [apiToken]",
		},
		{
			name:         "several secrets the contract carries",
			integrations: "  pvr:\n    compatible: [{app: sonarr}]\n    requires: [apiKey]",
		},
		{
			name:         "no requirements at all",
			integrations: "  sso:\n    compatible: [{app: authentik}]",
		},
		{
			name:         "a name the contract does not carry",
			integrations: "  sso:\n    compatible: [{app: authentik}]\n    requires: [apitoken]",
			wantErr:      `integrations.sso.requires lists "apitoken", which this contract does not carry`,
		},
		{
			name:         "a requirement against a contract the framework does not know",
			integrations: "  knowledgeBase:\n    compatible: [{app: affine}]\n    requires: [adminPassword]",
			wantErr:      "integrations.knowledgeBase.requires needs a contract the framework knows",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apps, err := NewLoader(writeMetadataApp(t, fmt.Sprintf(tmpl, tc.integrations))).LoadAll()
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, apps, "sso-app")
		})
	}
}
