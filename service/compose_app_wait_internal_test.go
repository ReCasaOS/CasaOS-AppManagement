package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inkly/CasaOS-AppManagement/pkg/config"
)

// shortestUpWaitTimeout is the shortest deadline the config accepts: below a second is
// an unset key or a typo, so a test that wants to watch one expire has to wait it out.
const shortestUpWaitTimeout = time.Second

func setUpWaitTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	previous := config.AppInfo.UpWaitTimeout
	t.Cleanup(func() { config.AppInfo.UpWaitTimeout = previous })
	config.AppInfo.UpWaitTimeout = d
}

// A wait that ran out of time must come back as an error. compose v2.27 returns nil
// once the context is cancelled, so without the deadline check an app that never came
// up is recorded as a successful apply -- and without the deadline itself this test
// does not finish at all.
func TestWaitForAppReportsBlownDeadline(t *testing.T) {
	setUpWaitTimeout(t, shortestUpWaitTimeout)

	err := waitForApp(context.Background(), "gluetun-stack", func(ctx context.Context) error {
		<-ctx.Done() // what compose does: poll until the context is done, then say nothing
		return nil
	})
	if err == nil {
		t.Fatal("a wait that hit its deadline must be an error, got nil")
	}

	if err := waitForApp(context.Background(), "quick", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("an app that came up must not be an error: %v", err)
	}

	failed := errors.New("no such image")
	if err := waitForApp(context.Background(), "broken", func(context.Context) error { return failed }); !errors.Is(err, failed) {
		t.Fatalf("compose's own error must reach the caller unchanged, got %v", err)
	}
}

// The deadline must not eat work that succeeded. A stack with a one-shot init container
// nobody depends on can never satisfy compose's running-or-healthy wait, however long it
// is given: its containers are up on the new definition when the deadline fires, so the
// apply must keep the file the user edited instead of restoring the backup over it.
func TestApplyKeepsTheNewFileWhenOnlyTheWaitTimedOut(t *testing.T) {
	setUpWaitTimeout(t, shortestUpWaitTimeout)

	timedOut := waitForApp(context.Background(), "db-with-migrate", func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	})
	if !errors.Is(timedOut, errUpNotConfirmed) {
		t.Fatalf("a blown deadline must be errUpNotConfirmed so the apply can tell it from a failure, got %v", timedOut)
	}
	if !keepNewDefinition(timedOut) {
		t.Fatal("an app that is up but unconfirmed must keep the new compose file, not be rolled back")
	}

	// and everything else is still a failed apply
	if keepNewDefinition(errors.New("no such image: nginx:9.9")) {
		t.Fatal("an app that failed to start must be rolled back")
	}
	if !keepNewDefinition(nil) {
		t.Fatal("an app that came up must keep the new compose file")
	}

	// a caller that cancels is not an app that came up
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := waitForApp(ctx, "aborted", func(context.Context) error { return nil })
	if !errors.Is(cancelled, context.Canceled) || keepNewDefinition(cancelled) {
		t.Fatalf("a cancelled wait must roll back, got %v", cancelled)
	}
}

// The deadline is a knob: five fixed minutes is not defensible on the hardware this
// runs on -- a slow ARM box with a VPN handshake and two chained 60s start periods.
func TestUpWaitTimeoutComesFromConfig(t *testing.T) {
	setUpWaitTimeout(t, 20*time.Minute)
	if got := upWaitTimeout(); got != 20*time.Minute {
		t.Fatalf("UpWaitTimeout from app-management.conf must be used, got %s", got)
	}

	// ini leaves the field zero when the key is absent, and parses a unit-less `300`
	// as 300ns: neither may become the deadline in force
	for _, unset := range []time.Duration{0, 300, -time.Minute} {
		setUpWaitTimeout(t, unset)
		if got := upWaitTimeout(); got != defaultUpWaitTimeout {
			t.Fatalf("a %s config value must fall back to the default, got %s", unset, got)
		}
	}
}
