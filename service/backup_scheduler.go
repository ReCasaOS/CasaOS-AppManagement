package service

import (
	"context"
	stdjson "encoding/json"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// The part that runs backups nobody asked for, on time, and says so afterwards.

// BackupSchedulePath is where the standing arrangements live.
var BackupSchedulePath = "/var/lib/casaos/backups/schedules.json"

var backupScheduleLock sync.Mutex

// BackupSchedules reads the standing arrangements.
//
// A file that will not parse is an error, not an empty list: starting over
// silently would switch off every backup on the box and leave nothing saying so.
func BackupSchedules() ([]BackupSchedule, error) {
	backupScheduleLock.Lock()
	defer backupScheduleLock.Unlock()

	return readSchedules()
}

func readSchedules() ([]BackupSchedule, error) {
	content, err := os.ReadFile(BackupSchedulePath)
	if os.IsNotExist(err) || (err == nil && len(content) == 0) {
		return []BackupSchedule{}, nil
	}
	if err != nil {
		return nil, err
	}

	var schedules []BackupSchedule
	if err := stdjson.Unmarshal(content, &schedules); err != nil {
		return nil, err
	}

	return schedules, nil
}

// SaveBackupSchedules replaces the lot.
func SaveBackupSchedules(schedules []BackupSchedule) error {
	backupScheduleLock.Lock()
	defer backupScheduleLock.Unlock()

	return writeSchedules(schedules)
}

func writeSchedules(schedules []BackupSchedule) error {
	encoded, err := stdjson.MarshalIndent(schedules, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(BackupSchedulePath), 0o700); err != nil {
		return err
	}

	temp := BackupSchedulePath + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return err
	}

	return os.Rename(temp, BackupSchedulePath)
}

// backupRunner is what a scheduled run needs, as an interface so the tick can be
// tested without a daemon, a network or a clock.
type backupRunner interface {
	Run(ctx context.Context, schedule BackupSchedule, stamp string) (BackupManifest, error)
	Runs(destination, app string) ([]string, error)
	Purge(ctx context.Context, destination, remotePath string) error
}

// RunDueBackups runs everything that has an unrun slot, records each one, and
// applies retention.
//
// It returns rather than loops, and takes `now`, so the whole of the decision can
// be exercised in a test. main drives it from the crontab that is already there,
//
// which is the only part that reads a clock.
//
// A schedule's LastRun is written whether the run SUCCEEDED or not. Retrying a
// failing backup every minute until the disk fills is worse than waiting for
// tomorrow's slot, and the failure is in the log where somebody can see it.
func RunDueBackups(ctx context.Context, now time.Time, runner backupRunner) {
	schedules, err := BackupSchedules()
	if err != nil {
		logger.Error("could not read the backup schedules, so none ran", zap.Error(err))
		return
	}

	due, broken := DueSchedules(schedules, now)
	for _, complaint := range broken {
		logger.Error("a backup schedule cannot be understood and will never run", zap.Error(complaint))
	}

	if len(due) == 0 {
		return
	}

	for _, schedule := range due {
		runScheduled(ctx, now, schedule, runner)

		for i := range schedules {
			if schedules[i].App == schedule.App && schedules[i].Destination == schedule.Destination {
				schedules[i].LastRun = now
			}
		}
	}

	if err := SaveBackupSchedules(schedules); err != nil {
		// Worth shouting about: unrecorded runs repeat at the next tick.
		logger.Error("could not record when the backups ran, so they may run again", zap.Error(err))
	}
}

func runScheduled(ctx context.Context, now time.Time, schedule BackupSchedule, runner backupRunner) {
	stamp := now.UTC().Format("2006-01-02T15-04-05Z")
	record := BackupRunRecord{
		App: schedule.App, Destination: schedule.Destination, Stamp: stamp,
		StartedAt: now, Scheduled: true, ContainersStopped: schedule.HoldStill,
	}

	manifest, err := runner.Run(ctx, schedule, stamp)
	finishBackupRecord(&record, manifest, err)

	if err != nil {
		logger.Error("scheduled backup failed",
			zap.Error(err), zap.String("app", schedule.App), zap.String("destination", schedule.Destination))

		// Nothing is deleted after a failure. Applying retention now could drop a
		// good backup from last week in favour of a broken one from tonight.
		return
	}

	applyRetention(ctx, schedule, runner)
}

func applyRetention(ctx context.Context, schedule BackupSchedule, runner backupRunner) {
	if schedule.Keep <= 0 {
		return
	}

	stamps, err := runner.Runs(schedule.Destination, schedule.App)
	if err != nil {
		logger.Error("could not list what is already at the destination, so nothing was deleted",
			zap.Error(err), zap.String("app", schedule.App))

		return
	}

	for _, stamp := range RunsToPurge(stamps, schedule.Keep) {
		if err := runner.Purge(ctx, schedule.Destination, path.Join(schedule.App, stamp)); err != nil {
			logger.Error("could not delete an old backup",
				zap.Error(err), zap.String("app", schedule.App), zap.String("run", stamp))
		}
	}
}
