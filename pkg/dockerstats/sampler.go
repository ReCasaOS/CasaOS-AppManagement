package dockerstats

import (
	"cmp"
	"context"
	"errors"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/docker/api/types/container"
	"go.uber.org/zap"
)

var (
	ErrUnknownItem = errors.New("unknown item")
	ErrUnavailable = errors.New("stats unavailable")
)

const (
	// callTimeout bounds each List, Inspect and the whole Stats pass of a tick.
	callTimeout = 4 * time.Second
	// ponytail: polled once a minute instead of following Docker events; limits and RestartCount change rarely and the history is per minute anyway
	inspectEvery = time.Minute
	// missedTicks: a container absent from this many lists in a row is gone.
	missedTicks = 2
	maxDetail   = 200
	// maxInterfaces: series keys are permanent (48 per item, 16 fixed ones), so
	// at most this many interfaces are recorded (3 keys each).
	maxInterfaces = 8
)

// inspectBudget bounds the whole inspect pass of a tick; what is left waits
// for the next tick. A variable only so tests can shorten it.
var inspectBudget = 2 * time.Second

// ifName is what Linux accepts as an interface name (IFNAMSIZ 15); it keeps
// network keys well under Common's 63-byte key limit.
var ifName = regexp.MustCompile(`^[a-zA-Z0-9_.@-]{1,15}$`)

// logError is replaced by tests: the shared logger is not initialised there.
var logError = func(msg string, err error) { logger.Error(msg, zap.Error(err)) }

type Config struct {
	Dir      string // /var/lib/casaos/stats/app_management
	Source   Source
	Clock    stats.Clock
	Interval time.Duration // 5s in production
	// Emit must not block; minutes = the rule's window. Called from the sampler
	// goroutine or a route goroutine.
	Emit func(e stats.Event, who Identity, minutes int)
}

type ItemInfo struct {
	Identity
	State       string   // running|stopped|paused|crashed|unknown
	CPUPercent  *float64 // latest user+system, nil if no sample in the last 3 intervals
	MemoryUsed  *uint64  // working set
	MemoryLimit *uint64  // declared limit only
	Alerts      int
	StartedAt   *time.Time // inspect State.StartedAt, nil when unknown (container known only from disk)
	Detail      string     // image name, at most 200 characters, "" when unknown
}

type SeriesView struct {
	Result        stats.Result // alert/* series already removed (Common's Query never returns them)
	CPUAllocation *float64     // cores
	MemoryLimit   *uint64
	Notes         []string
}

// row is what the routes read of a container; the sampler publishes a copy per tick.
type row struct {
	id        Identity
	state     string
	cpu       float64
	cpuAt     time.Time // zero: no CPU rate yet
	mem       uint64
	memAt     time.Time
	alloc     Allocation
	cores     float64 // machine cores as Docker last reported them; 0 unknown
	startedAt *time.Time
	detail    string
}

// entry is the sampler's own state of a container; only Tick touches it.
type entry struct {
	row
	dockerID    string
	dockerState string // Docker's raw state at the last list
	skipLogged  bool   // its ring file was skipped and that was logged
	health      float64
	healthOK    bool
	inspectedAt time.Time // zero: inspect again
	restarts    int
	hasRestarts bool
	snap        *Snapshot
	missed      int
	resolved    bool // Resolve ran since it stopped running
}

type Sampler struct {
	cfg   Config
	store *stats.Store
	ev    *stats.Evaluator

	tick      sync.Mutex        // Tick never runs twice at once; Close waits for it
	closed    bool              // under tick
	reg       map[string]*entry // Tick only
	lastSweep time.Time

	mu       sync.Mutex // guards the fields below
	view     map[string]row
	up       bool // the last List succeeded
	down     bool // the outage was logged
	capUntil time.Time
	capLog   time.Time
	errLog   time.Time
}

// New opens the store and the evaluator, wires the evaluator to the store and
// rebuilds the alert counters of every item on disk.
func New(cfg Config) (*Sampler, error) {
	st, err := stats.Open(cfg.Dir, cfg.Clock)
	if err != nil {
		return nil, err
	}
	s := &Sampler{cfg: cfg, store: st, reg: map[string]*entry{}, view: map[string]row{}}
	ev, err := stats.NewEvaluator(st, cfg.Dir, s.emit)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	s.ev = ev
	st.SetOnMinute(ev.OnMinute)
	if sk := st.Skipped(); len(sk) > 0 {
		logError("some stats history files could not be read and are left untouched", errors.New(strings.Join(sk, ", ")))
	}
	for _, id := range st.Items() {
		ev.Rebuild(id)
	}
	return s, nil
}

