package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGateway implements sharing.GatewayManagerInterface for testing.
type fakeGateway struct {
	running       bool
	tailnetDomain string
}

func (g *fakeGateway) EnsureRunning(_ context.Context) error { return nil }
func (g *fakeGateway) Stop(_ context.Context) error          { return nil }
func (g *fakeGateway) StopAndPurge(_ context.Context) error  { return nil }
func (g *fakeGateway) IsRunning(_ context.Context) bool      { return g.running }
func (g *fakeGateway) GetTailnetDomain(_ context.Context) (string, error) {
	if g.tailnetDomain == "" {
		return "", fmt.Errorf("no domain")
	}
	return g.tailnetDomain, nil
}

func newDeveloperTestServer() *Server {
	appStore := NewFakeAppStore()
	catalogCache := NewFakeCatalogCache()
	appHub := NewAppEventHub(appStore)
	appStore.SetOnChange(appHub.Broadcast)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	server := &Server{
		cfg:          ServerConfig{},
		router:       chi.NewRouter(),
		catalog:      catalogCache,
		appStore:     appStore,
		appHub:       appHub,
		shareStore:   newFakeShareStore(),
		tailnetStore: newFakeTailnetStore(),
		logger:       logger,
	}

	server.setupMiddleware()
	server.setupRoutes()
	return server
}

func TestDeveloperGraph_Empty(t *testing.T) {
	server := newDeveloperTestServer()

	req := httptest.NewRequest("GET", "/api/system/developer", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var graph developerGraph
	err := json.NewDecoder(w.Body).Decode(&graph)
	require.NoError(t, err)
	assert.Empty(t, graph.Nodes)
	assert.Empty(t, graph.Edges)
}

func TestDeveloperGraph_WithApps(t *testing.T) {
	server := newDeveloperTestServer()

	fakeStore := server.appStore.(*FakeAppStore)
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID:        "postgres",
		DisplayName: "PostgreSQL",
		Status:      "running",
		IsSystem:    true,
	})
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID:              "immich",
		DisplayName:       "Immich",
		Status:            "running",
		IsSystem:          false,
		IntegrationConfig: map[string]string{"database": "postgres"},
	})

	req := httptest.NewRequest("GET", "/api/system/developer", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var graph developerGraph
	err := json.NewDecoder(w.Body).Decode(&graph)
	require.NoError(t, err)

	assert.Len(t, graph.Nodes, 2)
	assert.Len(t, graph.Edges, 1)

	edge := graph.Edges[0]
	assert.Equal(t, "immich", edge.Source)
	assert.Equal(t, "postgres", edge.Target)
	assert.Equal(t, "database", edge.Label)

	nodeMap := make(map[string]graphNode)
	for _, n := range graph.Nodes {
		nodeMap[n.ID] = n
	}

	pgNode := nodeMap["postgres"]
	assert.Equal(t, "PostgreSQL", pgNode.DisplayName)
	assert.True(t, pgNode.IsSystem)
	assert.Equal(t, "running", pgNode.Status)
	assert.Equal(t, "app", pgNode.NodeType)

	immichNode := nodeMap["immich"]
	assert.Equal(t, "Immich", immichNode.DisplayName)
	assert.False(t, immichNode.IsSystem)
	assert.Equal(t, "app", immichNode.NodeType)
}

func TestDeveloperGraph_WithTailnet(t *testing.T) {
	server := newDeveloperTestServer()

	// Seed a tailnet connection
	fakeTailnet := server.tailnetStore.(*fakeTailnetStore)
	fakeTailnet.conns["tn-abc"] = &store.TailnetConnection{
		ID:     "tn-abc",
		Name:   "My Tailnet",
		Type:   "tailscale",
		Status: "active",
	}

	// Seed an app with a tailnet_id
	fakeStore := server.appStore.(*FakeAppStore)
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID:   "jellyfin",
		DisplayName: "Jellyfin",
		Status:      "running",
		TailnetID:   "tn-abc",
	})

	req := httptest.NewRequest("GET", "/api/system/developer", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var graph developerGraph
	err := json.NewDecoder(w.Body).Decode(&graph)
	require.NoError(t, err)

	// 3 nodes: jellyfin (app) + ts:jellyfin (tailnet node) + tailnet (connection)
	assert.Len(t, graph.Nodes, 3)

	nodeMap := make(map[string]graphNode)
	for _, n := range graph.Nodes {
		nodeMap[n.ID] = n
	}

	jellyfinNode := nodeMap["jellyfin"]
	assert.Equal(t, "Jellyfin", jellyfinNode.DisplayName)
	assert.Equal(t, "app", jellyfinNode.NodeType)

	tsNode := nodeMap["ts:jellyfin"]
	assert.Equal(t, "Jellyfin Tunnel", tsNode.DisplayName)
	assert.Equal(t, "running", tsNode.Status)
	assert.True(t, tsNode.IsSystem)
	assert.Equal(t, "app", tsNode.NodeType)

	tailnetNode := nodeMap["conn:tailnet:tn-abc"]
	assert.Equal(t, "My Tailnet", tailnetNode.DisplayName)
	assert.Equal(t, "active", tailnetNode.Status)
	assert.Equal(t, "connection", tailnetNode.NodeType)

	// 1 edge: tailnet connection → ts:jellyfin (no traefik, so no route edge)
	assert.Len(t, graph.Edges, 1)
	edge := graph.Edges[0]
	assert.Equal(t, "conn:tailnet:tn-abc", edge.Source)
	assert.Equal(t, "ts:jellyfin", edge.Target)
	assert.Equal(t, "tailnet", edge.Label)
}

