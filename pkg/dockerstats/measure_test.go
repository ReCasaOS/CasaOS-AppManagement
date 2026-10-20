package dockerstats

// The two fixtures are HAND-WRITTEN from the field names of Docker 28 stats
// answers (no Docker daemon was available to capture them):
//   - stats-cgroup2.json: memory_stats.stats has anon, file, inactive_file (no
//     swap); blkio ops are lower case ("read"/"write").
//   - stats-cgroup1.json: memory_stats.stats has rss, cache, swap,
//     total_inactive_file; blkio ops are capitalised ("Read"/"Write"/"Total").
// Measure accepts both spellings. Whether a real cgroup v2 daemon fills
// usage_in_usermode/usage_in_kernelmode is not verified here.

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/docker/docker/api/types/container"
	"gotest.tools/v3/assert"
)

func load(t *testing.T, name string) *container.StatsResponse {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	assert.NilError(t, err)
	var s container.StatsResponse
	assert.NilError(t, json.Unmarshal(b, &s))
	return &s
}

const gap = 15 * time.Second

// step measures a first sample, then a second one 5 s later. base shapes both
// stats, change only the second.
func step(t *testing.T, fixture string, a Allocation, base, change func(s *container.StatsResponse)) (first, second stats.Sample) {
	t.Helper()
	p := load(t, fixture)
	p.Read = t0
	if base != nil {
		base(p)
	}
	f, snap := Measure(nil, p, t0, a, gap)
	c := load(t, fixture)
	c.Read = t0.Add(5 * time.Second)
	if base != nil {
		base(c)
	}
	if change != nil {
		change(c)
	}
	s, _ := Measure(&snap, c, c.Read, a, gap)
	return f, s
}

func has(m stats.Sample, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}

func lacks(t *testing.T, m stats.Sample, keys ...string) {
	t.Helper()
	for _, k := range keys {
		_, ok := m[k]
		assert.Assert(t, !ok, "%s present in %v", k, m)
	}
}

func TestCPUIsPercentOfOneCoreFromTheCounterDeltas(t *testing.T) {
	_, s := step(t, "stats-cgroup2.json", Allocation{}, nil, func(c *container.StatsResponse) {
		c.CPUStats.CPUUsage.UsageInUsermode += 1_000_000_000
		c.CPUStats.CPUUsage.UsageInKernelmode += 500_000_000
		c.CPUStats.ThrottlingData.ThrottledTime += 1_000_000_000
	})
	assert.Equal(t, s["cpu/user"], 20.0)
	assert.Equal(t, s["cpu/system"], 10.0)
	assert.Equal(t, s["cpu/throttled"], 20.0)
}

func TestTheFirstSampleHasGaugesButNoRates(t *testing.T) {
	f, _ := step(t, "stats-cgroup2.json", Allocation{}, nil, nil)
	assert.Assert(t, has(f, "memory/rss", "pids/count", "alert/mem_high"), "%v", f)
	lacks(t, f, "cpu/user", "cpu/system", "network/rx:eth0", "disk/read", "alert/cpu_high")
}

func TestAPreviousSampleOlderThanMaxGapIsIgnored(t *testing.T) {
	p := load(t, "stats-cgroup2.json")
	p.Read = t0
	_, snap := Measure(nil, p, t0, Allocation{}, gap)
	c := load(t, "stats-cgroup2.json")
	c.Read = t0.Add(60 * time.Second)
	c.CPUStats.CPUUsage.UsageInUsermode += 1_000_000_000
	s, _ := Measure(&snap, c, c.Read, Allocation{}, gap)
	assert.Assert(t, has(s, "memory/rss", "pids/count"))
	lacks(t, s, "cpu/user", "network/rx:eth0", "disk/read", "alert/cpu_high")
}

