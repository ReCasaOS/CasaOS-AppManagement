package service

import (
	stdjson "encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func logInTempDir(t *testing.T) {
	t.Helper()

	was := BackupLogPath
	BackupLogPath = filepath.Join(t.TempDir(), "runs.json")
	t.Cleanup(func() { BackupLogPath = was })
}

func run(app string, when time.Time, failure string) BackupRunRecord {
	return BackupRunRecord{
		App: app, Destination: "offsite", Stamp: when.Format("2006-01-02T15-04-05Z"),
		StartedAt: when, FinishedAt: when.Add(time.Minute), Error: failure,
	}
}

// A feature that logs only its successes is how a box goes six months backing
// nothing up while its screen says everything is fine.
func TestAFailedRunIsRecordedLikeAnyOther(t *testing.T) {
	logInTempDir(t)

	if err := RecordBackupRun(run("nextcloud", at(10, 3, 0), "AccessDenied")); err != nil {
		t.Fatal(err)
	}

	records, err := BackupRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("want one, got %d", len(records))
	}
	if records[0].Succeeded() {
		t.Fatal("this one failed, and the record has to say so")
	}
	if records[0].Error != "AccessDenied" {
		t.Fatalf("with the reason: %q", records[0].Error)
	}
}

func TestRunsComeBackNewestFirst(t *testing.T) {
	logInTempDir(t)

	for _, day := range []int{8, 10, 9} {
		if err := RecordBackupRun(run("app", at(day, 3, 0), "")); err != nil {
			t.Fatal(err)
		}
	}

	records, _ := BackupRuns()
	if len(records) != 3 {
		t.Fatalf("want 3, got %d", len(records))
	}
	if !records[0].StartedAt.After(records[1].StartedAt) || !records[1].StartedAt.After(records[2].StartedAt) {
		t.Fatalf("out of order: %v", records)
	}
}

// Enough to see a destination that has been failing every night for a fortnight,
// without the file growing without end.
func TestTheLogStopsGrowing(t *testing.T) {
	logInTempDir(t)

	for i := 0; i < backupLogKept+25; i++ {
		if err := RecordBackupRun(run("app", at(10, 0, 0).Add(time.Duration(i)*time.Minute), "")); err != nil {
			t.Fatal(err)
		}
	}

	records, _ := BackupRuns()
	if len(records) != backupLogKept {
		t.Fatalf("want %d, got %d", backupLogKept, len(records))
	}

	// and it is the OLD ones that went
	oldest := records[len(records)-1].StartedAt
	if !oldest.After(at(10, 0, 0)) {
		t.Fatal("the oldest runs are the ones dropped")
	}
}

func TestNothingHasRunYet(t *testing.T) {
	logInTempDir(t)

	records, err := BackupRuns()
	if err != nil {
		t.Fatalf("a missing file is not an error, it is an empty history: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("got %v", records)
	}
}

// Quietly starting over would throw away every past run at the first bad byte,
// and the person who most needs this record is the one whose box is misbehaving.
func TestALogThatWillNotParseIsAnErrorRatherThanAFreshStart(t *testing.T) {
	logInTempDir(t)

	if err := os.WriteFile(BackupLogPath, []byte("{ this is not the log"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := BackupRuns(); err == nil {
		t.Fatal("want an error rather than an empty history")
	}
	if err := RecordBackupRun(run("app", at(10, 3, 0), "")); err == nil {
		t.Fatal("and writing must not overwrite what could not be read")
	}
}

// A log truncated half-way through a write is a log nobody can read at all, which
// loses every run rather than the one being added.
func TestTheLogIsReplacedRatherThanRewrittenInPlace(t *testing.T) {
	logInTempDir(t)

	if err := RecordBackupRun(run("app", at(10, 3, 0), "")); err != nil {
		t.Fatal(err)
	}

	// no temp file is left behind
	if _, err := os.Stat(BackupLogPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("the temporary file should have been renamed away")
	}

	content, err := os.ReadFile(BackupLogPath)
	if err != nil {
		t.Fatal(err)
	}
	var records []BackupRunRecord
	if err := stdjson.Unmarshal(content, &records); err != nil {
		t.Fatalf("the file on disk must be readable: %v", err)
	}
}
