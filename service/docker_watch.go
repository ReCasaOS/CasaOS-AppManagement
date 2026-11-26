package service

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen/message_bus"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	client2 "github.com/docker/docker/client"
	"go.uber.org/zap"
)

// The Docker watch: what happens to an app's containers when nobody asked for it.
//
// A crash, a failing health check or a restart loop goes through no operation that would
// report it, and it is what the owner wants to hear about away from the dashboard. The
// watch follows Docker's events for the containers of the compose projects -- the apps --
// and publishes four events, which the core's alerts turn into messages. What an event
// means is decided by observe and tick, pure over the events and the Begin registry, so
// that the decisions are tested without a daemon:
//
//  1. a die with a non-zero exit code is app:container-died, unless an operation holds
//     the app or somebody asked for the stop;
//  2. health_status: unhealthy is app:container-unhealthy, held or not: an operation that
//     ends leaves it unhealthy, and Docker says so only once;
//  3. more than restartsForALoop starts of a container within watchWindow, none of them
//     asked for nor an operation's, is app:container-restarting, once an episode;
//  4. after those, every troubled container of the app running and not unhealthy for
//     watchWindow is app:container-healthy, once for the app, which ends the episode:
//     the core's alert is the app's, not a container's.
//
// heal, pure too, decides which containers the watch restarts: one running and unhealthy
// for healAfter, unless the owner turned it off or anything runs on the app, at most
// healBudget times an app within healWindow. Past that it says so once an episode, and
// leaves the app alone. Its own restarts are asked for: no death, no step towards a loop.

const (
	healAfter  = 5 * time.Minute
	healWindow = 6 * time.Hour
	healBudget = 3

	healRestartTimeout = 2 * time.Minute
	healStopSeconds    = 60 // well within healRestartTimeout
)

// AutoHealEnabled is whether the owner lets the watch restart an unhealthy container.
var AutoHealEnabled = func() bool { return true }

const (
	// watchWindow is how recent the starts of a restart loop are, and how long a
	// container runs well before it is healthy again.
	watchWindow = 10 * time.Minute

	restartsForALoop = 3

	// stopGrace is how long after a kill a die or a start is the stop or the restart
	// somebody asked for: Docker sends the stop signal, then SIGKILL once the grace
	// period is over, and logs a kill for each. A kill that stopped nothing -- a
	// SIGHUP that reloads a configuration -- counts for no longer.
	stopGrace = time.Minute
)

// containerEvent is what the watch reads of one Docker event.
type containerEvent struct {
	at        time.Time
	app       string // the compose project
	container string // the container's name
	action    string // die, kill, start or health_status: <status>
	exitCode  string // of a die
}

// watchedContainer is what the watch remembers of one container. By name, not ID:
// compose gives a container it recreates the name of the one it replaces, so an update
// carries a trouble over to the container that may end it.
type watchedContainer struct {
	app       string
	last      time.Time   // the last event, to forget a container nothing happens to
	killedAt  time.Time   // the last kill
	starts    []time.Time // the starts that count towards a loop, within watchWindow
	running   bool
	unhealthy bool
	fineSince time.Time // running and not unhealthy since
	troubled  bool      // 1, 2 or 3 was published: 4 is owed
	looping   bool      // 3 was published in this episode

	unhealthySince time.Time // the first unhealthy since it last started, died or was healthy
}

// dockerWatch decides what the events of the apps' containers mean. One goroutine uses
// it: it has no lock.
type dockerWatch struct {
	containers map[string]*watchedContainer
	restarts   map[string][]time.Time // by app, heal's restarts within healWindow
	stopped    map[string]bool        // by app, auto-restart-stopped published in this episode
}

func newDockerWatch() *dockerWatch {
	return &dockerWatch{containers: map[string]*watchedContainer{}, restarts: map[string][]time.Time{}, stopped: map[string]bool{}}
}

// published is one event the watch publishes.
type published struct {
	eventType  message_bus.EventType
	properties map[string]string
}

