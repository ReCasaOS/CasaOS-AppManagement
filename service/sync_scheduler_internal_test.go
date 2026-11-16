package service

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakePasses stands in for runSync: it says which task ran in which mode, and, when gate is
// set, holds each pass until the test lets it go.
type fakePasses struct {
	mu      sync.Mutex
	ran     []string
	modes   []syncMode
	origins []BackupOrigin
	started chan string   // receives the task's name as a pass begins, when gate is set
	gate    chan struct{} // closed to let the held passes end
}

func (f *fakePasses) pass(ctx context.Context, task SyncTask, mode syncMode, _ syncEngine) {
	f.mu.Lock()
	f.ran = append(f.ran, task.Name)
	f.modes = append(f.modes, mode)
	f.origins = append(f.origins, backupOriginOf(ctx))
	f.mu.Unlock()

	if f.gate != nil {
		f.started <- task.Name
		<-f.gate
	}
}

func (f *fakePasses) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.ran)
}

// syncTickIn is syncBox with no upgrade open, nothing said about one yet, and the passes faked.
// held makes each pass wait for close(passes.gate).
func syncTickIn(t *testing.T, held bool, tasks ...SyncTask) (*fakePasses, *upgradeFlag) {
	t.Helper()

	syncBox(t)
	flag := upgradeFlagIn(t)
	forgetSyncHoldNotice()

	passes := &fakePasses{}
	if held {
		passes.started, passes.gate = make(chan string, 8), make(chan struct{})
	}
	was := runSyncPass
	runSyncPass = passes.pass
	t.Cleanup(func() {
		if held {
			select {
			case <-passes.gate:
			default:
				close(passes.gate)
			}
		}
		syncPasses.Wait()
		runSyncPass = was
		forgetSyncHoldNotice()
	})

	if err := writeSyncTasks(tasks); err != nil {
		t.Fatal(err)
	}

	return passes, flag
}

func dueTask(id, name string) SyncTask {
	task := goodTask("/DATA")
	task.ID, task.Name = id, name

	return task
}

func forgetSyncHoldNotice() {
	lastUpgradeHoldLogMu.Lock()
	defer lastUpgradeHoldLogMu.Unlock()

	lastSyncHoldLog = time.Time{}
}

func TestALongBackupDoesNotHoldSyncs(t *testing.T) {
	passes, _ := syncTickIn(t, false, dueTask("s1", "photos"))

	backupTick.Lock()
	defer backupTick.Unlock()

	RunDueSyncs(context.Background(), at(10, 7, 0), nil)

	if ran := passes.names(); !slices.Equal(ran, []string{"photos"}) {
		t.Fatalf("a backup tick in progress is none of the syncs' business: %v", ran)
	}
	if passes.origins[0] != OriginScheduled {
		t.Fatalf("the tick's passes say they are scheduled: %q", passes.origins[0])
	}
}

func TestSyncsGoOneAtATime(t *testing.T) {
	passes, _ := syncTickIn(t, true, dueTask("s2", "videos"), dueTask("s1", "photos"))

	first := make(chan struct{})
	go func() {
		defer close(first)
		RunDueSyncs(context.Background(), at(10, 7, 0), nil)
	}()

	if name := <-passes.started; name != "photos" {
		t.Fatalf("by name, photos first: %s", name)
	}

	// the tick of the next minute finds the first one still going, and leaves it be
	RunDueSyncs(context.Background(), at(10, 7, 1), nil)
	if ran := passes.names(); len(ran) != 1 {
		t.Fatalf("one pass at a time: %v", ran)
	}

	close(passes.gate)
	<-first

	if ran := passes.names(); !slices.Equal(ran, []string{"photos", "videos"}) {
		t.Fatalf("the first tick runs both, one after the other: %v", ran)
	}
}

func TestAParkedTaskIsNotDue(t *testing.T) {
	waitsFirst, waitsConfirm := dueTask("s1", "a"), dueTask("s2", "b")
	waitsFirst.NeedsFirstPass = true
	waitsConfirm.NeedsDeleteConfirm = true
	off := dueTask("s4", "d")
	off.Enabled = false
	passes, _ := syncTickIn(t, false, waitsFirst, waitsConfirm, dueTask("s3", "c"), off)

	RunDueSyncs(context.Background(), at(10, 7, 0), nil)

	if ran := passes.names(); !slices.Equal(ran, []string{"c"}) {
		t.Fatalf("a task that waits for its owner, or is off, does not run: %v", ran)
	}
}

