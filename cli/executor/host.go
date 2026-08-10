package executor

import (
	"context"
	"encoding/json"
	"strings"
)

// Host describes a runtime environment the CLI can execute against.
type Host interface {
	// Executor returns the executor used to run commands on the host.
	Executor() Executor
	// Ports maps logical service names to guest port numbers.
	Ports() map[string]string
	// DataDirs returns the runtime directory layout on the host.
	DataDirs() DataDirs
	// Ready reports whether the host is running and usable.
	Ready() bool
}

// DataDirs describes where bloud artifacts live on a runtime host.
type DataDirs struct {
	HostAgentDir string // where the host-agent binary lives
	DataDir      string // app data, secrets, traefik config
	AppsDir      string // mounted apps directory
}

// SSHHost is a Host running on a Lima VM, reached through an SSHExecutor.
type SSHHost struct {
	instance string
	remote   Executor
	local    Executor
	ports    map[string]string
	dataDirs DataDirs
}

// NewSSHHost returns a Host backed by a Lima VM. remote executes inside the
// VM; local executes on the machine running the CLI (used for VM status checks).
func NewSSHHost(instance string, remote, local Executor, ports map[string]string, dataDirs DataDirs) *SSHHost {
	return &SSHHost{instance: instance, remote: remote, local: local, ports: ports, dataDirs: dataDirs}
}

// Executor returns the executor that runs commands inside the VM.
func (h *SSHHost) Executor() Executor { return h.remote }

// Ports returns the host-agent service port map.
func (h *SSHHost) Ports() map[string]string { return h.ports }

// DataDirs returns the runtime directory layout inside the VM.
func (h *SSHHost) DataDirs() DataDirs { return h.dataDirs }

// Ready reports whether the Lima VM is running.
func (h *SSHHost) Ready() bool {
	res, err := h.local.Run(context.Background(), RunSpec{
		Command: "limactl",
		Args:    []string{"list", "--json"},
	})
	if err != nil {
		return false
	}
	return IsVMNameRunning(res.Stdout, h.instance)
}

// IsVMNameRunning reports whether limactl list --json output shows name Running.
func IsVMNameRunning(out, name string) bool {
	return vmStatus(out, name) == "Running"
}

// IsVMNamePresent reports whether limactl list --json output lists name at all.
func IsVMNamePresent(out, name string) bool {
	return vmStatus(out, name) != ""
}

func vmStatus(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var vm struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(line), &vm) != nil {
			continue
		}
		if vm.Name == name {
			return vm.Status
		}
	}
	return ""
}
