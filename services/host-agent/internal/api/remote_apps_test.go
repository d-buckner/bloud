package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/slug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPI_AddRemoteApp(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	body := strings.NewReader(`{"appId":"test-app","tailnetAddr":"ts-test.tail123.ts.net","hostLabel":"Johan"}`)
	w := serverRequest(t, server, "POST", "/api/sharing/remote-apps", body)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.NotEmpty(t, response["intentId"], "response should contain intentId")
}

func TestAPI_AddRemoteApp_WithSSO(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	// Add a catalog app with SSO config
	server.catalog.(*FakeCatalogCache).AddApp(&catalog.App{
		CatalogID:   "navidrome",
		DisplayName: "Navidrome",
		SSO: catalog.SSO{
			Strategy:    "forward-auth",
			BypassPaths: []string{"/rest/"},
		},
	})

	body := strings.NewReader(`{"appId":"navidrome","tailnetAddr":"ts-nav.tail123.ts.net","hostLabel":"Johan"}`)
	w := serverRequest(t, server, "POST", "/api/sharing/remote-apps", body)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.NotEmpty(t, response["intentId"], "response should contain intentId")
}

func TestAPI_AddRemoteApp_MissingFields(t *testing.T) {
	server, _ := setupTestServer(t)

	tests := []struct {
		name string
		body string
		want string
	}{
		{"missing appId", `{"tailnetAddr":"ts.net","hostLabel":"X"}`, "appId is required"},
		{"missing tailnetAddr", `{"appId":"test-app","hostLabel":"X"}`, "tailnetAddr is required"},
		{"missing hostLabel", `{"appId":"test-app","tailnetAddr":"ts.net"}`, "hostLabel is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.NewReader(tt.body)
			w := serverRequest(t, server, "POST", "/api/sharing/remote-apps", body)

			assert.Equal(t, http.StatusBadRequest, w.Code)

			var resp map[string]string
			require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
			assert.Equal(t, tt.want, resp["error"])
		})
	}
}

func TestAPI_AddRemoteApp_UnknownApp(t *testing.T) {
	server, _ := setupTestServer(t)

	body := strings.NewReader(`{"appId":"nonexistent","tailnetAddr":"ts.net","hostLabel":"X"}`)
	w := serverRequest(t, server, "POST", "/api/sharing/remote-apps", body)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAPI_ListRemoteApps_Empty(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	w := serverRequest(t, server, "GET", "/api/sharing/remote-apps", nil)

	assert.Equal(t, http.StatusOK, w.Code)

	var apps []store.RemoteApp
	err := json.NewDecoder(w.Body).Decode(&apps)
	require.NoError(t, err)
	assert.Empty(t, apps)
}

func TestAPI_ListRemoteApps_WithApps(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	// Add a remote app directly to the store
	fakeStore := server.remoteAppStore.(*FakeRemoteAppStore)
	fakeStore.Create(store.RemoteApp{
		ID:                 "test-id-1",
		HostLabel:          "Johan",
		AppID:              "jellyfin",
		AppName:            "Jellyfin",
		TailnetAddr:        "ts-jf.tail123.ts.net",
		Status:             "active",
		BypassPaths:        []string{},
	})

	w := serverRequest(t, server, "GET", "/api/sharing/remote-apps", nil)

	assert.Equal(t, http.StatusOK, w.Code)

	var apps []store.RemoteApp
	err := json.NewDecoder(w.Body).Decode(&apps)
	require.NoError(t, err)
	assert.Len(t, apps, 1)
	assert.Equal(t, "Johan", apps[0].HostLabel)
}

func TestAPI_DeleteRemoteApp(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	// Add a remote app
	fakeStore := server.remoteAppStore.(*FakeRemoteAppStore)
	fakeStore.Create(store.RemoteApp{
		ID:          "delete-me",
		HostLabel:   "Johan",
		AppID:       "jellyfin",
		AppName:     "Jellyfin",
		Status:      "active",
		BypassPaths: []string{},
	})

	w := serverRequest(t, server, "DELETE", "/api/sharing/remote-apps/delete-me", nil)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.NotEmpty(t, response["intentId"], "response should contain intentId")
}

func TestAPI_DeleteRemoteApp_NotFound(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	w := serverRequest(t, server, "DELETE", "/api/sharing/remote-apps/nonexistent", nil)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Johan", "johan"},
		{"Johan's server", "johan-s-server"},
		{"My Server 123", "my-server-123"},
		{"  spaces  ", "spaces"},
		{"UPPER-case", "upper-case"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, slug.Slugify(tt.input))
		})
	}
}