func TestDeveloperGraph_LocalConnection(t *testing.T) {
	server := newDeveloperTestServer()

	fakeStore := server.appStore.(*FakeAppStore)
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID:        "traefik",
		DisplayName: "Traefik",
		Status:      "running",
		IsSystem:    true,
	})
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID:        "jellyfin",
		DisplayName: "Jellyfin",
		Status:      "running",
	})

	req := httptest.NewRequest("GET", "/api/system/developer", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var graph developerGraph
	err := json.NewDecoder(w.Body).Decode(&graph)
	require.NoError(t, err)

	// 3 nodes: traefik, jellyfin, conn:local
	assert.Len(t, graph.Nodes, 3)

	nodeMap := make(map[string]graphNode)
	for _, n := range graph.Nodes {
		nodeMap[n.ID] = n
	}

	localNode := nodeMap["conn:local"]
	assert.Equal(t, "LAN", localNode.DisplayName)
	assert.Equal(t, "connection", localNode.NodeType)
	assert.Equal(t, "active", localNode.Status)

	// Should have a route edge from local → traefik
	var routeEdge *graphEdge
	for i := range graph.Edges {
		if graph.Edges[i].Source == "conn:local" {
			routeEdge = &graph.Edges[i]
			break
		}
	}
	require.NotNil(t, routeEdge)
	assert.Equal(t, "traefik", routeEdge.Target)
	assert.Equal(t, "route", routeEdge.Label)
}

func TestDeveloperGraph_SSOEdgeLabel(t *testing.T) {
	server := newDeveloperTestServer()

	// Seed catalog entries with different SSO strategies
	fakeCatalog := server.catalog.(*FakeCatalogCache)
	fakeCatalog.AddApp(&catalog.App{
		CatalogID:        "navidrome",
		DisplayName: "Navidrome",
		SSO:         catalog.SSO{Strategy: "forward-auth"},
	})
	fakeCatalog.AddApp(&catalog.App{
		CatalogID:        "jellyfin",
		DisplayName: "Jellyfin",
		SSO:         catalog.SSO{Strategy: "ldap"},
	})

	// Seed installed apps with sso integration config
	fakeStore := server.appStore.(*FakeAppStore)
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID:        "authentik",
		DisplayName: "Authentik",
		Status:      "running",
		IsSystem:    true,
	})
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID:              "navidrome",
		DisplayName:       "Navidrome",
		Status:            "running",
		IntegrationConfig: map[string]string{"sso": "authentik"},
	})
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID:              "jellyfin",
		DisplayName:       "Jellyfin",
		Status:            "running",
		IntegrationConfig: map[string]string{"sso": "authentik"},
	})

	req := httptest.NewRequest("GET", "/api/system/developer", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var graph developerGraph
	err := json.NewDecoder(w.Body).Decode(&graph)
	require.NoError(t, err)

	// Build edge lookup by source
	edgesBySource := make(map[string]graphEdge)
	for _, e := range graph.Edges {
		edgesBySource[e.Source] = e
	}

	// navidrome → authentik should use "forward-auth" label
	navEdge := edgesBySource["navidrome"]
	assert.Equal(t, "authentik", navEdge.Target)
	assert.Equal(t, "forward-auth", navEdge.Label)

	// jellyfin → authentik should use "ldap" label
	jellyEdge := edgesBySource["jellyfin"]
	assert.Equal(t, "authentik", jellyEdge.Target)
	assert.Equal(t, "ldap", jellyEdge.Label)
}

