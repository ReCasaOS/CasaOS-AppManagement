package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A wait that ran out of time must come back as an error. compose v2.27 returns nil
// once the context is cancelled, so without the deadline check an app that never came
// up is recorded as a successful apply and never rolled back -- and without the
// deadline itself this test does not finish at all.
func TestWaitForAppReportsBlownDeadline(t *testing.T) {
	defer func(previous time.Duration) { upWaitTimeout = previous }(upWaitTimeout)
	upWaitTimeout = 50 * time.Millisecond

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
