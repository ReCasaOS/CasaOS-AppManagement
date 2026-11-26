package service

import (
	"errors"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"gotest.tools/v3/assert"
)

// step is one thing that happens to a container of the app jarvis, at minutes after the
// first: a Docker event, the listing the watch resumes from after a break in the event
// stream, or with no action the watch's tick.
type step struct {
	at     float64
	action string
	on     string // the container, jarvis-web-1 if empty
	exit   string // of a die
	held   bool   // an operation holds jarvis
	want   string // the events published, if any
}

var (
	died       = common.EventTypeAppContainerDied.Name
	unhealthy  = common.EventTypeAppContainerUnhealthy.Name
	restarting = common.EventTypeAppContainerRestarting.Name
	healthy    = common.EventTypeAppContainerHealthy.Name
)

// what the listing of the running containers says of the step's container
const (
	listed          = "listed running"
	listedUnhealthy = "listed unhealthy"
	notListed       = "not listed"
)

func TestTheDockerWatchDecisions(t *testing.T) {
	first := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		steps []step
	}{
		{"1. a crash", []step{{at: 0, action: "die", exit: "1", want: died}}},
		{"1. an exit that went well", []step{{at: 0, action: "die", exit: "0"}}},
		{"1. while an operation holds the app", []step{{at: 0, action: "die", exit: "137", held: true}}},
		{"1. a stop somebody asked for", []step{
			{at: 0, action: "kill"},
			{at: 0.2, action: "kill"}, // the SIGKILL at the end of the grace period
			{at: 0.2, action: "die", exit: "137"},
		}},
		{"1. long after a signal that stopped nothing", []step{
			{at: 0, action: "kill"},
			{at: 5, action: "die", exit: "1", want: died},
		}},
		{"1. every crash says so", []step{
			{at: 0, action: "die", exit: "1", want: died},
			{at: 0.1, action: "start"},
			{at: 0.2, action: "die", exit: "1", want: died},
		}},
		{"1. a crash stops the container", []step{
			{at: 0, action: "start"},
			{at: 1, action: "die", exit: "1", want: died},
			{at: 31},
		}},

		{"2. unhealthy", []step{
			{at: 0, action: "start"},
			{at: 1, action: "health_status: unhealthy", want: unhealthy},
		}},
		{"2. even while an operation holds the app, which would not say so", []step{
			{at: 0, action: "health_status: unhealthy", held: true, want: unhealthy},
		}},

		{"3. a fourth start within ten minutes", []step{
			{at: 0, action: "start"},
			{at: 3, action: "start"},
			{at: 6, action: "start"},
			{at: 9, action: "start", want: restarting},
		}},
		{"3. once an episode", []step{
			{at: 0, action: "start"},
			{at: 1, action: "start"},
			{at: 2, action: "start"},
			{at: 3, action: "start", want: restarting},
			{at: 4, action: "start"},
			{at: 5, action: "start"},
		}},
		{"3. four starts over more than ten minutes", []step{
			{at: 0, action: "start"},
			{at: 4, action: "start"},
			{at: 8, action: "start"},
			{at: 12, action: "start"},
		}},
		{"3. restarts somebody asked for", []step{
			{at: 0, action: "start"},
			{at: 1, action: "kill"}, {at: 1.1, action: "start"},
			{at: 2, action: "kill"}, {at: 2.1, action: "start"},
			{at: 3, action: "kill"}, {at: 3.1, action: "start"},
		}},
		{"3. what an operation starts", []step{
			{at: 0, action: "start", held: true},
			{at: 1, action: "start", held: true},
			{at: 2, action: "start", held: true},
			{at: 3, action: "start", held: true},
		}},

		{"4. ten minutes running after a crash", []step{
			{at: 0, action: "die", exit: "1", want: died},
			{at: 1, action: "start"},
			{at: 10.9},
			{at: 11, want: healthy},
			{at: 30},
		}},
		{"4. never while it does not run", []step{
			{at: 0, action: "die", exit: "1", want: died},
			{at: 30},
		}},
		{"4. never while it is unhealthy", []step{
			{at: 0, action: "start"},
			{at: 1, action: "health_status: unhealthy", want: unhealthy},
			{at: 30},
			{at: 31, action: "health_status: healthy"},
			{at: 40.9},
			{at: 41, want: healthy},
		}},
		{"4. ten minutes after the restarts stop, and the next loop is a new episode", []step{
			{at: 0, action: "start"},
			{at: 1, action: "start"},
			{at: 2, action: "start"},
			{at: 3, action: "start", want: restarting},
			{at: 13, want: healthy},
			{at: 20, action: "start"},
			{at: 21, action: "start"},
			{at: 22, action: "start"},
			{at: 23, action: "start", want: restarting},
		}},
		{"4. never without a trouble first", []step{
			{at: 0, action: "start"},
			{at: 30},
		}},
		{"4. a container forgotten, then unhealthy, then healthy", []step{
			{at: 0, action: "start"},
			{at: 20}, // forgotten: nothing happened to it for ten minutes
			{at: 60, action: "health_status: unhealthy", want: unhealthy},
			{at: 65, action: "health_status: healthy"},
			{at: 74.9},
			{at: 75, want: healthy},
		}},
		{"4. unhealthy as the watch starts, then healthy", []step{
			{at: 0, action: "health_status: unhealthy", want: unhealthy},
			{at: 1, action: "health_status: healthy"},
			{at: 10.9},
			{at: 11, want: healthy},
		}},
		{"4. a health status says the container runs, even when its start went unseen", []step{
			{at: 0, action: "die", exit: "1", want: died},
			{at: 1, action: "health_status: healthy"},
			{at: 10.9},
			{at: 11, want: healthy},
		}},
		{"4. an app is healthy again once all its containers are", []step{
			{at: 1, action: "health_status: unhealthy", want: unhealthy},
			{at: 2, action: "die", on: "jarvis-db-1", exit: "1", want: died},
			{at: 2.1, action: "start", on: "jarvis-db-1"},
			{at: 20},
			{at: 21, action: "health_status: healthy"},
			{at: 30.9},
			{at: 31, want: healthy}, // once, for the app
			{at: 50},
		}},

		{"4. after a break in the stream, a container that started meanwhile", []step{
			{at: 0, action: "die", exit: "1", want: died},
			{at: 5, action: listed},
			{at: 14.9},
			{at: 15, want: healthy},
		}},
		{"4. after a break in the stream, a container that stopped meanwhile", []step{
			{at: 0, action: "die", exit: "1", want: died},
			{at: 1, action: "start"},
			{at: 5, action: notListed},
			{at: 30},
		}},
		{"4. after a break in the stream, a container still unhealthy", []step{
			{at: 0, action: "health_status: unhealthy", want: unhealthy},
			{at: 5, action: listedUnhealthy},
			{at: 30},
			{at: 31, action: "health_status: healthy"},
			{at: 41, want: healthy},
		}},
		{"4. after a break in the stream, a container that recovered meanwhile", []step{
			{at: 0, action: "health_status: unhealthy", want: unhealthy},
			{at: 5, action: listed},
			{at: 14.9},
			{at: 15, want: healthy},
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			watch := newDockerWatch()
			for i, s := range c.steps {
				at := first.Add(time.Duration(s.at * float64(time.Minute)))
				container := s.on
				if container == "" {
					container = "jarvis-web-1"
				}

				var out []published
				switch s.action {
				case "":
					out = watch.tick(at)
				case listed, listedUnhealthy, notListed:
					running := map[string]bool{}
					if s.action != notListed {
						running[container] = s.action == listedUnhealthy
					}
					watch.resume(at, running)
				default:
					out = watch.observe(containerEvent{at: at, app: "jarvis", container: container, action: s.action, exitCode: s.exit}, s.held)
				}

				got := ""
				for _, p := range out {
					got += p.eventType.Name
				}
				assert.Equal(t, got, s.want, "step %d", i)
			}
		})
	}
}

