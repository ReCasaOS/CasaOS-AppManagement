package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The deferred release covers every way a backup can fail except the one where
// nothing runs at all: a kill, a crash, a power cut. The containers are down, the
// code that knew about them is gone, and the app simply stays off.
func TestAnAppLeftStoppedByAProcessThatDiedIsStartedAgain(t *testing.T) {
	holdNoteInTempDir(t)

	// a backup begins and the process never gets to release
	docker := &fakeDocker{}
	if _, err := holdApp(context.Background(), docker, "nextcloud", lists(
		[2]string{"c1", "running"},
		[2]string{"c2", "running"},
	)); err != nil {
		t.Fatal(err)
	}

	// ... and the service starts up again, with a different Docker handle
	next := &fakeDocker{}
	RecoverHeldApps(next)

	// in reverse, the way a release does, so a stack comes back as it went down
	if strings.Join(next.started, ",") != "c2,c1" {
		t.Fatalf("want c2,c1 -- got %v", next.started)
	}

	// and the note is gone, so a second startup does nothing
	again := &fakeDocker{}
	RecoverHeldApps(again)
	if len(again.started) != 0 {
		t.Fatalf("the note should have been cleared: %v", again.started)
	}
}

// A backup that finished properly must leave nothing for startup to act on.
func TestAReleasedAppLeavesNothingBehind(t *testing.T) {
	holdNoteInTempDir(t)

	docker := &fakeDocker{}
	release, err := holdApp(context.Background(), docker, "nextcloud", lists([2]string{"c1", "running"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}

	next := &fakeDocker{}
	RecoverHeldApps(next)
	if len(next.started) != 0 {
		t.Fatalf("nothing was left behind: %v", next.started)
	}
}

// The note is written BEFORE the first stop. One written afterwards would not be
// there for the crash it exists to survive.
func TestTheNoteExistsBeforeAnythingIsStopped(t *testing.T) {
	holdNoteInTempDir(t)

	// a Docker that dies on the very first stop, as a kill would
	docker := &fakeDocker{failStop: map[string]error{"c1": errDead}}
	if _, err := holdApp(context.Background(), docker, "nextcloud", lists([2]string{"c1", "running"})); err == nil {
		t.Fatal("want the failure")
	}

	held, err := readHeld()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := held["nextcloud"]; !ok {
		t.Fatal("the note has to exist even though no container went down")
	}
}

// Several apps held at once each come back.
func TestEveryHeldAppComesBack(t *testing.T) {
	holdNoteInTempDir(t)

	first := &fakeDocker{}
	if _, err := holdApp(context.Background(), first, "alpha", lists([2]string{"a1", "running"})); err != nil {
		t.Fatal(err)
	}
	if _, err := holdApp(context.Background(), first, "beta", lists([2]string{"b1", "running"})); err != nil {
		t.Fatal(err)
	}

	next := &fakeDocker{}
	RecoverHeldApps(next)

	if len(next.started) != 2 {
		t.Fatalf("both apps: %v", next.started)
	}
}

func TestNothingHeldIsNotAnError(t *testing.T) {
	holdNoteInTempDir(t)

	docker := &fakeDocker{}
	RecoverHeldApps(docker)

	if len(docker.started) != 0 {
		t.Fatalf("%v", docker.started)
	}
}

// A container that does not come back keeps its note, so the next startup tries
// again. Clearing it there -- with the daemon most likely just not up yet -- is
// how an app stays off with nothing left pointing at it.
func TestANoteSurvivesAStartThatFailed(t *testing.T) {
	holdNoteInTempDir(t)

	docker := &fakeDocker{}
	if _, err := holdApp(context.Background(), docker, "nextcloud", lists([2]string{"c1", "running"})); err != nil {
		t.Fatal(err)
	}

	// the box comes up with docker not listening yet
	down := &fakeDocker{failStart: map[string]error{"c1": errDead}}
	RecoverHeldApps(down)

	held, err := readHeld()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := held["nextcloud"]; !ok {
		t.Fatal("the note has to survive, or nothing tries again")
	}

	// ... and the boot after that, with a daemon that answers
	up := &fakeDocker{}
	RecoverHeldApps(up)

	if strings.Join(up.started, ",") != "c1" {
		t.Fatalf("%v", up.started)
	}

	held, err = readHeld()
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Fatalf("now it goes: %v", held)
	}
}

var errDead = errors.New("daemon went away")
