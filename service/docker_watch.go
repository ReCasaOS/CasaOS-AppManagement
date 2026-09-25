package service

import (
	"context"
	"slices"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen/message_bus"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/compose/v5/pkg/api"
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
//  4. after one of those, the container running and not unhealthy for watchWindow is
//     app:container-healthy, which ends the episode.

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
}

// dockerWatch decides what the events of the apps' containers mean. One goroutine uses
// it: it has no lock.
type dockerWatch struct {
	containers map[string]*watchedContainer
}

func newDockerWatch() *dockerWatch {
	return &dockerWatch{containers: map[string]*watchedContainer{}}
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
		c.running = false
		if e.exitCode != "0" && !asked && !held {
			c.troubled = true
			p := containerPublished(common.EventTypeAppContainerDied, e.app, e.container)
			p.properties[common.PropertyTypeContainerExitCode.Name] = e.exitCode

			return []published{p}
		}
	case "start":
		c.running, c.unhealthy, c.fineSince = true, false, e.at
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

		return []published{containerPublished(common.EventTypeAppContainerUnhealthy, e.app, e.container)}
	case "health_status: healthy":
		if c.unhealthy {
			c.unhealthy, c.fineSince = false, e.at
		}
	}

	return nil
}

// tick returns what the time passing publishes at now: 4, for a troubled container that
// has run well for watchWindow. A container nothing happened to for as long, and that
// owes nothing, is forgotten.
func (w *dockerWatch) tick(now time.Time) []published {
	var out []published
	for name, c := range w.containers {
		switch {
		case c.troubled && c.running && !c.unhealthy && now.Sub(c.fineSince) >= watchWindow:
			c.troubled, c.looping = false, false
			out = append(out, containerPublished(common.EventTypeAppContainerHealthy, c.app, name))
		case !c.troubled && now.Sub(c.last) >= watchWindow:
			delete(w.containers, name)
		}
	}

	return out
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

		// Nobody watched while it was broken: a container runs again once it says so,
		// and none that may have stopped meanwhile is taken for healthy.
		for _, c := range watch.containers {
			c.running = false
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
		filters.Arg("label", api.ProjectLabel),
		filters.Arg("event", string(events.ActionDie)),
		filters.Arg("event", string(events.ActionKill)),
		filters.Arg("event", string(events.ActionStart)),
		filters.Arg("event", string(events.ActionHealthStatus)), // a prefix to Docker: every status
	)})

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		var out []published

		select {
		case err := <-errs:
			return err
		case now := <-ticker.C:
			out = w.tick(now)
		case m := <-messages:
			app := m.Actor.Attributes[api.ProjectLabel]
			// a one-off container is somebody's `docker compose run`, not the app
			if app == "" || m.Actor.Attributes[api.OneoffLabel] == "True" {
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
			go PublishEventWrapper(ctx, p.eventType, p.properties)
		}
	}
}
