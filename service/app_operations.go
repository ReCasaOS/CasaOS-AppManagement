package service

import (
	"fmt"
	"sync"
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

// handOver renames what holds app when an operation passes its hold to one it starts, so
// that a refusal names what really runs.
func handOver(app, kind string) {
	appOperations.Lock()
	defer appOperations.Unlock()

	if _, ok := appOperations.running[app]; ok {
		appOperations.running[app] = kind
	}
}
