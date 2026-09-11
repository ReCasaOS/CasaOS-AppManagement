package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
)

// Holding an app still while it is copied.
//
// Copying a database while it is writing produces a backup that looks fine and
// does not restore. The only general answer is to stop the app for as long as the
// copy takes -- which is a real outage, chosen deliberately, and the manifest
// records which of the two was done.
//
// Everything here is written around one property: whatever happens, the
// containers that were running before come back. A backup that fails is a bad
// afternoon; a backup that fails and leaves the app off is an outage nobody is
// watching for.

// containerStopStarter is the narrow half of DockerService this needs. Narrow so
// the guarantee above can be tested against a fake that fails on demand, which is
// the only way to know the recovery paths work.
type containerStopStarter interface {
	StopContainer(id string) error
	StartContainer(id string) error
}

// heldApp is the state to undo: the containers that were running, in the order
// they were stopped.
type heldApp struct {
	docker  containerStopStarter
	app     string
	stopped []string
}

// runningContainerIDs is every container of the app that is up, in a stable order.
//
// Only running ones: stopping what is already stopped does nothing, and starting
// it afterwards would turn a backup into a way of starting an app the owner had
// deliberately switched off.
func runningContainerIDs(containerLists map[string][]codegen.ContainerSummary) []string {
	ids := []string{}

	for _, list := range containerLists {
		for _, container := range list {
			// `restarting` and `paused` are not settled states to copy from either,
			// but stopping them is the same call and they come back the same way.
			switch container.State {
			case "running", "restarting", "paused":
				if container.ID != "" {
					ids = append(ids, container.ID)
				}
			}
		}
	}

	sort.Strings(ids)

	return ids
}

// holdApp stops the app's running containers and returns what will start them
// again.
//
// The release function is returned even when this fails, and it is never nil: a
// caller that defers it immediately cannot forget, and a failure part-way through
// has already put some containers down that must come back up. Returning nil on
// the error path is how half an app stays stopped.
func holdApp(ctx context.Context, docker containerStopStarter, app string, containerLists map[string][]codegen.ContainerSummary) (release func() error, err error) {
	held := &heldApp{docker: docker, app: app}

	running := runningContainerIDs(containerLists)

	// Written down BEFORE the first stop. A process killed between the two is
	// exactly what this note exists for, and one written afterwards would not be
	// there yet. A note that cannot be written is not fatal -- the backup still
	// runs and the deferred release still covers every failure except the one
	// where nothing runs at all.
	if err := rememberHeld(app, running, time.Now()); err != nil {
		logger.Error("could not write down which containers are being stopped", zap.Error(err), zap.String("app", app))
	}

	for _, id := range running {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return held.release, ctxErr
		}

		if stopErr := docker.StopContainer(id); stopErr != nil {
			// Whatever is already down comes back up: an app half stopped is worse
			// than one that was never touched, and the caller cannot tell how far
			// this got.
			return held.release, fmt.Errorf("could not stop container %s, so the backup did not run: %w", id, stopErr)
		}

		held.stopped = append(held.stopped, id)
	}

	return held.release, nil
}

// release starts back exactly the containers this stopped, and says what it could
// not.
//
// In reverse order, so a stack whose services depend on each other comes back the
// way it went down. Every container is attempted even after one fails -- stopping
// at the first error would leave the rest off for the sake of tidier reporting.
func (h *heldApp) release() error {
	failures := []error{}

	for i := len(h.stopped) - 1; i >= 0; i-- {
		if err := h.docker.StartContainer(h.stopped[i]); err != nil {
			failures = append(failures, fmt.Errorf("container %s did not start again: %w", h.stopped[i], err))
		}
	}

	h.stopped = nil

	// Erased only once every container has been attempted. A note left behind by a
	// release that half-failed is a note that gets acted on at the next startup,
	// which is the right outcome.
	if err := forgetHeld(h.app); err != nil {
		failures = append(failures, err)
	}

	return errors.Join(failures...)
}
