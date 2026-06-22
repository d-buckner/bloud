package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

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
		podmanClient: nil,
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

	// Find the edge
	edge := graph.Edges[0]
	assert.Equal(t, "immich", edge.Source)
	assert.Equal(t, "postgres", edge.Target)
	assert.Equal(t, "database", edge.Label)

	// Verify node properties
	nodeMap := make(map[string]graphNode)
	for _, n := range graph.Nodes {
		nodeMap[n.ID] = n
	}

	pgNode := nodeMap["postgres"]
	assert.Equal(t, "PostgreSQL", pgNode.DisplayName)
	assert.True(t, pgNode.IsSystem)
	assert.Equal(t, "running", pgNode.Status)
	assert.Nil(t, pgNode.Sidecar)

	immichNode := nodeMap["immich"]
	assert.Equal(t, "Immich", immichNode.DisplayName)
	assert.False(t, immichNode.IsSystem)
	assert.Nil(t, immichNode.Sidecar) // no podmanClient
}

func TestDeveloperGraph_NoPodman(t *testing.T) {
	server := newDeveloperTestServer()
	// podmanClient is already nil in newDeveloperTestServer

	fakeStore := server.appStore.(*FakeAppStore)
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

	assert.Len(t, graph.Nodes, 1)
	assert.Equal(t, "jellyfin", graph.Nodes[0].ID)
	assert.Nil(t, graph.Nodes[0].Sidecar)
}
