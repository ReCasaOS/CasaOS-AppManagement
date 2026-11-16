package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/rclone"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/restic"
)

// Scheduled folder sync: the tasks file, and everything a task is refused for.

type SyncDirection string

const (
	SyncDownload SyncDirection = "download"
	SyncUpload   SyncDirection = "upload"
	SyncBoth     SyncDirection = "both"
)

// ScheduleHourly runs at the minute of At, every hour; the hour is ignored.
const ScheduleHourly ScheduleEvery = "hourly"

// SyncTask is one folder kept in step with one folder of a destination.
type SyncTask struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Destination string        `json:"destination"`
	RemotePath  string        `json:"remote_path"`
	LocalPath   string        `json:"local_path"`
	Direction   SyncDirection `json:"direction"`
	Mirror      bool          `json:"mirror"`
	Archive     bool          `json:"archive"`
	ArchiveDays int           `json:"archive_days"`
	Every       ScheduleEvery `json:"every"`
	At          string        `json:"at"`
	Weekday     time.Weekday  `json:"weekday"`
	Enabled     bool          `json:"enabled"`
	// written by the box only
	LastRun            time.Time `json:"last_run"`
	LastResult         string    `json:"last_result"` // "", "ok", "recovered", "failed", "stopped"
	LastError          string    `json:"last_error"`
	LastFiles          int64     `json:"last_files"`
	LastBytes          int64     `json:"last_bytes"`
	FirstPassDone      bool      `json:"first_pass_done"`
	NeedsFirstPass     bool      `json:"needs_first_pass"`
	NeedsDeleteConfirm bool      `json:"needs_delete_confirm"`
}

// SyncRefusal is an error that comes from what was asked, not from the box: reported as a
// bad request, as BadSignIn is.
type SyncRefusal string

func (e SyncRefusal) Error() string { return string(e) }

var (
	SyncTasksPath  = "/var/lib/casaos/backups/sync.json"
	BisyncStateDir = "/var/lib/casaos/backups/bisync"

	// where a synced folder may live, and the one place inside them that belongs to apps
	syncLocalRoots = []string{"/DATA", "/media", "/mnt"}
	syncAppData    = "/DATA/AppData"

	syncTasksLock sync.Mutex
)

// replaceable in tests
var (
	syncDestinationKind = func(name string) (string, bool, error) { return rclone.NewClient().DestinationKind(name) }
	syncDestinations    = func() ([]string, error) { return rclone.NewClient().Destinations() }
	// installed apps and the apps backups are scheduled for; an error means "no names"
	syncAppNames = func() []string {
		names := []string{}
		if MyService != nil {
			if apps, err := MyService.Compose().List(context.Background()); err == nil {
				for name := range apps {
					names = append(names, name)
				}
			}
		}
		if schedules, err := BackupSchedules(); err == nil {
			for _, schedule := range schedules {
				names = append(names, schedule.App)
			}
		}

		return names
	}
	// where a destination writes: backend, account ("" when it cannot be told), root in it
	syncDestinationPlace = func(name string) (backend, account, root string, err error) {
		return rclone.NewClient().DestinationPlace(name)
	}
	// the folders directly under one folder of a destination
	syncStampFolders = func(destination, folder string) ([]string, error) {
		return rclone.NewClient().Runs(destination, folder)
	}
	// a mount point lives on another device than its parent
	isMountRoot = func(folder string) bool {
		if filepath.Dir(folder) == folder {
			return true
		}
		own, ok := deviceOf(folder)
		parent, okParent := deviceOf(filepath.Dir(folder))

		return ok && okParent && own != parent
	}
)

// isStamp says whether a folder is named like a backup's run or a pass's archive.
func isStamp(name string) bool {
	_, err := time.Parse(syncStampLayout, name)

	return err == nil
}

// validSyncID is the shape of the ids the box issues; anything else could name a folder it should not.
func validSyncID(id string) bool {
	if len(id) != 17 || id[0] != 's' {
		return false
	}
	_, err := hex.DecodeString(id[1:])

	return err == nil && strings.ToLower(id) == id
}

