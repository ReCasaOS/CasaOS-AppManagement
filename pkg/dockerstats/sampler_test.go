package dockerstats

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/docker/docker/api/types/container"
	"gotest.tools/v3/assert"
)

func TestARunningContainerIsRecordedUnderItsStableID(t *testing.T) {
	c := web("a")
	c.Cores = 0.2 // +1.0e9 ns of user time every 5 s
	h := newHarness(t, c)
	h.ticks(2)
	assert.DeepEqual(t, values(h.live(webID, "cpu/user")), []float64{20})
	h.src.set(func() { c.ID, c.User, c.Image = "b", 0, "web:2" }) // recreated: same labels, another Docker ID
	h.tick()
	assert.Equal(t, h.item(webID).Detail, "web:2", "a recreated container is inspected again at once")
	rows := h.items()
	assert.Equal(t, len(rows), 1)
	assert.Equal(t, rows[0].ID, webID)
	assert.Equal(t, rows[0].Name, "web")
	assert.Equal(t, len(values(h.live(webID, "memory/rss"))), 3)
}

func TestWhileAContainerIsRecreatedTheRunningOneIsListed(t *testing.T) {
	old, cur := web("old"), web("new")
	old.State, old.Status = "exited", "Exited (0) 1 second ago"
	h := newHarness(t, cur, old)
	h.ticks(2)
	assert.Equal(t, h.item(webID).State, "running")
	assert.Equal(t, len(h.items()), 1)
	h.src.set(func() { h.src.ctrs = []*fakeCtr{old, cur} })
	h.tick()
	assert.Equal(t, h.item(webID).State, "running")
}

func TestTheFirstSampleOfAContainerHasNoCPURate(t *testing.T) {
	c := web("a")
	c.Cores = 0.5
	h := newHarness(t, c)
	h.tick()
	assert.Equal(t, len(values(h.live(webID, "memory/rss"))), 1)
	assert.Assert(t, h.live(webID, "cpu/user") == nil)
	assert.Assert(t, h.item(webID).CPUPercent == nil)
}

func TestAnyOtherContainerIsListedUnderAutresConteneurs(t *testing.T) {
	h := newHarness(t, &fakeCtr{ID: "x", Name: "nginx", State: "running", Status: "Up", Mem: 1 << 20})
	h.tick()
	it := h.item("container:nginx")
	assert.Equal(t, it.Group, "Autres conteneurs")
	assert.Equal(t, it.Kind, "container")
	assert.Equal(t, it.Name, "nginx")
}

func TestOnlyRunningContainersAreSampled(t *testing.T) {
	ctr := func(id, state, status string) *fakeCtr {
		return &fakeCtr{ID: id, Name: id, State: state, Status: status, Cores: 1, Mem: 1 << 20}
	}
	h := newHarness(t,
		ctr("run", "running", "Up 1 hour"),
		ctr("paused", "paused", "Up 1 hour (Paused)"),
		ctr("created", "created", "Created"),
		ctr("exited0", "exited", "Exited (0) 1 minute ago"),
		ctr("exited1", "exited", "Exited (1) 1 minute ago"),
		ctr("killed", "exited", "Exited (137) 1 minute ago"),
		ctr("restarting", "restarting", "Restarting (1) 2 seconds ago"),
	)
	h.ticks(3)
	for _, id := range h.src.asked {
		assert.Equal(t, id, "run")
	}
	want := map[string]string{"run": "running", "paused": "paused", "created": "stopped", "exited0": "stopped", "exited1": "crashed", "killed": "stopped", "restarting": "crashed"}
	rows := h.items()
	assert.Equal(t, len(rows), len(want))
	for _, it := range rows {
		name := strings.TrimPrefix(it.ID, "container:")
		assert.Equal(t, it.State, want[name], name)
		if name != "run" {
			assert.Assert(t, it.CPUPercent == nil && it.MemoryUsed == nil, name)
		} else {
			assert.Assert(t, it.CPUPercent != nil && it.MemoryUsed != nil)
		}
	}
	assert.DeepEqual(t, h.s.store.Items(), []string{"container:run"})
}

func TestOneContainerFailingDoesNotDropTheOthers(t *testing.T) {
	a, b, c := web("a"), web("b"), web("c")
	b.Labels, c.Labels = compose("jarvis", "db", "1"), compose("jarvis", "cache", "1")
	b.NoStats = true
	h := newHarness(t, a, b, c)
	h.ticks(2)
	assert.DeepEqual(t, h.s.store.Items(), []string{"app:jarvis:cache:1", webID})
	assert.Equal(t, len(h.items()), 3)
}