// observe returns what one event of an app's container publishes; held is whether an
// operation holds the app.
func (w *dockerWatch) observe(e containerEvent, held bool) []published {
	c := w.containers[e.container]
	if c == nil {
		c = &watchedContainer{}
		w.containers[e.container] = c
	}
	c.app, c.last = e.app, e.at
	asked := !c.killedAt.IsZero() && e.at.Sub(c.killedAt) <= stopGrace

	switch e.action {
	case "kill":
		c.killedAt = e.at
	case "die":
		c.running, c.unhealthySince = false, time.Time{}
		if e.exitCode != "0" && !asked && !held {
			c.troubled = true
			p := containerPublished(common.EventTypeAppContainerDied, e.app, e.container)
			p.properties[common.PropertyTypeContainerExitCode.Name] = e.exitCode

			return []published{p}
		}
	case "start":
		c.running, c.unhealthy, c.fineSince, c.unhealthySince = true, false, e.at, time.Time{}
		if asked || held {
			break
		}
		c.starts = append(slices.DeleteFunc(c.starts, func(start time.Time) bool {
			return e.at.Sub(start) >= watchWindow
		}), e.at)
		if len(c.starts) > restartsForALoop && !c.looping {
			c.troubled, c.looping = true, true

			return []published{containerPublished(common.EventTypeAppContainerRestarting, e.app, e.container)}
		}
	case "health_status: unhealthy":
		c.unhealthy, c.troubled = true, true
		if c.unhealthySince.IsZero() {
			c.unhealthySince = e.at
		}

		return []published{containerPublished(common.EventTypeAppContainerUnhealthy, e.app, e.container)}
	// Docker checks the health of a running container only: a health status says it
	// runs, even one whose start the watch did not see -- it was forgotten, or ran
	// before the watch did.
	case "health_status: healthy":
		if c.unhealthy || !c.running {
			c.fineSince = e.at
		}
		c.running, c.unhealthy, c.unhealthySince = true, false, time.Time{}
	}

	return nil
}

// heal returns the containers to restart at now, and what that publishes: enabled is
// whether the owner lets it, held whether an operation holds an app.
func (w *dockerWatch) heal(now time.Time, enabled bool, held func(app string) bool) (restart []string, out []published) {
	if !enabled {
		return nil, nil
	}
	picked := map[string]int{} // by app, restarts decided in this call
	for _, name := range slices.Sorted(maps.Keys(w.containers)) {
		c := w.containers[name]
		if !c.running || !c.unhealthy || c.unhealthySince.IsZero() || now.Sub(c.unhealthySince) < healAfter || held(c.app) {
			continue
		}
		w.restarts[c.app] = slices.DeleteFunc(w.restarts[c.app], func(at time.Time) bool {
			return now.Sub(at) >= healWindow
		})
		n := len(w.restarts[c.app]) + picked[c.app]
		if n >= healBudget {
			if !w.stopped[c.app] {
				w.stopped[c.app] = true
				out = append(out, containerPublished(common.EventTypeAppContainerAutoRestartStopped, c.app, name))
			}

			continue
		}
		picked[c.app]++
		restart = append(restart, name)
		p := containerPublished(common.EventTypeAppContainerAutoRestarted, c.app, name)
		p.properties[common.PropertyTypeAutoRestartCount.Name] = strconv.Itoa(n + 1)
		out = append(out, p)
	}

	return restart, out
}

// restarted counts a restart of name heal decided at now, done or not, and makes the die
// and start that follow asked for. A restart that failed waits healAfter again.
func (w *dockerWatch) restarted(name string, now time.Time) {
	c := w.containers[name]
	if c == nil {
		return
	}
	w.restarts[c.app] = append(w.restarts[c.app], now)
	c.killedAt = now
	if !c.unhealthySince.IsZero() {
		c.unhealthySince = now
	}
}

