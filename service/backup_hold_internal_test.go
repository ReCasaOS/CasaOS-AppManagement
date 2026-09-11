package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
)

// A Docker that can be told to fail on a particular container, which is the only
// way to find out whether the recovery paths work.
type fakeDocker struct {
	stopped   []string
	started   []string
	failStop  map[string]error
	failStart map[string]error
}

func (f *fakeDocker) StopContainer(id string) error {
	if err := f.failStop[id]; err != nil {
		return err
	}

	f.stopped = append(f.stopped, id)

	return nil
}

func (f *fakeDocker) StartContainer(id string) error {
	if err := f.failStart[id]; err != nil {
		return err
	}

	f.started = append(f.started, id)

	return nil
}

func lists(states ...[2]string) map[string][]codegen.ContainerSummary {
	out := map[string][]codegen.ContainerSummary{}
	for i, s := range states {
		service := string(rune('a' + i))
		out[service] = []codegen.ContainerSummary{{ID: s[0], State: s[1]}}
	}

	return out
}

func TestOnlyWhatWasRunningIsStoppedAndStartedAgain(t *testing.T) {
	docker := &fakeDocker{}

	release, err := holdApp(context.Background(), docker, lists(
		[2]string{"c1", "running"},
		[2]string{"c2", "exited"},
		[2]string{"c3", "running"},
		[2]string{"c4", "created"},
	))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(docker.stopped, ",") != "c1,c3" {
		t.Fatalf("only the running ones: %v", docker.stopped)
	}

	if err := release(); err != nil {
		t.Fatal(err)
	}

	// starting a container the owner had deliberately switched off would turn a
	// backup into a way of switching it on
	if strings.Join(docker.started, ",") != "c3,c1" {
		t.Fatalf("exactly those, in reverse: %v", docker.started)
	}
}

// An app half stopped is worse than one that was never touched, and the caller
// cannot tell how far this got.
func TestAFailureToStopPutsBackWhatWasAlreadyDown(t *testing.T) {
	docker := &fakeDocker{failStop: map[string]error{"c2": errors.New("daemon said no")}}

	release, err := holdApp(context.Background(), docker, lists(
		[2]string{"c1", "running"},
		[2]string{"c2", "running"},
		[2]string{"c3", "running"},
	))

	if err == nil {
		t.Fatal("the backup must not run on a half-stopped app")
	}
	if !strings.Contains(err.Error(), "the backup did not run") {
		t.Fatalf("say what it means: %v", err)
	}

	// the release function is returned even on the error path, and is never nil
	if release == nil {
		t.Fatal("returning nil here is how half an app stays stopped")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(docker.started, ",") != "c1" {
		t.Fatalf("what went down comes back up: %v", docker.started)
	}
}

// Stopping at the first failure would leave the rest of the app off for the sake
// of a tidier error.
func TestEveryContainerIsStartedEvenAfterOneFails(t *testing.T) {
	docker := &fakeDocker{failStart: map[string]error{"c2": errors.New("port already bound")}}

	release, err := holdApp(context.Background(), docker, lists(
		[2]string{"c1", "running"},
		[2]string{"c2", "running"},
		[2]string{"c3", "running"},
	))
	if err != nil {
		t.Fatal(err)
	}

	err = release()
	if err == nil {
		t.Fatal("a container that did not come back is worth an error")
	}
	if !strings.Contains(err.Error(), "c2") {
		t.Fatalf("name the one that failed: %v", err)
	}

	if strings.Join(docker.started, ",") != "c3,c1" {
		t.Fatalf("the others still came back: %v", docker.started)
	}
}

// Releasing twice must not start an app somebody stopped in between.
func TestReleasingTwiceStartsNothingTheSecondTime(t *testing.T) {
	docker := &fakeDocker{}

	release, err := holdApp(context.Background(), docker, lists([2]string{"c1", "running"}))
	if err != nil {
		t.Fatal(err)
	}

	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}

	if strings.Join(docker.started, ",") != "c1" {
		t.Fatalf("once, not twice: %v", docker.started)
	}
}

// A paused or restarting container is not a settled state to copy from either,
// and it stops and starts with the same calls.
func TestPausedAndRestartingCountAsUp(t *testing.T) {
	docker := &fakeDocker{}

	if _, err := holdApp(context.Background(), docker, lists(
		[2]string{"c1", "paused"},
		[2]string{"c2", "restarting"},
		[2]string{"c3", "dead"},
	)); err != nil {
		t.Fatal(err)
	}

	if strings.Join(docker.stopped, ",") != "c1,c2" {
		t.Fatalf("got %v", docker.stopped)
	}
}

func TestACancelledContextStopsStoppingAndGivesBackWhatItTook(t *testing.T) {
	docker := &fakeDocker{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	release, err := holdApp(ctx, docker, lists([2]string{"c1", "running"}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the cancellation, got %v", err)
	}
	if len(docker.stopped) != 0 {
		t.Fatalf("nothing should have been stopped: %v", docker.stopped)
	}
	if release == nil {
		t.Fatal("still never nil")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

// A service the compose file declares but Docker runs nothing for contributes no
// container to stop, and an empty app is not an error.
func TestAnAppThatIsAlreadyDown(t *testing.T) {
	docker := &fakeDocker{}

	release, err := holdApp(context.Background(), docker, map[string][]codegen.ContainerSummary{
		"web":  {},
		"idle": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(docker.stopped) != 0 {
		t.Fatalf("nothing to stop: %v", docker.stopped)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if len(docker.started) != 0 {
		t.Fatalf("and nothing to start: %v", docker.started)
	}
}