func TestDockerDownGivesUnavailableAndNoPanic(t *testing.T) {
	c := web("a")
	c.Cores = 0.3
	h := newHarness(t, c)
	h.ticks(2)
	rss := len(values(h.live(webID, "memory/rss")))
	var logged int
	logError = func(string, error) { logged++ }
	defer func() { logError = func(string, error) {} }()
	h.src.set(func() { h.src.listErr = errors.New("connection refused") })
	h.ticks(3)
	assert.Equal(t, logged, 1, "the outage is logged once, not every tick")
	_, err := h.s.Items()
	assert.Assert(t, errors.Is(err, ErrUnavailable))
	assert.Equal(t, len(values(h.live(webID, "memory/rss"))), rss)
	assert.Equal(t, len(h.s.store.Items()), 1)
}

func TestItemsIsUnavailableBeforeTheFirstTick(t *testing.T) {
	h := newHarness(t, web("a"))
	_, err := h.s.Items()
	assert.Assert(t, errors.Is(err, ErrUnavailable))
}

func TestAfterDockerComesBackThereIsAGapNotASpike(t *testing.T) {
	c := web("a")
	c.Cores = 0.2
	h := newHarness(t, c)
	h.ticks(2) // 0 s, 5 s
	h.src.set(func() { h.src.listErr = errors.New("down"); c.Cores = 1 })
	for h.clock.Now().Before(t0.Add(195 * time.Second)) { // 10 s .. 190 s
		h.tick()
	}
	h.src.set(func() { h.src.listErr = nil; c.Cores = 0.2 })
	h.tick() // 195 s: baseline 190 s old
	vals := values(h.live(webID, "cpu/user"))
	assert.DeepEqual(t, vals, []float64{20})
	h.tick()
	assert.DeepEqual(t, values(h.live(webID, "cpu/user")), []float64{20, 20})
}

func TestAContainerThatDisappearsIsForgottenAfterTwoMissedTicks(t *testing.T) {
	c := web("a")
	h := newHarness(t, c)
	h.ticks(2)
	h.src.set(func() { h.src.ctrs = nil })
	h.tick()
	assert.Equal(t, h.item(webID).State, "running")
	h.tick()
	it := h.item(webID)
	assert.Equal(t, it.State, "stopped")
	assert.Equal(t, it.Name, "web")
	assert.Assert(t, it.StartedAt == nil && it.Detail == "")
	v, err := h.s.Series(webID, stats.RangeLive)
	assert.NilError(t, err)
	assert.DeepEqual(t, v.Notes, []string{"container_gone"})
	h.clock.Advance(8 * 24 * time.Hour)
	h.tick()
	_, err = h.s.Series(webID, stats.RangeLive)
	assert.Assert(t, errors.Is(err, ErrUnknownItem), "%v", err)
}

func TestARecreatedContainerThatIsAbsentForOneTickIsNotForgotten(t *testing.T) {
	c := web("a")
	h := newHarness(t, c)
	h.ticks(2)
	h.src.set(func() { h.src.ctrs = nil })
	h.tick()
	h.src.set(func() { c.ID = "b"; h.src.ctrs = []*fakeCtr{c} })
	h.ticks(2)
	v, err := h.s.Series(webID, stats.RangeLive)
	assert.NilError(t, err)
	assert.Assert(t, !slices.Contains(v.Notes, "container_gone"))
	assert.Equal(t, len(values(h.live(webID, "memory/rss"))), 4)
	assert.Equal(t, h.item(webID).State, "running")
	// absences that are not consecutive never add up
	h.src.set(func() { h.src.ctrs = nil })
	h.tick()
	assert.Equal(t, h.item(webID).State, "running", "one absence after a return is not gone")
	h.src.set(func() { h.src.ctrs = []*fakeCtr{c} })
	h.tick()
	v, err = h.s.Series(webID, stats.RangeLive)
	assert.NilError(t, err)
	assert.Assert(t, !slices.Contains(v.Notes, "container_gone"))
}

