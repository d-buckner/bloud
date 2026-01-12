package system

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

// Stats represents system resource usage (percentages as integers)
type Stats struct {
	CPU    int `json:"cpu"`
	Memory int `json:"memory"`
	Disk   int `json:"disk"`
}

// statsCache holds cached system stats updated in background
var (
	statsCache     *Stats
	statsCacheMu   sync.RWMutex
	statsOnce      sync.Once
)

// StartStatsCollector starts background stats collection
// Call this once at startup
func StartStatsCollector(ctx context.Context) {
	statsOnce.Do(func() {
		// Initialize with zeros
		statsCacheMu.Lock()
		statsCache = &Stats{}
		statsCacheMu.Unlock()

		// Start background collector
		go collectStatsLoop(ctx)
	})
}

// collectStatsLoop runs in background and updates cached stats
func collectStatsLoop(ctx context.Context) {
	// Do initial collection immediately
	collectStats()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collectStats()
		}
	}
}

// collectStats updates the cached stats
func collectStats() {
	stats := &Stats{}

	// Get CPU usage (1 second sample - this blocks but runs in background)
	cpuPercent, err := cpu.Percent(time.Second, false)
	if err == nil && len(cpuPercent) > 0 {
		stats.CPU = int(math.Round(cpuPercent[0]))
	}

	// Get memory usage
	memStats, err := mem.VirtualMemory()
	if err == nil {
		stats.Memory = int(math.Round(memStats.UsedPercent))
	}

	// Get disk usage for root partition
	diskStats, err := disk.Usage("/")
	if err == nil {
		stats.Disk = int(math.Round(diskStats.UsedPercent))
	}

	statsCacheMu.Lock()
	statsCache = stats
	statsCacheMu.Unlock()
}

// GetStats returns cached system resource usage (instant response)
func GetStats() (*Stats, error) {
	statsCacheMu.RLock()
	defer statsCacheMu.RUnlock()

	if statsCache == nil {
		return &Stats{}, nil
	}

	// Return a copy
	return &Stats{
		CPU:    statsCache.CPU,
		Memory: statsCache.Memory,
		Disk:   statsCache.Disk,
	}, nil
}

// Generation represents a NixOS system generation
type Generation struct {
	Number  int    `json:"number"`
	Date    string `json:"date"`
	Current bool   `json:"current"`
	NixosVersion string `json:"nixosVersion,omitempty"`
}

// ListGenerations returns output from nixos-rebuild list-generations
func ListGenerations() (string, error) {
	cmd := exec.Command("nixos-rebuild", "list-generations")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to run nixos-rebuild list-generations: %w", err)
	}
	return string(output), nil
}

// ParseGenerations parses the output from nixos-rebuild list-generations
// Example line: "  1   2024-01-01 12:00:00"
// Example current: "  5   2024-01-05 15:30:00   (current)"
func ParseGenerations(output string) ([]Generation, error) {
	var generations []Generation

	// Regex to match generation lines
	// Example: "  5   2024-01-05 15:30:00   (current)"
	re := regexp.MustCompile(`\s*(\d+)\s+([0-9]{4}-[0-9]{2}-[0-9]{2}\s+[0-9]{2}:[0-9]{2}:[0-9]{2})(\s+\(current\))?`)

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		matches := re.FindStringSubmatch(line)
		if len(matches) >= 3 {
			number, err := strconv.Atoi(matches[1])
			if err != nil {
				continue
			}

			gen := Generation{
				Number:  number,
				Date:    matches[2],
				Current: len(matches) > 3 && matches[3] != "",
			}
			generations = append(generations, gen)
		}
	}

	// Sort by date descending (most recent first)
	sort.Slice(generations, func(i, j int) bool {
		return generations[i].Date > generations[j].Date
	})

	return generations, nil
}
