//go:build linux

package dockerstats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	client2 "github.com/docker/docker/client"
	"golang.org/x/sync/errgroup"
)

const (
	costContainers = 30
	costLabel      = "stats-cost=1"
	costWindow     = 60 * time.Second
	costBudget     = 0.5 // percent of one core
)

func costCleanup() {
	out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label="+costLabel).Output()
	if ids := strings.Fields(string(out)); len(ids) > 0 {
		_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
	}
}

// selfCPUTicks is the CPU time of this process, in clock ticks (100 per second).
func selfCPUTicks() float64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	d := time.Duration(ru.Utime.Nano()+ru.Stime.Nano()) * time.Nanosecond
	return d.Seconds() * 100
}

// dockerdCPUTicks reads utime and stime (fields 14 and 15) of dockerd.
func dockerdCPUTicks(t *testing.T) float64 {
	out, err := exec.Command("pidof", "dockerd").Output()
	if err != nil {
		t.Fatalf("dockerd pid: %v", err)
	}
	pid := strings.Fields(string(out))[0]
	raw, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	f := strings.Fields(s[strings.LastIndex(s, ")")+2:]) // field 3 onwards
	u, _ := strconv.ParseFloat(f[11], 64)                // field 14
	k, _ := strconv.ParseFloat(f[12], 64)                // field 15
	return u + k
}

// measure runs fn for costWindow and returns the CPU used, as percent of one core.
func measure(t *testing.T, fn func(ctx context.Context)) float64 {
	self0, d0 := selfCPUTicks(), dockerdCPUTicks(t)
	ctx, cancel := context.WithTimeout(context.Background(), costWindow)
	defer cancel()
	start := time.Now()
	fn(ctx)
	wall := time.Since(start).Seconds()
	ticks := (selfCPUTicks() - self0) + (dockerdCPUTicks(t) - d0)
	return ticks / 100 / wall * 100
}

func TestCost(t *testing.T) {
	if os.Getenv("STATS_COST") != "1" {
		t.Skip("set STATS_COST=1 (needs Docker and busybox)")
	}
	defer costCleanup()
	t.Cleanup(costCleanup)

	for i := 0; i < costContainers; i++ {
		name := fmt.Sprintf("statscost-%d", i)
		if out, err := exec.Command("docker", "run", "-d", "--label", costLabel, "--name", name, "busybox", "sleep", "3600").CombinedOutput(); err != nil {
			t.Fatalf("start %s: %v: %s", name, err, out)
		}
	}
	time.Sleep(10 * time.Second)

	cli, err := client2.NewClientWithOpts(client2.FromEnv, client2.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	list, err := cli.ContainerList(context.Background(), container.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range list {
		if c.Labels["stats-cost"] == "1" {
			ids = append(ids, c.ID)
		}
	}

	// A: a one-shot per container every 5 s, 8 in flight at most, one list per tick.
	a := measure(t, func(ctx context.Context) {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			_, _ = cli.ContainerList(ctx, container.ListOptions{All: true})
			var g errgroup.Group
			g.SetLimit(8)
			for _, id := range ids {
				g.Go(func() error {
					r, err := cli.ContainerStatsOneShot(ctx, id)
					if err != nil {
						return nil
					}
					defer r.Body.Close()
					_, _ = io.Copy(io.Discard, r.Body)
					return nil
				})
			}
			_ = g.Wait()
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	})

	// B: one streaming call per container, every frame decoded.
	b := measure(t, func(ctx context.Context) {
		var wg sync.WaitGroup
		for _, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, err := cli.ContainerStats(ctx, id, true)
				if err != nil {
					return
				}
				defer r.Body.Close()
				dec := json.NewDecoder(r.Body)
				for {
					var s container.StatsResponse
					if dec.Decode(&s) != nil {
						return
					}
				}
			}()
		}
		wg.Wait()
	})

	t.Logf("A poll: %.3f %% of one core; B streams: %.3f %% of one core (%d containers)", a, b, len(ids))
	if a >= costBudget && b >= costBudget {
		t.Fatalf("both strategies are over %.1f %% (A %.3f, B %.3f)", costBudget, a, b)
	}
}