// firing runs a 1-core-limited container at full use until cpu_high (2 min) fires.
func firing(t *testing.T) (*harness, *fakeCtr) {
	t.Helper()
	c := web("a")
	c.NanoCPUs, c.Cores = 1e9, 1
	h := newHarness(t, c)
	h.tick()
	assert.NilError(t, h.s.SetRules(webID, []stats.Rule{cpuRule(2)}))
	for len(h.states()) == 0 && h.clock.Now().Before(t0.Add(5*time.Minute)) {
		h.tick()
	}
	assert.DeepEqual(t, h.states(), []string{"firing"})
	return h, c
}

func (h *harness) active() int {
	_, act, err := h.s.Alerts(webID)
	assert.NilError(h.t, err)
	return len(act)
}

func TestAHighCPUFiresOnceAndResolvesOnce(t *testing.T) {
	h, c := firing(t)
	e := h.evs[0]
	assert.Equal(t, e.ev.Key, "cpu_high")
	assert.Equal(t, e.ev.Item, webID)
	assert.Equal(t, e.who.Name, "web")
	assert.Equal(t, e.minutes, 2)
	h.ticks(12 * 3)
	assert.DeepEqual(t, h.states(), []string{"firing"})
	h.src.set(func() { c.Cores = 0.1 })
	h.ticks(12 * 3)
	assert.DeepEqual(t, h.states(), []string{"firing", "resolved"})
	assert.Equal(t, h.active(), 0)
}

func TestAnAlertOfAContainerThatDisappearsIsResolved(t *testing.T) {
	h, _ := firing(t)
	h.src.set(func() { h.src.ctrs = nil })
	h.tick()
	assert.DeepEqual(t, h.states(), []string{"firing"})
	h.tick()
	assert.DeepEqual(t, h.states(), []string{"firing", "resolved"})
	assert.Equal(t, h.active(), 0)
	h.ticks(12 * 3)
	assert.DeepEqual(t, h.states(), []string{"firing", "resolved"})
}

func TestAStoppedFiringContainerResolves(t *testing.T) {
	for _, st := range []struct{ state, status string }{{"exited", "Exited (0) 1 second ago"}, {"paused", "Up 1 hour (Paused)"}} {
		t.Run(st.state, func(t *testing.T) {
			h, c := firing(t)
			h.src.set(func() { c.State, c.Status = st.state, st.status })
			h.tick()
			assert.DeepEqual(t, h.states(), []string{"firing", "resolved"})
			assert.Equal(t, h.active(), 0)
			assert.Assert(t, h.item(webID).CPUPercent == nil, "a stopped container shows no live CPU")
			h.ticks(12 * 3)
			assert.DeepEqual(t, h.states(), []string{"firing", "resolved"})
			h.src.set(func() { c.State, c.Status = "running", "Up 1 second" })
			h.ticks(12 * 4)
			assert.DeepEqual(t, h.states(), []string{"firing", "resolved", "firing"})
			h.src.set(func() { c.State, c.Status = st.state, st.status })
			h.tick()
			assert.DeepEqual(t, h.states(), []string{"firing", "resolved", "firing", "resolved"})
		})
	}
}

func TestAFailedStatsCallDoesNotResolve(t *testing.T) {
	h, c := firing(t)
	h.src.set(func() { c.NoStats = true })
	h.ticks(12 * 3)
	assert.DeepEqual(t, h.states(), []string{"firing"})
	assert.Equal(t, h.active(), 1)
	assert.Assert(t, h.item(webID).CPUPercent == nil, "no sample in the last 3 intervals")
	rss := h.live(webID, "memory/rss")
	for _, p := range rss[len(rss)-12*3:] {
		assert.Assert(t, p.Avg == nil, "a failed call is a gap, not a value")
	}
	h.src.set(func() { c.NoStats = false })
	h.ticks(12 * 3)
	assert.DeepEqual(t, h.states(), []string{"firing"})
	h.src.set(func() { c.Cores = 0.1 })
	h.ticks(12 * 3)
	assert.DeepEqual(t, h.states(), []string{"firing", "resolved"})
}

