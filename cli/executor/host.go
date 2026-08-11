package executor

import (
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

// SSHHost is a Host reached through a Transport (Lima VM via limactl, QEMU
// guest via ssh). The transport owns remote execution and readiness checks.
type SSHHost struct {
	remote   Transport
	ports    map[string]string
	dataDirs DataDirs
}

// NewSSHHost returns a Host backed by a Transport. remote executes on the guest
// and answers readiness checks.
func NewSSHHost(remote Transport, ports map[string]string, dataDirs DataDirs) *SSHHost {
	return &SSHHost{remote: remote, ports: ports, dataDirs: dataDirs}
}

// Executor returns the executor that runs commands on the guest.
func (h *SSHHost) Executor() Executor { return h.remote }

// Ports returns the host-agent service port map.
func (h *SSHHost) Ports() map[string]string { return h.ports }

// DataDirs returns the runtime directory layout on the guest.
func (h *SSHHost) DataDirs() DataDirs { return h.dataDirs }

// Ready reports whether the guest is running and reachable.
func (h *SSHHost) Ready() bool { return h.remote.Ready() }

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
