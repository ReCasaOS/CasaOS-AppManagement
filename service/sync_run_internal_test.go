package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/rclone"
)

type syncCall struct {
	src, dst string
	o        rclone.SyncOptions
}

type bisyncCall struct {
	path1, path2 string
	o            rclone.BisyncOptions
}

// fakeSyncEngine records what a pass asked of rclone and answers from its maps.
type fakeSyncEngine struct {
	calls     []string
	syncs     []syncCall
	bisyncs   []bisyncCall
	empty     map[string]bool  // fs -> empty; absent means not empty
	emptyErr  map[string]error // fs -> error of IsEmpty
	counts    map[string]int64 // fs -> files
	folders   map[string][]string
	purged    []string
	syncErr   error
	bisyncErr []error // by call; past the end, nil
	during    func()  // runs inside a sync or bisync call
}

func (f *fakeSyncEngine) SyncDirectory(ctx context.Context, srcFs, dstFs string, o rclone.SyncOptions) error {
	f.calls = append(f.calls, "sync")
	f.syncs = append(f.syncs, syncCall{srcFs, dstFs, o})
	if f.during != nil {
		f.during()
	}
	if f.syncErr == nil && ctx.Err() != nil {
		return ctx.Err()
	}

	return f.syncErr
}

func (f *fakeSyncEngine) BisyncDirectory(ctx context.Context, path1, path2 string, o rclone.BisyncOptions) error {
	f.calls = append(f.calls, "bisync")
	f.bisyncs = append(f.bisyncs, bisyncCall{path1, path2, o})
	if f.during != nil {
		f.during()
	}
	if i := len(f.bisyncs) - 1; i < len(f.bisyncErr) && f.bisyncErr[i] != nil {
		return f.bisyncErr[i]
	}

	return ctx.Err()
}

func (f *fakeSyncEngine) CountFiles(_ context.Context, fs string) (int64, error) {
	f.calls = append(f.calls, "count "+fs)
	return f.counts[fs], nil
}

func (f *fakeSyncEngine) IsEmpty(fs string) (bool, error) {
	f.calls = append(f.calls, "empty "+fs)
	if err := f.emptyErr[fs]; err != nil {
		return false, err
	}

	return f.empty[fs], nil
}

func (f *fakeSyncEngine) Folders(fs, remote string) ([]string, error) {
	f.calls = append(f.calls, "folders "+fs+" "+remote)
	names, ok := f.folders[fs+remote]
	if !ok {
		return nil, errors.New("error in ListJSON: directory not found")
	}

	return names, nil
}

func (f *fakeSyncEngine) Purge(_ context.Context, destinationName, remotePath string) error {
	f.purged = append(f.purged, destinationName+":"+remotePath)
	return nil
}

func (f *fakeSyncEngine) touchedRclone() bool {
	return len(f.syncs)+len(f.bisyncs)+len(f.purged) > 0
}

var syncRunNow = time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC)

const syncRunStamp = "2026-11-16T03-00-00Z"