// workdir is "" for an id the box did not issue: callers skip it.
func (t SyncTask) workdir() string {
	if !validSyncID(t.ID) {
		return ""
	}

	return filepath.Join(BisyncStateDir, t.ID)
}

// localArchive is where a pass puts what it overwrites or deletes on the box: beside the synced
// folder, never in it (rclone refuses a backup dir that overlaps the destination, and anything
// inside would be synced).
func (t SyncTask) localArchive(stamp string) string {
	return filepath.Join(syncArchiveFolder(t.LocalPath), stamp)
}

func syncArchiveFolder(local string) string {
	return filepath.Join(filepath.Dir(local), "."+filepath.Base(local)+"-archive")
}

// remoteArchive is the same in the destination, without the remote's name. A task with no
// remote_path is a download, and archives on the box only.
func (t SyncTask) remoteArchive(stamp string) string {
	if t.RemotePath == "" {
		return ""
	}

	return path.Join(t.remoteArchiveFolder(), stamp)
}

// remoteArchiveFolder holds every pass's archive in the destination; "" without a remote_path.
func (t SyncTask) remoteArchiveFolder() string {
	if t.RemotePath == "" {
		return ""
	}

	return t.RemotePath + "-archive"
}

// SyncTasks reads the file. One that will not parse is an error, never an empty list.
func SyncTasks() ([]SyncTask, error) {
	syncTasksLock.Lock()
	defer syncTasksLock.Unlock()

	return readSyncTasks()
}

func readSyncTasks() ([]SyncTask, error) {
	content, err := os.ReadFile(SyncTasksPath)
	if os.IsNotExist(err) || (err == nil && len(content) == 0) {
		return []SyncTask{}, nil
	}
	if err != nil {
		return nil, err
	}

	tasks := []SyncTask{}
	if err := stdjson.Unmarshal(content, &tasks); err != nil {
		return nil, fmt.Errorf("the sync tasks cannot be read: %w", err)
	}

	return tasks, nil
}

func writeSyncTasks(tasks []SyncTask) error {
	encoded, err := stdjson.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(SyncTasksPath), 0o700); err != nil {
		return err
	}

	temp := SyncTasksPath + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return err
	}

	return os.Rename(temp, SyncTasksPath)
}

// updateSyncTask changes one task as it is now in the file; a task that is gone stays gone.
func updateSyncTask(id string, change func(*SyncTask)) error {
	syncTasksLock.Lock()
	defer syncTasksLock.Unlock()

	tasks, err := readSyncTasks()
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].ID == id {
			change(&tasks[i])

			return writeSyncTasks(tasks)
		}
	}

	return nil
}

