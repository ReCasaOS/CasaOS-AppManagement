package service

import (
	"context"
	"testing"
)

// The scheduler wrote its runs down and the on-demand route did not, so the
// History tab never showed a backup somebody had asked for with the button.

func TestABackupSomebodyAskedForIsWrittenDown(t *testing.T) {
	logInTempDir(t)

	app, _ := appWithOneBindAndOneVolume(t)
	docker := &fakeBackupDocker{mountpoints: map[string]string{"demo_data": "/var/lib/docker/volumes/demo_data/_data"}}

	if _, err := BackupOnDemand(context.Background(), app, docker, &fakeCopier{}, BackupOptions{
		Destination: "offsite", Stamp: "2026-09-12T20-20-29Z", HoldStill: false,
	}); err != nil {
		t.Fatal(err)
	}

	runs, err := BackupRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("one run: %v", runs)
	}

	run := runs[0]
	if run.App != "demo" || run.Destination != "offsite" || run.Stamp != "2026-09-12T20-20-29Z" {
		t.Fatalf("%+v", run)
	}
	if run.Scheduled {
		t.Fatal("this one was asked for, not scheduled")
	}
	if run.Error != "" || run.Copied == 0 || run.FinishedAt.IsZero() {
		t.Fatalf("a finished, successful run: %+v", run)
	}
}

// A failure that is not written down is a failure nobody finds out about until
// they need the backup.
func TestAFailedBackupIsWrittenDownWithItsReason(t *testing.T) {
	logInTempDir(t)

	app, dir := appWithOneBindAndOneVolume(t)
	docker := &fakeBackupDocker{mountpoints: map[string]string{"demo_data": "/var/lib/docker/volumes/demo_data/_data"}}
	copier := &fakeCopier{failOn: dir + "/config"}

	if _, err := BackupOnDemand(context.Background(), app, docker, copier, BackupOptions{
		Destination: "offsite", Stamp: "2026-09-12T20-20-29Z", HoldStill: false,
	}); err == nil {
		t.Fatal("want the failure back")
	}

	runs, err := BackupRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Error == "" {
		t.Fatalf("the failure, with its reason: %+v", runs)
	}
}
