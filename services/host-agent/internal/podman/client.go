package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Client communicates with the Podman API via Unix socket
type Client struct {
	httpClient *http.Client
	socketPath string
}

// ContainerConfig specifies how to create a container
type ContainerConfig struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Env     map[string]string `json:"env,omitempty"`
	Ports   []PortMapping     `json:"portmappings,omitempty"`
	Volumes []VolumeMount     `json:"mounts,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// PortMapping maps container port to host
type PortMapping struct {
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port,omitempty"`
	Protocol      string `json:"protocol,omitempty"` // tcp or udp
}

// VolumeMount mounts a host path into the container
type VolumeMount struct {
	Source      string   `json:"source"`
	Destination string   `json:"destination"`
	Type        string   `json:"type"` // bind, volume, tmpfs
	Options     []string `json:"options,omitempty"`
}

// Container represents a running or stopped container
type Container struct {
	ID      string   `json:"Id"`
	Names   []string `json:"Names"`
	Image   string   `json:"Image"`
	State   string   `json:"State"`
	Status  string   `json:"Status"`
	Created int64    `json:"Created"`
}

// NewClient creates a Podman client connected to the default socket
func NewClient() (*Client, error) {
	socketPath := defaultSocketPath()

	// Check if socket exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("podman socket not found at %s", socketPath)
	}

	return NewClientWithSocket(socketPath)
}

// NewClientWithSocket creates a client connected to a specific socket path
func NewClientWithSocket(socketPath string) (*Client, error) {
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
		Timeout: 30 * time.Second,
	}

	return &Client{
		httpClient: httpClient,
		socketPath: socketPath,
	}, nil
}

// defaultSocketPath returns the default Podman socket location
func defaultSocketPath() string {
	// For rootless podman, the socket is in the user's runtime directory
	if xdgRuntime := os.Getenv("XDG_RUNTIME_DIR"); xdgRuntime != "" {
		return filepath.Join(xdgRuntime, "podman", "podman.sock")
	}

	// Fallback to standard Linux location
	uid := os.Getuid()
	return fmt.Sprintf("/run/user/%d/podman/podman.sock", uid)
}

// Ping checks if Podman is running and accessible
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.get(ctx, "/libpod/_ping")
	if err != nil {
		return fmt.Errorf("failed to ping podman: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("podman ping returned status %d", resp.StatusCode)
	}

	return nil
}

// PullImage pulls an image from a registry
func (c *Client) PullImage(ctx context.Context, image string) error {
	params := url.Values{}
	params.Set("reference", image)

	resp, err := c.post(ctx, "/libpod/images/pull?"+params.Encode(), nil)
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	defer resp.Body.Close()

	// Read and discard the stream response (progress output)
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("image pull returned status %d", resp.StatusCode)
	}

	return nil
}

// CreateContainer creates a new container
func (c *Client) CreateContainer(ctx context.Context, config ContainerConfig) (string, error) {
	// Build the Podman API spec
	spec := buildContainerSpec(config)

	body, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("failed to marshal container spec: %w", err)
	}

	params := url.Values{}
	params.Set("name", config.Name)

	resp, err := c.post(ctx, "/libpod/containers/create?"+params.Encode(), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create container returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return result.ID, nil
}

// StartContainer starts a stopped container
func (c *Client) StartContainer(ctx context.Context, nameOrID string) error {
	resp, err := c.post(ctx, fmt.Sprintf("/libpod/containers/%s/start", nameOrID), nil)
	if err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("start container returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// StopContainer stops a running container
func (c *Client) StopContainer(ctx context.Context, nameOrID string, timeout int) error {
	params := url.Values{}
	if timeout > 0 {
		params.Set("timeout", fmt.Sprintf("%d", timeout))
	}

	path := fmt.Sprintf("/libpod/containers/%s/stop", nameOrID)
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	resp, err := c.post(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stop container returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// RemoveContainer removes a container
func (c *Client) RemoveContainer(ctx context.Context, nameOrID string, force bool) error {
	params := url.Values{}
	if force {
		params.Set("force", "true")
	}

	path := fmt.Sprintf("/libpod/containers/%s", nameOrID)
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	resp, err := c.delete(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remove container returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// ListContainers returns all containers (running and stopped)
func (c *Client) ListContainers(ctx context.Context) ([]Container, error) {
	params := url.Values{}
	params.Set("all", "true")

	resp, err := c.get(ctx, "/libpod/containers/json?"+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list containers returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var containers []Container
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return containers, nil
}

// GetContainer returns info about a specific container
func (c *Client) GetContainer(ctx context.Context, nameOrID string) (*Container, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/libpod/containers/%s/json", nameOrID))
	if err != nil {
		return nil, fmt.Errorf("failed to get container: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get container returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var container Container
	if err := json.NewDecoder(resp.Body).Decode(&container); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &container, nil
}

// HTTP helpers

func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	return c.doRequest(ctx, http.MethodGet, path, nil)
}

func (c *Client) post(ctx context.Context, path string, body io.Reader) (*http.Response, error) {
	return c.doRequest(ctx, http.MethodPost, path, body)
}

func (c *Client) delete(ctx context.Context, path string) (*http.Response, error) {
	return c.doRequest(ctx, http.MethodDelete, path, nil)
}

func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	// The host doesn't matter for Unix sockets, but we need a valid URL
	url := "http://podman" + path

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.httpClient.Do(req)
}

// buildContainerSpec converts our simple config to Podman's container spec
func buildContainerSpec(config ContainerConfig) map[string]interface{} {
	spec := map[string]interface{}{
		"image": config.Image,
	}

	// Environment variables
	if len(config.Env) > 0 {
		var envList []string
		for k, v := range config.Env {
			envList = append(envList, fmt.Sprintf("%s=%s", k, v))
		}
		spec["env"] = envList
	}

	// Port mappings
	if len(config.Ports) > 0 {
		var portMappings []map[string]interface{}
		for _, p := range config.Ports {
			pm := map[string]interface{}{
				"container_port": p.ContainerPort,
			}
			if p.HostPort > 0 {
				pm["host_port"] = p.HostPort
			}
			if p.Protocol != "" {
				pm["protocol"] = p.Protocol
			}
			portMappings = append(portMappings, pm)
		}
		spec["portmappings"] = portMappings
	}

	// Volume mounts
	if len(config.Volumes) > 0 {
		var mounts []map[string]interface{}
		for _, v := range config.Volumes {
			mount := map[string]interface{}{
				"source":      v.Source,
				"destination": v.Destination,
				"type":        v.Type,
			}
			if len(v.Options) > 0 {
				mount["options"] = v.Options
			}
			mounts = append(mounts, mount)
		}
		spec["mounts"] = mounts
	}

	// Labels
	if len(config.Labels) > 0 {
		spec["labels"] = config.Labels
	}

	return spec
}