// SaveSyncTasks replaces the list with what the owner sent, after refusing what must be refused.
// What only the box writes is kept from the stored task.
func SaveSyncTasks(wanted []SyncTask) ([]SyncTask, error) {
	syncTasksLock.Lock()
	defer syncTasksLock.Unlock()

	stored, err := readSyncTasks()
	if err != nil {
		return nil, err
	}

	tasks := make([]SyncTask, len(wanted))
	var restart []string              // work folders to clear once the file is written
	var create []string               // download folders new in this save
	list := make([]bool, len(wanted)) // whose destination is listed for backups: new or moved there in this save
	for i, task := range wanted {
		before, known := SyncTask{}, false
		for _, s := range stored {
			if validSyncID(task.ID) && s.ID == task.ID {
				before, known = s, true
			}
		}
		if known {
			if slices.ContainsFunc(tasks[:i], func(t SyncTask) bool { return t.ID == task.ID }) {
				return nil, SyncRefusal("two tasks carry the same id")
			}
		}

		if known {
			// compared as they are saved below: a hand-edited "/Photos/" is no move
			if remote, err := cleanRemotePath(before.RemotePath); err == nil {
				before.RemotePath = remote
			}
			before.LocalPath = filepath.Clean(before.LocalPath)
			task.LastRun, task.LastResult, task.LastError = before.LastRun, before.LastResult, before.LastError
			task.LastFiles, task.LastBytes = before.LastFiles, before.LastBytes
			task.FirstPassDone, task.NeedsFirstPass, task.NeedsDeleteConfirm = before.FirstPassDone, before.NeedsFirstPass, before.NeedsDeleteConfirm
		} else {
			// an id the box never gave is not kept: it would name a folder of the box
			bytes := make([]byte, 8)
			if _, err := rand.Read(bytes); err != nil {
				return nil, err
			}
			task.ID = "s" + hex.EncodeToString(bytes)
			task.LastRun, task.LastResult, task.LastError, task.LastFiles, task.LastBytes = time.Time{}, "", "", 0, 0
			task.FirstPassDone, task.NeedsFirstPass, task.NeedsDeleteConfirm = false, false, false
		}

		if task.Direction == SyncBoth {
			task.Mirror = true
		}
		if remote, err := cleanRemotePath(task.RemotePath); err == nil {
			task.RemotePath = remote
		}
		task.LocalPath = filepath.Clean(task.LocalPath)
		if task.Every == ScheduleHourly {
			if _, minute, err := parseAt(task.At); err == nil {
				task.At = fmt.Sprintf("00:%02d", minute)
			}
		}

		// a task moved to other folders or turned around is another task: what it waited for
		// was about the old ones
		if known && (task.Destination != before.Destination || task.RemotePath != before.RemotePath ||
			task.LocalPath != before.LocalPath || task.Direction != before.Direction) {
			task.FirstPassDone, task.NeedsFirstPass, task.NeedsDeleteConfirm = false, false, false
			restart = append(restart, task.workdir())
		}
		// only a folder asked for now: an unplugged disk's folder is not made again on the system disk
		if task.Direction == SyncDownload && (!known || task.LocalPath != before.LocalPath || task.Direction != before.Direction) {
			create = append(create, task.LocalPath)
		}
		// an untouched task's destination is not listed again: one offline would block every save,
		// under the lock the passes write their results with; each pass lists it anyway
		list[i] = !known || task.Destination != before.Destination || task.RemotePath != before.RemotePath

		tasks[i] = task
	}

	for i, task := range tasks {
		if err := checkSyncTaskListing(task, slices.Concat(tasks[:i], tasks[i+1:]), list[i]); err != nil {
			return nil, err
		}
	}

	for _, folder := range create {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			return nil, err
		}
	}

	if err := writeSyncTasks(tasks); err != nil {
		return nil, err
	}

	for _, s := range stored {
		if !slices.ContainsFunc(tasks, func(t SyncTask) bool { return t.ID == s.ID }) {
			restart = append(restart, s.workdir())
		}
	}
	for _, dir := range restart {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}

	return tasks, nil
}

// cleanRemotePath is the folder in the destination as it is stored: no slash at the ends,
// and no way out of the destination.
func cleanRemotePath(raw string) (string, error) {
	raw = strings.ReplaceAll(raw, "\\", "/")
	if slices.Contains(strings.Split(raw, "/"), "..") {
		return "", SyncRefusal("the folder in the destination cannot go up with \"..\"")
	}

	return strings.Trim(path.Clean("/"+raw), "/"), nil
}

// checkSyncTask is every reason to refuse a task, against the other tasks. Each pass asks again:
// the file can have been edited, an app installed, a disk mounted since it was saved.
func checkSyncTask(task SyncTask, others []SyncTask) error {
	return checkSyncTaskListing(task, others, true)
}

