package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/rclone"
)

// One pass of a sync task: everything that stops it before a file moves, the pass itself, what
// it says afterwards, and the archives it lets go of.
//
// A pass can delete. Every reason not to run is asked before rclone is, and an empty side --
// an unmounted disk, a folder emptied by mistake -- is one: carried over, it would empty the other.

// syncEngine is rclone, as a pass needs it. *rclone.Client is one; the tests pass a fake.
type syncEngine interface {
	SyncDirectory(ctx context.Context, srcFs, dstFs string, o rclone.SyncOptions) error
	BisyncDirectory(ctx context.Context, path1, path2 string, o rclone.BisyncOptions) error
	CountFiles(ctx context.Context, fs string) (int64, error)
	IsEmpty(fs string) (bool, error)
	Folders(fs, remote string) ([]string, error)
	Purge(ctx context.Context, destinationName, remotePath string) error
}

var _ syncEngine = (*rclone.Client)(nil)

type syncMode int

const (
	syncNormal syncMode = iota
	// syncFirstPass unites both sides again, one of them empty or not
	syncFirstPass
	// syncConfirmDeletes lets one pass delete more than the cap
	syncConfirmDeletes
)

// syncStampLayout names a pass's archive folder, as the backups name their runs.
const syncStampLayout = "2006-01-02T15-04-05Z"

var errSyncSourceEmpty = errors.New("the source is empty: nothing was synced, so the other side was left as it was")

// runSync runs one pass and writes how it went into the task. It never returns an error: a
// pass that cannot run is a failed pass, read from the task like any other.
func runSync(ctx context.Context, task SyncTask, mode syncMode, engine syncEngine) {
	now := backupClock()
	stamp := now.UTC().Format(syncStampLayout)

	if err := syncGuards(task, mode, engine); err != nil {
		writeSyncResult(task, now, err, false, 0, 0)
		return
	}

	running, endLive := beginLive(ctx, task.Name, task.Destination, stamp)
	liveKind(running, "sync")

	var err error
	firstPassDone := false
	if task.Direction == SyncBoth {
		firstPassDone, err = twoWayPass(running, task, mode, stamp, engine)
	} else {
		err = oneWayPass(running, task, mode, stamp, engine)
	}
	if !errors.Is(err, rclone.ErrSyncLocked) {
		err = stoppedFromInterface(running, err)
	}
	files, bytes := liveFigures(running)
	endLive()

	if errors.Is(err, rclone.ErrSyncLocked) {
		// another pass of the same folders is alive in rclone: this one never happened
		logger.Info("sync pass skipped: another pass holds the folders", zap.String("task", task.Name))
		return
	}

	writeSyncResult(task, now, err, firstPassDone, files, bytes)
	if err == nil {
		purgeSyncArchive(ctx, task, now, engine)
	}
}

// syncGuards is every reason not to touch either side, asked again at each pass.
func syncGuards(task SyncTask, mode syncMode, engine syncEngine) error {
	tasks, err := SyncTasks()
	if err != nil {
		return err
	}
	others := slices.DeleteFunc(tasks, func(other SyncTask) bool { return other.ID == task.ID })
	if err := checkSyncTask(task, others); err != nil {
		return err
	}

	if info, err := os.Stat(task.LocalPath); err != nil || !info.IsDir() {
		return errors.New("the folder on this box is missing")
	}

	if task.Direction == SyncBoth && task.workdir() == "" {
		return errors.New("this task's id was not issued by the box, so it has nowhere to keep its state: save it again")
	}

	return checkSyncSides(task, mode, engine)
}

// checkSyncSides refuses a pass that would carry an empty side over a full one.
func checkSyncSides(task SyncTask, mode syncMode, engine syncEngine) error {
	entries, err := os.ReadDir(task.LocalPath)
	if err != nil {
		return err
	}
	boxEmpty := len(entries) == 0

	cloudEmpty := func() (bool, error) {
		fs, err := syncCloudFs(task, task.RemotePath)
		if err != nil {
			return false, err
		}
		empty, err := engine.IsEmpty(fs)
		if isNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("the destination %q cannot be read: %w", task.Destination, err)
		}

		return empty, nil
	}

	switch task.Direction {
	case SyncUpload:
		if !boxEmpty {
			return nil
		}
		empty, err := cloudEmpty()
		if err != nil {
			return err
		}
		if !empty {
			return errSyncSourceEmpty
		}
	case SyncDownload:
		empty, err := cloudEmpty()
		if err != nil {
			return err
		}
		if empty && !boxEmpty {
			return errSyncSourceEmpty
		}
	case SyncBoth:
		empty, err := cloudEmpty()
		if err != nil {
			return err
		}
		if empty != boxEmpty && mode != syncFirstPass {
			return errSyncSourceEmpty
		}
	}

	return nil
}

// isNotFound is rclone saying a folder is not there yet, which for a sync is a folder with nothing in it.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "directory not found")
}