func TestNothingRunsWhileAnUpgradeIsOpen(t *testing.T) {
	passes, flag := syncTickIn(t, false, dueTask("s1", "photos"), dueTask("s2", "videos"))
	flag.present = true

	before, err := SyncTasks()
	if err != nil {
		t.Fatal(err)
	}

	RunDueSyncs(context.Background(), at(10, 7, 0), nil)
	RunDueSyncs(context.Background(), at(10, 7, 1), nil)

	if ran := passes.names(); len(ran) != 0 {
		t.Fatalf("nothing runs through an upgrade: %v", ran)
	}
	after, err := SyncTasks()
	if err != nil {
		t.Fatal(err)
	}
	for i := range after {
		if !after[i].LastRun.Equal(before[i].LastRun) {
			t.Fatalf("the slot stays unrun: %v", after[i].LastRun)
		}
	}
	if len(flag.asked) != 2 {
		t.Fatalf("each tick stops at its first task: asked %d times", len(flag.asked))
	}
	if len(flag.said) != 1 {
		t.Fatalf("one line an hour: %d", len(flag.said))
	}
	if left := RunningOperations(); len(left) != 0 {
		t.Fatalf("a held task is not listed any more: %v", left)
	}
}

func TestTheMarkComesBeforeTheUpgradeFlagIsRead(t *testing.T) {
	passes, flag := syncTickIn(t, false, dueTask("s1", "photos"))

	var seen []AppOperation
	flag.onAsk = func() { seen = RunningOperations() }

	RunDueSyncs(context.Background(), at(10, 7, 0), nil)

	if !slices.Contains(seen, AppOperation{App: "sync:photos", Kind: "sync"}) {
		t.Fatalf("the core must see the pass before it is let through: %v", seen)
	}
	if len(passes.names()) != 1 {
		t.Fatalf("and then it runs: %v", passes.names())
	}
	if left := RunningOperations(); len(left) != 0 {
		t.Fatalf("a pass that is over is not listed any more: %v", left)
	}
}

func TestRunNowRefusesWhatItShould(t *testing.T) {
	t.Run("unknown id", func(t *testing.T) {
		syncTickIn(t, false, dueTask("s1", "photos"))

		if err := StartSync("nope", syncNormal, nil); !errors.Is(err, ErrSyncNotFound) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("already running", func(t *testing.T) {
		passes, _ := syncTickIn(t, true, dueTask("s1", "photos"))

		if err := StartSync("s1", syncNormal, nil); err != nil {
			t.Fatal(err)
		}
		<-passes.started

		if err := StartSync("s1", syncNormal, nil); !errors.Is(err, ErrSyncRunning) {
			t.Fatalf("got %v", err)
		}
		// nor does the tick start it again meanwhile
		RunDueSyncs(context.Background(), at(10, 7, 0), nil)
		if ran := passes.names(); len(ran) != 1 {
			t.Fatalf("one pass of a task at a time: %v", ran)
		}
		if passes.origins[0] != OriginOnDemand {
			t.Fatalf("a run now says so: %q", passes.origins[0])
		}

		close(passes.gate)
		syncPasses.Wait()

		if err := StartSync("s1", syncNormal, nil); err != nil {
			t.Fatalf("over, it can run again: %v", err)
		}
		<-passes.started
	})

	t.Run("upgrade open", func(t *testing.T) {
		passes, flag := syncTickIn(t, false, dueTask("s1", "photos"))
		flag.present = true

		if err := StartSync("s1", syncNormal, nil); !errors.Is(err, ErrSyncUpgrade) {
			t.Fatalf("got %v", err)
		}
		if len(passes.names()) != 0 || len(RunningOperations()) != 0 {
			t.Fatalf("nothing ran, nothing is left listed: %v %v", passes.names(), RunningOperations())
		}
	})

	t.Run("wrong state", func(t *testing.T) {
		plain := dueTask("s1", "photos")
		both := dueTask("s2", "both")
		both.Direction, both.FirstPassDone = SyncBoth, true
		waiting := dueTask("s3", "waiting")
		waiting.NeedsDeleteConfirm = true
		off := dueTask("s4", "off")
		off.Enabled = false
		passes, _ := syncTickIn(t, false, plain, both, waiting, off)

		for _, c := range []struct {
			id   string
			mode syncMode
		}{
			{"s1", syncConfirmDeletes}, // nothing to confirm
			{"s1", syncFirstPass},      // one way has no first pass
			{"s2", syncFirstPass},      // done already, and not asked for again
			{"s3", syncNormal},         // the owner answers the banner first
		} {
			if err := StartSync(c.id, c.mode, nil); !errors.Is(err, ErrSyncWrongState) {
				t.Fatalf("%s in mode %d: got %v", c.id, c.mode, err)
			}
		}

		// and what is asked for runs, a task that is off included
		for _, id := range []string{"s3", "s4"} {
			mode := syncNormal
			if id == "s3" {
				mode = syncConfirmDeletes
			}
			if err := StartSync(id, mode, nil); err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			syncPasses.Wait()
		}
		if ran := passes.names(); !slices.Equal(ran, []string{"waiting", "off"}) || passes.modes[0] != syncConfirmDeletes {
			t.Fatalf("got %v %v", ran, passes.modes)
		}
	})
}
