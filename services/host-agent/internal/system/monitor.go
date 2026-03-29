package system

import (
	"context"
	"fmt"
	"math"
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

// StorageStats represents detailed storage information
type StorageStats struct {
	Used       uint64 `json:"used"`
	Total      uint64 `json:"total"`
	Free       uint64 `json:"free"`
	Percentage int    `json:"percentage"`
	Path       string `json:"path"`
}

// GetStorageStats returns detailed disk storage information
func GetStorageStats() (*StorageStats, error) {
	diskStats, err := disk.Usage("/")
	if err != nil {
		return nil, fmt.Errorf("failed to get disk usage: %w", err)
	}

	return &StorageStats{
		Used:       diskStats.Used,
		Total:      diskStats.Total,
		Free:       diskStats.Free,
		Percentage: int(math.Round(diskStats.UsedPercent)),
		Path:       "/",
	}, nil
}

