package service

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/rclone"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// The sync tasks' tick, and the passes somebody asks for: one pass of a task at a time, and
// none while an operating system upgrade is open.

var (
	ErrSyncNotFound   = errors.New("no sync task with this id")
	ErrSyncRunning    = errors.New("this sync task is already running")
	ErrSyncUpgrade    = errors.New("an operating system upgrade is open")
	ErrSyncWrongState = errors.New("this sync task does not wait for that")
)

// runSyncPass is runSync, a variable so a test can count passes and hold them.
var runSyncPass = runSync

// syncTick is held by the tick that is running. Not backupTick: a backup can last hours, and
// the syncs have slots of their own to keep.
var syncTick sync.Mutex

// syncRunning is the tasks a pass runs for, by id, the tick's and the ones asked for alike.
var syncRunning = struct {
	sync.Mutex
	ids map[string]struct{}
}{ids: map[string]struct{}{}}

// syncPasses counts the passes asked for and still going, so a test can wait for them.
var syncPasses sync.WaitGroup

// lastSyncHoldLog is when the tick last said syncs were waiting for an upgrade, under
// lastUpgradeHoldLogMu. Its own, so the backups' hourly line is not spent by the syncs'.
var lastSyncHoldLog time.Time

// claimSync claims a task for one pass and lists it for the core, or says another pass has it.
// The caller looks for an upgrade only after: see RunDueBackups for why the order matters.
func claimSync(task SyncTask) (release func(), ok bool) {
	syncRunning.Lock()
	defer syncRunning.Unlock()

	if _, running := syncRunning.ids[task.ID]; running {
		return nil, false
	}
	syncRunning.ids[task.ID] = struct{}{}
	listed := markInProgress("sync:"+task.Name, "sync")

	return func() {
		listed()
		syncRunning.Lock()
		defer syncRunning.Unlock()
		delete(syncRunning.ids, task.ID)
	}, true
}

// syncTaskByID reads the task as it is now in the file.
func syncTaskByID(id string) (SyncTask, bool, error) {
	tasks, err := SyncTasks()
	if err != nil {
		return SyncTask{}, false, err
	}
	i := slices.IndexFunc(tasks, func(task SyncTask) bool { return task.ID == id })
	if i < 0 {
		return SyncTask{}, false, nil
	}

	return tasks[i], true, nil
}

// syncTaskDue is syncDue for the tick: a task that waits for its owner's answer is never due.
func syncTaskDue(task SyncTask, now time.Time) bool {
	if task.NeedsFirstPass || task.NeedsDeleteConfirm || syncResultUnrecorded(task.ID) {
		return false
	}
	due, err := syncDue(task, now)
	if err != nil {
		logger.Error("a sync task's schedule cannot be understood and will never run", zap.Error(err), zap.String("task", task.Name))
	}

	return due
}

// RunDueSyncs runs the sync tasks that have an unrun slot, one after the other, by name. Each
// pass writes its own result, LastRun included, so a failed pass waits for its next slot.
//
// One tick at a time, as for the backups: a tick that finds another running leaves its tasks
// to it. A task is read again from the file when its turn comes, so what was edited or deleted
// during the pass before it is honoured.
//
// While an operating system upgrade is open nothing runs and nothing is written: the slot stays
// unrun and runs once the upgrade is closed. The tick says so in one line, at most once an hour.
func RunDueSyncs(ctx context.Context, now time.Time, engine syncEngine) {
	if !syncTick.TryLock() {
		return
	}
	defer syncTick.Unlock()

	tasks, err := SyncTasks()
	if err != nil {
		logger.Error("could not read the sync tasks, so none ran", zap.Error(err))
		return
	}
	tasks = slices.DeleteFunc(tasks, func(task SyncTask) bool { return !syncTaskDue(task, now) })
	slices.SortFunc(tasks, func(a, b SyncTask) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})

	ctx = WithBackupOrigin(ctx, OriginScheduled)
	for done, listed := range tasks {
		release, ok := claimSync(listed)
		if !ok {
			// somebody's run now has it; the slot stays unrun for the next tick
			continue
		}

		// Asked before each pass and not once for the tick: a pass can last hours.
		if open, err := osUpgradeOpen(); open {
			release()
			if noticeDue(&lastSyncHoldLog) {
				logHeldForUpgrade("sync tasks are waiting: an operating system upgrade is open, or whether one is cannot be told",
					zap.Int("waiting", len(tasks)-done), zap.Error(err))
			}

			return
		}

		// read under the claim: a pass that ended since the list was read may have parked it
		task, found, err := syncTaskByID(listed.ID)
		if err != nil {
			release()
			logger.Error("could not read the sync tasks, so the rest did not run", zap.Error(err))
			return
		}
		if !found || !syncTaskDue(task, now) {
			release()
			continue
		}

		func() {
			defer release()
			runSyncPass(ctx, task, syncNormal, engine)
		}()
	}
}

// syncModeFits says whether the task waits for what is asked. A plain pass is refused while a
// banner waits for the owner's answer; a task that is off can still be run now.
func syncModeFits(task SyncTask, mode syncMode) bool {
	if syncResultUnrecorded(task.ID) {
		return false
	}
	switch mode {
	case syncConfirmDeletes:
		return task.NeedsDeleteConfirm
	case syncFirstPass:
		return task.Direction == SyncBoth && (task.NeedsFirstPass || !task.FirstPassDone)
	default:
		return !task.NeedsFirstPass && !task.NeedsDeleteConfirm
	}
}

// StartSync runs one pass of the task now and returns as soon as it is claimed and started.
// The pass is detached from any request, and its result is read from the task afterwards.
func StartSync(id string, mode syncMode, engine syncEngine) error {
	task, found, err := syncTaskByID(id)
	if err != nil {
		return err
	}
	if !found {
		return ErrSyncNotFound
	}

	release, ok := claimSync(task)
	if !ok {
		return ErrSyncRunning
	}
	// listed before the flag is looked for, as the tick does
	if open, _ := osUpgradeOpen(); open {
		release()
		return ErrSyncUpgrade
	}
	// read again under the claim: a pass that ended since may have parked the task
	if task, found, err = syncTaskByID(id); err != nil || !found || !syncModeFits(task, mode) {
		release()
		switch {
		case err != nil:
			return err
		case !found:
			return ErrSyncNotFound
		}

		return ErrSyncWrongState
	}

	ctx := WithBackupOrigin(context.Background(), OriginOnDemand)
	syncPasses.Add(1)
	go func() {
		defer syncPasses.Done()
		defer release()
		runSyncPass(ctx, task, mode, engine)
	}()

	return nil
}

// The three passes the routes ask for, with the box's own rclone.
func RunSyncNow(id string) error { return StartSync(id, syncNormal, rclone.NewClient()) }

func RunSyncFirstPass(id string) error { return StartSync(id, syncFirstPass, rclone.NewClient()) }

func ConfirmSyncDeletes(id string) error {
	return StartSync(id, syncConfirmDeletes, rclone.NewClient())
}