// checkSyncTaskListing is checkSyncTask, the listing of the destination for backups only when list.
func checkSyncTaskListing(task SyncTask, others []SyncTask, list bool) error {
	if strings.TrimSpace(task.Name) == "" {
		return SyncRefusal("a sync task needs a name")
	}
	for _, other := range others {
		if other.Name == task.Name {
			return SyncRefusal(fmt.Sprintf("another sync task is already called %q", task.Name))
		}
	}

	switch task.Direction {
	case SyncDownload, SyncUpload, SyncBoth:
	default:
		return SyncRefusal(fmt.Sprintf("%q is not a direction of sync", task.Direction))
	}

	switch task.Every {
	case ScheduleDaily, ScheduleHourly:
	case ScheduleWeekly:
		if task.Weekday < time.Sunday || task.Weekday > time.Saturday {
			return SyncRefusal("that is not a day of the week")
		}
	default:
		return SyncRefusal(fmt.Sprintf("%q is not a frequency", task.Every))
	}
	if _, _, err := parseAt(task.At); err != nil {
		return SyncRefusal(err.Error())
	}
	if task.ArchiveDays < 0 {
		return SyncRefusal("the days an archive is kept cannot be negative")
	}

	names, err := syncDestinations()
	if err != nil {
		return err
	}
	if !slices.Contains(names, task.Destination) {
		return SyncRefusal(fmt.Sprintf("there is no destination called %q", task.Destination))
	}
	_, encrypted, err := syncDestinationKind(task.Destination)
	if err != nil {
		return err
	}
	if encrypted && task.Direction != SyncUpload {
		return SyncRefusal("an encrypted destination only takes uploads: its files cannot be read back as folders")
	}

	remote, err := cleanRemotePath(task.RemotePath)
	if err != nil {
		return err
	}
	if remote == "" && task.Direction != SyncDownload {
		return SyncRefusal("an upload needs a folder in the destination: the root is not the place for it")
	}
	if remote != "" {
		// the folder, and the archive that sits beside it
		for _, folder := range []string{remote, remote + "-archive"} {
			first, _, _ := strings.Cut(folder, "/")
			// Dropbox and OneDrive do not tell "Immich" from "immich"
			if strings.EqualFold(first, restic.RepositoryFolder) || strings.EqualFold(first, SystemBackupName) ||
				slices.ContainsFunc(syncAppNames(), func(app string) bool { return strings.EqualFold(app, first) }) {
				return SyncRefusal(fmt.Sprintf("%q belongs to the backups: pick another folder", first))
			}
		}
	}
	if remote != "" && list {
		// an app's backups this box does not know of: another box's, an uninstalled app's
		first, _, _ := strings.Cut(remote, "/")
		folders, err := syncStampFolders(task.Destination, first)
		if err != nil && !isNotFound(err) {
			return SyncRefusal(fmt.Sprintf("the destination %q cannot be read, so whether %q holds backups cannot be told: %v", task.Destination, first, err))
		}
		if slices.ContainsFunc(folders, isStamp) {
			return SyncRefusal(fmt.Sprintf("%q holds backups: pick another folder", first))
		}
	}

	if err := checkOtherDestinationsBackups(task.Destination, remote, names); err != nil {
		return err
	}

	local, err := checkSyncLocal(task.LocalPath)
	if err != nil {
		return err
	}

	for _, other := range others {
		if other.Destination == task.Destination {
			otherRemote, _ := cleanRemotePath(other.RemotePath)
			if within(remote, otherRemote) || within(otherRemote, remote) {
				return SyncRefusal(fmt.Sprintf("the folder in the destination overlaps the one of %q", other.Name))
			}
			if (otherRemote != "" && within(remote, otherRemote+"-archive")) || (remote != "" && within(otherRemote, remote+"-archive")) {
				return SyncRefusal(fmt.Sprintf("the folder in the destination is where the archive of %q is kept", other.Name))
			}
		}
		if otherLocal, err := resolveSyncPath(other.LocalPath); err == nil && (within(local, otherLocal) || within(otherLocal, local)) {
			return SyncRefusal(fmt.Sprintf("the local folder overlaps the one of %q", other.Name))
		} else if err == nil && (within(local, syncArchiveFolder(otherLocal)) || within(otherLocal, syncArchiveFolder(local))) {
			return SyncRefusal(fmt.Sprintf("the local folder is where the archive of %q is kept", other.Name))
		}
	}

	return nil
}