func TestStartedAtAndImageAreReported(t *testing.T) {
	c := web("a")
	c.StartedAt, c.Image = "2026-10-01T11:00:00.123456789Z", "nginx\x1b[31m:\x00latest\n"
	other := &fakeCtr{ID: "o", Name: "long", State: "running", Status: "Up", Image: strings.Repeat("é", 500), StartedAt: "0001-01-01T00:00:00Z"}
	h := newHarness(t, c, other)
	h.tick()
	it := h.item(webID)
	assert.Assert(t, it.StartedAt != nil)
	assert.Equal(t, it.StartedAt.UnixNano(), time.Date(2026, 10, 1, 11, 0, 0, 123456789, time.UTC).UnixNano())
	assert.Equal(t, it.Detail, "nginx[31m:latest")
	long := h.item("container:long")
	assert.Equal(t, long.Detail, strings.Repeat("é", 200))
	assert.Assert(t, long.StartedAt == nil)
	h.src.set(func() { h.src.ctrs = nil })
	h.ticks(2)
	gone := h.item(webID)
	assert.Assert(t, gone.StartedAt == nil && gone.Detail == "")
}

func TestTheCapacityErrorIsLoggedOnceAnHourAndListed(t *testing.T) {
	h := newHarness(t)
	var logged int
	logError = func(string, error) { logged++ }
	defer func() { logError = func(string, error) {} }()
	assert.Equal(t, len(h.s.Notes()), 0)
	for i := range 3 {
		h.s.noteRecordError(stats.ErrTooManyItems, h.clock.Now())
		if i < 2 {
			h.clock.Advance(5 * time.Minute)
		}
	}
	assert.Equal(t, logged, 1)
	assert.DeepEqual(t, h.s.Notes(), []string{"stats_capacity"})
	h.clock.Advance(51 * time.Minute) // an hour and a minute after the first
	h.s.noteRecordError(stats.ErrTooManyItems, h.clock.Now())
	assert.Equal(t, logged, 2)
	h.clock.Advance(2 * time.Hour)
	assert.Equal(t, len(h.s.Notes()), 0)
}

func TestOtherRecordErrorsAreLoggedOnceAMinute(t *testing.T) {
	h := newHarness(t)
	var logged int
	logError = func(string, error) { logged++ }
	defer func() { logError = func(string, error) {} }()
	for range 12 {
		h.s.noteRecordError(stats.ErrSeriesCap, h.clock.Now())
		h.clock.Advance(5 * time.Second)
	}
	assert.Equal(t, logged, 1)
	h.s.noteRecordError(stats.ErrSeriesCap, h.clock.Now())
	assert.Equal(t, logged, 2)
	assert.Equal(t, len(h.s.Notes()), 0)
}

func TestARestartOfTheServiceKeepsHistoryAndAlertState(t *testing.T) {
	h, _ := firing(t)
	h.ticks(12)
	before, err := h.s.Series(webID, stats.Range15m)
	assert.NilError(t, err)
	assert.Assert(t, len(before.Result.Series) > 0)
	assert.NilError(t, h.s.Close())
	h.open()
	v, err := h.s.Series(webID, stats.Range15m)
	assert.NilError(t, err)
	assert.Assert(t, len(values(v.Result.Series[0].Points)) > 0)
	h.ticks(12 * 3)
	assert.Equal(t, len(h.items()), 1)
	assert.DeepEqual(t, h.states(), []string{"firing"})
	assert.Equal(t, h.active(), 1)
}

func TestAnAlertThatEndedWhileTheServiceWasDownResolvesAtStart(t *testing.T) {
	h, _ := firing(t)
	assert.NilError(t, h.s.Close())
	h.clock.Advance(30 * time.Minute)
	h.open()
	assert.DeepEqual(t, h.states(), []string{"firing", "resolved"})
	assert.Equal(t, h.active(), 0)
}

func TestHealthAndRestartsAreRecordedWhenKnown(t *testing.T) {
	sick := web("a")
	sick.Status, sick.Restarts = "Up 1 hour (unhealthy)", 3
	plain := &fakeCtr{ID: "p", Name: "plain", State: "running", Status: "Up 1 hour", Mem: 1 << 20}
	h := newHarness(t, sick, plain)
	h.tick()
	assert.DeepEqual(t, values(h.live(webID, "health/ok")), []float64{0})
	assert.DeepEqual(t, values(h.live(webID, "restarts/count")), []float64{3})
	assert.Assert(t, h.live("container:plain", "health/ok") == nil)
}

func TestInspectIsRepeatedOnlyOncePerMinute(t *testing.T) {
	h := newHarness(t, web("a"))
	h.ticks(24)
	assert.Assert(t, h.src.inspects >= 2 && h.src.inspects <= 3, "%d inspects", h.src.inspects)
}