func TestDeveloperGraph_Gateway(t *testing.T) {
	server := newDeveloperTestServer()
	server.gateway = &fakeGateway{running: true}

	// Seed tailnet connection
	fakeTailnet := server.tailnetStore.(*fakeTailnetStore)
	fakeTailnet.conns["tn-abc"] = &store.TailnetConnection{
		ID:     "tn-abc",
		Name:   "My Tailnet",
		Type:   "tailscale",
		Status: "active",
	}

	// Seed apps — traefik (system) + jellyfin with tailnet
	fakeStore := server.appStore.(*FakeAppStore)
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID:   "traefik",
		DisplayName: "Traefik",
		Status:      "running",
		IsSystem:    true,
	})
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID:   "jellyfin",
		DisplayName: "Jellyfin",
		Status:      "running",
		TailnetID:   "tn-abc",
	})

	req := httptest.NewRequest("GET", "/api/system/developer", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var graph developerGraph
	err := json.NewDecoder(w.Body).Decode(&graph)
	require.NoError(t, err)

	nodeMap := make(map[string]graphNode)
	for _, n := range graph.Nodes {
		nodeMap[n.ID] = n
	}

	// Gateway node should exist as a system app
	gwNode, ok := nodeMap["sys:gateway"]
	require.True(t, ok, "gateway node should exist")
	assert.Equal(t, "Tailnet Gateway", gwNode.DisplayName)
	assert.Equal(t, "running", gwNode.Status)
	assert.True(t, gwNode.IsSystem)
	assert.Equal(t, "app", gwNode.NodeType)

	// Tailnet node container should exist
	tsNode, ok := nodeMap["ts:jellyfin"]
	require.True(t, ok, "tailnet node ts:jellyfin should exist")
	assert.Equal(t, "Jellyfin Tunnel", tsNode.DisplayName)
	assert.Equal(t, "running", tsNode.Status)
	assert.True(t, tsNode.IsSystem)

	// Tailnet connection node should exist
	_, ok = nodeMap["conn:tailnet:tn-abc"]
	assert.True(t, ok, "tailnet connection node should exist")

	edgeSet := make(map[string]graphEdge)
	for _, e := range graph.Edges {
		edgeSet[e.Source+"→"+e.Target] = e
	}

	// Tailnet → gateway
	gwEdge, ok := edgeSet["conn:tailnet:tn-abc→sys:gateway"]
	require.True(t, ok, "should have tailnet→gateway edge")
	assert.Equal(t, "tailnet", gwEdge.Label)

	// Gateway → traefik (SOCKS5 proxy for remote app LAN access)
	proxyEdge, ok := edgeSet["sys:gateway→traefik"]
	require.True(t, ok, "should have gateway→traefik edge")
	assert.Equal(t, "proxy", proxyEdge.Label)

	// Tailnet → ts:jellyfin (tailnet node container)
	tnToTsEdge, ok := edgeSet["conn:tailnet:tn-abc→ts:jellyfin"]
	require.True(t, ok, "should have tailnet→ts:jellyfin edge")
	assert.Equal(t, "tailnet", tnToTsEdge.Label)

	// ts:jellyfin → traefik (tailnet node proxies to traefik)
	tsToTraefikEdge, ok := edgeSet["ts:jellyfin→traefik"]
	require.True(t, ok, "should have ts:jellyfin→traefik edge")
	assert.Equal(t, "route", tsToTraefikEdge.Label)

	// Local → traefik
	localEdge, ok := edgeSet["conn:local→traefik"]
	require.True(t, ok, "should have local→traefik edge")
	assert.Equal(t, "route", localEdge.Label)

	// Without a tailnet domain, tailnet connection uses store name
	connNode := nodeMap["conn:tailnet:tn-abc"]
	assert.Equal(t, "My Tailnet", connNode.DisplayName)

	// LAN connection node
	localNode := nodeMap["conn:local"]
	assert.Equal(t, "LAN", localNode.DisplayName)
}

func TestDeveloperGraph_TailnetDomain(t *testing.T) {
	server := newDeveloperTestServer()
	server.gateway = &fakeGateway{running: true, tailnetDomain: "tail12756a.ts.net"}

	// Seed tailnet connection
	fakeTailnet := server.tailnetStore.(*fakeTailnetStore)
	fakeTailnet.conns["tn-abc"] = &store.TailnetConnection{
		ID:     "tn-abc",
		Name:   "My Tailnet",
		Type:   "tailscale",
		Status: "active",
	}

	// Seed app with tailnet
	fakeStore := server.appStore.(*FakeAppStore)
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID:   "traefik",
		DisplayName: "Traefik",
		Status:      "running",
		IsSystem:    true,
	})
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID:   "jellyfin",
		DisplayName: "Jellyfin",
		Status:      "running",
		TailnetID:   "tn-abc",
	})

	req := httptest.NewRequest("GET", "/api/system/developer", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var graph developerGraph
	err := json.NewDecoder(w.Body).Decode(&graph)
	require.NoError(t, err)

	// Tailnet domain should be in the response
	assert.Equal(t, "tail12756a.ts.net", graph.TailnetDomain)

	nodeMap := make(map[string]graphNode)
	for _, n := range graph.Nodes {
		nodeMap[n.ID] = n
	}

	// Tailnet connection should show the bloud.{domain} URL
	connNode := nodeMap["conn:tailnet:tn-abc"]
	assert.Equal(t, "bloud.tail12756a.ts.net", connNode.DisplayName)
}
