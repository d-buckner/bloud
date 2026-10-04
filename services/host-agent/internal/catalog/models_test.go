// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestApp_HeadlessField covers both ends of the contract: `headless: true` in a
// metadata.yaml reaches the struct, and the JSON omits the key when it is not
// set, so the wire shape is unchanged for every app that has a UI.
func TestApp_HeadlessField(t *testing.T) {
	var withFlag App
	require.NoError(t, yaml.Unmarshal([]byte("name: affine-mcp\nheadless: true\n"), &withFlag))
	assert.True(t, withFlag.Headless)

	var withoutFlag App
	require.NoError(t, yaml.Unmarshal([]byte("name: jellyfin\n"), &withoutFlag))
	assert.False(t, withoutFlag.Headless)

	out, err := json.Marshal(withoutFlag)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(out), "headless"),
		"an unset flag must not appear on the wire: %s", out)

	out, err = json.Marshal(withFlag)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"headless":true`)
}

func TestContainerDefs_PluralReturnsDirectly(t *testing.T) {
	app := &App{
		CatalogID: "myapp",
		Containers: []ContainerDef{
			{Name: "apps-myapp-server", Image: "myimage:1.0"},
			{Name: "apps-myapp-db", Image: "postgres:16", DependsOn: []string{"apps-myapp-server"}},
		},
	}
	defs := app.ContainerDefs()
	if len(defs) != 2 {
		t.Fatalf("expected 2 defs, got %d", len(defs))
	}
	if defs[0].Name != "apps-myapp-server" {
		t.Errorf("expected first name 'apps-myapp-server', got %q", defs[0].Name)
	}
	if defs[1].Name != "apps-myapp-db" {
		t.Errorf("expected second name 'apps-myapp-db', got %q", defs[1].Name)
	}
	if len(defs[1].DependsOn) != 1 || defs[1].DependsOn[0] != "apps-myapp-server" {
		t.Errorf("expected DependsOn [apps-myapp-server], got %v", defs[1].DependsOn)
	}
}

func TestContainerDefs_NeitherFieldReturnsNil(t *testing.T) {
	app := &App{CatalogID: "empty"}
	defs := app.ContainerDefs()
	if defs != nil {
		t.Errorf("expected nil, got %v", defs)
	}
}

func TestContainerDefs_YAMLPluralUnmarshal(t *testing.T) {
	raw := `
name: immich
displayName: Immich
containers:
  - name: apps-immich-postgres
    image: pgvector/pgvector:pg16
    network: immich-internal
    restartPolicy: always
    environment:
      POSTGRES_DB: immich
    healthCheck:
      test: ["CMD-SHELL", "pg_isready -U immich"]
      interval: 5
      timeout: 5
      retries: 10
  - name: apps-immich-server
    image: ghcr.io/immich-app/immich-server:release
    networks:
      - immich-internal
      - apps-net
    ports:
      - host: 2283
        container: 2283
    dependsOn:
      - apps-immich-postgres
`
	var app App
	if err := yaml.Unmarshal([]byte(raw), &app); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	defs := app.ContainerDefs()
	if len(defs) != 2 {
		t.Fatalf("expected 2 defs, got %d", len(defs))
	}

	pg := defs[0]
	if pg.Name != "apps-immich-postgres" {
		t.Errorf("postgres name: got %q", pg.Name)
	}
	if pg.HealthCheck == nil {
		t.Fatal("expected postgres health check, got nil")
	}
	if pg.HealthCheck.Retries != 10 {
		t.Errorf("postgres health check retries: expected 10, got %d", pg.HealthCheck.Retries)
	}
	if pg.Environment["POSTGRES_DB"] != "immich" {
		t.Errorf("postgres env: expected POSTGRES_DB=immich, got %q", pg.Environment["POSTGRES_DB"])
	}

	srv := defs[1]
	if srv.Name != "apps-immich-server" {
		t.Errorf("server name: got %q", srv.Name)
	}
	if len(srv.Networks) != 2 {
		t.Errorf("server networks: expected 2, got %d", len(srv.Networks))
	}
	if len(srv.DependsOn) != 1 || srv.DependsOn[0] != "apps-immich-postgres" {
		t.Errorf("server dependsOn: expected [apps-immich-postgres], got %v", srv.DependsOn)
	}
	if len(srv.Ports) != 1 || srv.Ports[0].Host != 2283 {
		t.Errorf("server ports: expected host=2283, got %v", srv.Ports)
	}
}
