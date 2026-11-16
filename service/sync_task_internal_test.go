package service

import (
	"errors"
	"os"
	"path/filepath"
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
	oldKind, oldDests, oldNames, oldMount := syncDestinationKind, syncDestinations, syncAppNames, isMountRoot
	t.Cleanup(func() {
		SyncTasksPath, BisyncStateDir, syncLocalRoots, syncAppData = oldPath, oldState, oldRoots, oldApp
		syncDestinationKind, syncDestinations, syncAppNames, isMountRoot = oldKind, oldDests, oldNames, oldMount
	})

	SyncTasksPath = filepath.Join(root, "state", "sync.json")
	BisyncStateDir = filepath.Join(root, "state", "bisync")
	syncLocalRoots = []string{data, filepath.Join(root, "media"), filepath.Join(root, "mnt")}
	syncAppData = filepath.Join(data, "AppData")
	syncDestinations = func() ([]string, error) { return []string{"drive", "vault"}, nil }
	syncDestinationKind = func(name string) (string, bool, error) { return "drive", name == "vault", nil }
	syncAppNames = func() []string { return []string{"immich"} }
	isMountRoot = func(string) bool { return false }

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