func (s *Sampler) emit(e stats.Event) {
	if s.cfg.Emit == nil {
		return
	}
	who, ok := ParseID(e.Item)
	if !ok {
		who = Identity{ID: e.Item}
	}
	minutes := 0
	for _, r := range s.ev.Rules(e.Item) {
		if r.Key == e.Key {
			minutes = r.Minutes
		}
	}
	s.cfg.Emit(e, who, minutes)
}

// Run ticks now, then every Interval, until ctx ends. A slow tick delays the
// next one; ticks never overlap.
func (s *Sampler) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick is one pass: list, inspect, measure, record, forget the gone ones.
func (s *Sampler) Tick(ctx context.Context) {
	s.tick.Lock()
	defer s.tick.Unlock()
	if s.closed {
		return
	}
	now := s.cfg.Clock.Now()

	lctx, cancel := context.WithTimeout(ctx, callTimeout)
	list, err := s.cfg.Source.List(lctx)
	cancel()
	if err != nil && ctx.Err() != nil {
		return // shutting down, not an outage
	}
	s.mu.Lock()
	if err != nil {
		// a gap: nothing recorded, nothing resolved (Docker away is not evidence the alerts ended)
		if !s.down {
			logError("Docker is unreachable, container stats are paused", err)
		}
		s.up, s.down = false, true
		s.mu.Unlock()
		return
	}
	s.up, s.down = true, false
	s.mu.Unlock()

	// one summary per identity; while a container is recreated, the running one wins
	seen := map[string]container.Summary{}
	for _, c := range list {
		id, ok := Identify(c.Labels, c.Names)
		if !ok {
			continue
		}
		if prev, dup := seen[id.ID]; dup && prev.State == "running" {
			continue
		}
		seen[id.ID] = c
	}

	var running []string
	byDocker := map[string]*entry{}
	ictx, icancel := context.WithTimeout(ctx, inspectBudget)
	defer icancel()
	for key, c := range seen {
		e := s.reg[key]
		if e == nil {
			e = &entry{}
			e.id, _ = Identify(c.Labels, c.Names)
			s.reg[key] = e
		}
		e.missed = 0
		if e.dockerID != c.ID { // new or recreated: its counters and settings start over
			e.dockerID, e.snap, e.inspectedAt = c.ID, nil, time.Time{}
		}
		if e.dockerState != string(c.State) { // started, stopped, paused: read it again
			e.dockerState, e.inspectedAt = string(c.State), time.Time{}
		}
		e.state = StateOf(c)
		e.health, e.healthOK = HealthOf(c.Status)
		// an unchanged stopped container is never inspected again
		due := e.inspectedAt.IsZero() || (c.State == "running" && elapsed(now, e.inspectedAt, inspectEvery))
		if due && ictx.Err() == nil {
			s.inspect(ictx, e, now)
		}
		if string(c.State) == "running" {
			e.resolved = false
			running = append(running, c.ID)
			byDocker[c.ID] = e
			continue
		}
		e.snap, e.cpuAt, e.memAt = nil, time.Time{}, time.Time{}
		if !e.resolved {
			s.ev.Resolve(key)
			e.resolved = true
		}
	}

	if len(running) > 0 {
		sctx, cancel := context.WithTimeout(ctx, callTimeout)
		got := s.cfg.Source.Stats(sctx, running)
		cancel()
		for _, dockerID := range running {
			if st, ok := got[dockerID]; ok && st != nil {
				s.measure(byDocker[dockerID], st, now)
			} // else a gap: no Record, neither a zero nor a resolution
		}
	}

	for key, e := range s.reg {
		if _, ok := seen[key]; ok {
			continue
		}
		if e.missed++; e.missed < missedTicks {
			continue
		}
		if !e.resolved {
			s.ev.Resolve(key)
		}
		if err := s.store.Forget(key, now); err != nil && !errors.Is(err, stats.ErrUnknownItem) {
			s.noteRecordError(err, now)
		}
		delete(s.reg, key)
	}

	if elapsed(now, s.lastSweep, time.Hour) {
		if err := s.store.Sweep(now); err != nil {
			logError("stats sweep failed", err)
		}
		s.ev.Prune()
		s.lastSweep = now
	}

	view := make(map[string]row, len(s.reg))
	for k, e := range s.reg {
		view[k] = e.row
	}
	s.mu.Lock()
	s.view = view
	s.mu.Unlock()
}

// elapsed: d has passed since then, or the clock stepped back before it.
func elapsed(now, then time.Time, d time.Duration) bool {
	return now.Sub(then) >= d || now.Before(then)
}

