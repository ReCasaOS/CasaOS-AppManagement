package docker

import (
	"context"
	"encoding/json"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

// CPUPercent is how busy a container has been BETWEEN two samples, which is why
// a single sample cannot answer it: CPU usage is a counter that only goes up, so
// a lone reading says how much CPU the container has used since it started, not
// how much it is using now.
//
// Docker ships the previous sample inside the current one, so the arithmetic is
// the same as `docker stats`: the container's own delta over the machine's, times
// the number of CPUs it could have been running on. A container pinned at 100% of
// two cores reads 200, like the CLI, rather than 100.
//
// Zero when the deltas make no sense -- the first frame of a stream carries an
// empty previous sample, and a container that stopped between samples goes
// backwards.
func CPUPercent(s *types.StatsJSON) float64 {
	if s == nil {
		return 0
	}

	usage := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	system := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	if usage <= 0 || system <= 0 {
		return 0
	}

	cpus := float64(s.CPUStats.OnlineCPUs)
	if cpus == 0 {
		// Older daemons report no count and only the per-core list
		cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpus == 0 {
		cpus = 1
	}

	return usage / system * cpus * 100
}

// MemoryUsage is what the container actually holds, and its ceiling.
//
// Docker's raw `usage` counts the page cache, which makes a container that has
// merely read a large file look like it is holding it. Both the CLI and every
// dashboard worth trusting subtract that: `inactive_file` under cgroup v2, and
// `total_inactive_file` under v1. Neither key is present on Windows, where the
// raw value is already the working set.
//
// The limit is the host's whole memory when the container declares none, which
// is Docker's own answer and is what makes the percentage meaningful.
func MemoryUsage(s *types.StatsJSON) (used uint64, limit uint64) {
	if s == nil {
		return 0, 0
	}

	used = s.MemoryStats.Usage
	for _, key := range []string{"inactive_file", "total_inactive_file"} {
		if cache, ok := s.MemoryStats.Stats[key]; ok {
			if cache > used {
				return 0, s.MemoryStats.Limit
			}
			used -= cache
			break
		}
	}

	return used, s.MemoryStats.Limit
}

// SampleContainerStats reads a container's usage.
//
// It takes TWO frames off the stream rather than asking for one, because one is
// not enough: the one-shot endpoint returns an empty previous sample, so CPU
// cannot be computed from it at all. The daemon emits roughly once a second, so
// this call lasts about that long and the caller's context is what bounds it.
func SampleContainerStats(ctx context.Context, cli *client.Client, id string) (*types.StatsJSON, error) {
	response, err := cli.ContainerStats(ctx, id, true)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	decoder := json.NewDecoder(response.Body)

	var stats types.StatsJSON
	for frames := 0; frames < 2; frames++ {
		if err := decoder.Decode(&stats); err != nil {
			if frames > 0 && err == io.EOF {
				// The container stopped after its first frame: what we have is real,
				// even though the CPU delta will come out as zero.
				break
			}
			return nil, err
		}
	}

	return &stats, nil
}
