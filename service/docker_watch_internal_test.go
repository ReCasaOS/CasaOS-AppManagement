package service

import (
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
