//go:build linux

package dockerstats

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"gotest.tools/v3/assert"
)

const (
	proofLabel   = "stats-proof=1"
	proofProject = "statsproof"
	proofRun     = 150 * time.Second
	proofLimited = "statsproof-limited"
	proofPlain   = "statsproof-plain"
	proofApp     = "app:" + proofProject + ":web:1"
	limitedID    = "container:" + proofLimited
	plainID      = "container:" + proofPlain
	mem64M       = 64 << 20
)

// proofCleanup removes every container and network of the proof, also those a
// killed earlier run left behind.
func proofCleanup() {
	_ = exec.Command("docker", "compose", "-p", proofProject, "down", "--remove-orphans", "-t", "1").Run()
	out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label="+proofLabel).Output()
	if ids := strings.Fields(string(out)); len(ids) > 0 {
		_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
	}
}

func dockerRun(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// eventually polls ok every half second until it holds or d has passed.
func eventually(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(d); !ok(); {
		if time.Now().After(end) {
			t.Fatalf("after %v: %s", d, what)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func rowOf(s *Sampler, id string) (ItemInfo, bool) {
	items, err := s.Items()
	if err != nil {
		return ItemInfo{}, false
	}
	for _, it := range items {
		if it.ID == id {
			return it, true
		}
	}
	return ItemInfo{}, false
}

func series(t *testing.T, s *Sampler, id string, r stats.Range) SeriesView {
	t.Helper()
	v, err := s.Series(id, r)
	assert.NilError(t, err, id)
	return v
}

func TestProof(t *testing.T) {
	if os.Getenv("STATS_PROOF") != "1" {
		t.Skip("set STATS_PROOF=1 (needs Docker, docker compose and busybox)")
	}

	t.Run("DockerAway", func(t *testing.T) {
		t.Setenv("DOCKER_HOST", "unix:///nonexistent")
		src, err := NewDockerSource()
		assert.NilError(t, err)
		defer src.Close()
		s, err := New(Config{Dir: t.TempDir(), Source: src, Clock: stats.SystemClock{}, Interval: time.Second})
		assert.NilError(t, err)
		defer s.Close()
		s.Tick(context.Background())
		_, err = s.Items()
		assert.ErrorIs(t, err, ErrUnavailable)
	})

	t.Run("RealDocker", func(t *testing.T) {
		proofCleanup() // leftovers of an earlier killed run
		defer proofCleanup()
		t.Cleanup(proofCleanup)

		file, err := writeCompose(t.TempDir())
		assert.NilError(t, err)
		dockerRun(t, "compose", "-p", proofProject, "-f", file, "up", "-d")
		dockerRun(t, "run", "-d", "--label", proofLabel, "--name", proofLimited, "--memory", "64m",
			"--health-cmd", "true", "--health-interval", "1s", "busybox", "sleep", "3600")
		dockerRun(t, "run", "-d", "--label", proofLabel, "--name", proofPlain, "busybox", "sleep", "3600")

		src, err := NewDockerSource()
		assert.NilError(t, err)
		defer src.Close()
		cfg := Config{Dir: t.TempDir(), Source: src, Clock: stats.SystemClock{}, Interval: time.Second}
		s, err := New(cfg)
		assert.NilError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		runDone := make(chan struct{})
		go func() { s.Run(ctx); close(runDone) }()
		stopped := false
		stop := func() {
			if !stopped {
				stopped = true
				cancel()
				<-runDone
				assert.NilError(t, s.Close())
			}
		}
		defer stop()
		began := time.Now()

		// (a) the three identities, each with CPU, memory and network series
		for _, id := range []string{proofApp, limitedID, plainID} {
			eventually(t, 30*time.Second, id+" has cpu, memory and network series", func() bool {
				v, err := s.Series(id, stats.RangeLive)
				if err != nil {
					return false
				}
				for _, key := range []string{"cpu/user", "memory/rss", "network/rx:eth0", "restarts/count"} {
					if _, ok := lastValue(v.Result, key); !ok {
						return false
					}
				}
				return true
			})
		}

		// (b) the declared limits and the allocation the percentages are read against
		lim := series(t, s, limitedID, stats.RangeLive)
		v, ok := lastValue(lim.Result, "memory/limit")
		assert.Assert(t, ok)
		assert.Equal(t, v, float64(mem64M))
		assert.Assert(t, lim.MemoryLimit != nil && *lim.MemoryLimit == mem64M)
		assert.Assert(t, slices.Contains(lim.Notes, "no_cpu_limit"), "%v", lim.Notes)
		assert.Assert(t, lim.CPUAllocation != nil && *lim.CPUAllocation >= 1, "the machine's cores")
		for _, id := range []string{proofApp, plainID} {
			v := series(t, s, id, stats.RangeLive)
			assert.Assert(t, pointsOf(v.Result, "memory/limit") == nil, id)
			assert.Assert(t, v.MemoryLimit == nil, id)
			assert.Assert(t, slices.Contains(v.Notes, "no_memory_limit"), "%s %v", id, v.Notes)
		}
		app := series(t, s, proofApp, stats.RangeLive)
		assert.Assert(t, app.CPUAllocation != nil && *app.CPUAllocation == 0.5, "%v", app.CPUAllocation)
		assert.Assert(t, !slices.Contains(app.Notes, "no_cpu_limit"), "%v", app.Notes)
		eventually(t, 30*time.Second, "the busy compose container uses about its 0.5 core and is throttled", func() bool {
			it, ok := rowOf(s, proofApp)
			th, thok := lastValue(series(t, s, proofApp, stats.RangeLive).Result, "cpu/throttled")
			return ok && it.CPUPercent != nil && *it.CPUPercent > 25 && *it.CPUPercent <= 60 && thok && th > 0
		})
		it, _ := rowOf(s, limitedID)
		assert.Assert(t, it.MemoryLimit != nil && *it.MemoryLimit == mem64M)
		assert.Equal(t, it.Group, "Autres conteneurs")
		it, _ = rowOf(s, proofApp)
		assert.Equal(t, it.Kind, "app-container")
		assert.Equal(t, it.App, proofProject)

		// (c) the health probe
		eventually(t, 30*time.Second, "health/ok becomes 1", func() bool {
			v, ok := lastValue(series(t, s, limitedID, stats.RangeLive).Result, "health/ok")
			return ok && v == 1
		})

		// a stopped container stays listed with its history, its alerts resolved
		dockerRun(t, "stop", "-t", "1", proofPlain)
		eventually(t, 15*time.Second, "the stopped container is listed as stopped", func() bool {
			it, ok := rowOf(s, plainID)
			return ok && it.State == "stopped"
		})
		_, active, err := s.Alerts(plainID)
		assert.NilError(t, err)
		assert.Equal(t, len(active), 0)
		pv := series(t, s, plainID, stats.RangeLive)
		assert.Assert(t, !slices.Contains(pv.Notes, "container_gone"), "%v", pv.Notes)
		_, ok = lastValue(pv.Result, "cpu/user")
		assert.Assert(t, ok, "the stopped container keeps its history")

		// (d) a removed container is forgotten: its history stays, marked gone
		dockerRun(t, "rm", "-f", proofLimited)
		time.Sleep(15 * time.Second)
		it, ok = rowOf(s, limitedID)
		assert.Assert(t, ok, "a removed container with history is still listed")
		assert.Equal(t, it.State, "stopped")
		assert.Assert(t, it.CPUPercent == nil && it.MemoryUsed == nil)
		gone := series(t, s, limitedID, stats.RangeLive)
		assert.Assert(t, slices.Contains(gone.Notes, "container_gone"), "%v", gone.Notes)

		// (e) a service restart keeps the minutes written before it
		if left := proofRun - time.Since(began); left > 0 {
			time.Sleep(left)
		}
		before := written(series(t, s, proofApp, stats.Range15m).Result, "cpu/user")
		assert.Assert(t, len(before) >= 2, "minutes written in %v: %v", proofRun, before)
		stop()
		s2, err := New(cfg)
		assert.NilError(t, err)
		defer s2.Close()
		after := written(series(t, s2, proofApp, stats.Range15m).Result, "cpu/user")
		for _, at := range before {
			assert.Assert(t, slices.Contains(after, at), "minute %d lost by the restart: %v", at, after)
		}
		s2.Tick(context.Background())
		it, ok = rowOf(s2, proofApp)
		assert.Assert(t, ok && it.State == "running", "%+v", it)
		it, ok = rowOf(s2, limitedID)
		assert.Assert(t, ok && it.State == "stopped", "the removed container's history survives the restart: %+v", it)
		t.Logf("minutes kept across the restart: %d; run %v", len(before), time.Since(began).Round(time.Second))
	})
}
