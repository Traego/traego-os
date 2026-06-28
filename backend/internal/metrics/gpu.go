package metrics

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/mem"
)

// detectGPUVRAMGB returns total GPU memory in GB. On NVIDIA hosts it queries
// nvidia-smi (the real GPU boxes); on Apple Silicon the GPU shares unified
// system memory, so it reports that. Returns 0 when no GPU memory is
// determinable. Detected once at startup since VRAM is static.
func detectGPUVRAMGB() float64 {
	if out, err := exec.Command("nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits").Output(); err == nil {
		if gb := parseNvidiaVRAMGB(out); gb > 0 {
			return gb
		}
	}
	if runtime.GOOS == "darwin" {
		// Apple Silicon: unified memory is shared between CPU and GPU.
		if vm, err := mem.VirtualMemory(); err == nil {
			return round1(float64(vm.Total) / (1 << 30))
		}
	}
	return 0
}

// parseNvidiaVRAMGB sums nvidia-smi "memory.total" output (one MB value per GPU
// line) into total GB.
func parseNvidiaVRAMGB(out []byte) float64 {
	var totalMB float64
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if mb, err := strconv.ParseFloat(line, 64); err == nil {
			totalMB += mb
		}
	}
	return round1(totalMB / 1024)
}