// checkOtherDestinationsBackups refuses a folder that holds, is, or lies inside the root of
// another destination of this box on the same account: that root holds its backups, at any
// depth. Places are read from the local rclone config only. A destination without a root keeps
// its backups at the top of its account, where the first-folder checks above cover them.
func checkOtherDestinationsBackups(destination, remote string, names []string) error {
	place := func(name string) (backend, account, root string, err error) {
		backend, account, root, err = syncDestinationPlace(name)
		if err != nil {
			err = SyncRefusal(fmt.Sprintf("where the destination %q writes cannot be read: %v", name, err))
		}

		return backend, account, strings.ToLower(root), err
	}
	backend, account, root, err := place(destination)
	if err != nil {
		return err
	}
	written := []string{path.Join(root, strings.ToLower(remote))}
	if remote != "" {
		written = append(written, written[0]+"-archive")
	}

	for _, name := range names {
		if name == destination {
			continue // its own backups: the first-folder checks above
		}
		otherBackend, otherAccount, otherRoot, err := place(name)
		if err != nil {
			return err
		}
		if otherRoot == "" || otherBackend != backend || (account != "" && otherAccount != "" && account != otherAccount) {
			continue
		}
		for _, folder := range written {
			if within(otherRoot, folder) {
				return SyncRefusal(fmt.Sprintf("this folder holds the backups of the destination %q", name))
			}
			if within(folder, otherRoot) {
				return SyncRefusal(fmt.Sprintf("this folder lies inside the backups of the destination %q", name))
			}
		}
	}

	return nil
}

// syncDue says whether a task has a slot it has not run yet. Whether it may run (first pass,
// deletions to confirm) is the scheduler's question, not this one's.
func syncDue(task SyncTask, now time.Time) (bool, error) {
	if !task.Enabled {
		return false, nil
	}

	slot, err := mostRecentSlot(BackupSchedule{Every: task.Every, At: task.At, Weekday: task.Weekday}, now)
	if err != nil {
		return false, err
	}

	return task.LastRun.Before(slot), nil
}

// within says whether child is folder or lies under it ("" is the root of everything).
func within(child, folder string) bool {
	return folder == "" || child == folder || strings.HasPrefix(child, strings.TrimSuffix(folder, "/")+"/")
}

// checkSyncLocal says where a local folder really is, or why it may not be synced.
func checkSyncLocal(raw string) (string, error) {
	if !filepath.IsAbs(raw) {
		return "", SyncRefusal("the local folder must be a full path")
	}
	real, err := resolveSyncPath(raw)
	if err != nil {
		return "", err
	}

	inside := false
	for _, root := range syncLocalRoots {
		if rootReal, err := resolveSyncPath(root); err == nil && real != rootReal && within(real, rootReal) {
			inside = true
		}
	}
	if !inside {
		return "", SyncRefusal("the local folder must be a folder of yours under " + strings.Join(syncLocalRoots, ", "))
	}
	if appData, err := resolveSyncPath(syncAppData); err == nil && within(real, appData) {
		return "", SyncRefusal("the data of the apps is backed up, not synced")
	}
	if isMountRoot(real) {
		return "", SyncRefusal("that folder is a whole disk: pick a folder inside it")
	}

	return real, nil
}

// resolveSyncPath follows the links of the longest part that exists, so a link cannot lead out
// of the folders a sync is allowed in.
func resolveSyncPath(raw string) (string, error) {
	rest := ""
	for existing := filepath.Clean(raw); ; existing = filepath.Dir(existing) {
		real, err := filepath.EvalSymlinks(existing)
		if err == nil {
			return filepath.Join(real, rest), nil
		}
		if !os.IsNotExist(err) || filepath.Dir(existing) == existing {
			return "", err
		}
		rest = filepath.Join(filepath.Base(existing), rest)
	}
}
