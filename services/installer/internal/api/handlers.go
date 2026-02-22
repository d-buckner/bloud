package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud-v3/services/installer/internal/disks"
	"codeberg.org/d-buckner/bloud-v3/services/installer/internal/installer"
	"codeberg.org/d-buckner/bloud-v3/services/installer/internal/sse"
)

type StatusResponse struct {
	Phase       installer.Phase `json:"phase"`
	Hostname    string          `json:"hostname"`
	IPAddresses []string        `json:"ipAddresses"`
	CPU         string          `json:"cpu"`
	MemoryGB    int             `json:"memoryGB"`
}

// DiskInfo is the API representation of a disk, with frontend-friendly field names.
type DiskInfo struct {
	Device          string  `json:"device"`
	SizeGB          float64 `json:"sizeGB"`
	Model           string  `json:"model"`
	HasExistingData bool    `json:"hasExistingData"`
}

type DisksResponse struct {
	Disks        []DiskInfo `json:"disks"`
	AutoSelected string     `json:"autoSelected"`
	Ambiguous    bool       `json:"ambiguous"`
}

func toDiskInfo(d disks.Disk) DiskInfo {
	return DiskInfo{
		Device:          d.Device,
		SizeGB:          float64(d.SizeBytes) / 1e9,
		Model:           d.Model,
		HasExistingData: diskHasPartitions(d.Device),
	}
}

// diskHasPartitions checks sysfs for existing partitions on the device.
func diskHasPartitions(device string) bool {
	name := filepath.Base(device)
	entries, err := os.ReadDir("/sys/block/" + name)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), name) {
			return true
		}
	}
	return false
}

type InstallRequestBody struct {
	Disk       string `json:"disk"`
	Encryption bool   `json:"encryption"`
	FlakePath  string `json:"flakePath"`
}

type InstallResponse struct {
	Started bool `json:"started"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{"mode": "installer"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if s.mock {
		respondJSON(w, http.StatusOK, StatusResponse{
			Phase:       s.installer.Phase(),
			Hostname:    "bloud",
			IPAddresses: []string{"192.168.1.42"},
			CPU:         "Intel Core i5-8250U (mock)",
			MemoryGB:    16,
		})
		return
	}

	hostname, _ := os.Hostname()
	ips := localIPs()
	cpu := cpuModel()
	memGB := memoryGB()

	respondJSON(w, http.StatusOK, StatusResponse{
		Phase:       s.installer.Phase(),
		Hostname:    hostname,
		IPAddresses: ips,
		CPU:         cpu,
		MemoryGB:    memGB,
	})
}

func (s *Server) handleDisks(w http.ResponseWriter, r *http.Request) {
	if s.mock {
		mockDisks := []DiskInfo{
			{Device: "/dev/sda", SizeGB: 465.8, Model: "Samsung 870 EVO", HasExistingData: true},
			{Device: "/dev/sdb", SizeGB: 111.8, Model: "Kingston SSD", HasExistingData: false},
		}
		respondJSON(w, http.StatusOK, DisksResponse{
			Disks:        mockDisks,
			AutoSelected: "/dev/sda",
			Ambiguous:    false,
		})
		return
	}

	all, err := disks.Enumerate()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to enumerate disks: "+err.Error())
		return
	}

	bootDev := bootDevice()
	selected := disks.AutoSelect(all, bootDev)

	autoSelectedPath := ""
	if selected != nil {
		autoSelectedPath = selected.Device
	}

	diskInfos := make([]DiskInfo, len(all))
	for i, d := range all {
		diskInfos[i] = toDiskInfo(d)
	}

	respondJSON(w, http.StatusOK, DisksResponse{
		Disks:        diskInfos,
		AutoSelected: autoSelectedPath,
		Ambiguous:    disks.AreAmbiguous(all),
	})
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	var body InstallRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Disk == "" {
		respondError(w, http.StatusBadRequest, "disk is required")
		return
	}

	req := installer.InstallRequest{
		Disk:       body.Disk,
		Encryption: body.Encryption,
		FlakePath:  body.FlakePath,
	}

	if s.mock {
		if err := s.installer.StartMock(req); err != nil {
			respondError(w, http.StatusConflict, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, InstallResponse{Started: true})
		return
	}

	all, err := disks.Enumerate()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to enumerate disks")
		return
	}

	if !diskExists(all, body.Disk) {
		respondError(w, http.StatusBadRequest, "disk not found: "+body.Disk)
		return
	}

	if err := s.installer.Start(r.Context(), req); err != nil {
		respondError(w, http.StatusConflict, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, InstallResponse{Started: true})
}

func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	sw, err := sse.NewWriter(w)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ch, unsub := s.installer.Subscribe()
	defer unsub()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := sw.Send(event); err != nil {
				return
			}
			if event.Phase == installer.PhaseComplete || event.Phase == installer.PhaseFailed {
				return
			}
		}
	}
}

func (s *Server) handleReboot(w http.ResponseWriter, r *http.Request) {
	if s.installer.Phase() != installer.PhaseComplete {
		respondError(w, http.StatusConflict, "installation not complete")
		return
	}

	if s.mock {
		respondJSON(w, http.StatusOK, map[string]bool{"rebooting": true})
		return
	}

	if err := exec.CommandContext(r.Context(), "systemctl", "reboot").Run(); err != nil {
		respondError(w, http.StatusInternalServerError, "reboot failed: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]bool{"rebooting": true})
}

func diskExists(all []disks.Disk, device string) bool {
	for _, d := range all {
		if d.Device == device {
			return true
		}
	}
	return false
}

func localIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var addrs []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		ifAddrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range ifAddrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			addrs = append(addrs, ip.String())
		}
	}
	return addrs
}

func cpuModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func memoryGB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			var kb int
			fmt.Sscanf(strings.TrimPrefix(line, "MemTotal:"), "%d", &kb)
			return kb / (1024 * 1024)
		}
	}
	return 0
}

func bootDevice() string {
	f, err := os.Open("/proc/cmdline")
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return ""
	}
	cmdline := scanner.Text()

	for _, field := range strings.Fields(cmdline) {
		if !strings.HasPrefix(field, "root=") {
			continue
		}
		val := strings.TrimPrefix(field, "root=")
		if strings.HasPrefix(val, "/dev/") {
			return stripPartitionSuffix(val)
		}
	}
	return ""
}

func stripPartitionSuffix(device string) string {
	if strings.Contains(device, "nvme") || strings.Contains(device, "mmcblk") {
		if idx := strings.LastIndex(device, "p"); idx > 0 {
			return device[:idx]
		}
		return device
	}
	trimmed := strings.TrimRight(device, "0123456789")
	return trimmed
}