// The core's alerts say which app, which container and how it ended.
func TestTheDockerWatchSaysWhatDied(t *testing.T) {
	watch := newDockerWatch()

	out := watch.observe(containerEvent{at: time.Now(), app: "jarvis", container: "jarvis-web-1", action: "die", exitCode: "1"}, false)

	assert.Equal(t, len(out), 1)
	assert.Equal(t, out[0].eventType.Name, died)
	assert.DeepEqual(t, out[0].properties, map[string]string{
		"app:name":                   "jarvis",
		"docker:container:name":      "jarvis-web-1",
		"docker:container:exit-code": "1",
	})
}

var (
	autoRestarted      = common.EventTypeAppContainerAutoRestarted.Name
	autoRestartStopped = common.EventTypeAppContainerAutoRestartStopped.Name
)

func notHeld(string) bool { return false }

// sick makes jarvis-web-1 start and turn unhealthy at at.
func sick(w *dockerWatch, at time.Time) {
	w.observe(containerEvent{at: at, app: "jarvis", container: "jarvis-web-1", action: "start"}, false)
	w.observe(containerEvent{at: at, app: "jarvis", container: "jarvis-web-1", action: "health_status: unhealthy"}, false)
}

func eventNames(out []published) string {
	got := ""
	for _, p := range out {
		got += p.eventType.Name
	}
	return got
}

