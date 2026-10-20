//go:build linux

package dockerstats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// clockTicks is the kernel's USER_HZ, the unit of /proc/<pid>/stat times. It is 100 on
// every Linux architecture Docker supports (it is fixed in the userspace ABI, not the
// kernel's internal HZ).
const clockTicks = 100

// selfCPUTicks is the CPU time of this process, in clock ticks.
func selfCPUTicks() float64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return float64(ru.Utime.Nano()+ru.Stime.Nano()) / 1e9 * clockTicks
}

// stackTicks is the CPU of dockerd, containerd and every containerd shim, per process
// name. A stats call goes dockerd -> containerd -> shim -> cgroup files, so counting
// dockerd alone would undercount both strategies. Processes are found by scanning /proc
// comm names, which is independent of cgroup version and of how systemd names the units.
func stackTicks(t *testing.T) map[string]float64 {
	m, err := procCPUTicks("/proc", dockerStackProc)
	if err != nil {
		t.Fatal(err)
	}
	if m["dockerd"] == 0 && len(m) == 0 {
		t.Fatal("no dockerd, containerd or shim process found in /proc")
	}
	return m
}

func sumTicks(m map[string]float64) (n float64) {
	for _, v := range m {
		n += v
	}
	return n
}

// measure runs fn for costWindow and returns the CPU used, as percent of one core. A
// shim that exits during the window (a container removed) can only lower the stack
// figure; the containers live for the whole window.
func measure(t *testing.T, name string, fn func(ctx context.Context)) float64 {
	self0, stack0 := selfCPUTicks(), stackTicks(t)
	ctx, cancel := context.WithTimeout(context.Background(), costWindow)
	defer cancel()
	start := time.Now()
	fn(ctx)
	wall := time.Since(start).Seconds()
	self := selfCPUTicks() - self0
	stack1 := stackTicks(t)
	var stack float64
	for comm, v := range stack1 {
		d := (v - stack0[comm]) / clockTicks / wall * 100
		t.Logf("%s: %-16s %.3f %% of one core", name, comm, d)
		stack += v - stack0[comm]
	}
	t.Logf("%s: %-16s %.3f %% of one core", name, "test process", self/clockTicks/wall*100)
	return (self + stack) / clockTicks / wall * 100
}

func TestCost(t *testing.T) {
	if os.Getenv("STATS_COST") != "1" {
		t.Skip("set STATS_COST=1 (needs Docker and busybox)")
	}
	costCleanup() // leftovers of an earlier killed run
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

	if len(ids) != costContainers {
		t.Fatalf("%d labelled containers running, want %d", len(ids), costContainers)
	}

	// A: a one-shot per container every 5 s, 8 in flight at most, one list per tick.
	a := measure(t, "A", func(ctx context.Context) {
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
	b := measure(t, "B", func(ctx context.Context) {
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
