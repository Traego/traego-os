// Package metrics reads live host CPU, memory and load for the machine the
// controller runs on. It uses gopsutil so the numbers are real and correct on
// Linux (incl. containers) and macOS alike.
package metrics

import (
	"os"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// Sample is a point-in-time view of a machine's resources.
type Sample struct {
	Hostname   string  `json:"hostname"`
	OS         string  `json:"os"`
	Cores      int     `json:"cores"`
	CPUPercent float64 `json:"cpu_percent"`
	Load1      float64 `json:"load1"`
	MemUsedGB   float64 `json:"mem_used_gb"`
	MemTotalGB  float64 `json:"mem_total_gb"`
	DiskUsedGB  float64 `json:"disk_used_gb"`
	DiskTotalGB float64 `json:"disk_total_gb"`
	GPUVRAMGB   float64 `json:"gpu_vram_gb"` // total GPU memory (NVIDIA) or unified memory (Apple Silicon)
	UptimeSec   int64   `json:"uptime_sec"`
}

// Collector produces Samples. CPU percent is measured as a delta between calls
// (gopsutil keeps the prior reading), so create one and Sample() on an interval.
type Collector struct {
	start time.Time
	gpuGB float64
}

// NewCollector returns a Collector and primes the CPU baseline.
func NewCollector() *Collector {
	_, _ = cpu.Percent(0, false) // prime so the first real Sample has a delta
	return &Collector{start: time.Now(), gpuGB: detectGPUVRAMGB()}
}

// Sample reads current host stats. CPU percent is the busy fraction since the
// previous call.
func (c *Collector) Sample() Sample {
	host_, _ := os.Hostname()
	s := Sample{Hostname: host_, OS: runtime.GOOS, Cores: runtime.NumCPU(), GPUVRAMGB: c.gpuGB}

	if pcts, err := cpu.Percent(0, false); err == nil && len(pcts) > 0 {
		s.CPUPercent = round1(pcts[0])
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		s.MemTotalGB = round1(bytesToGB(vm.Total))
		s.MemUsedGB = round1(bytesToGB(vm.Used))
	}
	if du, err := disk.Usage("/"); err == nil {
		s.DiskTotalGB = round1(bytesToGB(du.Total))
		s.DiskUsedGB = round1(bytesToGB(du.Used))
	}
	if l, err := load.Avg(); err == nil {
		s.Load1 = round1(l.Load1)
	}
	if up, err := host.Uptime(); err == nil {
		s.UptimeSec = int64(up)
	} else {
		s.UptimeSec = int64(time.Since(c.start).Seconds())
	}
	return s
}

func bytesToGB(b uint64) float64 { return float64(b) / (1 << 30) }
func round1(f float64) float64   { return float64(int64(f*10+0.5)) / 10 }
