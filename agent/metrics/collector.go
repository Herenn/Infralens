// Package metrics provides host resource monitoring (CPU, RAM) using gopsutil.
package metrics

import (
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
)

// HostMetrics contains CPU and memory usage statistics.
type HostMetrics struct {
	NodeName   string    `json:"node_name"`
	Timestamp  time.Time `json:"timestamp"`
	CPUPercent float64   `json:"cpu_percent"` // 0-100
	MemPercent float64   `json:"mem_percent"` // 0-100
	MemUsed    uint64    `json:"mem_used"`    // bytes
	MemTotal   uint64    `json:"mem_total"`   // bytes
}

// Collector gathers host metrics periodically.
type Collector struct {
	nodeName string
}

// NewCollector creates a new metrics collector.
func NewCollector(nodeName string) *Collector {
	return &Collector{
		nodeName: nodeName,
	}
}

// Collect gathers current CPU and memory metrics.
// CPU usage is averaged across all cores.
func (c *Collector) Collect() (*HostMetrics, error) {
	// Get CPU usage (0 interval = since last call, false = total across all CPUs)
	// Using a small interval to get current usage
	cpuPercents, err := cpu.Percent(100*time.Millisecond, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get CPU usage: %w", err)
	}

	var cpuPercent float64
	if len(cpuPercents) > 0 {
		cpuPercent = cpuPercents[0]
	}

	// Get memory usage
	memInfo, err := mem.VirtualMemory()
	if err != nil {
		return nil, fmt.Errorf("failed to get memory usage: %w", err)
	}

	return &HostMetrics{
		NodeName:   c.nodeName,
		Timestamp:  time.Now(),
		CPUPercent: cpuPercent,
		MemPercent: memInfo.UsedPercent,
		MemUsed:    memInfo.Used,
		MemTotal:   memInfo.Total,
	}, nil
}

// GetHostMetrics is a convenience function to get metrics without creating a collector.
func GetHostMetrics(nodeName string) (*HostMetrics, error) {
	c := NewCollector(nodeName)
	return c.Collect()
}