func TestTicksNeverOverlap(t *testing.T) {
	h := newHarness(t, web("a"))
	h.s.cfg.Interval = 10 * time.Millisecond
	h.src.listSleep = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.s.Run(ctx); close(done) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	h.src.mu.Lock()
	defer h.src.mu.Unlock()
	assert.Equal(t, h.src.peakLists, 1)
}

func TestAnUnreadableStoreMakesSeriesUnavailable(t *testing.T) {
	t.Run("closed store", func(t *testing.T) {
		h := newHarness(t, web("a"))
		h.ticks(2)
		assert.NilError(t, h.s.Close())
		_, err := h.s.Series(webID, stats.Range1h)
		assert.Assert(t, errors.Is(err, ErrUnavailable), "%v", err)
	})
	t.Run("ring skipped at open", func(t *testing.T) {
		other := &fakeCtr{ID: "o", Name: "other", State: "running", Status: "Up", Mem: 1 << 20}
		w := web("a")
		h := newHarness(t, w, other)
		h.ticks(2)
		assert.NilError(t, h.s.Close())
		rings, _ := filepath.Glob(filepath.Join(h.dir, "*.ring"))
		assert.Equal(t, len(rings), 2)
		var webRing string // the header holds the item name
		for _, r := range rings {
			if b, _ := os.ReadFile(r); strings.Contains(string(b), webID) {
				webRing = r
			}
		}
		assert.Equal(t, filepath.Base(webRing), ringName(webID), "Common's ring file name")
		assert.NilError(t, os.Remove(webRing))
		if err := os.Symlink(filepath.Join(h.dir, "nowhere"), webRing); err != nil {
			t.Skipf("no symlinks here: %v", err)
		}
		var logged int
		logError = func(string, error) { logged++ }
		defer func() { logError = func(string, error) {} }()
		h.open()
		h.ticks(3) // never crashes, the other container is still recorded
		assert.Equal(t, logged, 2, "once at Open, once for the item")
		_, err := h.s.Series(webID, stats.RangeLive)
		assert.Assert(t, errors.Is(err, ErrUnavailable), "%v", err)
		assert.Equal(t, len(values(h.live("container:other", "memory/rss"))), 3)
		assert.Equal(t, len(h.items()), 2)
		// stopped (never recorded again), then gone: still unavailable, not unknown
		h.src.set(func() { w.State, w.Status = "exited", "Exited (0) 1 second ago" })
		h.tick()
		_, err = h.s.Series(webID, stats.RangeLive)
		assert.Assert(t, errors.Is(err, ErrUnavailable), "stopped: %v", err)
		h.src.set(func() { h.src.ctrs = []*fakeCtr{other} })
		h.ticks(2)
		_, err = h.s.Series(webID, stats.RangeLive)
		assert.Assert(t, errors.Is(err, ErrUnavailable), "gone: %v", err)
		fi, err := os.Lstat(webRing)
		assert.NilError(t, err)
		assert.Assert(t, fi.Mode()&os.ModeSymlink != 0, "the unreadable file was replaced")
	})
}

func TestInterfacesAreCappedToEightStableNames(t *testing.T) {
	c := web("a")
	c.Nets = map[string]container.NetworkStats{"a b": {RxBytes: 1}, "": {RxBytes: 1}}
	for i := range 11 {
		c.Nets["eth"+strconv.Itoa(i)] = container.NetworkStats{RxBytes: 1}
	}
	var errs []error
	logError = func(_ string, err error) { errs = append(errs, err) }
	defer func() { logError = func(string, error) {} }()
	h := newHarness(t, c)
	h.ticks(2)
	assert.Equal(t, len(errs), 0, "%v", errs)
	v, err := h.s.Series(webID, stats.RangeLive)
	assert.NilError(t, err)
	var got []string
	for _, sd := range v.Result.Series {
		if sd.Chart == "network" && strings.HasPrefix(sd.Name, "rx:") {
			got = append(got, strings.TrimPrefix(sd.Name, "rx:"))
		}
	}
	assert.DeepEqual(t, got, []string{"eth0", "eth1", "eth2", "eth3", "eth4", "eth5", "eth6", "eth7"})
}

