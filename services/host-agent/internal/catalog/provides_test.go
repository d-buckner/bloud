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
// contract names, an inference endpoint's path is concatenated onto an
// address, and a contract name no consumer can use is a typo. Each has to fail
// the load rather
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
			name:     "modelSource with the endpoint path this contract carries",
			port:     "11434",
			provides: "  modelSource:\n    values: {path: /v1}",
		},
		{
			name:     "modelSource with no path at all",
			port:     "11434",
			provides: "  modelSource: {}",
			wantErr:  `provides.modelSource.values must declare "path"`,
		},
		{
			name:     "modelSource with a relative path",
			port:     "11434",
			provides: "  modelSource:\n    values: {path: v1}",
			wantErr:  "provides.modelSource.values.path must be an absolute path",
		},
		{
			name:     "a secret the contract does not carry",
			port:     "8989",
			provides: "  pvr:\n    secrets: [apiKey, somethingElse]",
			wantErr:  `provides.pvr publishes "somethingElse", which this contract does not carry`,
		},
		{
			name:     "modelSource on an app with no port to build the address from",
			port:     "0",
			provides: "  modelSource:\n    values: {path: /v1}",
			wantErr:  "provides.modelSource declares values that are resolved against the app's address",
		},
		{
			name:     "mcp with both values declared",
			port:     "3011",
			provides: "  mcp:\n    secrets: [httpToken]\n    values: {path: /mcp, serverName: simple}",
		},
		{
			name:     "mcp with the path supplied at runtime",
			port:     "3010",
			provides: "  mcp:\n    secrets: [httpToken]\n    values: {serverName: affine}\n    runtimeValues: [path]",
		},
		{
			name:     "a required value neither declared nor marked runtime",
			port:     "3010",
			provides: "  mcp:\n    secrets: [httpToken]\n    values: {serverName: affine}",
			wantErr:  `provides.mcp.values must declare "path"`,
		},
		{
			name:     "a runtime value the contract does not carry",
			port:     "3010",
			provides: "  mcp:\n    secrets: [httpToken]\n    values: {path: /mcp, serverName: affine}\n    runtimeValues: [bogus]",
			wantErr:  `provides.mcp.runtimeValues names "bogus", which this contract does not carry`,
		},
		{
			name:     "a value declared both statically and at runtime",
			port:     "3010",
			provides: "  mcp:\n    secrets: [httpToken]\n    values: {path: /mcp, serverName: affine}\n    runtimeValues: [path]",
			wantErr:  `provides.mcp declares "path" both in values and in runtimeValues`,
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

// TestValidateApp_RequiredIntegrationNeedsDefault pins the one place the graph's
// ordering guarantee used to rest on a convention nothing enforced.
//
// A required integration's provider is recorded from the `default: true`
// compatible entry, and for a required integration that recorded choice is the
// only source of the dependency edge: computeAppDeps skips the compatible scan
// entirely. So a required integration with no default records nothing, produces
// no edge, and the app installs with no dependency at all. It then resolves an
// empty credential on every pass and fails forever with no plan-time error
// anywhere near the cause. Two defaults are rejected for the same reason in the
// other direction: which one wins would depend on list iteration order.
func TestValidateApp_RequiredIntegrationNeedsDefault(t *testing.T) {
	const tmpl = `name: wrapper-app
displayName: Wrapper App
description: An app
category: productivity
port: 3011
integrations:
  mcp:
    required: true
    requires: [httpToken]
%s
containers:
  - name: apps-wrapper-app
    image: example/wrapper:1.0
`

	cases := []struct {
		name       string
		compatible string
		wantErr    string
	}{
		{
			name:       "exactly one default",
			compatible: "    compatible: [{app: target, default: true}]",
		},
		{
			name:       "one default among several compatible entries",
			compatible: "    compatible: [{app: other}, {app: target, default: true}]",
		},
		{
			name:       "no default at all",
			compatible: "    compatible: [{app: target}]",
			wantErr:    "integrations.mcp is required but declares no `default: true` compatible entry",
		},
		{
			name:       "no compatible entries at all",
			compatible: "    compatible: []",
			wantErr:    "integrations.mcp is required but declares no `default: true` compatible entry",
		},
		{
			name:       "two defaults",
			compatible: "    compatible: [{app: target, default: true}, {app: other, default: true}]",
			wantErr:    "integrations.mcp is required but declares 2 `default: true` entries",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewLoader(writeMetadataApp(t, fmt.Sprintf(tmpl, tc.compatible))).LoadAll()
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestValidateApp_OptionalIntegrationNeedsNoDefault keeps the rule scoped: an
// optional contract binds every compatible provider that turns out to be
// installed, so it has no recorded choice to be missing.
func TestValidateApp_OptionalIntegrationNeedsNoDefault(t *testing.T) {
	const metadata = `name: harness-app
displayName: Harness App
description: An app
category: productivity
integrations:
  mcp:
    required: false
    multi: true
    requires: [httpToken]
    compatible:
      - app: affine
      - app: sonarr
containers:
  - name: apps-harness-app
    image: example/harness:1.0
`
	apps, err := NewLoader(writeMetadataApp(t, metadata)).LoadAll()
	require.NoError(t, err)
	assert.Len(t, apps["harness-app"].Integrations["mcp"].Compatible, 2)
}