func TestACounterThatWentBackwardsGivesNoRate(t *testing.T) {
	_, s := step(t, "stats-cgroup2.json", Allocation{}, nil, func(c *container.StatsResponse) {
		c.CPUStats.CPUUsage.UsageInUsermode -= 1
		c.CPUStats.CPUUsage.UsageInKernelmode += 500_000_000
	})
	lacks(t, s, "cpu/user")
	assert.Assert(t, has(s, "cpu/system", "network/rx:eth0"))

	two := func(c *container.StatsResponse) {
		c.Networks["eth1"] = container.NetworkStats{RxBytes: 100, TxBytes: 100}
	}
	_, s = step(t, "stats-cgroup2.json", Allocation{}, two, func(c *container.StatsResponse) {
		n := c.Networks["eth0"]
		n.RxBytes = 1
		c.Networks["eth0"] = n
	})
	lacks(t, s, "network/rx:eth0")
	assert.Assert(t, has(s, "network/tx:eth0", "network/dropped:eth0", "network/rx:eth1"))
}

func TestMemoryKeysOfBothCgroupVersions(t *testing.T) {
	f, _ := step(t, "stats-cgroup2.json", Allocation{}, nil, nil)
	assert.Equal(t, f["memory/rss"], 12582912.0)
	assert.Equal(t, f["memory/cache"], 16777216.0)
	lacks(t, f, "memory/swap")

	f, _ = step(t, "stats-cgroup1.json", Allocation{}, nil, nil)
	assert.Equal(t, f["memory/rss"], 12582912.0)
	assert.Equal(t, f["memory/cache"], 16777216.0)
	assert.Equal(t, f["memory/swap"], 2097152.0)
}

func TestMemoryLimitSeriesOnlyWhenDeclared(t *testing.T) {
	f, _ := step(t, "stats-cgroup2.json", Allocation{MemLimit: 512 << 20}, nil, nil)
	assert.Equal(t, f["memory/limit"], 536870912.0)
	f, _ = step(t, "stats-cgroup2.json", Allocation{}, nil, nil)
	lacks(t, f, "memory/limit")
}

func TestNetworkIsOneSeriesPerInterface(t *testing.T) {
	two := func(c *container.StatsResponse) { c.Networks["eth1"] = container.NetworkStats{} }
	_, s := step(t, "stats-cgroup2.json", Allocation{}, two, func(c *container.StatsResponse) {
		for _, n := range []string{"eth0", "eth1"} {
			v := c.Networks[n]
			v.RxBytes += 5000
			v.TxBytes += 10000
			v.RxDropped += 5
			v.TxDropped += 20
			c.Networks[n] = v
		}
	})
	for _, n := range []string{"eth0", "eth1"} {
		assert.Equal(t, s["network/rx:"+n], 1000.0)
		assert.Equal(t, s["network/tx:"+n], 2000.0)
		assert.Equal(t, s["network/dropped:"+n], 5.0)
	}
}

func TestDiskSumsDevicesAndAcceptsBothCases(t *testing.T) {
	base := func(c *container.StatsResponse) {
		c.BlkioStats.IoServiceBytesRecursive = []container.BlkioStatEntry{
			{Major: 8, Minor: 0, Op: "read"}, {Major: 8, Minor: 16, Op: "Read"},
			{Major: 8, Minor: 0, Op: "Write"}, {Major: 8, Minor: 0, Op: "Total"},
		}
		c.BlkioStats.IoServicedRecursive = []container.BlkioStatEntry{
			{Major: 8, Minor: 0, Op: "Read"}, {Major: 8, Minor: 0, Op: "write"}, {Major: 8, Minor: 0, Op: "Total"},
		}
	}
	_, s := step(t, "stats-cgroup2.json", Allocation{}, base, func(c *container.StatsResponse) {
		b := c.BlkioStats.IoServiceBytesRecursive
		b[0].Value, b[1].Value, b[2].Value, b[3].Value = 3000, 2000, 10000, 999999
		i := c.BlkioStats.IoServicedRecursive
		i[0].Value, i[1].Value, i[2].Value = 50, 100, 999
	})
	assert.Equal(t, s["disk/read"], 1000.0)
	assert.Equal(t, s["disk/write"], 2000.0)
	assert.Equal(t, s["disk/iops_read"], 10.0)
	assert.Equal(t, s["disk/iops_write"], 20.0)
}