func TestSeriesScaleAndNotes(t *testing.T) {
	limited := web("a")
	limited.NanoCPUs, limited.MemLimit = 1.5e9, 512<<20
	free := &fakeCtr{ID: "f", Name: "free", State: "running", Status: "Up", Mem: 1 << 20}
	h := newHarness(t, limited, free)
	h.tick()
	v, err := h.s.Series(webID, stats.RangeLive)
	assert.NilError(t, err)
	assert.Equal(t, *v.CPUAllocation, 1.5)
	assert.Equal(t, *v.MemoryLimit, uint64(512<<20))
	assert.DeepEqual(t, v.Notes, []string{})
	assert.Equal(t, *h.item(webID).MemoryLimit, uint64(512<<20))
	v, err = h.s.Series("container:free", stats.RangeLive)
	assert.NilError(t, err)
	assert.Equal(t, *v.CPUAllocation, 4.0)
	assert.Assert(t, v.MemoryLimit == nil)
	assert.DeepEqual(t, v.Notes, []string{"no_cpu_limit", "no_memory_limit"})
	for _, sd := range v.Result.Series {
		assert.Assert(t, sd.Chart != "alert")
	}
	_, err = h.s.Series("container:nope", stats.RangeLive)
	assert.Assert(t, errors.Is(err, ErrUnknownItem))
	_, _, err = h.s.Alerts("container:nope")
	assert.Assert(t, errors.Is(err, ErrUnknownItem))
	assert.Assert(t, errors.Is(h.s.SetRules("container:nope", nil), ErrUnknownItem))
}

func TestOverTheItemCapContainersAreListedAndAForgottenOneFreesItsSlot(t *testing.T) {
	var ctrs []*fakeCtr
	for i := range stats.MaxItems + 1 {
		n := "c" + strconv.Itoa(i)
		ctrs = append(ctrs, &fakeCtr{ID: n, Name: n, State: "running", Status: "Up", Mem: 1 << 20})
	}
	h := newHarness(t, ctrs...)
	h.tick()
	assert.Equal(t, len(h.s.store.Items()), stats.MaxItems)
	assert.Equal(t, len(h.items()), stats.MaxItems+1)
	assert.DeepEqual(t, h.s.Notes(), []string{"stats_capacity"})
	var gone string
	h.src.set(func() {
		for i, c := range h.src.ctrs {
			if slices.Contains(h.s.store.Items(), "container:"+c.ID) {
				gone = c.ID
				h.src.ctrs = slices.Delete(h.src.ctrs, i, i+1)
				return
			}
		}
	})
	h.ticks(3) // gone after two missed ticks; its slot is free on the next
	assert.Assert(t, gone != "")
	assert.Equal(t, len(h.s.store.Items()), stats.MaxItems+1, "the forgotten one is kept, and the last one is now recorded")
}

// pointAt is the live point of key at the time at, nil when absent.
func (h *harness) pointAt(id, key string, at time.Time) *float64 {
	for _, p := range h.live(id, key) {
		if p.T == at.Unix() {
			return p.Avg
		}
	}
	return nil
}

func TestAGapEndingInTheLastTickOfAMinuteDoesNotResolve(t *testing.T) {
	for _, end := range []int{0, 25, 50, 55} {
		t.Run(strconv.Itoa(end), func(t *testing.T) {
			h, c := firing(t)
			h.src.set(func() { c.NoStats = true })
			start := h.clock.Now()
			for h.clock.Now().Sub(start) < 70*time.Second || h.clock.Now().Second() != end {
				h.tick()
			}
			h.src.set(func() { c.NoStats = false })
			first := h.clock.Now()
			h.tick()
			if end == 55 {
				assert.Assert(t, h.pointAt(webID, "memory/rss", first) == nil, "alone in its minute: not recorded")
				h.tick()
				assert.Assert(t, h.pointAt(webID, "cpu/user", first.Add(5*time.Second)) != nil, "the skipped reading is the next baseline")
			} else {
				assert.Assert(t, h.pointAt(webID, "memory/rss", first) != nil, "recorded")
			}
			h.ticks(12 * 3)
			assert.DeepEqual(t, h.states(), []string{"firing"})
			assert.Equal(t, h.active(), 1)
		})
	}
}

