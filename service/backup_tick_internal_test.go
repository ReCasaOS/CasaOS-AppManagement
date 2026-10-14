package service

import (
	"context"
	"sync"
	"testing"
	"time"
)

// gatedRunner is a runner whose backups end when the test lets them, and which says when one
// starts. during, when set, is what the test does while a backup runs.
type gatedRunner struct {
	mu      sync.Mutex
	ran     []string
	started chan string
	release chan struct{}
	during  func(app string)
}

func newGatedRunner() *gatedRunner {
	return &gatedRunner{started: make(chan string, 16), release: make(chan struct{})}
}

// letGo ends every backup, the ones that wait and the ones still to come.
func (g *gatedRunner) letGo() {
	select {
	case <-g.release:
	default:
		close(g.release)
	}
}

func (g *gatedRunner) Run(_ context.Context, schedule BackupSchedule, _ string) (BackupManifest, error) {
	g.mu.Lock()
	g.ran = append(g.ran, schedule.App)
	g.mu.Unlock()

	if g.during != nil {
		g.during(schedule.App)
	}
	g.started <- schedule.App
	<-g.release

	return BackupManifest{}, nil
}

func (g *gatedRunner) Runs(string, string) ([]string, error) { return nil, nil }

func (g *gatedRunner) Purge(context.Context, string, string) error { return nil }

func (g *gatedRunner) apps() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]string{}, g.ran...)
}

// A catch-up after a hold is every owed backup at once, and cron starts a tick every minute
// whether the last one is over or not. The next tick used to read the same schedules, none
// of them recorded yet, and run every backup again beside the first.
func TestABackupThatOutlastsTheMinuteIsNotStartedAgainByTheNextTick(t *testing.T) {
	schedulerInTempDir(t)
	_ = SaveBackupSchedules(threeDue())

	// no tick outlives the test, whatever it did: the next test's files are not its to write
	var ticks sync.WaitGroup
	runner := newGatedRunner()
	t.Cleanup(func() {
		runner.letGo()
		ticks.Wait()
	})

	tick := func(now time.Time) <-chan struct{} {
		over := make(chan struct{})
		ticks.Add(1)
		go func() {
			defer ticks.Done()
			defer close(over)
			RunDueBackups(context.Background(), now, runner)
		}()

		return over
	}

	first := tick(at(10, 7, 0))
	<-runner.started

	// a minute later: the first backup is still going
	select {
	case <-tick(at(10, 7, 1)):
	case <-time.After(5 * time.Second):
		t.Fatal("the next tick waits on a backup: it started what the first tick runs")
	}

	runner.letGo()
	<-first

	if got := runner.apps(); len(got) != 3 || got[0] != "immich" || got[1] != "jellyfin" || got[2] != "nextcloud" {
		t.Fatalf("each backup once, one after another: %v", got)
	}

	RunDueBackups(context.Background(), at(10, 7, 2), runner)
	if got := runner.apps(); len(got) != 3 {
		t.Fatalf("nothing is owed once the first tick is over: %v", got)
	}
}

// A tick can last hours, and a process that stops half way must owe only what it did not do:
// each backup is written down when it ends, not all of them when the tick does.
func TestEachBackupIsRecordedWhenItEnds(t *testing.T) {
	schedulerInTempDir(t)
	_ = SaveBackupSchedules(threeDue())

	runner := newGatedRunner()
	runner.letGo()

	var recorded []string
	runner.during = func(app string) {
		schedules, _ := BackupSchedules()
		for _, schedule := range schedules {
			if !schedule.LastRun.IsZero() {
				recorded = append(recorded, app+" finds "+schedule.App)
			}
		}
	}

	RunDueBackups(context.Background(), at(10, 7, 0), runner)

	want := []string{"jellyfin finds immich", "nextcloud finds immich", "nextcloud finds jellyfin"}
	if len(recorded) != len(want) {
		t.Fatalf("recorded before each backup: %v, want %v", recorded, want)
	}
	for i := range want {
		if recorded[i] != want[i] {
			t.Fatalf("recorded before each backup: %v, want %v", recorded, want)
		}
	}
}

// The owner saves the schedules while a long tick runs: what they saved stays, with the runs
// the tick recorded.
func TestASchedulePageSavedDuringATickIsNotUndoneByIt(t *testing.T) {
	schedulerInTempDir(t)
	_ = SaveBackupSchedules(threeDue())

	runner := newGatedRunner()
	runner.letGo()
	runner.during = func(app string) {
		if app != "immich" {
			return
		}

		schedules, err := BackupSchedules()
		if err != nil {
			t.Error(err)
			return
		}
		schedules[1].Keep = 7
		added := daily(at(10, 3, 0)) // ran at three: not due at seven
		added.App = "sonarr"
		if err := SaveBackupSchedules(append(schedules, added)); err != nil {
			t.Error(err)
		}
	}

	RunDueBackups(context.Background(), at(10, 7, 0), runner)

	schedules, err := BackupSchedules()
	if err != nil || len(schedules) != 4 {
		t.Fatalf("the schedule that was added is still there: %+v %v", schedules, err)
	}
	for _, schedule := range schedules {
		switch schedule.App {
		case "jellyfin":
			if schedule.Keep != 7 {
				t.Fatalf("the edit is still there: keep %d", schedule.Keep)
			}
			fallthrough
		case "immich", "nextcloud":
			if schedule.LastRun.IsZero() {
				t.Fatalf("%s ran and is recorded", schedule.App)
			}
		case "sonarr":
			if !schedule.LastRun.Equal(at(10, 3, 0)) {
				t.Fatalf("a schedule that did not run keeps its last run: %v", schedule.LastRun)
			}
		}
	}
}
