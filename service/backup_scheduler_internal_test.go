package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
)

type fakeRunner struct {
	ran     []string
	purged  []string
	present []string
	fail    error
	listErr error
}

func (f *fakeRunner) Run(_ context.Context, schedule BackupSchedule, stamp string) (BackupManifest, error) {
	f.ran = append(f.ran, schedule.App+"@"+stamp)
	if f.fail != nil {
		return BackupManifest{}, f.fail
	}

	return BackupManifest{Operations: []BackupOperation{{}, {}}}, nil
}

func (f *fakeRunner) Runs(_, _ string) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}

	return f.present, nil
}

func (f *fakeRunner) Purge(_ context.Context, _, remotePath string) error {
	f.purged = append(f.purged, remotePath)

	return nil
}

func schedulerInTempDir(t *testing.T) {
	t.Helper()

	// the scheduler reports failures through the shared logger, which main
	// initialises in production and a test has to ask for
	logger.LogInitConsoleOnly()

	dir := t.TempDir()
	wasSchedules, wasLog := BackupSchedulePath, BackupLogPath
	BackupSchedulePath = filepath.Join(dir, "schedules.json")
	BackupLogPath = filepath.Join(dir, "runs.json")
	t.Cleanup(func() { BackupSchedulePath, BackupLogPath = wasSchedules, wasLog })
}

func TestADueScheduleRunsAndIsRecorded(t *testing.T) {
	schedulerInTempDir(t)

	if err := SaveBackupSchedules([]BackupSchedule{daily(time.Time{})}); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)

	if len(runner.ran) != 1 {
		t.Fatalf("want one run, got %v", runner.ran)
	}

	records, _ := BackupRuns()
	if len(records) != 1 || !records[0].Scheduled || !records[0].Succeeded() {
		t.Fatalf("the run is in the log, marked scheduled: %+v", records)
	}

	// and it does not run again on the next tick
	RunDueBackups(context.Background(), at(10, 7, 1), runner)
	if len(runner.ran) != 1 {
		t.Fatalf("once per slot: %v", runner.ran)
	}
}

// Retrying a failing backup every minute until the disk fills is worse than
// waiting for tomorrow's slot, and the failure is in the log where it can be seen.
func TestAFailedRunStillMarksItsSlotDone(t *testing.T) {
	schedulerInTempDir(t)
	_ = SaveBackupSchedules([]BackupSchedule{daily(time.Time{})})

	runner := &fakeRunner{fail: errors.New("AccessDenied")}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)
	RunDueBackups(context.Background(), at(10, 7, 1), runner)

	if len(runner.ran) != 1 {
		t.Fatalf("a failure does not retry every minute: %v", runner.ran)
	}

	records, _ := BackupRuns()
	if len(records) != 1 || records[0].Succeeded() {
		t.Fatalf("and it is written down as a failure: %+v", records)
	}
}

// Applying retention after a failure could drop a good backup from last week in
// favour of a broken one from tonight.
func TestNothingIsDeletedAfterAFailedRun(t *testing.T) {
	schedulerInTempDir(t)

	schedule := daily(time.Time{})
	schedule.Keep = 1
	_ = SaveBackupSchedules([]BackupSchedule{schedule})

	runner := &fakeRunner{fail: errors.New("nope"), present: []string{"old-1", "old-2", "old-3"}}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)

	if len(runner.purged) != 0 {
		t.Fatalf("nothing is deleted after a failure: %v", runner.purged)
	}
}

func TestRetentionDeletesTheOldestAfterASuccessfulRun(t *testing.T) {
	schedulerInTempDir(t)

	schedule := daily(time.Time{})
	schedule.Keep = 2
	_ = SaveBackupSchedules([]BackupSchedule{schedule})

	runner := &fakeRunner{present: []string{
		"2026-09-08T03-00-00Z", "2026-09-09T03-00-00Z", "2026-09-10T03-00-00Z", "2026-09-11T03-00-00Z",
	}}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)

	if len(runner.purged) != 2 {
		t.Fatalf("two go: %v", runner.purged)
	}
	for _, gone := range runner.purged {
		if !strings.HasPrefix(gone, "nextcloud/") {
			t.Fatalf("purged inside the app's folder, not the app: %q", gone)
		}
		if strings.Contains(gone, "2026-09-11") {
			t.Fatal("never the newest")
		}
	}
}

func TestAScheduleWithNoRetentionDeletesNothing(t *testing.T) {
	schedulerInTempDir(t)
	_ = SaveBackupSchedules([]BackupSchedule{daily(time.Time{})}) // Keep is zero

	runner := &fakeRunner{present: []string{"a", "b", "c", "d"}}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)

	if len(runner.purged) != 0 {
		t.Fatalf("no policy is not keep none: %v", runner.purged)
	}
}

// A destination that cannot be listed is a destination nothing should be deleted
// from.
func TestNothingIsDeletedWhenTheDestinationCannotBeListed(t *testing.T) {
	schedulerInTempDir(t)

	schedule := daily(time.Time{})
	schedule.Keep = 1
	_ = SaveBackupSchedules([]BackupSchedule{schedule})

	runner := &fakeRunner{listErr: errors.New("timeout")}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)

	if len(runner.purged) != 0 {
		t.Fatalf("%v", runner.purged)
	}
}

// Starting over silently would switch off every backup on the box with nothing
// saying so.
func TestScheduleFileThatWillNotParseIsAnError(t *testing.T) {
	schedulerInTempDir(t)

	if err := writeRaw(BackupSchedulePath, "[ not schedules"); err != nil {
		t.Fatal(err)
	}

	if _, err := BackupSchedules(); err == nil {
		t.Fatal("want an error rather than an empty list")
	}

	// and the tick runs nothing rather than deciding nothing is scheduled
	runner := &fakeRunner{}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)
	if len(runner.ran) != 0 {
		t.Fatalf("%v", runner.ran)
	}
}

func TestNoSchedulesAtAll(t *testing.T) {
	schedulerInTempDir(t)

	schedules, err := BackupSchedules()
	if err != nil || len(schedules) != 0 {
		t.Fatalf("a missing file is an empty list: %v %v", schedules, err)
	}

	runner := &fakeRunner{}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)
	if len(runner.ran) != 0 {
		t.Fatal("nothing to run")
	}
}
