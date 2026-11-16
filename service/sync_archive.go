package service

import (
	"context"
	"os"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/rclone"
)

// purgeSyncArchive lets go of the archives older than the task keeps them, on both sides. Only
// folders named like a pass's stamp are ever removed; a failure is said, and the pass stays good.
func purgeSyncArchive(ctx context.Context, task SyncTask, now time.Time, engine syncEngine) {
	if task.ArchiveDays <= 0 {
		return
	}
	oldest := now.Add(-time.Duration(task.ArchiveDays) * 24 * time.Hour)
	expired := func(name string) bool {
		stamp, err := time.Parse(syncStampLayout, name)
		return err == nil && stamp.Before(oldest)
	}

	// each side keeps only the archives this task wrote there: an upload writes none on the box,
	// a download none in the destination, where another box's task may keep its own
	if task.Direction != SyncUpload {
		purgeBoxArchive(task, expired)
	}
	if task.Direction == SyncDownload || task.RemotePath == "" {
		return
	}
	names, err := engine.Folders(rclone.Destination(task.Destination, ""), task.remoteArchiveFolder())
	if isNotFound(err) {
		return
	}
	if err != nil {
		logger.Error("the sync archive in the destination could not be read", zap.Error(err), zap.String("task", task.Name))
		return
	}
	for _, name := range names {
		if expired(name) {
			if err := engine.Purge(ctx, task.Destination, task.remoteArchive(name)); err != nil {
				logger.Error("an old sync archive could not be removed", zap.Error(err), zap.String("task", task.Name))
			}
		}
	}
}

func purgeBoxArchive(task SyncTask, expired func(name string) bool) {
	// a link in its place could lead anywhere
	folder := syncArchiveFolder(task.LocalPath)
	if info, err := os.Lstat(folder); err == nil && info.IsDir() {
		entries, err := os.ReadDir(folder)
		if err != nil {
			logger.Error("the sync archive on this box could not be read", zap.Error(err), zap.String("task", task.Name))
		}
		for _, entry := range entries {
			if entry.IsDir() && expired(entry.Name()) {
				if err := os.RemoveAll(task.localArchive(entry.Name())); err != nil {
					logger.Error("an old sync archive could not be removed", zap.Error(err), zap.String("task", task.Name))
				}
			}
		}
	}
}
