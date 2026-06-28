package metrics

import (
	"testing"
)

func TestParseNvidiaVRAMGB(t *testing.T) {
	// single GPU: 24564 MB ~= 24 GB
	if got := parseNvidiaVRAMGB([]byte("24564\n")); got < 23.9 || got > 24.1 {
		t.Fatalf("single GPU = %v, want ~24", got)
	}
	// two GPUs sum
	if got := parseNvidiaVRAMGB([]byte("24564\n24564\n")); got < 47.9 || got > 48.1 {
		t.Fatalf("two GPUs = %v, want ~48", got)
	}
	// empty / junk -> 0
	if got := parseNvidiaVRAMGB([]byte("")); got != 0 {
		t.Fatalf("empty = %v, want 0", got)
	}
	if got := parseNvidiaVRAMGB([]byte("\n  \n")); got != 0 {
		t.Fatalf("blank lines = %v, want 0", got)
	}
}

func TestRound1(t *testing.T) {
	cases := map[float64]float64{1.24: 1.2, 1.25: 1.3, 0: 0, 99.99: 100}
	for in, want := range cases {
		if got := round1(in); got != want {
			t.Fatalf("round1(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestBytesToGB(t *testing.T) {
	if got := round1(bytesToGB(24 * (1 << 30))); got != 24 {
		t.Fatalf("bytesToGB(24GiB) = %v, want 24", got)
	}
}

// Sample must return real, populated values on the host it runs on (Linux CI or
// a dev Mac) — never panics, always reports cores, total memory and a hostname.
func TestSampleIsPopulated(t *testing.T) {
	c := NewCollector()
	s := c.Sample()
	if s.Cores < 1 {
		t.Fatalf("cores = %d", s.Cores)
	}
	if s.MemTotalGB <= 0 {
		t.Fatalf("mem_total = %v, expected real memory", s.MemTotalGB)
	}
	if s.DiskTotalGB <= 0 {
		t.Fatalf("disk_total = %v, expected real disk", s.DiskTotalGB)
	}
	if s.Hostname == "" {
		t.Fatal("hostname empty")
	}
	if s.OS == "" {
		t.Fatal("os empty")
	}
	if s.UptimeSec < 0 {
		t.Fatalf("negative uptime: %d", s.UptimeSec)
	}
	// CPU% is a non-negative percentage
	if s.CPUPercent < 0 || s.CPUPercent > 100 {
		t.Fatalf("cpu%% out of range: %v", s.CPUPercent)
	}
}

// Two consecutive samples should both be sane (CPU% delta computed by gopsutil).
func TestSampleTwiceStable(t *testing.T) {
	c := NewCollector()
	_ = c.Sample()
	s := c.Sample()
	if s.MemTotalGB <= 0 || s.Cores < 1 {
		t.Fatalf("second sample incomplete: %+v", s)
	}
}