// syncRunBox is syncBox with a still clock and one stored task whose box folder holds a file.
func syncRunBox(t *testing.T, direction SyncDirection) (SyncTask, *fakeSyncEngine) {
	t.Helper()

	data := syncBox(t)
	was := backupClock
	backupClock = func() time.Time { return syncRunNow }
	t.Cleanup(func() { backupClock = was })
	syncDestinationKind = func(name string) (string, bool, error) { return "s3", false, nil }

	task := goodTask(data)
	task.ID = "s0123456789abcdef"
	task.Direction = direction
	task.Mirror = direction == SyncBoth
	task.FirstPassDone = true
	if err := os.MkdirAll(task.LocalPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task.LocalPath, "a.jpg"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	storeSyncTask(t, task)

	return task, &fakeSyncEngine{}
}

func storeSyncTask(t *testing.T, task SyncTask) {
	t.Helper()
	if err := writeSyncTasks([]SyncTask{task}); err != nil {
		t.Fatal(err)
	}
}

func storedSyncTask(t *testing.T, id string) SyncTask {
	t.Helper()
	tasks, err := SyncTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("no task %s", id)

	return SyncTask{}
}

func cloudOf(task SyncTask) string { return rclone.Destination(task.Destination, task.RemotePath) }

func wantFailed(t *testing.T, task SyncTask, contains string) {
	t.Helper()
	if task.LastResult != "failed" || !strings.Contains(task.LastError, contains) || !task.LastRun.Equal(syncRunNow) {
		t.Fatalf("want failed with %q at %v, got %q %q at %v", contains, syncRunNow, task.LastResult, task.LastError, task.LastRun)
	}
}

func TestAMissingLocalFolderFailsThePass(t *testing.T) {
	task, engine := syncRunBox(t, SyncUpload)
	if err := os.RemoveAll(task.LocalPath); err != nil {
		t.Fatal(err)
	}

	runSync(context.Background(), task, syncNormal, engine)

	wantFailed(t, storedSyncTask(t, task.ID), "the folder on this box is missing")
	if len(engine.calls) != 0 {
		t.Fatalf("the engine was asked %v", engine.calls)
	}
}

func TestAnEmptySourceNeverReachesRclone(t *testing.T) {
	t.Run("download from an empty cloud", func(t *testing.T) {
		task, engine := syncRunBox(t, SyncDownload)
		task.Mirror = true
		storeSyncTask(t, task)
		engine.empty = map[string]bool{cloudOf(task): true}

		runSync(context.Background(), task, syncNormal, engine)

		wantFailed(t, storedSyncTask(t, task.ID), "the source is empty")
		if engine.touchedRclone() {
			t.Fatalf("rclone was asked %v", engine.calls)
		}
	})

	t.Run("upload of an empty folder", func(t *testing.T) {
		task, engine := syncRunBox(t, SyncUpload)
		task.Mirror = true
		storeSyncTask(t, task)
		if err := os.Remove(filepath.Join(task.LocalPath, "a.jpg")); err != nil {
			t.Fatal(err)
		}

		runSync(context.Background(), task, syncNormal, engine)

		wantFailed(t, storedSyncTask(t, task.ID), "the source is empty")
		if engine.touchedRclone() {
			t.Fatalf("rclone was asked %v", engine.calls)
		}
	})

	t.Run("both ways with one side empty", func(t *testing.T) {
		for _, emptyBox := range []bool{true, false} {
			task, engine := syncRunBox(t, SyncBoth)
			if emptyBox {
				if err := os.Remove(filepath.Join(task.LocalPath, "a.jpg")); err != nil {
					t.Fatal(err)
				}
			} else {
				engine.empty = map[string]bool{cloudOf(task): true}
			}

			runSync(context.Background(), task, syncNormal, engine)

			wantFailed(t, storedSyncTask(t, task.ID), "the source is empty")
			if engine.touchedRclone() {
				t.Fatalf("empty box %v: rclone was asked %v", emptyBox, engine.calls)
			}
		}
	})

	t.Run("an explicit first pass unites an empty side", func(t *testing.T) {
		task, engine := syncRunBox(t, SyncBoth)
		engine.empty = map[string]bool{cloudOf(task): true}

		runSync(context.Background(), task, syncFirstPass, engine)

		if len(engine.bisyncs) != 2 || storedSyncTask(t, task.ID).LastResult != "ok" {
			t.Fatalf("a first pass may start from one empty side: %v", engine.calls)
		}
	})
}

func TestAGoneDestinationIsNamedAndChangesNothing(t *testing.T) {
	task, engine := syncRunBox(t, SyncBoth)
	engine.emptyErr = map[string]error{cloudOf(task): errors.New(`didn't find section in config file`)}

	runSync(context.Background(), task, syncNormal, engine)

	stored := storedSyncTask(t, task.ID)
	wantFailed(t, stored, task.Destination)
	if stored.NeedsFirstPass || !stored.FirstPassDone || engine.touchedRclone() {
		t.Fatalf("a gone destination changed the task or reached rclone: %+v %v", stored, engine.calls)
	}
}

func TestASymlinkSwappedAfterSavingIsCaughtAtRun(t *testing.T) {
	task, engine := syncRunBox(t, SyncUpload)
	real := task.LocalPath
	link := filepath.Join(filepath.Dir(real), "Link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	task.LocalPath = link
	storeSyncTask(t, task)

	// saved while it led to a folder of the user's; it now leads to the system
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}

	runSync(context.Background(), task, syncNormal, engine)

	if stored := storedSyncTask(t, task.ID); stored.LastResult != "failed" || stored.LastError == "" {
		t.Fatalf("a link to /etc ran: %+v", stored)
	}
	if len(engine.calls) != 0 {
		t.Fatalf("the engine was asked %v", engine.calls)
	}
}

func TestOneWayOptions(t *testing.T) {
	t.Run("copy, archived", func(t *testing.T) {
		for _, direction := range []SyncDirection{SyncDownload, SyncUpload} {
			task, engine := syncRunBox(t, direction)
			task.Archive = true
			storeSyncTask(t, task)

			runSync(context.Background(), task, syncNormal, engine)

			if len(engine.syncs) != 1 {
				t.Fatalf("%s: %v", direction, engine.calls)
			}
			call := engine.syncs[0]
			if call.o.Mirror {
				t.Fatalf("%s: a copy mirrored", direction)
			}
			src, dst, archive := cloudOf(task), task.LocalPath, task.localArchive(syncRunStamp)
			if direction == SyncUpload {
				src, dst, archive = task.LocalPath, cloudOf(task), rclone.Destination(task.Destination, task.remoteArchive(syncRunStamp))
			}
			if call.src != src || call.dst != dst || call.o.BackupDir != archive {
				t.Fatalf("%s: got %+v, want %s -> %s archived in %s", direction, call, src, dst, archive)
			}
		}
	})

	t.Run("mirror caps deletions at half, never under ten", func(t *testing.T) {
		for files, cap := range map[int64]int64{40: 20, 6: 10} {
			task, engine := syncRunBox(t, SyncUpload)
			task.Mirror = true
			storeSyncTask(t, task)
			engine.counts = map[string]int64{cloudOf(task): files}

			runSync(context.Background(), task, syncNormal, engine)

			if len(engine.syncs) != 1 || !engine.syncs[0].o.Mirror || engine.syncs[0].o.MaxDelete != cap {
				t.Fatalf("%d files: %+v", files, engine.syncs)
			}
			if engine.syncs[0].o.BackupDir != "" {
				t.Fatalf("no archive asked, got %q", engine.syncs[0].o.BackupDir)
			}
		}
	})
}

func TestTheFirstTwoWayPassIsTwoPasses(t *testing.T) {
	task, engine := syncRunBox(t, SyncBoth)
	task.FirstPassDone = false
	task.Archive = true
	storeSyncTask(t, task)

	runSync(context.Background(), task, syncNormal, engine)

	if len(engine.bisyncs) != 2 || !engine.bisyncs[0].o.Resync || engine.bisyncs[1].o.Resync {
		t.Fatalf("want a resync then a normal pass, got %+v", engine.bisyncs)
	}
	for _, call := range engine.bisyncs {
		if call.path1 != task.LocalPath || call.path2 != cloudOf(task) || call.o.Workdir != task.workdir() ||
			call.o.BackupDir1 != task.localArchive(syncRunStamp) ||
			call.o.BackupDir2 != rclone.Destination(task.Destination, task.remoteArchive(syncRunStamp)) || call.o.Force {
			t.Fatalf("pass options: %+v", call)
		}
	}
	if stored := storedSyncTask(t, task.ID); !stored.FirstPassDone || stored.LastResult != "ok" {
		t.Fatalf("after both passes: %+v", stored)
	}

	task, engine = syncRunBox(t, SyncBoth)
	task.FirstPassDone = false
	storeSyncTask(t, task)
	engine.bisyncErr = []error{nil, errors.New("rclone: boom")}

	runSync(context.Background(), task, syncNormal, engine)

	if stored := storedSyncTask(t, task.ID); stored.FirstPassDone || stored.LastResult != "failed" {
		t.Fatalf("a failed second pass is no first pass: %+v", stored)
	}

	task, engine = syncRunBox(t, SyncBoth)
	runSync(context.Background(), task, syncNormal, engine)
	if len(engine.bisyncs) != 1 || engine.bisyncs[0].o.Resync {
		t.Fatalf("once done, one normal pass: %+v", engine.bisyncs)
	}
}

func TestATwoWayPassWithoutAWorkdirCallsNothing(t *testing.T) {
	task, engine := syncRunBox(t, SyncBoth)
	task.ID = "not-one-of-ours"
	storeSyncTask(t, task)

	runSync(context.Background(), task, syncFirstPass, engine)

	if stored := storedSyncTask(t, task.ID); stored.LastResult != "failed" || stored.LastError == "" {
		t.Fatalf("got %+v", stored)
	}
	if len(engine.calls) != 0 {
		t.Fatalf("the engine was asked %v", engine.calls)
	}
}

func TestDriveSkipsGoogleDocs(t *testing.T) {
	task, engine := syncRunBox(t, SyncBoth)
	task.Archive = true
	storeSyncTask(t, task)
	syncDestinationKind = func(string) (string, bool, error) { return "drive", false, nil }

	runSync(context.Background(), task, syncNormal, engine)

	if len(engine.bisyncs) != 1 {
		t.Fatalf("%v", engine.calls)
	}
	call := engine.bisyncs[0]
	if call.path2 != "casaos-backup-drive,skip_gdocs=true:Photos" ||
		call.o.BackupDir2 != "casaos-backup-drive,skip_gdocs=true:Photos-archive/"+syncRunStamp {
		t.Fatalf("got %q and %q", call.path2, call.o.BackupDir2)
	}
}

func TestTooManyDeletesParksTheTask(t *testing.T) {
	for _, direction := range []SyncDirection{SyncUpload, SyncBoth} {
		task, engine := syncRunBox(t, direction)
		task.Mirror = true
		storeSyncTask(t, task)
		tooMany := fmt.Errorf("%w: rclone: failed to delete 12 files", rclone.ErrTooManyDeletes)
		engine.syncErr, engine.bisyncErr = tooMany, []error{tooMany}

		runSync(context.Background(), task, syncNormal, engine)

		stored := storedSyncTask(t, task.ID)
		if stored.LastResult != "failed" || !stored.NeedsDeleteConfirm {
			t.Fatalf("%s: %+v", direction, stored)
		}
	}
}

func TestConfirmingLiftsTheCapOnce(t *testing.T) {
	for _, direction := range []SyncDirection{SyncUpload, SyncBoth} {
		task, engine := syncRunBox(t, direction)
		task.Mirror = true
		task.NeedsDeleteConfirm = true
		storeSyncTask(t, task)
		engine.counts = map[string]int64{cloudOf(task): 100}

		runSync(context.Background(), task, syncConfirmDeletes, engine)

		if direction == SyncUpload && (len(engine.syncs) != 1 || engine.syncs[0].o.MaxDelete != -1) {
			t.Fatalf("upload: %+v", engine.syncs)
		}
		if direction == SyncBoth && (len(engine.bisyncs) != 1 || !engine.bisyncs[0].o.Force) {
			t.Fatalf("both: %+v", engine.bisyncs)
		}
		if stored := storedSyncTask(t, task.ID); stored.NeedsDeleteConfirm || stored.LastResult != "ok" {
			t.Fatalf("%s: %+v", direction, stored)
		}

		// the next normal pass is capped again
		engine = &fakeSyncEngine{counts: engine.counts}
		runSync(context.Background(), storedSyncTask(t, task.ID), syncNormal, engine)
		if direction == SyncUpload && engine.syncs[0].o.MaxDelete != 50 {
			t.Fatalf("cap not back: %+v", engine.syncs)
		}
		if direction == SyncBoth && engine.bisyncs[0].o.Force {
			t.Fatalf("force not dropped: %+v", engine.bisyncs)
		}
	}
}

func TestALockedPassLeavesNoTrace(t *testing.T) {
	task, engine := syncRunBox(t, SyncBoth)
	task.LastRun, task.LastResult, task.LastError = syncRunNow.Add(-time.Hour), "ok", ""
	storeSyncTask(t, task)
	engine.bisyncErr = []error{fmt.Errorf("%w: prior lock file found", rclone.ErrSyncLocked)}

	runSync(context.Background(), task, syncNormal, engine)

	stored := storedSyncTask(t, task.ID)
	if !stored.LastRun.Equal(task.LastRun) || stored.LastResult != "ok" || stored.LastError != "" {
		t.Fatalf("a locked pass wrote %+v", stored)
	}
}

func TestLostStateAsksForAFirstPass(t *testing.T) {
	aborted := fmt.Errorf("%w: rclone: bisync aborted", rclone.ErrBisyncAborted)

	task, engine := syncRunBox(t, SyncBoth)
	engine.bisyncErr = []error{aborted}
	runSync(context.Background(), task, syncNormal, engine)
	if stored := storedSyncTask(t, task.ID); stored.LastResult != "failed" || !stored.NeedsFirstPass {
		t.Fatalf("no listing left: %+v", stored)
	}

	task, engine = syncRunBox(t, SyncBoth)
	if err := os.MkdirAll(task.workdir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task.workdir(), "a..b.path1.lst-old"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	engine.bisyncErr = []error{aborted}
	runSync(context.Background(), task, syncNormal, engine)
	if stored := storedSyncTask(t, task.ID); stored.LastResult != "failed" || stored.NeedsFirstPass {
		t.Fatalf("a cut pass recovers by itself: %+v", stored)
	}
}

func TestAPassAfterAnInterruptionSaysRecovered(t *testing.T) {
	for direction, want := range map[SyncDirection]string{SyncBoth: "recovered", SyncDownload: "ok"} {
		task, engine := syncRunBox(t, direction)
		task.LastResult, task.LastError, task.NeedsFirstPass = "stopped", "stopped from the interface", true
		storeSyncTask(t, task)

		runSync(context.Background(), task, syncNormal, engine)

		stored := storedSyncTask(t, task.ID)
		if stored.LastResult != want || stored.LastError != "" || stored.NeedsFirstPass || !stored.LastRun.Equal(syncRunNow) {
			t.Fatalf("%s: want %s, got %+v", direction, want, stored)
		}
	}
}

func TestAStoppedPassSaysStopped(t *testing.T) {
	task, engine := syncRunBox(t, SyncUpload)
	engine.during = func() {
		for _, backup := range LiveBackups() {
			if backup.App == task.Name {
				_ = StopLiveBackup(backup.ID)
			}
		}
	}

	runSync(context.Background(), task, syncNormal, engine)

	if stored := storedSyncTask(t, task.ID); stored.LastResult != "stopped" {
		t.Fatalf("got %+v", stored)
	}
}

func TestOldArchivesGoAfterAGoodPassOnly(t *testing.T) {
	old := syncRunNow.Add(-31 * 24 * time.Hour).Format("2006-01-02T15-04-05Z")
	recent := syncRunNow.Add(-29 * 24 * time.Hour).Format("2006-01-02T15-04-05Z")

	setup := func(t *testing.T, days int) (SyncTask, *fakeSyncEngine, string) {
		task, engine := syncRunBox(t, SyncBoth)
		task.Archive, task.ArchiveDays = true, days
		storeSyncTask(t, task)
		box := syncArchiveFolder(task.LocalPath)
		for _, name := range []string{old, recent, "notes"} {
			if err := os.MkdirAll(filepath.Join(box, name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		engine.folders = map[string][]string{rclone.Destination(task.Destination, "") + task.RemotePath + "-archive": {old, recent, "notes"}}

		return task, engine, box
	}
	left := func(t *testing.T, box string) []string {
		entries, err := os.ReadDir(box)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		slices.Sort(names)

		return names
	}

	task, engine, box := setup(t, 30)
	runSync(context.Background(), task, syncNormal, engine)
	if got := left(t, box); !slices.Equal(got, []string{recent, "notes"}) {
		t.Fatalf("box archive left %v", got)
	}
	if !slices.Equal(engine.purged, []string{"drive:Photos-archive/" + old}) {
		t.Fatalf("cloud purged %v", engine.purged)
	}

	task, engine, box = setup(t, 30)
	engine.bisyncErr = []error{errors.New("rclone: boom")}
	runSync(context.Background(), task, syncNormal, engine)
	if got := left(t, box); len(got) != 3 || len(engine.purged) != 0 {
		t.Fatalf("a failed pass purged: %v %v", got, engine.purged)
	}

	task, engine, box = setup(t, 0)
	runSync(context.Background(), task, syncNormal, engine)
	if got := left(t, box); len(got) != 3 || len(engine.purged) != 0 {
		t.Fatalf("0 days purged: %v %v", got, engine.purged)
	}
}

func TestARunningSyncIsListedAsSync(t *testing.T) {
	task, engine := syncRunBox(t, SyncUpload)
	var seen []LiveBackup
	engine.during = func() { seen = LiveBackups() }

	runSync(context.Background(), task, syncNormal, engine)

	if !slices.ContainsFunc(seen, func(b LiveBackup) bool { return b.Kind == "sync" && b.App == task.Name }) {
		t.Fatalf("running: %+v", seen)
	}
	if slices.ContainsFunc(LiveBackups(), func(b LiveBackup) bool { return b.App == task.Name }) {
		t.Fatal("still listed after the pass")
	}
}

func TestAMissingCloudFolderIsAnEmptySource(t *testing.T) {
	for _, direction := range []SyncDirection{SyncDownload, SyncBoth} {
		task, engine := syncRunBox(t, direction)
		task.Mirror = true
		storeSyncTask(t, task)
		engine.emptyErr = map[string]error{cloudOf(task): errors.New("error in ListJSON: directory not found")}

		runSync(context.Background(), task, syncNormal, engine)

		wantFailed(t, storedSyncTask(t, task.ID), "the source is empty")
		if engine.touchedRclone() {
			t.Fatalf("%s: rclone was asked %v", direction, engine.calls)
		}
	}
}

func TestANewTwoWayTaskWithAnEmptySideStillRunsItsFirstPass(t *testing.T) {
	task, engine := syncRunBox(t, SyncBoth)
	task.FirstPassDone = false
	storeSyncTask(t, task)
	engine.empty = map[string]bool{cloudOf(task): true}

	runSync(context.Background(), task, syncNormal, engine)

	if len(engine.bisyncs) != 2 || !engine.bisyncs[0].o.Resync || engine.bisyncs[1].o.Resync {
		t.Fatalf("want a resync then a normal pass, got %v", engine.calls)
	}
	if stored := storedSyncTask(t, task.ID); stored.LastResult != "ok" || !stored.FirstPassDone {
		t.Fatalf("got %+v", stored)
	}
}

func TestAPassThroughALinkArchivesBesideTheRealFolder(t *testing.T) {
	task, engine := syncRunBox(t, SyncDownload)
	real, err := filepath.EvalSymlinks(task.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(filepath.Dir(task.LocalPath)), "Link")
	if err := os.Symlink(task.LocalPath, link); err != nil {
		t.Fatal(err)
	}
	task.LocalPath, task.Archive = link, true
	storeSyncTask(t, task)

	runSync(context.Background(), task, syncNormal, engine)

	want := filepath.Join(filepath.Dir(real), "."+filepath.Base(real)+"-archive", syncRunStamp)
	if len(engine.syncs) != 1 || engine.syncs[0].dst != real || engine.syncs[0].o.BackupDir != want {
		t.Fatalf("want %s archived in %s, got %+v", real, want, engine.syncs)
	}
}

func TestEachSidePurgesOnlyTheArchivesItWrote(t *testing.T) {
	old := syncRunNow.Add(-31 * 24 * time.Hour).Format(syncStampLayout)
	for _, direction := range []SyncDirection{SyncDownload, SyncUpload} {
		task, engine := syncRunBox(t, direction)
		task.Archive, task.ArchiveDays = true, 7
		storeSyncTask(t, task)
		box := filepath.Join(syncArchiveFolder(task.LocalPath), old)
		if err := os.MkdirAll(box, 0o755); err != nil {
			t.Fatal(err)
		}
		engine.folders = map[string][]string{rclone.Destination(task.Destination, "") + task.RemotePath + "-archive": {old}}

		runSync(context.Background(), task, syncNormal, engine)

		_, statErr := os.Stat(box)
		listedCloud := slices.ContainsFunc(engine.calls, func(c string) bool { return strings.HasPrefix(c, "folders ") })
		switch direction {
		case SyncDownload:
			if listedCloud || len(engine.purged) != 0 || !os.IsNotExist(statErr) {
				t.Fatalf("download: cloud %v %v, box %v", engine.calls, engine.purged, statErr)
			}
		case SyncUpload:
			if statErr != nil || len(engine.purged) != 1 {
				t.Fatalf("upload: box %v, cloud %v", statErr, engine.purged)
			}
		}
	}
}