// heals expects heal at at to restart jarvis-web-1 as the count-th in the window, then restarts it.
func heals(t *testing.T, w *dockerWatch, at time.Time, count string) {
	t.Helper()
	restart, out := w.heal(at, true, notHeld)
	assert.DeepEqual(t, restart, []string{"jarvis-web-1"})
	assert.Equal(t, len(out), 1)
	assert.Equal(t, out[0].eventType.Name, autoRestarted)
	assert.Equal(t, out[0].properties[common.PropertyTypeAutoRestartCount.Name], count)
	w.restarted("jarvis-web-1", at)
}

func TestHealRestartsAfterFiveMinutes(t *testing.T) {
	first := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	w := newDockerWatch()
	sick(w, first)

	restart, out := w.heal(first.Add(healAfter-time.Second), true, notHeld)
	assert.Equal(t, len(restart), 0)
	assert.Equal(t, len(out), 0)

	restart, out = w.heal(first.Add(healAfter), true, notHeld)
	assert.DeepEqual(t, restart, []string{"jarvis-web-1"})
	assert.Equal(t, len(out), 1)
	assert.Equal(t, out[0].eventType.Name, autoRestarted)
	assert.DeepEqual(t, out[0].properties, map[string]string{
		"app:name":              "jarvis",
		"docker:container:name": "jarvis-web-1",
		"auto-restart:count":    "1",
	})
}

func TestHealNotWhileHeld(t *testing.T) {
	first := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	w := newDockerWatch()
	sick(w, first)

	restart, out := w.heal(first.Add(time.Hour), true, func(app string) bool { return app == "jarvis" })
	assert.Equal(t, len(restart), 0)
	assert.Equal(t, len(out), 0)
}

func TestHealNotWhenOff(t *testing.T) {
	first := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	w := newDockerWatch()
	sick(w, first)

	restart, out := w.heal(first.Add(time.Hour), false, notHeld)
	assert.Equal(t, len(restart), 0)
	assert.Equal(t, len(out), 0)
}

func TestHealResetsWhenHealthyInBetween(t *testing.T) {
	first := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	w := newDockerWatch()
	sick(w, first)
	w.observe(containerEvent{at: first.Add(3 * time.Minute), app: "jarvis", container: "jarvis-web-1", action: "health_status: healthy"}, false)
	w.observe(containerEvent{at: first.Add(4 * time.Minute), app: "jarvis", container: "jarvis-web-1", action: "health_status: unhealthy"}, false)

	restart, _ := w.heal(first.Add(healAfter), true, notHeld)
	assert.Equal(t, len(restart), 0)
	heals(t, w, first.Add(4*time.Minute+healAfter), "1")
}

func TestHealBudgetThreeInSixHours(t *testing.T) {
	first := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	w := newDockerWatch()
	for i, count := range []string{"1", "2", "3"} {
		at := first.Add(time.Duration(i) * time.Hour)
		sick(w, at)
		heals(t, w, at.Add(healAfter), count)
	}

	fourth := first.Add(3 * time.Hour)
	sick(w, fourth)
	got := ""
	for m := 5; m < 30; m++ {
		restart, out := w.heal(fourth.Add(time.Duration(m)*time.Minute), true, notHeld)
		assert.Equal(t, len(restart), 0, "minute %d", m)
		got += eventNames(out)
	}
	assert.Equal(t, got, autoRestartStopped)
}

func TestHealBudgetSlides(t *testing.T) {
	first := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	w := newDockerWatch()
	for i, count := range []string{"1", "2", "3"} {
		at := first.Add(time.Duration(i) * time.Hour)
		sick(w, at)
		heals(t, w, at.Add(healAfter), count)
	}

	// the first restart, at 5 min, is out of the window: the others are the 1st and 2nd
	at := first.Add(healWindow + time.Minute)
	sick(w, at)
	heals(t, w, at.Add(healAfter), "3")
}