// syncCloudFs is the destination side as rclone reads it. Google Docs have no size and no
// download a two-way pass could compare, so a both-ways pass on Drive leaves them out -- and its
// cloud archive must be written through the same remote, rclone wants the two on one.
func syncCloudFs(task SyncTask, remotePath string) (string, error) {
	if task.Direction != SyncBoth {
		return rclone.Destination(task.Destination, remotePath), nil
	}

	backend, _, err := syncDestinationKind(task.Destination)
	if err != nil {
		return "", fmt.Errorf("the destination %q cannot be read: %w", task.Destination, err)
	}
	if backend == "drive" {
		return rclone.DestinationPrefix + task.Destination + ",skip_gdocs=true:" + remotePath, nil
	}

	return rclone.Destination(task.Destination, remotePath), nil
}

// oneWayPass copies or mirrors one side onto the other. A mirror deletes at most half of what
// the written side holds (ten at least) unless the deletions were confirmed.
func oneWayPass(ctx context.Context, task SyncTask, mode syncMode, stamp string, engine syncEngine) error {
	cloud := rclone.Destination(task.Destination, task.RemotePath)
	src, dst, archive := cloud, task.LocalPath, task.localArchive(stamp)
	if task.Direction == SyncUpload {
		src, dst, archive = task.LocalPath, cloud, rclone.Destination(task.Destination, task.remoteArchive(stamp))
	}

	o := rclone.SyncOptions{Mirror: task.Mirror, MaxDelete: -1}
	if task.Archive {
		o.BackupDir = archive
	}
	if task.Mirror && mode != syncConfirmDeletes {
		files, err := engine.CountFiles(ctx, dst)
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("the files on the written side could not be counted, so nothing was synced: %w", err)
		}
		o.MaxDelete = max(10, files/2)
	}

	return engine.SyncDirectory(ctx, src, dst, o)
}

// twoWayPass runs bisync with the box as path1. A first pass is a resync, then a normal pass,
// and only both together make the first pass done.
func twoWayPass(ctx context.Context, task SyncTask, mode syncMode, stamp string, engine syncEngine) (firstPassDone bool, err error) {
	workdir := task.workdir()
	if workdir == "" {
		return false, errors.New("this task has nowhere to keep its state")
	}
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return false, err
	}

	path2, err := syncCloudFs(task, task.RemotePath)
	if err != nil {
		return false, err
	}
	o := rclone.BisyncOptions{Workdir: workdir, Force: mode == syncConfirmDeletes}
	if task.Archive {
		o.BackupDir1 = task.localArchive(stamp)
		if o.BackupDir2, err = syncCloudFs(task, task.remoteArchive(stamp)); err != nil {
			return false, err
		}
	}

	if mode != syncFirstPass && task.FirstPassDone {
		return false, engine.BisyncDirectory(ctx, task.LocalPath, path2, o)
	}

	resync := o
	resync.Resync = true
	if err := engine.BisyncDirectory(ctx, task.LocalPath, path2, resync); err != nil {
		return false, err
	}
	if err := engine.BisyncDirectory(ctx, task.LocalPath, path2, o); err != nil {
		return false, err
	}

	return true, nil
}

// writeSyncResult says how a pass ended in the task as it is now in the file.
func writeSyncResult(task SyncTask, now time.Time, err error, firstPassDone bool, files, bytes int64) {
	if err != nil {
		logger.Error("sync pass failed", zap.Error(err), zap.String("task", task.Name), zap.String("destination", task.Destination))
	}
	lostState := errors.Is(err, rclone.ErrBisyncAborted) && syncStateLost(task.workdir())

	updateErr := updateSyncTask(task.ID, func(stored *SyncTask) {
		stored.LastRun = now
		switch {
		case err == nil:
			result := "ok"
			if task.Direction == SyncBoth && (stored.LastResult == "failed" || stored.LastResult == "stopped") {
				result = "recovered"
			}
			stored.LastResult, stored.LastError = result, ""
			stored.LastFiles, stored.LastBytes = files, bytes
			stored.NeedsDeleteConfirm, stored.NeedsFirstPass = false, false
			if firstPassDone {
				stored.FirstPassDone = true
			}
		case errors.Is(err, ErrStoppedFromInterface):
			stored.LastResult, stored.LastError = "stopped", err.Error()
		default:
			stored.LastResult, stored.LastError = "failed", err.Error()
			if errors.Is(err, rclone.ErrTooManyDeletes) {
				stored.NeedsDeleteConfirm = true
			}
			if lostState {
				stored.NeedsFirstPass = true
			}
		}
	})
	if updateErr != nil {
		logger.Error("the result of a sync pass could not be written", zap.Error(updateErr), zap.String("task", task.Name))
	}
}

// syncStateLost says bisync has no listing left to start from. A .lst-old without its .lst is a
// pass cut short, which the next one recovers.
func syncStateLost(workdir string) bool {
	if workdir == "" {
		return false
	}
	lists, _ := filepath.Glob(filepath.Join(workdir, "*.path1.lst"))
	old, _ := filepath.Glob(filepath.Join(workdir, "*.path1.lst-old"))

	return len(lists) == 0 && len(old) == 0
}
