package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		Name:        "postgres",
		DisplayName: "PostgreSQL",
		Status:      "running",
		IsSystem:    true,
	})
	fakeStore.AddApp(&store.InstalledApp{
		Name:              "immich",
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
		Name:        "jellyfin",
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

	// 2 nodes: jellyfin (app) + tailnet (connection)
	assert.Len(t, graph.Nodes, 2)

	nodeMap := make(map[string]graphNode)
	for _, n := range graph.Nodes {
		nodeMap[n.ID] = n
	}

	jellyfinNode := nodeMap["jellyfin"]
	assert.Equal(t, "Jellyfin", jellyfinNode.DisplayName)
	assert.Equal(t, "app", jellyfinNode.NodeType)

	tailnetNode := nodeMap["conn:tailnet:tn-abc"]
	assert.Equal(t, "My Tailnet", tailnetNode.DisplayName)
	assert.Equal(t, "active", tailnetNode.Status)
	assert.Equal(t, "connection", tailnetNode.NodeType)

	// 1 edge: tailnet connection → jellyfin
	assert.Len(t, graph.Edges, 1)
	edge := graph.Edges[0]
	assert.Equal(t, "conn:tailnet:tn-abc", edge.Source)
	assert.Equal(t, "jellyfin", edge.Target)
	assert.Equal(t, "tailnet", edge.Label)
}

func TestDeveloperGraph_LocalConnection(t *testing.T) {
	server := newDeveloperTestServer()

	fakeStore := server.appStore.(*FakeAppStore)
	fakeStore.AddApp(&store.InstalledApp{
		Name:        "traefik",
		DisplayName: "Traefik",
		Status:      "running",
		IsSystem:    true,
	})
	fakeStore.AddApp(&store.InstalledApp{
		Name:        "jellyfin",
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
		Name:        "navidrome",
		DisplayName: "Navidrome",
		SSO:         catalog.SSO{Strategy: "forward-auth"},
	})
	fakeCatalog.AddApp(&catalog.App{
		Name:        "jellyfin",
		DisplayName: "Jellyfin",
		SSO:         catalog.SSO{Strategy: "ldap"},
	})

	// Seed installed apps with sso integration config
	fakeStore := server.appStore.(*FakeAppStore)
	fakeStore.AddApp(&store.InstalledApp{
		Name:              "authentik",
		DisplayName:       "Authentik",
		Status:            "running",
		IsSystem:          true,
	})
	fakeStore.AddApp(&store.InstalledApp{
		Name:              "navidrome",
		DisplayName:       "Navidrome",
		Status:            "running",
		IntegrationConfig: map[string]string{"sso": "authentik"},
	})
	fakeStore.AddApp(&store.InstalledApp{
		Name:              "jellyfin",
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