// healNow restarts with restart what heal decides at now, and returns what that publishes.
// Each restart holds its app, as an operation would: an app something started on since
// heal decided -- held tells, Begin refuses -- is left alone, and nothing is said of it. A
// restart that failed counts in the budget, but neither the alert nor the journal says it
// was done: the service log does.
func (w *dockerWatch) healNow(now time.Time, enabled bool, held func(app string) bool, restart func(name string) error) []published {
	names, out := w.heal(now, enabled, held)
	untold := map[string]bool{}
	for _, name := range names {
		app := w.containers[name].app
		if held(app) {
			untold[name] = true

			continue
		}
		end, err := Begin(app, "auto-restart")
		if err != nil {
			logger.Info("an unhealthy container was not restarted: its app is busy", zap.Error(err), zap.String("container", name))
			untold[name] = true

			continue
		}
		err = restart(name)
		end()
		if err != nil {
			logger.Error("an unhealthy container could not be restarted", zap.Error(err), zap.String("container", name))
			untold[name] = true
		} else {
			RecordAppEvent(app, "auto-restarted", "", "", name)
		}
		w.restarted(name, now)
	}

	return slices.DeleteFunc(out, func(p published) bool {
		return p.eventType.Name == common.EventTypeAppContainerAutoRestarted.Name && untold[p.properties[common.PropertyTypeContainerName.Name]]
	})
}

// tick returns what the time passing publishes at now: 4, for an app whose troubled
// containers have all run well for watchWindow. A container nothing happened to for as
// long, and that owes nothing, is forgotten.
func (w *dockerWatch) tick(now time.Time) []published {
	well := map[string][]string{} // by app, its troubled containers that run well again
	unwell := map[string]bool{}   // the apps with a troubled container that does not
	for name, c := range w.containers {
		switch {
		case !c.troubled:
			if now.Sub(c.last) >= watchWindow {
				delete(w.containers, name)
			}
		case c.running && !c.unhealthy && now.Sub(c.fineSince) >= watchWindow:
			well[c.app] = append(well[c.app], name)
		default:
			unwell[c.app] = true
		}
	}

	var out []published
	for app, names := range well {
		if unwell[app] {
			continue
		}
		for _, name := range names {
			w.containers[name].troubled, w.containers[name].looping = false, false
		}
		delete(w.stopped, app)
		// one event for the app, which names one of its containers
		out = append(out, containerPublished(common.EventTypeAppContainerHealthy, app, slices.Min(names)))
	}

	return out
}

// resume takes the watch up again at now, after a break in the event stream, from the
// containers that run: by name, whether each is unhealthy, and its app. Nobody watched
// meanwhile: a container may have stopped, started or recovered, and one that runs has
// run well since now at best. One the watch did not know is known from now, an unhealthy
// one as unhealthy since now: Docker said so before, and will not again.
func (w *dockerWatch) resume(now time.Time, running map[string]bool, apps map[string]string) {
	for name, c := range w.containers {
		unhealthy, runs := running[name]
		c.running, c.unhealthy, c.fineSince = runs, unhealthy, now
		if !unhealthy || !runs {
			c.unhealthySince = time.Time{}
		} else if c.unhealthySince.IsZero() {
			c.unhealthySince = now
		}
	}
	for name, unhealthy := range running {
		if w.containers[name] != nil || apps[name] == "" {
			continue
		}
		c := &watchedContainer{app: apps[name], last: now, running: true, unhealthy: unhealthy, fineSince: now, troubled: unhealthy}
		if unhealthy {
			c.unhealthySince = now
		}
		w.containers[name] = c
	}
}

func containerPublished(eventType message_bus.EventType, app, container string) published {
	return published{eventType, map[string]string{
		common.PropertyTypeAppName.Name:       app,
		common.PropertyTypeContainerName.Name: container,
	}}
}

// longestPause is how long a broken event stream waits at most before it is followed
// again. A stream that lived longer than this broke for a new reason: the next pause
// starts short again.
const longestPause = time.Minute