// inspect runs under the pass's shared deadline (inspectBudget).
func (s *Sampler) inspect(ctx context.Context, e *entry, now time.Time) {
	resp, err := s.cfg.Source.Inspect(ctx, e.dockerID)
	if err != nil {
		return // tried again next tick
	}
	e.inspectedAt = now
	if b := resp.ContainerJSONBase; b != nil {
		e.alloc = AllocationOf(b.HostConfig)
		e.restarts, e.hasRestarts = b.RestartCount, true
		e.startedAt = nil
		if b.State != nil {
			if t, err := time.Parse(time.RFC3339Nano, b.State.StartedAt); err == nil && t.Year() > 1 {
				e.startedAt = &t
			}
		}
	}
	if resp.Config != nil {
		e.detail = cleanDetail(resp.Config.Image)
	}
}

// cleanDetail drops control and format characters (bidi overrides included)
// and invalid UTF-8, then cuts to 200 characters.
func cleanDetail(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	if r := []rune(s); len(r) > maxDetail {
		s = string(r[:maxDetail])
	}
	return s
}

func (s *Sampler) measure(e *entry, st *container.StatsResponse, now time.Time) {
	cp := *st
	cp.Networks = keepInterfaces(st.Networks)
	smp, snap := Measure(e.snap, &cp, now, e.alloc, MaxGapFactor*s.cfg.Interval)
	e.snap = &snap
	// The first reading after a gap has no CPU rate. Alone in its minute (the
	// next tick falls in the next minute) it would write a minute without
	// alert/cpu_high, which ends an active cpu_high; so it is not recorded (a
	// minute without any Record keeps the alert). Cost: one 5 s live point per
	// gap. Its snapshot is the baseline of the next tick.
	_, rate := smp["alert/cpu_high"]
	_, cpu := snap.counters["total"]
	skip := !rate && cpu && now.Add(s.cfg.Interval).Unix()/60 != now.Unix()/60
	if e.hasRestarts {
		smp["restarts/count"] = float64(e.restarts)
	}
	if e.healthOK {
		smp["health/ok"] = e.health
	}
	if u, ok := smp["cpu/user"]; ok {
		e.cpu, e.cpuAt = u+smp["cpu/system"], now
	}
	if st.MemoryStats.Usage > 0 {
		e.mem, _ = docker.MemoryUsage(st)
		e.memAt = now
	}
	if e.alloc.CPULimit == 0 {
		e.cores = e.alloc.CPUCores(st)
	}
	if skip {
		return
	}
	switch err := s.store.Record(e.id.ID, now, smp); {
	case err == nil:
	case errors.Is(err, stats.ErrRingSkipped):
		// its history could not be read at Open: never overwritten, reported by Series
		if !e.skipLogged {
			logError("a stats history file is unreadable; that container is not recorded", err)
		}
		e.skipLogged = true
	default: // partial errors included: the other keys were stored, no retry
		s.noteRecordError(err, now)
	}
}

// keepInterfaces keeps the valid interface names, shortest then lexical
// first (eth2 before eth10), at most maxInterfaces; the others are dropped,
// not summed: a sum over a changing set would fake a spike.
// Docker names them ethN inside the container, so a reconnect reuses the same
// keys; Common's 48-key cap (a partial error) bounds anything else.
func keepInterfaces(in map[string]container.NetworkStats) map[string]container.NetworkStats {
	var names []string
	for n := range in {
		if ifName.MatchString(n) {
			names = append(names, n)
		}
	}
	slices.SortFunc(names, func(a, b string) int { return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b)) })
	if len(names) > maxInterfaces {
		names = names[:maxInterfaces]
	}
	out := make(map[string]container.NetworkStats, len(names))
	for _, n := range names {
		out[n] = in[n]
	}
	return out
}

// noteRecordError logs the item cap once an hour (and lists stats_capacity for
// the hour after), any other error at most once a minute.
func (s *Sampler) noteRecordError(err error, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if errors.Is(err, stats.ErrTooManyItems) {
		s.capUntil = t.Add(time.Hour)
		if s.capLog.IsZero() || t.Sub(s.capLog) >= time.Hour {
			s.capLog = t
			logError("too many containers for the stats store; some are listed but not measured", err)
		}
		return
	}
	if s.errLog.IsZero() || t.Sub(s.errLog) >= time.Minute {
		s.errLog = t
		logError("recording container stats failed", err)
	}
}

