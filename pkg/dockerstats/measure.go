// Package dockerstats turns what Docker reports about a container into the
// series the stats store keeps. This file is the pure part: no clock, no I/O.
package dockerstats

import (
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/docker/docker/api/types/container"
)

// MaxGapFactor: a previous snapshot older than this many sampling intervals is
// not a baseline (Docker or the service was away).
const MaxGapFactor = 3

// Allocation is what the container was declared to be allowed to use.
type Allocation struct {
	CPULimit float64 // cores; 0 = none declared
	MemLimit uint64  // bytes; 0 = none declared
}

// AllocationOf reads the declared limits of a container.
func AllocationOf(h *container.HostConfig) Allocation {
	var a Allocation
	if h == nil {
		return a
	}
	switch {
	case h.NanoCPUs > 0:
		a.CPULimit = float64(h.NanoCPUs) / 1e9
	case h.CPUQuota > 0:
		period := h.CPUPeriod
		if period <= 0 {
			period = 100000
		}
		a.CPULimit = float64(h.CPUQuota) / float64(period)
	}
	if h.Memory > 0 {
		a.MemLimit = uint64(h.Memory)
	}
	return a
}

// CPUCores is the number of cores the container can use: its limit, else the
// machine's online cores as Docker reports them.
func (a Allocation) CPUCores(s *container.StatsResponse) float64 {
	switch {
	case a.CPULimit > 0:
		return a.CPULimit
	case s.CPUStats.OnlineCPUs > 0:
		return float64(s.CPUStats.OnlineCPUs)
	case len(s.CPUStats.CPUUsage.PercpuUsage) > 0:
		return float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	return 1
}

// Snapshot is what a sample leaves behind for the next one: when it was read
// and the cumulative counters rates are computed from.
type Snapshot struct {
	At       time.Time
	counters map[string]uint64
}

// Measure turns one Docker answer into a Sample. A rate needs prev to be at
// most maxGap older, and each counter is judged alone: one that went backwards
// (container recreated) drops its own series, not the others.
func Measure(prev *Snapshot, s *container.StatsResponse, now time.Time, a Allocation, maxGap time.Duration) (stats.Sample, Snapshot) {
	at := s.Read
	if at.IsZero() {
		at = now
	}
	cur := map[string]uint64{}
	smp := stats.Sample{}

	// counters
	if s.CPUStats.CPUUsage.TotalUsage > 0 {
		cpu := s.CPUStats.CPUUsage
		cur["total"] = cpu.TotalUsage
		cur["throttled"] = s.CPUStats.ThrottlingData.ThrottledTime
		if cpu.UsageInUsermode+cpu.UsageInKernelmode > 0 {
			cur["user"], cur["system"] = cpu.UsageInUsermode, cpu.UsageInKernelmode
		}
	}
	for name, n := range s.Networks {
		cur["rx:"+name], cur["tx:"+name] = n.RxBytes, n.TxBytes
		cur["rxd:"+name], cur["txd:"+name] = n.RxDropped, n.TxDropped
	}
	sumOps(cur, s.BlkioStats.IoServiceBytesRecursive, "read", "write")
	sumOps(cur, s.BlkioStats.IoServicedRecursive, "iops_read", "iops_write")

	// gauges
	if v, ok := firstOf(s.MemoryStats.Stats, "anon", "rss"); ok {
		smp["memory/rss"] = float64(v)
	}
	if v, ok := firstOf(s.MemoryStats.Stats, "file", "cache"); ok {
		smp["memory/cache"] = float64(v)
	}
	if v, ok := s.MemoryStats.Stats["swap"]; ok {
		smp["memory/swap"] = float64(v)
	}
	if a.MemLimit > 0 {
		smp["memory/limit"] = float64(a.MemLimit)
	}
	if s.PidsStats.Current > 0 {
		smp["pids/count"] = float64(s.PidsStats.Current)
	}
	if used, limit := docker.MemoryUsage(s); s.MemoryStats.Usage > 0 {
		if a.MemLimit > 0 {
			limit = a.MemLimit
		}
		if limit > 0 {
			smp["alert/mem_high"] = float64(used) / float64(limit) * 100
		}
	}

	snap := Snapshot{At: at, counters: cur}
	if prev == nil {
		return smp, snap
	}
	dt := at.Sub(prev.At)
	if dt <= 0 || dt > maxGap {
		return smp, snap
	}
	delta := func(key string) (float64, bool) {
		p, ok := prev.counters[key]
		c, ok2 := cur[key]
		if !ok || !ok2 || c < p {
			return 0, false
		}
		return float64(c - p), true
	}
	percent := func(series, key string) {
		if d, ok := delta(key); ok {
			smp[series] = d * 100 / float64(dt.Nanoseconds())
		}
	}
	perSecond := func(series, key string) {
		if d, ok := delta(key); ok {
			smp[series] = d / dt.Seconds()
		}
	}

	percent("cpu/user", "user")
	percent("cpu/system", "system")
	percent("cpu/throttled", "throttled")
	if d, ok := delta("total"); ok {
		smp["alert/cpu_high"] = d * 100 / float64(dt.Nanoseconds()) / a.CPUCores(s)
	}
	for name := range s.Networks {
		perSecond("network/rx:"+name, "rx:"+name)
		perSecond("network/tx:"+name, "tx:"+name)
		if r, ok := delta("rxd:" + name); ok {
			if t, ok := delta("txd:" + name); ok {
				smp["network/dropped:"+name] = (r + t) / dt.Seconds()
			}
		}
	}
	perSecond("disk/read", "read")
	perSecond("disk/write", "write")
	perSecond("disk/iops_read", "iops_read")
	perSecond("disk/iops_write", "iops_write")
	return smp, snap
}

func firstOf(m map[string]uint64, keys ...string) (uint64, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v, true
		}
	}
	return 0, false
}

// sumOps adds up the Read and Write entries of every device into counters
// named readKey / writeKey. Docker spells the operation "read" or "Read" depending on the
// cgroup version; "Total" and the rest are ignored. A counter exists only if at
// least one entry carries it.
func sumOps(cur map[string]uint64, entries []container.BlkioStatEntry, readKey, writeKey string) {
	var r, w uint64
	var seenR, seenW bool
	for _, e := range entries {
		switch strings.ToLower(e.Op) {
		case "read":
			r, seenR = r+e.Value, true
		case "write":
			w, seenW = w+e.Value, true
		}
	}
	if seenR {
		cur[readKey] = r
	}
	if seenW {
		cur[writeKey] = w
	}
}