// WatchDocker follows Docker's events for the apps' containers until ctx ends, and
// publishes what they mean. A stream that breaks -- Docker restarting, or not up yet --
// is followed again after a pause that doubles up to longestPause.
func WatchDocker(ctx context.Context) {
	watch := newDockerWatch()
	pause := time.Second

	for {
		followed := time.Now()
		err := watch.follow(ctx)
		if ctx.Err() != nil {
			return
		}

		if time.Since(followed) > longestPause {
			pause = time.Second
		}
		logger.Error("the Docker event stream broke, following it again", zap.Error(err), zap.Duration("in", pause))

		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		pause = min(2*pause, longestPause)
	}
}

// follow reads one stream of events until it breaks, and returns why.
func (w *dockerWatch) follow(ctx context.Context) error {
	cli, err := client2.NewClientWithOpts(client2.FromEnv, client2.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	defer cli.Close()

	// ends the client's reader with the stream
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	messages, errs := cli.Events(ctx, events.ListOptions{Filters: filters.NewArgs(
		filters.Arg("type", string(events.ContainerEventType)),
		// every container's, not only the apps': the logs collector records them all
		filters.Arg("event", string(events.ActionCreate)),
		filters.Arg("event", string(events.ActionStop)),
		filters.Arg("event", string(events.ActionOOM)),
		filters.Arg("event", string(events.ActionDie)),
		filters.Arg("event", string(events.ActionKill)),
		filters.Arg("event", string(events.ActionStart)),
		filters.Arg("event", string(events.ActionHealthStatus)), // a prefix to Docker: every status
	)})

	// What happened while nobody watched, from the containers that run. Listed once
	// Docker streams the events, so that nothing happens in between unseen.
	list, err := cli.ContainerList(ctx, container.ListOptions{Filters: filters.NewArgs(
		filters.Arg("label", api.ProjectLabel),
		filters.Arg("status", string(container.StateRunning)),
	)})
	if err != nil {
		return err
	}
	running, apps := map[string]bool{}, map[string]string{}
	for _, c := range list {
		for _, name := range c.Names {
			name = strings.TrimPrefix(name, "/")
			// Docker's status of an unhealthy container: "Up 2 hours (unhealthy)"
			running[name] = strings.Contains(c.Status, "(unhealthy)")
			if c.Labels[api.OneoffLabel] != "True" {
				apps[name] = c.Labels[api.ProjectLabel]
			}
		}
	}
	w.resume(time.Now(), running, apps)

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		var out []published

		select {
		case err := <-errs:
			return err
		case now := <-ticker.C:
			out = append(w.tick(now), w.healNow(now, AutoHealEnabled(), busy, func(name string) error {
				// the loop waits meanwhile, the events queue
				ctx, cancel := context.WithTimeout(ctx, healRestartTimeout)
				defer cancel()

				// within the context's time, whatever stop grace period the app asks for
				stop := healStopSeconds

				return cli.ContainerRestart(ctx, name, container.StopOptions{Timeout: &stop})
			})...)
		case m := <-messages:
			if f := onDockerEvent.Load(); f != nil {
				(*f)(m)
			}

			app := m.Actor.Attributes[api.ProjectLabel]
			// a one-off container is somebody's `docker compose run`, not the app
			if app == "" || m.Actor.Attributes[api.OneoffLabel] == "True" {
				continue
			}
			// widened for the logs collector only: observe decides on what it always had
			switch m.Action {
			case events.ActionCreate, events.ActionStop, events.ActionOOM:
				continue
			}

			out = w.observe(containerEvent{
				at:        time.Unix(0, m.TimeNano),
				app:       app,
				container: m.Actor.Attributes["name"],
				action:    string(m.Action),
				exitCode:  m.Actor.Attributes["exitCode"],
			}, held(app))
		}

		// never held up by the message bus: the next event is already on its way
		for _, p := range out {
			if f := onWatchPublished.Load(); f != nil {
				(*f)(p)
			}
			go PublishEventWrapper(ctx, p.eventType, p.properties)
		}
	}
}