func TestTheInspectPassIsBoundedAndConverges(t *testing.T) {
	defer func(b time.Duration) { inspectBudget = b }(inspectBudget)
	inspectBudget = 100 * time.Millisecond
	var ctrs []*fakeCtr
	for i := range 300 {
		n := "s" + strconv.Itoa(i)
		ctrs = append(ctrs, &fakeCtr{ID: n, Name: n, State: "exited", Status: "Exited (0) 1 day ago", Image: "img"})
	}
	h := newHarness(t, ctrs...)
	h.src.inspectSleep = 5 * time.Millisecond
	count := func() int {
		h.src.mu.Lock()
		defer h.src.mu.Unlock()
		return len(h.src.perID)
	}
	for i := 0; count() < 300; i++ {
		assert.Assert(t, i < 60, "the inspect pass does not converge: %d of 300", count())
		began := time.Now()
		h.tick()
		assert.Assert(t, time.Since(began) < inspectBudget+200*time.Millisecond, "tick took %v", time.Since(began))
	}
	h.ticks(12 * 3) // three minutes: an unchanged stopped container is never inspected again
	h.src.mu.Lock()
	for id, n := range h.src.perID {
		assert.Equal(t, n, 1, id)
	}
	h.src.mu.Unlock()
	for _, it := range h.items() {
		assert.Equal(t, it.Detail, "img", it.ID)
	}
	h.src.set(func() {
		ctrs[0].State, ctrs[0].Status = "running", "Up 1 second"
		ctrs[1].State, ctrs[1].Status = "dead", "Dead"
	})
	h.tick()
	h.src.mu.Lock()
	defer h.src.mu.Unlock()
	assert.Equal(t, h.src.perID["s0"], 2, "a container that starts is read again")
	assert.Equal(t, h.src.perID["s1"], 2, "a stopped container whose state changed is read again")
}

func TestAHungInspectIsCutByThePassDeadline(t *testing.T) {
	defer func(b time.Duration) { inspectBudget = b }(inspectBudget)
	inspectBudget = 100 * time.Millisecond
	h := newHarness(t, web("a"))
	h.src.inspectSleep = 2 * time.Second
	began := time.Now()
	h.tick()
	assert.Assert(t, time.Since(began) < time.Second, "tick took %v", time.Since(began))
	assert.Equal(t, h.item(webID).State, "running")
}

func TestAClockSteppedBackStillInspectsAndSweeps(t *testing.T) {
	h := newHarness(t, web("a"))
	h.tick()
	assert.Equal(t, h.src.inspects, 1)
	h.clock.t = t0.Add(-2 * time.Hour)
	h.tick()
	assert.Equal(t, h.src.inspects, 2)
	assert.Equal(t, h.s.lastSweep, t0.Add(-2*time.Hour))
}

func TestCloseWaitsForATickInFlightAndIsIdempotent(t *testing.T) {
	h := newHarness(t, web("a"))
	h.src.listSleep = 100 * time.Millisecond
	done := make(chan struct{})
	go func() { h.s.Tick(context.Background()); close(done) }()
	time.Sleep(20 * time.Millisecond)
	assert.NilError(t, h.s.Close())
	select {
	case <-done:
	default:
		t.Fatal("Close returned while a tick was still running")
	}
	assert.NilError(t, h.s.Close())
	h.s.Tick(context.Background()) // after Close: does nothing
	h.src.mu.Lock()
	defer h.src.mu.Unlock()
	assert.Equal(t, len(h.src.asked), 1)
}

func TestTwoConcurrentTicksDoNotOverlap(t *testing.T) {
	h := newHarness(t, web("a"))
	h.src.listSleep = 50 * time.Millisecond
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); h.s.Tick(context.Background()) }()
	}
	wg.Wait()
	assert.Equal(t, h.src.peakLists, 1)
}

func TestAShutdownIsNotAnOutage(t *testing.T) {
	h := newHarness(t, web("a"))
	h.tick()
	var logged int
	logError = func(string, error) { logged++ }
	defer func() { logError = func(string, error) {} }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.s.Tick(ctx)
	assert.Equal(t, logged, 0)
	_, err := h.s.Items()
	assert.NilError(t, err)
}

func TestFormatCharactersAreStrippedFromTheDetail(t *testing.T) {
	assert.Equal(t, cleanDetail("evil\u202egnp.exe\u200b\ufeff"), "evilgnp.exe")
}

func TestAContainerWithoutCPUCountersIsRecordedEveryTick(t *testing.T) {
	c := web("a")
	c.NoCPU = true
	h := newHarness(t, c)
	h.ticks(12) // 12:00:00 .. 12:00:55, the last one included
	assert.Equal(t, len(values(h.live(webID, "memory/rss"))), 12)
}
