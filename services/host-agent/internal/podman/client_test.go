package podman

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMockPodman creates a Unix socket server that mocks Podman API responses
func setupMockPodman(t *testing.T, handler http.Handler) (string, func()) {
	// Use /tmp for shorter socket path (macOS has 104 char limit)
	tmpDir, err := os.MkdirTemp("/tmp", "podman-test-")
	require.NoError(t, err)

	socketPath := filepath.Join(tmpDir, "p.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	server := &http.Server{Handler: handler}
	go server.Serve(listener)

	cleanup := func() {
		server.Close()
		listener.Close()
		os.RemoveAll(tmpDir)
	}

	return socketPath, cleanup
}

func TestClient_Ping(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/libpod/_ping" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
			return
		}
		http.NotFound(w, r)
	})

	socketPath, cleanup := setupMockPodman(t, handler)
	defer cleanup()

	client, err := NewClientWithSocket(socketPath)
	require.NoError(t, err)

	err = client.Ping(context.Background())
	assert.NoError(t, err)
}

func TestClient_ListContainers(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/libpod/containers/json" {
			containers := []Container{
				{
					ID:     "abc123",
					Names:  []string{"radarr"},
					Image:  "lscr.io/linuxserver/radarr:latest",
					State:  "running",
					Status: "Up 2 hours",
				},
				{
					ID:     "def456",
					Names:  []string{"sonarr"},
					Image:  "lscr.io/linuxserver/sonarr:latest",
					State:  "exited",
					Status: "Exited (0) 1 hour ago",
				},
			}
			json.NewEncoder(w).Encode(containers)
			return
		}
		http.NotFound(w, r)
	})

	socketPath, cleanup := setupMockPodman(t, handler)
	defer cleanup()

	client, err := NewClientWithSocket(socketPath)
	require.NoError(t, err)

	containers, err := client.ListContainers(context.Background())
	require.NoError(t, err)

	assert.Len(t, containers, 2)
	assert.Equal(t, "radarr", containers[0].Names[0])
	assert.Equal(t, "running", containers[0].State)
	assert.Equal(t, "sonarr", containers[1].Names[0])
	assert.Equal(t, "exited", containers[1].State)
}

func TestClient_CreateContainer(t *testing.T) {
	var receivedSpec map[string]interface{}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/libpod/containers/create" && r.Method == http.MethodPost {
			json.NewDecoder(r.Body).Decode(&receivedSpec)

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{
				"Id": "newcontainer123",
			})
			return
		}
		http.NotFound(w, r)
	})

	socketPath, cleanup := setupMockPodman(t, handler)
	defer cleanup()

	client, err := NewClientWithSocket(socketPath)
	require.NoError(t, err)

	config := ContainerConfig{
		Name:  "test-app",
		Image: "nginx:latest",
		Env: map[string]string{
			"PUID": "1000",
			"PGID": "1000",
		},
		Ports: []PortMapping{
			{ContainerPort: 80, HostPort: 8080},
		},
	}

	containerID, err := client.CreateContainer(context.Background(), config)
	require.NoError(t, err)

	assert.Equal(t, "newcontainer123", containerID)
	assert.Equal(t, "nginx:latest", receivedSpec["image"])
}

func TestClient_StartStopContainer(t *testing.T) {
	var startCalled, stopCalled bool

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/libpod/containers/mycontainer/start" && r.Method == http.MethodPost:
			startCalled = true
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/libpod/containers/mycontainer/stop" && r.Method == http.MethodPost:
			stopCalled = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	socketPath, cleanup := setupMockPodman(t, handler)
	defer cleanup()

	client, err := NewClientWithSocket(socketPath)
	require.NoError(t, err)

	ctx := context.Background()

	err = client.StartContainer(ctx, "mycontainer")
	require.NoError(t, err)
	assert.True(t, startCalled)

	err = client.StopContainer(ctx, "mycontainer", 10)
	require.NoError(t, err)
	assert.True(t, stopCalled)
}

func TestClient_RemoveContainer(t *testing.T) {
	var removeCalled bool
	var forceUsed bool

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/libpod/containers/mycontainer" && r.Method == http.MethodDelete {
			removeCalled = true
			forceUsed = r.URL.Query().Get("force") == "true"
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})

	socketPath, cleanup := setupMockPodman(t, handler)
	defer cleanup()

	client, err := NewClientWithSocket(socketPath)
	require.NoError(t, err)

	err = client.RemoveContainer(context.Background(), "mycontainer", true)
	require.NoError(t, err)
	assert.True(t, removeCalled)
	assert.True(t, forceUsed)
}

func TestClient_GetContainer(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/libpod/containers/existing/json":
			json.NewEncoder(w).Encode(Container{
				ID:    "abc123",
				Names: []string{"existing"},
				State: "running",
			})
		case "/libpod/containers/missing/json":
			w.WriteHeader(http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	})

	socketPath, cleanup := setupMockPodman(t, handler)
	defer cleanup()

	client, err := NewClientWithSocket(socketPath)
	require.NoError(t, err)

	ctx := context.Background()

	// Test existing container
	container, err := client.GetContainer(ctx, "existing")
	require.NoError(t, err)
	require.NotNil(t, container)
	assert.Equal(t, "running", container.State)

	// Test missing container
	container, err = client.GetContainer(ctx, "missing")
	require.NoError(t, err)
	assert.Nil(t, container)
}

func TestClient_DefaultSocketPath(t *testing.T) {
	// Test with XDG_RUNTIME_DIR set
	originalXDG := os.Getenv("XDG_RUNTIME_DIR")
	defer os.Setenv("XDG_RUNTIME_DIR", originalXDG)

	os.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	path := defaultSocketPath()
	assert.Equal(t, "/run/user/1000/podman/podman.sock", path)
}

func TestBuildContainerSpec(t *testing.T) {
	config := ContainerConfig{
		Name:  "test-app",
		Image: "nginx:latest",
		Env: map[string]string{
			"KEY1": "value1",
			"KEY2": "value2",
		},
		Ports: []PortMapping{
			{ContainerPort: 80, HostPort: 8080, Protocol: "tcp"},
			{ContainerPort: 443, HostPort: 8443},
		},
		Volumes: []VolumeMount{
			{Source: "/host/path", Destination: "/container/path", Type: "bind"},
		},
		Labels: map[string]string{
			"app": "test",
		},
	}

	spec := buildContainerSpec(config)

	assert.Equal(t, "nginx:latest", spec["image"])
	assert.NotNil(t, spec["env"])
	assert.NotNil(t, spec["portmappings"])
	assert.NotNil(t, spec["mounts"])
	assert.NotNil(t, spec["labels"])

	labels := spec["labels"].(map[string]string)
	assert.Equal(t, "test", labels["app"])
}
