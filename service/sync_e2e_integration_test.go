package service

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/rclone"
)

// Opt-in: runs the service layer of the scheduled folder sync against a real rclone daemon, on
// the fixed socket the service uses. Meant for a throwaway container, where this is root.
func TestSyncEndToEndWithRealRclone(t *testing.T) {
	if os.Getenv("CASAOS_RCLONE_INTEGRATION") != "1" {
		t.Skip("set CASAOS_RCLONE_INTEGRATION=1 to test a local rclone v1.75.1 daemon")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the production daemon uses a Unix socket")
	}
	binary := os.Getenv("RCLONE_BINARY")
	if binary == "" {
		binary = "rclone"
	}

	root := t.TempDir()
	cloudRoot := filepath.Join(root, "cloud")
	dataRoot := filepath.Join(root, "DATA")
	for _, dir := range []string{cloudRoot, dataRoot, filepath.Join(dataRoot, "AppData")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(rclone.SocketPath), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(rclone.SocketPath)
	config := filepath.Join(root, "rclone.conf")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := exec.Command(binary, "rcd", "--rc-addr", "unix://"+rclone.SocketPath, "--rc-no-auth", "--config", config, "--log-level", "ERROR")
	daemon.Stdout, daemon.Stderr = os.Stdout, os.Stderr
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill(); _ = daemon.Wait(); _ = os.Remove(rclone.SocketPath) })
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if conn, err := net.Dial("unix", rclone.SocketPath); err == nil {
			_ = conn.Close()
			if _, err := rclone.NewClient().Destinations(); err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the rclone daemon never answered on its socket")
		}
	}

	if err := rclone.NewClient().CreateDestination("e2e", "local", cloudRoot, map[string]string{}); err != nil {
		t.Fatal(err)
	}

	wasTasks, wasState, wasRoots, wasAppData, wasMount, wasNames, wasBusy := SyncTasksPath, BisyncStateDir, syncLocalRoots, syncAppData, isMountRoot, syncAppNames, osUpgradeBusyPath
	SyncTasksPath, BisyncStateDir = filepath.Join(root, "state", "sync.json"), filepath.Join(root, "state", "bisync")
	syncLocalRoots, syncAppData = []string{dataRoot}, filepath.Join(dataRoot, "AppData")
	isMountRoot = func(string) bool { return false }
	syncAppNames = func() []string { return nil }
	osUpgradeBusyPath = filepath.Join(root, "no-upgrade")
	t.Cleanup(func() {
		syncPasses.Wait()
		SyncTasksPath, BisyncStateDir, syncLocalRoots, syncAppData, isMountRoot, syncAppNames, osUpgradeBusyPath = wasTasks, wasState, wasRoots, wasAppData, wasMount, wasNames, wasBusy
	})

	// one sub-test's world: a box folder and a cloud folder of its own, and the task between them
	type world struct {
		task       SyncTask
		box, cloud string
	}
	newWorld := func(t *testing.T, name string, direction SyncDirection, mirror bool) *world {
		t.Helper()
		w := &world{box: filepath.Join(dataRoot, name, "box"), cloud: filepath.Join(cloudRoot, name)}
		for _, dir := range []string{w.box, w.cloud} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		w.task = SyncTask{
			Name: name, Destination: "e2e", RemotePath: name, LocalPath: w.box, Direction: direction,
			Mirror: mirror, Archive: true, Every: ScheduleDaily, At: "03:00", Enabled: true,
		}
		return w
	}
	save := func(t *testing.T, w *world) {
		t.Helper()
		saved, err := SaveSyncTasks([]SyncTask{w.task})
		if err != nil {
			t.Fatalf("saving the task was refused: %v", err)
		}
		w.task = saved[0]
	}
	current := func(t *testing.T, w *world) SyncTask {
		t.Helper()
		tasks, err := SyncTasks()
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range tasks {
			if task.ID == w.task.ID {
				return task
			}
		}
		t.Fatalf("the task %s is not in the tasks file", w.task.ID)
		return SyncTask{}
	}
	// pass starts a pass the way a route does and waits for it to write its result; it sleeps first
	// so that two passes never share a second (the archive stamp and the clock of the task)
	pass := func(t *testing.T, w *world, start func(string) error) (SyncTask, error) {
		t.Helper()
		time.Sleep(1100 * time.Millisecond)
		before := current(t, w).LastRun
		if err := start(w.task.ID); err != nil {
			return current(t, w), err
		}
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if task := current(t, w); !task.LastRun.Equal(before) && task.LastResult != "" {
				syncPasses.Wait()
				return current(t, w), nil
			}
		}
		t.Fatalf("no result was written within 30 s, the task is %+v", current(t, w))
		return SyncTask{}, nil
	}
	write := func(t *testing.T, dir, name, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	names := func(dir string) []string {
		entries, _ := os.ReadDir(dir)
		out := []string{}
		for _, entry := range entries {
			out = append(out, entry.Name())
		}
		slices.Sort(out)
		return out
	}
	content := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			return "<" + err.Error() + ">"
		}
		return string(b)
	}
	// archived finds a file under any stamp folder of an archive folder
	archived := func(archive, name string) (string, bool) {
		stamps, _ := filepath.Glob(filepath.Join(archive, "*", name))
		if len(stamps) == 0 {
			return "", false
		}
		return stamps[0], true
	}
	expectResult := func(t *testing.T, task SyncTask, result string) {
		t.Helper()
		if task.LastResult != result {
			t.Errorf("expected the result %q, found %q with the error %q", result, task.LastResult, task.LastError)
		}
	}
	expectFiles := func(t *testing.T, where, dir string, want ...string) {
		t.Helper()
		slices.Sort(want)
		if got := names(dir); !slices.Equal(got, want) {
			t.Errorf("expected %s to hold %v, found %v", where, want, got)
		}
	}

	t.Run("download brings the cloud's files and keeps what it overwrites", func(t *testing.T) {
		w := newWorld(t, "dl", SyncDownload, false)
		write(t, w.cloud, "a.txt", "the cloud's a, newer and longer")
		write(t, w.cloud, "b.txt", "b")
		write(t, w.box, "a.txt", "old a")
		save(t, w)

		task, err := pass(t, w, RunSyncNow)
		if err != nil {
			t.Fatalf("a download was refused: %v", err)
		}
		expectResult(t, task, "ok")
		expectFiles(t, "the box folder", w.box, "a.txt", "b.txt")
		if got := content(filepath.Join(w.box, "a.txt")); got != "the cloud's a, newer and longer" {
			t.Errorf("expected the box's a.txt to hold the cloud's content, found %q", got)
		}
		old, ok := archived(syncArchiveFolder(w.box), "a.txt")
		if !ok {
			t.Fatalf("expected the old a.txt under %s/<stamp>/, found %v", syncArchiveFolder(w.box), names(syncArchiveFolder(w.box)))
		}
		if got := content(old); got != "old a" {
			t.Errorf("expected the archived a.txt to hold %q, found %q", "old a", got)
		}
		t.Logf("download: last_files=%d last_bytes=%d", task.LastFiles, task.LastBytes)
	})

	t.Run("upload with mirror archives what it deletes in the cloud", func(t *testing.T) {
		w := newWorld(t, "up", SyncUpload, true)
		write(t, w.box, "keep.txt", "keep")
		write(t, w.box, "drop.txt", "drop me")
		write(t, w.box, "sub/deep.txt", "deep")
		save(t, w)

		task, err := pass(t, w, RunSyncNow)
		if err != nil {
			t.Fatalf("an upload was refused: %v", err)
		}
		expectResult(t, task, "ok")
		expectFiles(t, "the cloud folder", w.cloud, "drop.txt", "keep.txt", "sub")
		t.Logf("upload: last_files=%d last_bytes=%d", task.LastFiles, task.LastBytes)

		if err := os.Remove(filepath.Join(w.box, "drop.txt")); err != nil {
			t.Fatal(err)
		}
		task, err = pass(t, w, RunSyncNow)
		if err != nil {
			t.Fatalf("the second upload was refused: %v", err)
		}
		expectResult(t, task, "ok")
		expectFiles(t, "the cloud folder", w.cloud, "keep.txt", "sub")
		kept, ok := archived(w.cloud+"-archive", "drop.txt")
		if !ok {
			t.Fatalf("expected drop.txt under %s/<stamp>/, found %v", w.cloud+"-archive", names(w.cloud+"-archive"))
		}
		if got := content(kept); got != "drop me" {
			t.Errorf("expected the archived drop.txt to hold %q, found %q", "drop me", got)
		}
		t.Logf("upload with a deletion: last_files=%d last_bytes=%d", task.LastFiles, task.LastBytes)
	})

	t.Run("an empty source is refused and nothing moves", func(t *testing.T) {
		w := newWorld(t, "empty", SyncDownload, true)
		write(t, w.box, "one.txt", "1")
		write(t, w.box, "two.txt", "2")
		save(t, w) // the cloud folder is there and holds nothing

		task, err := pass(t, w, RunSyncNow)
		if err != nil {
			t.Fatalf("the pass was refused before it ran: %v", err)
		}
		expectResult(t, task, "failed")
		if !strings.Contains(task.LastError, "source is empty") {
			t.Errorf("expected the error to say the source is empty, found %q", task.LastError)
		}
		expectFiles(t, "the box folder", w.box, "one.txt", "two.txt")
		if got := content(filepath.Join(w.box, "one.txt")); got != "1" {
			t.Errorf("expected one.txt untouched, found %q", got)
		}
	})

	t.Run("the mirror cap parks the task, confirming lifts it", func(t *testing.T) {
		w := newWorld(t, "cap", SyncUpload, true)
		for i := 0; i < 30; i++ {
			write(t, w.cloud, fmt.Sprintf("f%02d.txt", i), fmt.Sprintf("file %d", i))
		}
		write(t, w.box, "f00.txt", "file 0")
		write(t, w.box, "f01.txt", "file 1")
		save(t, w)

		task, err := pass(t, w, RunSyncNow)
		if err != nil {
			t.Fatalf("the pass was refused before it ran: %v", err)
		}
		expectResult(t, task, "failed")
		if !task.NeedsDeleteConfirm {
			t.Errorf("expected needs_delete_confirm true, found false with the error %q", task.LastError)
		}
		if left := len(names(w.cloud)); left < 15 {
			t.Errorf("expected at least 15 files left in the cloud, found %d", left)
		}
		if err := RunSyncNow(w.task.ID); !errors.Is(err, ErrSyncWrongState) {
			t.Errorf("expected a plain run to be refused with ErrSyncWrongState, found %v", err)
		}

		task, err = pass(t, w, ConfirmSyncDeletes)
		if err != nil {
			t.Fatalf("confirming was refused: %v", err)
		}
		expectResult(t, task, "ok")
		if task.NeedsDeleteConfirm {
			t.Errorf("expected needs_delete_confirm false after confirming, found true")
		}
		expectFiles(t, "the cloud folder", w.cloud, "f00.txt", "f01.txt")
		for i := 2; i < 30; i++ {
			if _, ok := archived(w.cloud+"-archive", fmt.Sprintf("f%02d.txt", i)); !ok {
				t.Errorf("expected f%02d.txt in the cloud archive, found %v", i, names(w.cloud+"-archive"))
				break
			}
		}
	})

	t.Run("both ways: first pass unites, then changes cross, a conflict keeps both", func(t *testing.T) {
		w := newWorld(t, "both", SyncBoth, true)
		write(t, w.box, "x.txt", "from the box")
		write(t, w.cloud, "y.txt", "from the cloud")
		save(t, w)
		if w.task.FirstPassDone {
			t.Fatal("a new task starts with first_pass_done true")
		}

		// RunSyncNow is accepted by StartSync on a task whose first pass is not done, and runs the resync itself
		task, err := pass(t, w, RunSyncNow)
		if err != nil {
			t.Fatalf("RunSyncNow was refused on a new both-ways task: %v", err)
		}
		expectResult(t, task, "ok")
		if !task.FirstPassDone {
			t.Errorf("expected first_pass_done true, found false")
		}
		expectFiles(t, "the box folder", w.box, "x.txt", "y.txt")
		expectFiles(t, "the cloud folder", w.cloud, "x.txt", "y.txt")
		t.Logf("both, first pass: last_files=%d last_bytes=%d", task.LastFiles, task.LastBytes)

		write(t, w.box, "new.txt", "added on the box")
		write(t, w.box, "photo.jpg", "photo as it was")
		if err := os.Remove(filepath.Join(w.cloud, "y.txt")); err != nil {
			t.Fatal(err)
		}
		task, err = pass(t, w, RunSyncNow)
		if err != nil {
			t.Fatalf("the second pass was refused: %v", err)
		}
		expectResult(t, task, "ok")
		expectFiles(t, "the cloud folder", w.cloud, "new.txt", "photo.jpg", "x.txt")
		expectFiles(t, "the box folder", w.box, "new.txt", "photo.jpg", "x.txt")
		if kept, ok := archived(syncArchiveFolder(w.box), "y.txt"); !ok {
			t.Errorf("expected y.txt in the box archive, found %v", names(syncArchiveFolder(w.box)))
		} else if got := content(kept); got != "from the cloud" {
			t.Errorf("expected the archived y.txt to hold %q, found %q", "from the cloud", got)
		}
		t.Logf("both, changes crossing: last_files=%d last_bytes=%d", task.LastFiles, task.LastBytes)

		write(t, w.box, "photo.jpg", "edited on the box, and longer")
		write(t, w.cloud, "photo.jpg", "edited in the cloud")
		task, err = pass(t, w, RunSyncNow)
		if err != nil {
			t.Fatalf("the third pass was refused: %v", err)
		}
		t.Logf("both, conflict: result=%q error=%q last_files=%d last_bytes=%d", task.LastResult, task.LastError, task.LastFiles, task.LastBytes)
		for _, side := range []struct{ where, dir string }{{"the box folder", w.box}, {"the cloud folder", w.cloud}} {
			got := names(side.dir)
			for _, want := range []string{"photo.conflict1.jpg", "photo.conflict2.jpg"} {
				if !slices.Contains(got, want) {
					t.Errorf("expected %s to hold %s, found %v", side.where, want, got)
				}
			}
		}
	})

	t.Run("both ways: more than half gone parks the task", func(t *testing.T) {
		w := newWorld(t, "half", SyncBoth, true)
		all := []string{"1.txt", "2.txt", "3.txt", "4.txt", "5.txt", "6.txt"}
		for _, name := range all {
			write(t, w.box, name, "content of "+name)
		}
		save(t, w)
		task, err := pass(t, w, RunSyncNow)
		if err != nil {
			t.Fatalf("the first pass was refused: %v", err)
		}
		expectResult(t, task, "ok")
		expectFiles(t, "the cloud folder", w.cloud, all...)

		for _, name := range all[:4] {
			if err := os.Remove(filepath.Join(w.box, name)); err != nil {
				t.Fatal(err)
			}
		}
		task, err = pass(t, w, RunSyncNow)
		if err != nil {
			t.Fatalf("the pass was refused before it ran: %v", err)
		}
		expectResult(t, task, "failed")
		if !task.NeedsDeleteConfirm {
			t.Errorf("expected needs_delete_confirm true, found false with the error %q", task.LastError)
		}
		expectFiles(t, "the cloud folder", w.cloud, all...)

		task, err = pass(t, w, ConfirmSyncDeletes)
		if err != nil {
			t.Fatalf("confirming was refused: %v", err)
		}
		expectResult(t, task, "recovered") // a both-ways pass that follows a failed one says so (writeSyncResult)
		expectFiles(t, "the cloud folder", w.cloud, "5.txt", "6.txt")
		for _, name := range all[:4] {
			if _, ok := archived(w.cloud+"-archive", name); !ok {
				t.Errorf("expected %s in the cloud archive, found %v", name, names(w.cloud+"-archive"))
			}
		}
	})

	t.Run("folders of a destination are listed", func(t *testing.T) {
		got, err := rclone.NewClient().Folders(rclone.Destination("e2e", ""), "")
		if err != nil {
			t.Fatalf("listing the folders failed: %v", err)
		}
		for _, want := range []string{"dl", "up", "cap", "both"} {
			if !slices.Contains(got, want) {
				t.Errorf("expected the folders to hold %q, found %v", want, got)
			}
		}
		if slices.Contains(got, "a.txt") {
			t.Errorf("expected folders only, found %v", got)
		}
	})
}
