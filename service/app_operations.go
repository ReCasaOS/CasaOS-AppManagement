package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// One operation per app.
//
// Installs, updates, applies and backup holds each keep a marker of their own for their
// own readers, and none of them looks at the others': a settings save could land in the
// middle of a deployment that is switching the app's containers. This is the one place
// that refuses an overlap. The markers stay for what reads them.

// ErrAppBusy is the refusal, naming what holds the app. The API answers it with 409.
type ErrAppBusy struct {
	App     string
	Running string
}

func (e ErrAppBusy) Error() string {
	return fmt.Sprintf("`%s` is busy: %s in progress", e.App, e.Running)
}

var appOperations = struct {
	sync.Mutex
	running map[string]string
}{running: map[string]string{}}

// Begin claims app for an operation of the given kind, or refuses with ErrAppBusy. end
// releases it; calling it more than once is harmless.
func Begin(app, kind string) (end func(), err error) {
	appOperations.Lock()
	defer appOperations.Unlock()

	if running, ok := appOperations.running[app]; ok {
		return nil, ErrAppBusy{App: app, Running: running}
	}
	appOperations.running[app] = kind

	var once sync.Once

	return func() {
		once.Do(func() {
			appOperations.Lock()
			defer appOperations.Unlock()
			delete(appOperations.running, app)
		})
	}, nil
}

// howOftenToAskAgain is the gap between two attempts at a held app. Short enough
// that a hold let go is picked up without anybody noticing, long enough to be
// nothing at all next to the operations it waits for.
const howOftenToAskAgain = 250 * time.Millisecond

// beginWaiting claims app like Begin, except that a hold somebody else has makes it
// wait for its turn instead of refusing. It gives up with that same refusal when
// patience runs out or ctx ends.
//
// Only an operation that can sensibly queue should use it. An install, an update or a
// settings save is the answer to somebody clicking, and a refusal they can read beats
// a request that hangs. A backup is the other kind: it was asked for by a timer, or
// the moment after an install whose hold is about to be let go, and nobody meant it to
// fail because it arrived a few seconds early.
func beginWaiting(ctx context.Context, app, kind string, patience time.Duration) (end func(), err error) {
	deadline := time.Now().Add(patience)
	waited := false

	for {
		end, err := Begin(app, kind)
		if err == nil {
			if waited {
				logger.Info("the app came free", zap.String("app", app), zap.String("for", kind))
			}

			return end, nil
		}

		if !waited {
			waited = true

			var busy ErrAppBusy
			_ = errors.As(err, &busy)
			logger.Info("waiting for the app to come free",
				zap.String("app", app), zap.String("for", kind), zap.String("held_by", busy.Running))
		}

		if time.Now().After(deadline) {
			return nil, err
		}

		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(howOftenToAskAgain):
		}
	}
}

// handOver renames what holds app when an operation passes its hold to one it starts, so
// that a refusal names what really runs.
func handOver(app, kind string) {
	appOperations.Lock()
	defer appOperations.Unlock()

	if _, ok := appOperations.running[app]; ok {
		appOperations.running[app] = kind
	}
}