func TestHealIgnoresStoppedContainer(t *testing.T) {
	first := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	w := newDockerWatch()
	sick(w, first)
	w.observe(containerEvent{at: first.Add(time.Minute), app: "jarvis", container: "jarvis-web-1", action: "die", exitCode: "1"}, false)

	restart, out := w.heal(first.Add(time.Hour), true, notHeld)
	assert.Equal(t, len(restart), 0)
	assert.Equal(t, len(out), 0)
}

// The box's own restart is asked for: no death, and no step towards a loop -- with the
// kill Docker logs, or without it.
func TestOwnRestartIsNotADeathNorALoop(t *testing.T) {
	first := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	w := newDockerWatch()
	sick(w, first)

	for i := range 6 {
		at := first.Add(healAfter + time.Duration(i)*30*time.Second)
		w.restarted("jarvis-web-1", at)
		var out []published
		if i%2 == 0 {
			out = append(out, w.observe(containerEvent{at: at.Add(time.Second), app: "jarvis", container: "jarvis-web-1", action: "kill"}, false)...)
		}
		out = append(out, w.observe(containerEvent{at: at.Add(2 * time.Second), app: "jarvis", container: "jarvis-web-1", action: "die", exitCode: "137"}, false)...)
		out = append(out, w.observe(containerEvent{at: at.Add(3 * time.Second), app: "jarvis", container: "jarvis-web-1", action: "start"}, false)...)
		assert.Equal(t, eventNames(out), "", "restart %d", i)
	}
	assert.Equal(t, len(w.containers["jarvis-web-1"].starts), 1) // sick's own start only
}

func TestStoppedResetsWhenHealthyAgain(t *testing.T) {
	first := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	w := newDockerWatch()
	for i, count := range []string{"1", "2", "3"} {
		at := first.Add(time.Duration(i) * 10 * time.Minute)
		sick(w, at)
		heals(t, w, at.Add(healAfter), count)
	}

	sick(w, first.Add(30*time.Minute))
	_, out := w.heal(first.Add(35*time.Minute), true, notHeld)
	assert.Equal(t, eventNames(out), autoRestartStopped)

	w.observe(containerEvent{at: first.Add(40 * time.Minute), app: "jarvis", container: "jarvis-web-1", action: "health_status: healthy"}, false)
	assert.Equal(t, eventNames(w.tick(first.Add(50*time.Minute))), healthy)

	w.observe(containerEvent{at: first.Add(55 * time.Minute), app: "jarvis", container: "jarvis-web-1", action: "health_status: unhealthy"}, false)
	restart, out := w.heal(first.Add(60*time.Minute), true, notHeld)
	assert.Equal(t, len(restart), 0)
	assert.Equal(t, eventNames(out), autoRestartStopped)
}

// A restart that failed counts in the budget and waits healAfter again, but says nothing.
func TestHealFailedRestartCountsButSaysNothing(t *testing.T) {
	first := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	w := newDockerWatch()
	sick(w, first)

	tried := 0
	out := w.healNow(first.Add(healAfter), true, notHeld, func(string) error { tried++; return errors.New("no such container") })
	assert.Equal(t, tried, 1)
	assert.Equal(t, eventNames(out), "")
	assert.Equal(t, len(w.restarts["jarvis"]), 1)

	restart, _ := w.heal(first.Add(healAfter+4*time.Minute), true, notHeld)
	assert.Equal(t, len(restart), 0)

	out = w.healNow(first.Add(2*healAfter), true, notHeld, func(string) error { return nil })
	assert.Equal(t, eventNames(out), autoRestarted)
	assert.Equal(t, out[0].properties[common.PropertyTypeAutoRestartCount.Name], "2")
}

// A backup that copies the app live only marks it: still, nothing is restarted under it.
func TestHealNotWhileABackupRuns(t *testing.T) {
	first := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	w := newDockerWatch()
	sick(w, first)

	done := markInProgress("jarvis", "backup")
	tried := 0
	out := w.healNow(first.Add(time.Hour), true, busy, func(string) error { tried++; return nil })
	done()
	assert.Equal(t, tried, 0)
	assert.Equal(t, eventNames(out), "")

	out = w.healNow(first.Add(time.Hour), true, busy, func(string) error { tried++; return nil })
	assert.Equal(t, tried, 1)
	assert.Equal(t, eventNames(out), autoRestarted)
}
