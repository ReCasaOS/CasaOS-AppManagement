package service

import (
	stdjson "encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// syncBox points the tasks file, the work folders and the allowed roots inside a temp folder
// and says there is one plain destination, "drive", and one encrypted, "vault".
func syncBox(t *testing.T) (data string) {
	t.Helper()

	root := t.TempDir()
	data = filepath.Join(root, "DATA")
	if err := os.MkdirAll(filepath.Join(data, "AppData"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldPath, oldState, oldRoots, oldApp := SyncTasksPath, BisyncStateDir, syncLocalRoots, syncAppData
	oldKind, oldDests, oldNames, oldMount, oldStamps, oldPlace := syncDestinationKind, syncDestinations, syncAppNames, isMountRoot, syncStampFolders, syncDestinationPlace
	t.Cleanup(func() {
		SyncTasksPath, BisyncStateDir, syncLocalRoots, syncAppData = oldPath, oldState, oldRoots, oldApp
		syncDestinationKind, syncDestinations, syncAppNames, isMountRoot, syncStampFolders, syncDestinationPlace = oldKind, oldDests, oldNames, oldMount, oldStamps, oldPlace
		syncUnrecorded.Lock()
		clear(syncUnrecorded.ids)
		syncUnrecorded.Unlock()
	})

	SyncTasksPath = filepath.Join(root, "state", "sync.json")
	BisyncStateDir = filepath.Join(root, "state", "bisync")
	syncLocalRoots = []string{data, filepath.Join(root, "media"), filepath.Join(root, "mnt")}
	syncAppData = filepath.Join(data, "AppData")
	syncDestinations = func() ([]string, error) { return []string{"drive", "vault"}, nil }
	syncDestinationKind = func(name string) (string, bool, error) { return "drive", name == "vault", nil }
	syncAppNames = func() []string { return []string{"immich"} }
	isMountRoot = func(string) bool { return false }
	syncStampFolders = func(string, string) ([]string, error) { return nil, errors.New("directory not found") }
	syncDestinationPlace = func(string) (string, string, string, error) { return "drive", "", "", nil }

	return data
}

func goodTask(data string) SyncTask {
	return SyncTask{
		Name: "photos", Destination: "drive", RemotePath: "Photos", LocalPath: filepath.Join(data, "Gallery", "Photos"),
		Direction: SyncUpload, Every: ScheduleDaily, At: "03:00", Enabled: true,
	}
}

func refused(t *testing.T, err error, what string) {
	t.Helper()

	var refusal SyncRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("%s: want a refusal, got %v", what, err)
	}
}

func TestALocalFolderMustBeAUsersFolder(t *testing.T) {
	data := syncBox(t)
	if err := os.Symlink("/etc", filepath.Join(data, "l")); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(data, "Disk")
	if err := os.MkdirAll(disk, 0o755); err != nil {
		t.Fatal(err)
	}
	isMountRoot = func(folder string) bool { return folder == disk }

	for _, local := range []string{"/", "/etc", data, filepath.Join(data, "AppData", "x"), filepath.Join(data, "l"), disk, "relative/dir"} {
		task := goodTask(data)
		task.LocalPath = local
		refused(t, checkSyncTask(task, nil), local)
	}

	if err := checkSyncTask(goodTask(data), nil); err != nil {
		t.Fatalf("a folder under /DATA is allowed: %v", err)
	}
}

func TestTwoTasksNeverShareAFolder(t *testing.T) {
	data := syncBox(t)
	other := goodTask(data)
	other.Name = "other"

	local := goodTask(data)
	local.RemotePath = "Elsewhere"
	local.LocalPath = filepath.Join(other.LocalPath, "sub")
	refused(t, checkSyncTask(local, []SyncTask{other}), "local inside another")

	remote := goodTask(data)
	remote.LocalPath = filepath.Join(data, "Gallery", "Videos")
	remote.RemotePath = "Photos/2024"
	refused(t, checkSyncTask(remote, []SyncTask{other}), "remote inside another")

	remote.Destination = "vault"
	if err := checkSyncTask(remote, []SyncTask{other}); err != nil {
		t.Fatalf("the same name on another destination is another folder: %v", err)
	}

	archive := goodTask(data)
	archive.Name = "archive"
	archive.RemotePath = "Other"
	archive.LocalPath = filepath.Join(data, "Gallery", ".Photos-archive", "x")
	refused(t, checkSyncTask(archive, []SyncTask{other}), "inside another's archive")
	archive.LocalPath = filepath.Join(data, "Gallery", ".Photos-archive")
	refused(t, checkSyncTask(archive, []SyncTask{other}), "another's archive")

	remoteArchive := goodTask(data)
	remoteArchive.Name = "remote archive"
	remoteArchive.LocalPath = filepath.Join(data, "Gallery", "Videos")
	remoteArchive.RemotePath = "Photos-archive"
	refused(t, checkSyncTask(remoteArchive, []SyncTask{other}), "remote is another's archive")
	remoteArchive.RemotePath = "Photos-archive/2026"
	refused(t, checkSyncTask(remoteArchive, []SyncTask{other}), "remote inside another's archive")
	refused(t, checkSyncTask(other, []SyncTask{remoteArchive}), "archive over another's remote")

	same := goodTask(data)
	same.Name = "other"
	same.LocalPath = filepath.Join(data, "Gallery", "Videos")
	same.RemotePath = "Videos"
	refused(t, checkSyncTask(same, []SyncTask{other}), "same name")
}

func TestTheRemoteFolderIsCleanedAndKeptAwayFromBackups(t *testing.T) {
	data := syncBox(t)

	saved, err := SaveSyncTasks([]SyncTask{func() SyncTask { x := goodTask(data); x.RemotePath = "/Photos/"; return x }()})
	if err != nil || saved[0].RemotePath != "Photos" {
		t.Fatalf("remote path: %q, %v", saved[0].RemotePath, err)
	}

	syncAppNames = func() []string { return []string{"immich", "x-archive"} }
	archived := goodTask(data)
	archived.RemotePath = "x" // its archive "x-archive" is an app's backups
	refused(t, checkSyncTask(archived, nil), "archive in an app's backups")
	archived.RemotePath = "casaos-restic-archive"
	if err := checkSyncTask(archived, nil); err != nil {
		t.Fatalf("a name that only starts like the repository: %v", err)
	}

	for _, remote := range []string{"..", "a/../../b", "", "casaos-restic/x", "casaos-system", "immich/x", "immich"} {
		task := goodTask(data)
		task.RemotePath = remote
		refused(t, checkSyncTask(task, nil), remote)
	}

	task := goodTask(data)
	task.Direction, task.RemotePath = SyncDownload, ""
	if err := checkSyncTask(task, nil); err != nil {
		t.Fatalf("a download may take the whole destination: %v", err)
	}
}

func TestAnEncryptedDestinationOnlyTakesUploads(t *testing.T) {
	data := syncBox(t)

	task := goodTask(data)
	task.Destination = "vault"
	if err := checkSyncTask(task, nil); err != nil {
		t.Fatalf("upload to an encrypted destination: %v", err)
	}
	for _, direction := range []SyncDirection{SyncDownload, SyncBoth} {
		task.Direction = direction
		refused(t, checkSyncTask(task, nil), string(direction))
	}
}

func TestAnUnknownDestinationIsRefused(t *testing.T) {
	data := syncBox(t)

	task := goodTask(data)
	task.Destination = "nowhere"
	refused(t, checkSyncTask(task, nil), "destination")
}

func TestSavingKeepsWhatTheBoxWrote(t *testing.T) {
	data := syncBox(t)

	saved, err := SaveSyncTasks([]SyncTask{goodTask(data)})
	if err != nil {
		t.Fatal(err)
	}
	ran := time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC)
	if err := updateSyncTask(saved[0].ID, func(task *SyncTask) {
		task.LastRun, task.LastResult, task.FirstPassDone, task.LastFiles = ran, "ok", true, 12
	}); err != nil {
		t.Fatal(err)
	}

	put := saved[0]
	put.LastRun, put.LastResult, put.FirstPassDone, put.LastFiles = time.Time{}, "", false, 0
	put.Name = "renamed"
	saved, err = SaveSyncTasks([]SyncTask{put})
	if err != nil {
		t.Fatal(err)
	}

	if !saved[0].LastRun.Equal(ran) || saved[0].LastResult != "ok" || !saved[0].FirstPassDone || saved[0].LastFiles != 12 || saved[0].Name != "renamed" {
		t.Fatalf("the box's fields were lost or the owner's were not taken: %+v", saved[0])
	}
}

func TestMovingATwoWayTaskStartsItOver(t *testing.T) {
	data := syncBox(t)

	both := goodTask(data)
	both.Direction = SyncBoth
	saved, err := SaveSyncTasks([]SyncTask{both})
	if err != nil {
		t.Fatal(err)
	}
	if err := updateSyncTask(saved[0].ID, func(task *SyncTask) { task.FirstPassDone = true }); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(saved[0].workdir(), 0o755); err != nil {
		t.Fatal(err)
	}

	moved := saved[0]
	moved.RemotePath = "Pictures"
	saved, err = SaveSyncTasks([]SyncTask{moved})
	if err != nil {
		t.Fatal(err)
	}

	if saved[0].FirstPassDone {
		t.Fatal("a moved two-way task must make its first pass again")
	}
	if _, err := os.Stat(saved[0].workdir()); !os.IsNotExist(err) {
		t.Fatalf("the work folder must be gone: %v", err)
	}

	// a task taken off the list loses its work folder too
	if err := os.MkdirAll(saved[0].workdir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveSyncTasks(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(saved[0].workdir()); !os.IsNotExist(err) {
		t.Fatalf("the work folder of a removed task must be gone: %v", err)
	}
}

func TestBothAlwaysMirrors(t *testing.T) {
	data := syncBox(t)

	task := goodTask(data)
	task.Direction, task.Mirror = SyncBoth, false
	saved, err := SaveSyncTasks([]SyncTask{task})
	if err != nil || !saved[0].Mirror {
		t.Fatalf("mirror: %+v, %v", saved, err)
	}
}

func TestABrokenFileIsAnErrorNotAnEmptyList(t *testing.T) {
	data := syncBox(t)

	if err := os.MkdirAll(filepath.Dir(SyncTasksPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SyncTasksPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := SyncTasks(); err == nil {
		t.Fatal("a broken file must be an error")
	}
	if _, err := SaveSyncTasks([]SyncTask{goodTask(data)}); err == nil {
		t.Fatal("a save over a broken file must be an error")
	}
	if content, _ := os.ReadFile(SyncTasksPath); string(content) != "{not json" {
		t.Fatalf("the broken file must be left as it was: %q", content)
	}
}

func TestUpdateOfAVanishedTaskWritesNothing(t *testing.T) {
	syncBox(t)

	if err := updateSyncTask("gone", func(*SyncTask) { t.Fatal("nothing to change") }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SyncTasksPath); !os.IsNotExist(err) {
		t.Fatalf("nothing may be written: %v", err)
	}
}

func TestScheduleFieldsAreChecked(t *testing.T) {
	data := syncBox(t)

	for name, change := range map[string]func(*SyncTask){
		"every":     func(x *SyncTask) { x.Every = "monthly" },
		"at":        func(x *SyncTask) { x.At = "25:00" },
		"days":      func(x *SyncTask) { x.ArchiveDays = -1 },
		"weekday":   func(x *SyncTask) { x.Every, x.Weekday = ScheduleWeekly, 9 },
		"name":      func(x *SyncTask) { x.Name = " " },
		"hourly at": func(x *SyncTask) { x.Every, x.At = ScheduleHourly, "xx" },
	} {
		task := goodTask(data)
		change(&task)
		refused(t, checkSyncTask(task, nil), name)
	}

	task := goodTask(data)
	task.Every, task.At = ScheduleHourly, "07:20"
	saved, err := SaveSyncTasks([]SyncTask{task})
	if err != nil || saved[0].At != "00:20" {
		t.Fatalf("an hourly task keeps only its minute: %+v, %v", saved, err)
	}
}

func TestArchivesSitBesideTheFolderNeverInside(t *testing.T) {
	task := SyncTask{LocalPath: "/DATA/Gallery/Photos", RemotePath: "Photos"}
	stamp := "2026-11-16T03-00-00Z"

	if got := task.localArchive(stamp); got != "/DATA/Gallery/.Photos-archive/"+stamp {
		t.Fatalf("local archive: %q", got)
	}
	if got := task.remoteArchive(stamp); got != "Photos-archive/"+stamp {
		t.Fatalf("remote archive: %q", got)
	}
	if within(task.localArchive(stamp), task.LocalPath) {
		t.Fatal("the local archive must not lie in the synced folder")
	}
	if got := (SyncTask{LocalPath: "/DATA/x"}).remoteArchive(stamp); got != "" {
		t.Fatalf("no remote folder, no remote archive: %q", got)
	}
}

func TestAnIdTheBoxDidNotIssueNeverNamesAFolder(t *testing.T) {
	data := syncBox(t)

	if err := os.MkdirAll(BisyncStateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(BisyncStateDir), "x")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	stored := []SyncTask{goodTask(data), goodTask(data)}
	stored[0].ID, stored[1].ID, stored[1].Name = "", "../x", "two"
	stored[1].LocalPath = filepath.Join(data, "Other")
	stored[1].RemotePath = "Other"
	encoded, _ := stdjson.Marshal(stored)
	if err := os.WriteFile(SyncTasksPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if stored[0].workdir() != "" || stored[1].workdir() != "" {
		t.Fatal("an id of another shape has no work folder")
	}
	if _, err := SaveSyncTasks(nil); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{BisyncStateDir, outside} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s must survive: %v", dir, err)
		}
	}

	// an id of another shape in a PUT is replaced
	odd := goodTask(data)
	odd.ID = "../x"
	saved, err := SaveSyncTasks([]SyncTask{odd})
	if err != nil || !validSyncID(saved[0].ID) {
		t.Fatalf("id: %+v, %v", saved, err)
	}
}

func TestTwoTasksOfOnePutNeverShareAnId(t *testing.T) {
	data := syncBox(t)

	saved, err := SaveSyncTasks([]SyncTask{goodTask(data)})
	if err != nil {
		t.Fatal(err)
	}
	twin := saved[0]
	twin.Name, twin.RemotePath, twin.LocalPath = "twin", "Twin", filepath.Join(data, "Twin")
	_, err = SaveSyncTasks([]SyncTask{saved[0], twin})
	refused(t, err, "same id twice")
}

func TestADisabledTaskIsNeverDue(t *testing.T) {
	task := goodTask("/DATA")
	task.Enabled = false

	if due, err := syncDue(task, at(10, 7, 0)); due || err != nil {
		t.Errorf("got %v, %v", due, err)
	}
}

func TestAMissedSlotIsCaughtUpOnce(t *testing.T) {
	task := goodTask("/DATA")
	now := at(10, 7, 0)

	task.LastRun = at(8, 3, 0)
	if due, err := syncDue(task, now); !due || err != nil {
		t.Errorf("a slot missed since the day before yesterday: got %v, %v", due, err)
	}

	task.LastRun = now
	if due, _ := syncDue(task, now); due {
		t.Error("a slot already run is not due again")
	}
}

// A task moved to other folders, to another destination or turned around is another task: what
// it waited for was about the old ones. Confirming deletes on folders that never tripped the cap
// would run them uncapped.
func TestEditingAParkedTaskClearsItsFlags(t *testing.T) {
	data := syncBox(t)

	for what, edit := range map[string]func(*SyncTask){
		"remote path": func(task *SyncTask) { task.RemotePath = "Pictures" },
		"local path":  func(task *SyncTask) { task.LocalPath = filepath.Join(data, "Gallery", "Pictures") },
		"destination": func(task *SyncTask) { task.Destination = "vault" },
		"direction":   func(task *SyncTask) { task.Direction = SyncDownload },
	} {
		saved, err := SaveSyncTasks([]SyncTask{func() SyncTask { x := goodTask(data); x.Mirror = true; return x }()})
		if err != nil {
			t.Fatal(err)
		}
		if err := updateSyncTask(saved[0].ID, func(task *SyncTask) { task.NeedsDeleteConfirm, task.NeedsFirstPass = true, true }); err != nil {
			t.Fatal(err)
		}

		edited := saved[0]
		edit(&edited)
		saved, err = SaveSyncTasks([]SyncTask{edited})
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if saved[0].NeedsDeleteConfirm || saved[0].NeedsFirstPass {
			t.Fatalf("%s: the flags of the old folders were kept: %+v", what, saved[0])
		}
	}

	// a two-way task turned one-way and back starts over, its bisync state gone
	both := goodTask(data)
	both.Direction = SyncBoth
	saved, err := SaveSyncTasks([]SyncTask{both})
	if err != nil {
		t.Fatal(err)
	}
	if err := updateSyncTask(saved[0].ID, func(task *SyncTask) { task.FirstPassDone, task.NeedsFirstPass = true, true }); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(saved[0].workdir(), 0o755); err != nil {
		t.Fatal(err)
	}
	upload := saved[0]
	upload.Direction = SyncUpload
	if saved, err = SaveSyncTasks([]SyncTask{upload}); err != nil {
		t.Fatal(err)
	}
	if saved[0].FirstPassDone || saved[0].NeedsFirstPass {
		t.Fatalf("turned one-way: %+v", saved[0])
	}
	if _, err := os.Stat(saved[0].workdir()); !os.IsNotExist(err) {
		t.Fatalf("the bisync state must be gone: %v", err)
	}

	// a name changed is the same task, and so is one whose stored paths were written by hand
	if err := updateSyncTask(saved[0].ID, func(task *SyncTask) {
		task.NeedsDeleteConfirm, task.RemotePath, task.LocalPath = true, "/"+task.RemotePath+"/", task.LocalPath+"/"
	}); err != nil {
		t.Fatal(err)
	}
	renamed := saved[0]
	renamed.Name = "renamed"
	if saved, err = SaveSyncTasks([]SyncTask{renamed}); err != nil || !saved[0].NeedsDeleteConfirm {
		t.Fatalf("a rename keeps the banner: %+v, %v", saved, err)
	}
}

// The dialog sends the whole list on any edit: a download whose disk is unplugged must not have
// its folder made again on the system disk, where the next pass would copy the whole cloud.
func TestSavingDoesNotRecreateAnUnchangedDownloadFolder(t *testing.T) {
	data := syncBox(t)

	task := goodTask(data)
	task.Direction = SyncDownload
	saved, err := SaveSyncTasks([]SyncTask{task})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(task.LocalPath); err != nil {
		t.Fatalf("a new download's folder is made: %v", err)
	}

	if err := os.RemoveAll(task.LocalPath); err != nil { // the disk is unplugged
		t.Fatal(err)
	}
	// and the stored path was written by hand, with a slash at its end
	if err := updateSyncTask(saved[0].ID, func(stored *SyncTask) { stored.LocalPath += "/" }); err != nil {
		t.Fatal(err)
	}
	toggled := saved[0]
	toggled.Enabled = false
	if saved, err = SaveSyncTasks([]SyncTask{toggled}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(task.LocalPath); !os.IsNotExist(err) {
		t.Fatalf("an unchanged download's folder was made again: %v", err)
	}

	moved := saved[0]
	moved.LocalPath = filepath.Join(data, "Gallery", "Elsewhere")
	if _, err := SaveSyncTasks([]SyncTask{moved}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(moved.LocalPath); err != nil {
		t.Fatalf("a download moved to another folder has it made: %v", err)
	}
}

// Dropbox and OneDrive do not tell "Immich" from "immich": a typed case is no way around the
// backups.
func TestBackupFoldersAreRefusedWhateverTheirCase(t *testing.T) {
	data := syncBox(t)

	for _, remote := range []string{"Immich", "IMMICH/x", "CasaOS-System", "CASAOS-RESTIC/x", "Casaos-Restic"} {
		task := goodTask(data)
		task.RemotePath = remote
		refused(t, checkSyncTask(task, nil), remote)
	}
}

// Backups this box does not know of -- an uninstalled app's, another box's on the same
// destination -- are found by their runs' folders, named by their stamp.
func TestAFolderHoldingStampsIsABackupFolder(t *testing.T) {
	data := syncBox(t)
	var asked []string
	syncStampFolders = func(destination, folder string) ([]string, error) {
		asked = append(asked, destination+":"+folder)
		switch folder {
		case "nextcloud":
			return []string{"2026-01-01T03-00-00Z", "2026-01-02T03-00-00Z"}, nil
		case "Photos-archive":
			return []string{"2026-01-01T03-00-00Z"}, nil
		case "Holidays":
			return []string{"2024", "Summer"}, nil
		case "offline":
			return nil, errors.New("rclone: couldn't connect")
		}

		return nil, errors.New("error in ListJSON: directory not found")
	}

	for _, remote := range []string{"nextcloud", "nextcloud/photos", "offline"} {
		task := goodTask(data)
		task.RemotePath = remote
		refused(t, checkSyncTask(task, nil), remote)
	}

	asked = nil
	for _, remote := range []string{"Holidays/2024", "Photos", "New"} {
		task := goodTask(data)
		task.RemotePath = remote
		if err := checkSyncTask(task, nil); err != nil {
			t.Fatalf("%s: %v", remote, err)
		}
	}
	// the remote's own first folder only: its archive holds stamps by design
	if want := []string{"drive:Holidays", "drive:Photos", "drive:New"}; !slices.Equal(asked, want) {
		t.Fatalf("asked %v, want %v", asked, want)
	}
}

// A save sends the whole list back: an untouched task's destination is not listed again, so an
// offline one blocks neither switching another task off nor deleting one. A task new or moved
// in the destination is still listed, and each pass lists its own.
func TestSavingAnUntouchedTaskDoesNotListItsDestination(t *testing.T) {
	data := syncBox(t)
	listings := 0
	offline := false
	syncStampFolders = func(string, string) ([]string, error) {
		listings++
		if offline {
			return nil, errors.New("rclone: couldn't connect")
		}

		return nil, errors.New("error in ListJSON: directory not found")
	}

	other := goodTask(data)
	other.Name, other.RemotePath, other.LocalPath = "videos", "Videos", filepath.Join(data, "Gallery", "Videos")
	saved, err := SaveSyncTasks([]SyncTask{goodTask(data), other})
	if err != nil || listings != 2 {
		t.Fatalf("two new tasks are listed: %d, %v", listings, err)
	}

	offline, listings = true, 0
	toggled := saved[0]
	toggled.Enabled = false
	if saved, err = SaveSyncTasks([]SyncTask{toggled, saved[1]}); err != nil || listings != 0 {
		t.Fatalf("switching a task off: %d listings, %v", listings, err)
	}
	if saved, err = SaveSyncTasks([]SyncTask{saved[0]}); err != nil || listings != 0 {
		t.Fatalf("deleting a task: %d listings, %v", listings, err)
	}
	// a stored path written by hand is no move either
	if err := updateSyncTask(saved[0].ID, func(task *SyncTask) { task.RemotePath = "/" + task.RemotePath + "/" }); err != nil {
		t.Fatal(err)
	}
	if _, err = SaveSyncTasks([]SyncTask{saved[0]}); err != nil || listings != 0 {
		t.Fatalf("a hand-written path: %d listings, %v", listings, err)
	}

	moved := saved[0]
	moved.RemotePath = "Pictures"
	refused(t, func() error { _, err := SaveSyncTasks([]SyncTask{moved}); return err }(), "a moved remote path is listed")
	elsewhere := saved[0]
	elsewhere.Destination = "vault"
	refused(t, func() error { _, err := SaveSyncTasks([]SyncTask{elsewhere}); return err }(), "another destination is listed")
	refused(t, func() error { _, err := SaveSyncTasks([]SyncTask{saved[0], other}); return err }(), "a new task is listed")

	// and the pass lists it, whatever the save did
	refused(t, checkSyncTask(saved[0], nil), "the pass lists")
}

// A destination rooted deep in an account keeps its backups there: a task on another
// destination of the same account must not reach them, at any depth, whatever the case.
func TestAFolderOfAnotherDestinationsBackupsIsRefused(t *testing.T) {
	data := syncBox(t)
	type place struct{ backend, account, root string }
	places := map[string]place{
		"drive":  {"drive", "", ""},             // the task's, without a root
		"box1":   {"drive", "", "Backups/box1"}, // another's backups, deep
		"vault":  {"drive", "", "Vault-archive"},
		"s3a":    {"s3", "s3|AWS||KEY-A", ""},
		"s3b":    {"s3", "s3|AWS||KEY-B", "Backups/box1"},
		"s3a2":   {"s3", "s3|AWS||KEY-A", "Backups/box1"},
		"sftp":   {"sftp", "sftp|nas||22", "Backups/box1"},
		"broken": {},
	}
	names := []string{"drive", "box1", "vault"}
	syncDestinations = func() ([]string, error) { return names, nil }
	syncDestinationKind = func(name string) (string, bool, error) { return places[name].backend, false, nil }
	syncDestinationPlace = func(name string) (string, string, string, error) {
		if name == "broken" {
			return "", "", "", errors.New("rclone: couldn't find section")
		}
		p := places[name]

		return p.backend, p.account, p.root, nil
	}
	check := func(destination, remote string) error {
		task := goodTask(data)
		task.Destination, task.RemotePath = destination, remote
		return checkSyncTask(task, nil)
	}

	for _, remote := range []string{"Backups", "Backups/box1", "Backups/box1/photos", "backups/BOX1", "Vault"} {
		refused(t, check("drive", remote), remote)
	}
	if err := check("drive", "Backups"); err == nil || !strings.Contains(err.Error(), `holds the backups of the destination "box1"`) {
		t.Fatalf("the refusal names the destination: %v", err)
	}
	if err := check("drive", "Backups/box1/photos"); err == nil || !strings.Contains(err.Error(), "lies inside") {
		t.Fatalf("inside: %v", err)
	}
	for _, remote := range []string{"Backup", "Photos", "Backups/box", "Backups/box10"} {
		if err := check("drive", remote); err != nil {
			t.Fatalf("%s: %v", remote, err)
		}
	}

	// accounts: told apart only when the config can tell
	names = []string{"s3a", "s3b"}
	if err := check("s3a", "Backups"); err != nil {
		t.Fatalf("another s3 account: %v", err)
	}
	names = []string{"s3a", "s3a2"}
	refused(t, check("s3a", "Backups"), "the same s3 account")
	names = []string{"drive", "sftp"}
	if err := check("drive", "Backups"); err != nil {
		t.Fatalf("another backend: %v", err)
	}

	names = []string{"drive", "box1", "broken"}
	refused(t, check("drive", "Photos"), "a place that cannot be read")
	_, err := SaveSyncTasks([]SyncTask{func() SyncTask { x := goodTask(data); x.RemotePath = "Photos"; return x }()})
	refused(t, err, "a save with a place that cannot be read")
}
