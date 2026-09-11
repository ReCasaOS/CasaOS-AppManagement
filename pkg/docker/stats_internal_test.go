package docker

import (
	"math"
	"testing"

	"github.com/docker/docker/api/types"
)

func sample(total, preTotal, system, preSystem uint64, cpus uint32) *types.StatsJSON {
	s := &types.StatsJSON{}
	s.CPUStats.CPUUsage.TotalUsage = total
	s.CPUStats.SystemUsage = system
	s.CPUStats.OnlineCPUs = cpus
	s.PreCPUStats.CPUUsage.TotalUsage = preTotal
	s.PreCPUStats.SystemUsage = preSystem
	return s
}

func TestCPUPercent(t *testing.T) {
	cases := []struct {
		name  string
		stats *types.StatsJSON
		want  float64
	}{
		{
			// half the machine's CPU time on a 1-CPU host
			"half of one cpu", sample(150, 100, 200, 100, 1), 50,
		},
		{
			// the same share on four CPUs is four times the number, as docker stats reports it
			"a container pinned across four cpus", sample(200, 100, 200, 100, 4), 400,
		},
		{
			"idle", sample(100, 100, 200, 100, 1), 0,
		},
		{
			// the first frame of a stream has no previous sample at all
			"the first frame of a stream", sample(5_000_000, 0, 0, 0, 2), 0,
		},
		{
			// a counter that went backwards is a container that stopped, not -3%
			"usage that went backwards", sample(100, 150, 200, 100, 1), 0,
		},
		{
			"system time that did not move", sample(150, 100, 100, 100, 1), 0,
		},
		{"nil", nil, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CPUPercent(c.stats); math.Abs(got-c.want) > 0.001 {
				t.Fatalf("want %v, got %v", c.want, got)
			}
		})
	}
}

// Older daemons report no OnlineCPUs and only the per-core list; reading neither
// would divide the answer by the number of cores the container was using.
func TestCPUPercentFallsBackToThePerCoreList(t *testing.T) {
	s := sample(200, 100, 200, 100, 0)
	s.CPUStats.CPUUsage.PercpuUsage = []uint64{1, 2, 3, 4}

	if got := CPUPercent(s); math.Abs(got-400) > 0.001 {
		t.Fatalf("four cores in the list is four cores, got %v", got)
	}

	// and neither reported at all is one CPU rather than a division by zero
	bare := sample(150, 100, 200, 100, 0)
	if got := CPUPercent(bare); math.Abs(got-50) > 0.001 {
		t.Fatalf("want 50, got %v", got)
	}
}

// The raw `usage` counts the page cache, so a container that has merely read a
// large file reads as though it were holding it.
func TestMemoryUsageDoesNotCountThePageCache(t *testing.T) {
	v2 := &types.StatsJSON{}
	v2.MemoryStats.Usage = 900
	v2.MemoryStats.Limit = 2000
	v2.MemoryStats.Stats = map[string]uint64{"inactive_file": 400}

	if used, limit := MemoryUsage(v2); used != 500 || limit != 2000 {
		t.Fatalf("cgroup v2: want 500/2000, got %d/%d", used, limit)
	}

	v1 := &types.StatsJSON{}
	v1.MemoryStats.Usage = 900
	v1.MemoryStats.Limit = 2000
	v1.MemoryStats.Stats = map[string]uint64{"total_inactive_file": 100, "cache": 700}

	if used, _ := MemoryUsage(v1); used != 800 {
		t.Fatalf("cgroup v1: want 800, got %d", used)
	}

	// Windows reports neither key, and its usage is already the working set
	windows := &types.StatsJSON{}
	windows.MemoryStats.Usage = 900
	windows.MemoryStats.Limit = 2000

	if used, _ := MemoryUsage(windows); used != 900 {
		t.Fatalf("no cache key: want 900, got %d", used)
	}

	// a cache larger than the usage is nonsense, and must not wrap around uint64
	broken := &types.StatsJSON{}
	broken.MemoryStats.Usage = 100
	broken.MemoryStats.Limit = 2000
	broken.MemoryStats.Stats = map[string]uint64{"inactive_file": 400}

	if used, limit := MemoryUsage(broken); used != 0 || limit != 2000 {
		t.Fatalf("want 0/2000 rather than an underflow, got %d/%d", used, limit)
	}

	if used, limit := MemoryUsage(nil); used != 0 || limit != 0 {
		t.Fatalf("nil: want 0/0, got %d/%d", used, limit)
	}
}