// Items lists the containers Docker knows and those with history on disk.
func (s *Sampler) Items() ([]ItemInfo, error) {
	s.mu.Lock()
	up, view := s.up, s.view
	s.mu.Unlock()
	if !up {
		return nil, ErrUnavailable
	}
	now := s.cfg.Clock.Now()
	fresh := func(at time.Time) bool { return !at.IsZero() && now.Sub(at) <= MaxGapFactor*s.cfg.Interval }
	out := make([]ItemInfo, 0, len(view))
	for _, r := range view {
		it := ItemInfo{Identity: r.id, State: r.state, StartedAt: r.startedAt, Detail: r.detail}
		if fresh(r.cpuAt) {
			it.CPUPercent = &r.cpu
		}
		if fresh(r.memAt) {
			it.MemoryUsed = &r.mem
		}
		if r.alloc.MemLimit > 0 {
			it.MemoryLimit = &r.alloc.MemLimit
		}
		out = append(out, it)
	}
	for _, id := range s.store.Items() {
		if _, ok := view[id]; ok {
			continue
		}
		if who, ok := ParseID(id); ok {
			out = append(out, ItemInfo{Identity: who, State: "stopped"})
		}
	}
	for i := range out {
		out[i].Alerts = len(s.ev.Active(out[i].ID))
	}
	slices.SortFunc(out, func(a, b ItemInfo) int {
		return cmp.Or(strings.Compare(a.Group, b.Group), strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	return out, nil
}

// Notes: stats_capacity during the hour after the item cap was hit.
func (s *Sampler) Notes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Clock.Now().Before(s.capUntil) {
		return []string{"stats_capacity"}
	}
	return []string{}
}

// Series returns the history of id over r with its scale and notes.
func (s *Sampler) Series(id string, r stats.Range) (SeriesView, error) {
	s.mu.Lock()
	rw, live := s.view[id]
	s.mu.Unlock()
	res, err := s.store.Query(id, r)
	switch {
	case errors.Is(err, stats.ErrUnknownItem):
		if s.store.Unreadable(id) { // history exists but is unreadable
			return SeriesView{}, ErrUnavailable
		}
		if !live {
			return SeriesView{}, ErrUnknownItem
		}
		res = emptyResult(r, s.cfg.Clock.Now()) // listed, nothing stored yet
	case err != nil:
		return SeriesView{}, ErrUnavailable
	}
	v := SeriesView{Result: res, Notes: []string{}}
	if !live {
		v.Notes = append(v.Notes, "container_gone")
		return v, nil
	}
	switch {
	case rw.alloc.CPULimit > 0:
		v.CPUAllocation = &rw.alloc.CPULimit
	default:
		cores := rw.cores
		if cores <= 0 { // never measured (stopped since the start): the machine's own cores
			cores = float64(runtime.NumCPU())
		}
		v.CPUAllocation = &cores
		v.Notes = append(v.Notes, "no_cpu_limit")
	}
	if rw.alloc.MemLimit > 0 {
		v.MemoryLimit = &rw.alloc.MemLimit
	} else {
		v.Notes = append(v.Notes, "no_memory_limit")
	}
	return v, nil
}

// emptyResult is what Common's Query answers for the range when there is nothing
// stored: the range's step and window at now, no series.
func emptyResult(r stats.Range, now time.Time) stats.Result {
	step, n := int64(5), int64(stats.LiveSlots)
	switch r {
	case stats.Range15m:
		step, n = 60, 15
	case stats.Range1h:
		step, n = 60, 60
	case stats.Range24h:
		step, n = 300, 288
	case stats.Range7d:
		step, n = 1800, 336
	}
	floor := func(x, m int64) int64 { return x - x%m }
	to := floor(now.Unix(), step)
	if r != stats.RangeLive {
		to = floor(floor(now.Unix(), 60)-60, step)
	}
	return stats.Result{Range: r, StepSeconds: int(step), From: to - (n-1)*step, To: to, Series: []stats.SeriesData{}}
}

// lookup: nil for an item listed or on disk, ErrUnavailable for one whose
// history could not be read at start, ErrUnknownItem otherwise.
func (s *Sampler) lookup(id string) error {
	s.mu.Lock()
	_, ok := s.view[id]
	s.mu.Unlock()
	switch {
	case ok || slices.Contains(s.store.Items(), id):
		return nil
	case s.store.Unreadable(id):
		return ErrUnavailable
	}
	return ErrUnknownItem
}

// Alerts returns the rules and the firing alerts of id.
func (s *Sampler) Alerts(id string) ([]stats.Rule, []stats.Event, error) {
	if err := s.lookup(id); err != nil {
		return nil, nil, err
	}
	return s.ev.Rules(id), s.ev.Active(id), nil
}

// SetRules updates the rules sent for id; the Evaluator's *stats.RuleError on a
// bad value. Only an item with history takes rules: Prune drops the rules of any
// other within the hour. nil means the rules are in alerts.json; an error after
// they were applied means they are only in memory until the next good write.
func (s *Sampler) SetRules(id string, rules []stats.Rule) error {
	switch {
	case slices.Contains(s.store.Items(), id):
	case s.store.Unreadable(id):
		return ErrUnavailable
	default:
		return ErrUnknownItem
	}
	if err := s.ev.SetRules(id, rules); err != nil {
		return err
	}
	return s.ev.LastError()
}

// Close waits for a tick in flight, then closes the store; later calls return nil.
func (s *Sampler) Close() error {
	s.tick.Lock()
	defer s.tick.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.store.Close()
}