func TestEmptySectionsGiveNoKeys(t *testing.T) {
	empty := &container.StatsResponse{Read: t0}
	f, snap := Measure(nil, empty, t0, Allocation{}, gap)
	next := &container.StatsResponse{Read: t0.Add(5 * time.Second)}
	s, _ := Measure(&snap, next, next.Read, Allocation{}, gap)
	for k := range f {
		t.Errorf("unexpected key %s", k)
	}
	for k := range s {
		t.Errorf("unexpected key %s", k)
	}
}

func TestTheAlertSeriesUseTheEffectiveAllocation(t *testing.T) {
	base := func(c *container.StatsResponse) { c.CPUStats.OnlineCPUs = 8 }
	use := func(c *container.StatsResponse) { c.CPUStats.CPUUsage.TotalUsage += 2_500_000_000 }
	_, s := step(t, "stats-cgroup2.json", Allocation{CPULimit: 0.5}, base, use)
	assert.Equal(t, s["alert/cpu_high"], 100.0)
	_, s = step(t, "stats-cgroup2.json", Allocation{}, base, use)
	assert.Equal(t, s["alert/cpu_high"], 6.25)

	mem := func(c *container.StatsResponse) {
		c.MemoryStats.Usage = 450 << 20
		delete(c.MemoryStats.Stats, "inactive_file")
		c.MemoryStats.Limit = 4 << 30
	}
	f, _ := step(t, "stats-cgroup2.json", Allocation{MemLimit: 500 << 20}, mem, nil)
	assert.Equal(t, f["alert/mem_high"], 90.0)
	f, _ = step(t, "stats-cgroup2.json", Allocation{}, mem, nil)
	assert.Equal(t, f["alert/mem_high"], float64(450<<20)/float64(4<<30)*100)
}

func TestNoIowaitAndNoFdsAreEverProduced(t *testing.T) {
	use := func(c *container.StatsResponse) {
		c.CPUStats.CPUUsage.UsageInUsermode += 1_000_000_000
		c.CPUStats.CPUUsage.TotalUsage += 1_000_000_000
	}
	for _, fx := range []string{"stats-cgroup2.json", "stats-cgroup1.json"} {
		f, s := step(t, fx, Allocation{}, nil, use)
		lacks(t, f, "cpu/iowait", "pids/fds", "cpu/total")
		lacks(t, s, "cpu/iowait", "pids/fds", "cpu/total")
	}
}

func TestAllocationOf(t *testing.T) {
	cases := []struct {
		h    *container.HostConfig
		want Allocation
	}{
		{&container.HostConfig{Resources: container.Resources{NanoCPUs: 1_500_000_000}}, Allocation{CPULimit: 1.5}},
		{&container.HostConfig{Resources: container.Resources{CPUQuota: 50000, CPUPeriod: 100000}}, Allocation{CPULimit: 0.5}},
		{&container.HostConfig{Resources: container.Resources{CPUQuota: 50000}}, Allocation{CPULimit: 0.5}},
		{&container.HostConfig{Resources: container.Resources{Memory: 512 << 20}}, Allocation{MemLimit: 512 << 20}},
		{&container.HostConfig{}, Allocation{}},
		{nil, Allocation{}},
	}
	for i, c := range cases {
		assert.Equal(t, AllocationOf(c.h), c.want, "case %d", i)
	}
}

func TestCPUCoresFallbacks(t *testing.T) {
	s := &container.StatsResponse{}
	assert.Equal(t, Allocation{}.CPUCores(s), 1.0)
	s.CPUStats.CPUUsage.PercpuUsage = []uint64{1, 2}
	assert.Equal(t, Allocation{}.CPUCores(s), 2.0)
	s.CPUStats.OnlineCPUs = 4
	assert.Equal(t, Allocation{}.CPUCores(s), 4.0)
	assert.Equal(t, Allocation{CPULimit: 1.5}.CPUCores(s), 1.5)
}

func TestTwoReadingsOfTheSameInstantGiveNoRate(t *testing.T) {
	p := load(t, "stats-cgroup2.json")
	_, snap := Measure(nil, p, t0, Allocation{}, gap)
	c := load(t, "stats-cgroup2.json")
	c.CPUStats.CPUUsage.UsageInUsermode += 1_000_000_000
	s, _ := Measure(&snap, c, p.Read, Allocation{}, gap)
	lacks(t, s, "cpu/user", "network/rx:eth0", "alert/cpu_high")
}
