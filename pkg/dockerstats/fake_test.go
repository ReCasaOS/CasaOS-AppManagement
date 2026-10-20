package dockerstats

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/docker/docker/api/types/container"
	"gotest.tools/v3/assert"
)

func init() { logError = func(string, error) {} }

// fakeCtr is one scripted container. Cores is its CPU use (all user time),
// added to its counters each time the harness moves the clock.
type fakeCtr struct {
	ID, Name, State, Status string
	Labels                  map[string]string
	Image, StartedAt        string
	Restarts                int
	NanoCPUs, MemLimit      int64
	Cores                   float64
	User                    uint64 // cumulative ns
	Mem                     uint64
	Nets                    map[string]container.NetworkStats
	NoStats                 bool // the Stats call fails for it
	NoCPU                   bool // Docker reports no CPU counters for it
}

type fakeSource struct {
	mu        sync.Mutex
	ctrs      []*fakeCtr
	listErr   error
	listSleep time.Duration
	lists     int // concurrent List calls now
	peakLists int
	asked     []string // every id Stats was asked for
	inspects  int
	perID     map[string]int // inspects per Docker id
	// inspectSleep is how long one Inspect takes; it gives up when ctx ends
	inspectSleep time.Duration
}

func (f *fakeSource) List(ctx context.Context) ([]container.Summary, error) {
	f.mu.Lock()
	f.lists++
	f.peakLists = max(f.peakLists, f.lists)
	sleep := f.listSleep
	f.mu.Unlock()
	time.Sleep(sleep)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists--
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []container.Summary
	for _, c := range f.ctrs {
		out = append(out, container.Summary{ID: c.ID, Names: []string{"/" + c.Name}, Labels: c.Labels, State: container.ContainerState(c.State), Status: c.Status})
	}
	return out, nil
}

func (f *fakeSource) Inspect(ctx context.Context, id string) (container.InspectResponse, error) {
	f.mu.Lock()
	sleep := f.inspectSleep
	f.mu.Unlock()
	if sleep > 0 {
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return container.InspectResponse{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspects++
	if f.perID == nil {
		f.perID = map[string]int{}
	}
	f.perID[id]++
	for _, c := range f.ctrs {
		if c.ID == id {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					ID: c.ID, RestartCount: c.Restarts,
					State:      &container.State{StartedAt: c.StartedAt},
					HostConfig: &container.HostConfig{Resources: container.Resources{NanoCPUs: c.NanoCPUs, Memory: c.MemLimit}},
				},
				Config: &container.Config{Image: c.Image},
			}, nil
		}
	}
	return container.InspectResponse{}, errors.New("no such container")
}

func (f *fakeSource) Stats(ctx context.Context, ids []string) map[string]*container.StatsResponse {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, ids...)
	out := map[string]*container.StatsResponse{}
	for _, c := range f.ctrs {
		if !slices.Contains(ids, c.ID) || c.NoStats {
			continue
		}
		s := &container.StatsResponse{}
		s.CPUStats.CPUUsage.TotalUsage = c.User + 1
		s.CPUStats.CPUUsage.UsageInUsermode = c.User + 1
		if c.NoCPU {
			s.CPUStats.CPUUsage = container.CPUUsage{}
		}
		s.CPUStats.OnlineCPUs = 4
		s.MemoryStats.Usage = c.Mem
		s.MemoryStats.Limit = 8 << 30
		s.MemoryStats.Stats = map[string]uint64{"anon": c.Mem}
		s.Networks = c.Nets
		out[c.ID] = s
	}
	return out
}

// advance adds d of CPU use to every container.
func (f *fakeSource) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.ctrs {
		c.User += uint64(c.Cores * float64(d.Nanoseconds()))
	}
}

func (f *fakeSource) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

type emitted struct {
	ev      stats.Event
	who     Identity
	minutes int
}

type harness struct {
	t     *testing.T
	clock *fakeClock
	src   *fakeSource
	s     *Sampler
	dir   string
	mu    sync.Mutex
	evs   []emitted
}

func newHarness(t *testing.T, ctrs ...*fakeCtr) *harness {
	t.Helper()
	h := &harness{t: t, clock: &fakeClock{t0}, src: &fakeSource{ctrs: ctrs}, dir: t.TempDir()}
	h.open()
	return h
}

func (h *harness) open() {
	h.t.Helper()
	s, err := New(Config{Dir: h.dir, Source: h.src, Clock: h.clock, Interval: 5 * time.Second, Emit: func(e stats.Event, who Identity, minutes int) {
		h.mu.Lock()
		h.evs = append(h.evs, emitted{e, who, minutes})
		h.mu.Unlock()
	}})
	assert.NilError(h.t, err)
	h.t.Cleanup(func() { _ = s.Close() })
	h.s = s
}

// tick runs one Tick at the current time, then moves time (and CPU use) on by 5 s.
func (h *harness) tick() {
	h.s.Tick(context.Background())
	h.clock.Advance(5 * time.Second)
	h.src.advance(5 * time.Second)
}

func (h *harness) ticks(n int) {
	for range n {
		h.tick()
	}
}

func (h *harness) states() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.evs {
		out = append(out, e.ev.State)
	}
	return out
}

func (h *harness) items() []ItemInfo {
	h.t.Helper()
	it, err := h.s.Items()
	assert.NilError(h.t, err)
	return it
}

func (h *harness) item(id string) ItemInfo {
	h.t.Helper()
	for _, it := range h.items() {
		if it.ID == id {
			return it
		}
	}
	h.t.Fatalf("no row %s", id)
	return ItemInfo{}
}

// live returns the live points of key ("cpu/user"), nil if the series is absent.
func (h *harness) live(id, key string) []stats.Point {
	h.t.Helper()
	v, err := h.s.Series(id, stats.RangeLive)
	assert.NilError(h.t, err)
	chart, name := stats.SplitKey(key)
	for _, sd := range v.Result.Series {
		if sd.Chart == chart && sd.Name == name {
			return sd.Points
		}
	}
	return nil
}

func values(pts []stats.Point) []float64 {
	var out []float64
	for _, p := range pts {
		if p.Avg != nil {
			out = append(out, *p.Avg)
		}
	}
	return out
}

func web(dockerID string) *fakeCtr {
	return &fakeCtr{ID: dockerID, Name: "jarvis-web-1", State: "running", Status: "Up 1 hour", Labels: compose("jarvis", "web", "1"), Mem: 100 << 20}
}

const webID = "app:jarvis:web:1"
