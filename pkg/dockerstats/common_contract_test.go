package dockerstats

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"gotest.tools/v3/assert"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

var _ stats.Clock = (*fakeClock)(nil)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

const alertItem = "container:x"

type events struct {
	mu  sync.Mutex
	all []stats.Event
}

func (e *events) emit(ev stats.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.all = append(e.all, ev)
}

func (e *events) states() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var s []string
	for _, ev := range e.all {
		s = append(s, ev.State)
	}
	return s
}

// wired opens a store and an evaluator in one directory, hooked the only way services do it.
func wired(t *testing.T, clock stats.Clock, rules ...stats.Rule) (*stats.Store, *stats.Evaluator, *events, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := stats.Open(dir, clock)
	assert.NilError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ev := &events{}
	eval, err := stats.NewEvaluator(store, dir, ev.emit)
	assert.NilError(t, err)
	store.SetOnMinute(eval.OnMinute)
	if len(rules) > 0 {
		assert.NilError(t, eval.SetRules(alertItem, rules))
	}
	return store, eval, ev, dir
}

func cpuRule(minutes int) stats.Rule {
	return stats.Rule{Key: "cpu_high", Enabled: true, Threshold: 90, Minutes: minutes}
}

// high records alert/cpu_high: 100 every 5 s over [from, from+d].
func high(t *testing.T, s *stats.Store, from time.Time, d time.Duration) {
	t.Helper()
	for at := from; !at.After(from.Add(d)); at = at.Add(5 * time.Second) {
		assert.NilError(t, s.Record(alertItem, at, stats.Sample{"alert/cpu_high": 100}))
	}
}

func TestAForgottenItemThatReturnsIsKeptAndNotSwept(t *testing.T) {
	clock := &fakeClock{t0}
	store, err := stats.Open(t.TempDir(), clock)
	assert.NilError(t, err)
	defer store.Close()
	for at := t0; !at.After(t0.Add(2 * time.Minute)); at = at.Add(5 * time.Second) {
		assert.NilError(t, store.Record("container:x", at, stats.Sample{"cpu/user": 5}))
	}
	assert.NilError(t, store.Forget("container:x", t0.Add(2*time.Minute)))
	// it returns an hour later, then keeps being measured: a later stretch
	// of samples, 7 days and 12 hours after the first one
	for _, back := range []time.Time{t0.Add(time.Hour), t0.Add(7*24*time.Hour + 12*time.Hour)} {
		for at := back; !at.After(back.Add(2 * time.Minute)); at = at.Add(5 * time.Second) {
			assert.NilError(t, store.Record("container:x", at, stats.Sample{"cpu/user": 7}))
		}
	}
	clock.t = t0.Add(8*24*time.Hour + time.Hour)
	assert.NilError(t, store.Sweep(clock.Now()))
	assert.Assert(t, slices.Contains(store.Items(), "container:x"))
	res, err := store.Query("container:x", stats.Range24h)
	assert.NilError(t, err)
	assert.Assert(t, len(res.Series) > 0)
}

func TestAFiringAlertResolvesWhenResolveIsCalled(t *testing.T) {
	clock := &fakeClock{t0}
	store, eval, ev, _ := wired(t, clock, cpuRule(2))
	high(t, store, t0, 3*time.Minute)
	assert.DeepEqual(t, ev.states(), []string{"firing"})
	assert.Equal(t, len(eval.Active(alertItem)), 1)
	eval.Resolve(alertItem)
	assert.DeepEqual(t, ev.states(), []string{"firing", "resolved"})
	assert.Equal(t, len(eval.Active(alertItem)), 0)
	eval.Resolve(alertItem)
	assert.DeepEqual(t, ev.states(), []string{"firing", "resolved"})
}

func TestAMinuteWithoutRecordDoesNotResolve(t *testing.T) {
	clock := &fakeClock{t0}
	store, eval, ev, _ := wired(t, clock, cpuRule(2))
	high(t, store, t0, 3*time.Minute)
	assert.DeepEqual(t, ev.states(), []string{"firing"})
	// three minutes of silence, then high again
	high(t, store, t0.Add(7*time.Minute), 3*time.Minute)
	assert.DeepEqual(t, ev.states(), []string{"firing"})
	assert.Equal(t, len(eval.Active(alertItem)), 1)
}

func TestDisablingARuleResolvesItsAlert(t *testing.T) {
	clock := &fakeClock{t0}
	store, eval, ev, _ := wired(t, clock, cpuRule(2))
	high(t, store, t0, 3*time.Minute)
	assert.DeepEqual(t, ev.states(), []string{"firing"})
	off := cpuRule(2)
	off.Enabled = false
	assert.NilError(t, eval.SetRules(alertItem, []stats.Rule{off}))
	assert.DeepEqual(t, ev.states(), []string{"firing", "resolved"})
	assert.Equal(t, len(eval.Active(alertItem)), 0)
}

func TestTheHookFiresOncePerWrittenMinute(t *testing.T) {
	clock := &fakeClock{t0}
	store, _, ev, _ := wired(t, clock, cpuRule(1))
	high(t, store, t0.Add(5*time.Second), 50*time.Second)
	assert.Equal(t, len(ev.states()), 0, "minute 12:00 is not written before 12:01 arrives")
	assert.NilError(t, store.Record(alertItem, t0.Add(65*time.Second), stats.Sample{"alert/cpu_high": 100}))
	assert.DeepEqual(t, ev.states(), []string{"firing"})
	assert.Equal(t, ev.all[0].Since.UTC(), t0)
}

func TestAPointMarshalsAsAnArrayWithNulls(t *testing.T) {
	avg := 12.5
	b, err := json.Marshal(stats.Point{T: 1760000000, Avg: &avg})
	assert.NilError(t, err)
	assert.Equal(t, string(b), "[1760000000,12.5,null,null]")
}

func TestAResultMarshalsWithTheContractsNames(t *testing.T) {
	b, err := json.Marshal(stats.Result{Range: stats.Range1h, StepSeconds: 60, From: 1, To: 2})
	assert.NilError(t, err)
	for _, want := range []string{`"series":[]`, `"step_seconds"`, `"range"`, `"from"`, `"to"`} {
		assert.Assert(t, strings.Contains(string(b), want), "%s missing in %s", want, b)
	}
}

func TestSweepKeepsTheEvaluatorsFiles(t *testing.T) {
	clock := &fakeClock{t0}
	dir := t.TempDir()
	planted := []string{"alerts.json", "alerts.json.tmp", "alerts.json.bad"}
	for _, n := range planted {
		assert.NilError(t, os.WriteFile(filepath.Join(dir, n), []byte("{}"), 0o644))
	}
	store, err := stats.Open(dir, clock)
	assert.NilError(t, err)
	defer store.Close()
	check := func() {
		t.Helper()
		for _, n := range planted {
			_, err := os.Stat(filepath.Join(dir, n))
			assert.NilError(t, err, n)
		}
	}
	check()
	assert.NilError(t, store.Sweep(t0))
	assert.NilError(t, store.Sweep(t0.Add(8*24*time.Hour)))
	check()
	assert.NilError(t, store.Close())
	store, err = stats.Open(dir, &fakeClock{t0.Add(8 * 24 * time.Hour)})
	assert.NilError(t, err)
	defer store.Close()
	check()
}

func TestSetRulesRefusesOutOfRangeValues(t *testing.T) {
	_, eval, _, _ := wired(t, &fakeClock{t0})
	for _, c := range []struct {
		rule  stats.Rule
		field string
	}{
		{stats.Rule{Key: "cpu_high", Enabled: true, Threshold: 0, Minutes: 10}, "threshold"},
		{stats.Rule{Key: "cpu_high", Enabled: true, Threshold: 101, Minutes: 10}, "threshold"},
		{stats.Rule{Key: "cpu_high", Enabled: true, Threshold: 90, Minutes: 0}, "minutes"},
		{stats.Rule{Key: "cpu_high", Enabled: true, Threshold: 90, Minutes: 121}, "minutes"},
	} {
		err := eval.SetRules(alertItem, []stats.Rule{c.rule})
		var re *stats.RuleError
		assert.Assert(t, errors.As(err, &re), "%+v: %v", c.rule, err)
		assert.Equal(t, re.Index, 0)
		assert.Equal(t, re.Field, c.field)
	}
}

func TestAnEvaluatorStateSurvivesReopening(t *testing.T) {
	clock := &fakeClock{t0}
	store, eval, ev, dir := wired(t, clock, cpuRule(2))
	high(t, store, t0, 3*time.Minute)
	assert.DeepEqual(t, ev.states(), []string{"firing"})
	assert.Equal(t, len(eval.Active(alertItem)), 1)
	items := store.Items()

	again, err := stats.NewEvaluator(store, dir, nil)
	assert.NilError(t, err)
	assert.Equal(t, len(again.Active(alertItem)), 1)
	assert.Equal(t, again.Active(alertItem)[0].Key, "cpu_high")
	assert.DeepEqual(t, store.Items(), items)
}
