package service

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

type fakeUnits struct {
	stopped, started, restarted []string
	failStop                    string
}

func (f *fakeUnits) Stop(unit string) error {
	if unit == f.failStop {
		return errors.New("systemd said no")
	}
	f.stopped = append(f.stopped, unit)

	return nil
}

func (f *fakeUnits) Start(unit string) error   { f.started = append(f.started, unit); return nil }
func (f *fakeUnits) Restart(unit string) error { f.restarted = append(f.restarted, unit); return nil }

// What the box is, outside the data folder: planned like bind mounts, with the
// absent ones skipped and said so.
func TestAbsentPartsOfTheBoxAreSkippedWithAReason(t *testing.T) {
	present := map[string]bool{"/etc/casaos": true, "/var/lib/casaos/db": true, "/root/.config/rclone/rclone.conf": true}
	fs := boxFS(map[string][]systemChild{systemDataRoot: {}}, nil)
	fs.Exists = func(p string) bool { return present[p] }
	entries, err := SystemBackupInventory(fs)
	if err != nil {
		t.Fatal(err)
	}
	manifest := PlanBackup(SystemBackupName, entries, nil)

	if len(manifest.Operations) != 3 {
		t.Fatalf("three present: %+v", manifest.Operations)
	}
	if len(manifest.Skipped) != 6 || !strings.Contains(manifest.Skipped[0].Skip, "not present") {
		t.Fatalf("six absent, with the reason: %+v", manifest.Skipped)
	}
	if last := manifest.Skipped[len(manifest.Skipped)-1]; last.Path != VerificationSchedulePath {
		t.Fatalf("the verification schedule is part of the box: %+v", manifest.Skipped)
	}
	for _, op := range manifest.Operations {
		if op.Kind != BackupKindBind || !strings.HasPrefix(op.Destination, "binds/") {
			t.Fatalf("planned like a bind mount: %+v", op)
		}
	}
}

// The dashboard's API widgets, with the keys they call their APIs with, are part
// of the box, and on the fixed list: the core keeps the file where it is wherever
// the data folder goes. So it is one entry though it is a child of the data
// folder too, never left out for its size, and a restore may put it back. A box
// that has none has no such file, and that is a skip. The verification schedule
// stays last, which the tests around this one count on.
func TestSystemBackupHoldsTheAPIWidgets(t *testing.T) {
	atTheDefaultDataRoot(t)

	const widgets = "/var/lib/casaos/api_widgets.json"

	paths := systemBackupPaths()
	if !slices.Contains(paths, widgets) {
		t.Fatalf("the API widgets are part of the box: %v", paths)
	}
	if paths[len(paths)-1] != VerificationSchedulePath {
		t.Fatalf("the verification schedule stays last: %v", paths)
	}
	if !systemRestorable(widgets) {
		t.Fatal("a restore may put the API widgets back")
	}

	fs := boxFS(map[string][]systemChild{systemDataRoot: {{Name: "api_widgets.json"}}}, map[string]int64{widgets: systemEntryCap + 1})
	entries, err := SystemBackupInventory(fs)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range entries {
		if e.Path == widgets {
			found++
			if e.Skip != "" {
				t.Fatalf("on the fixed list, so never left out for its size: %+v", e)
			}
		}
	}
	if found != 1 {
		t.Fatalf("one entry, not one for the list and one for the data folder: %d in %+v", found, entries)
	}

	fs = boxFS(map[string][]systemChild{systemDataRoot: {}}, nil)
	fs.Exists = func(p string) bool { return p != widgets }
	if entries, err = SystemBackupInventory(fs); err != nil {
		t.Fatal(err)
	}
	manifest := PlanBackup(SystemBackupName, entries, nil)
	if len(manifest.Skipped) != 1 || manifest.Skipped[0].Path != widgets || !strings.Contains(manifest.Skipped[0].Skip, "not present") {
		t.Fatalf("a box with no widget skips the file and says so: %+v", manifest.Skipped)
	}
	for _, op := range manifest.Operations {
		if op.Source == widgets {
			t.Fatalf("nothing is planned for a file that is not there: %+v", op)
		}
	}
}

// the real one, kept before logInTempDir puts a restore's tightening out of the
// host's way
var tightenModes = tightenRestored

// tightenRestoredUnder points the modes a restore takes off at a folder of the
// test's, where writingRestorer writes, so nothing under /root is touched.
func tightenRestoredUnder(t *testing.T, root string) {
	t.Helper()

	was := tightenRestored
	tightenRestored = func(p string) error { return tightenModes(filepath.Join(root, p)) }
	t.Cleanup(func() { tightenRestored = was })
}

// writingRestorer puts each file back the way a destination that keeps no modes
// does: readable by everyone.
type writingRestorer struct {
	fakeRestorer
	root string
}

func (w *writingRestorer) RestoreFile(ctx context.Context, destination, remotePath, hostPath string) error {
	local := filepath.Join(w.root, hostPath)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(local, []byte("{}"), 0o644); err != nil {
		return err
	}
	// whatever the umask of the test is
	if err := os.Chmod(local, 0o644); err != nil {
		return err
	}

	return w.fakeRestorer.RestoreFile(ctx, destination, remotePath, hostPath)
}

// a backup of the box's fixed list in which every part is a file
func boxBackupOfFiles(t *testing.T) map[string][]byte {
	t.Helper()

	manifest := PlanBackup(SystemBackupName, systemLegacyEntries(func(string) bool { return true }), nil)
	for i := range manifest.Operations {
		manifest.Operations[i].Directory = false
	}
	raw, err := stdjson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	return map[string][]byte{path.Join(RootFor(SystemBackupName, restoreStamp), ManifestFileName): raw}
}

// The files that hold keys come back readable by root only, whatever mode the
// destination wrote them with and wherever the data folder is (the test's own
// here, so neither file is under it); the others keep the mode they came back with.
func TestSystemBackupRestoreLeavesTheSecretFilesToRootOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes do not apply")
	}
	logInTempDir(t)
	root := t.TempDir()
	tightenRestoredUnder(t, root)

	restorer := &writingRestorer{fakeRestorer: fakeRestorer{files: boxBackupOfFiles(t)}, root: root}
	if _, err := RestoreSystemBackup(context.Background(), restorer, &fakeUnits{}, SystemRestoreOptions{Destination: "offsite", Stamp: restoreStamp}); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]os.FileMode{
		"/root/.config/rclone/rclone.conf": 0o600,
		"/var/lib/casaos/api_widgets.json": 0o600,
		"/etc/samba/smb.casa.conf":         0o644,
	} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s came back %o, want %o", name, info.Mode().Perm(), want)
		}
	}
}

// A secret file whose mode could not be set is not reported as restored: the
// restore says so.
func TestSystemBackupRestoreSaysWhenASecretFileStaysReadable(t *testing.T) {
	logInTempDir(t)
	// logInTempDir puts the real one back
	tightenRestored = func(p string) error {
		if p == "/var/lib/casaos/api_widgets.json" {
			return errors.New("read-only file system")
		}

		return nil
	}

	restorer := &fakeRestorer{files: boxBackupOfFiles(t)}
	report, err := RestoreSystemBackup(context.Background(), restorer, &fakeUnits{}, SystemRestoreOptions{Destination: "offsite", Stamp: restoreStamp})
	if err == nil || !strings.Contains(err.Error(), "/var/lib/casaos/api_widgets.json") || !strings.Contains(err.Error(), "read-only file system") {
		t.Fatalf("want the failure, with the file and the reason, got %v", err)
	}
	for _, op := range report.Restored {
		if op.Source == "/var/lib/casaos/api_widgets.json" {
			t.Fatalf("reported as restored all the same: %+v", op)
		}
	}
}

// The data folder may sit on a disk that keeps no modes: what could not be
// tightened there is written down, and the restore goes on.
func TestARestoreGoesOnWhenATargetOfTheDataFolderCouldNotBeTightened(t *testing.T) {
	logInTempDir(t)
	atTheDefaultDataRoot(t)
	// logInTempDir puts the real one back
	tightenRestored = func(string) error { return errors.New("operation not permitted") }

	raw := manifestOf("/var/lib/casaos/tls", "/var/lib/casaos/fleet")
	restorer := &fakeRestorer{files: map[string][]byte{path.Join(RootFor(SystemBackupName, restoreStamp), ManifestFileName): raw}}

	report, err := RestoreSystemBackup(context.Background(), restorer, &fakeUnits{}, SystemRestoreOptions{Destination: "offsite", Stamp: restoreStamp})
	if err != nil || len(report.Restored) != 2 {
		t.Fatalf("both back, and no failure: %v %+v", err, report)
	}
}

// The services holding the files are stopped for the copy, in order, and
// started again in reverse -- and started again when the copy fails, too.
func TestTheServicesAreStoppedForTheCopyAndStartedAfter(t *testing.T) {
	logInTempDir(t)
	units := &fakeUnits{}
	copier := &fakeCopier{}

	if _, err := RunSystemBackup(context.Background(), copier, units, SystemBackupOptions{
		Destination: "offsite", Stamp: restoreStamp, HoldStill: true,
	}); err != nil {
		t.Fatal(err)
	}

	if strings.Join(units.stopped, ",") != "casaos,casaos-user-service,casaos-local-storage" {
		t.Fatalf("stopped %v", units.stopped)
	}
	if strings.Join(units.started, ",") != "casaos-local-storage,casaos-user-service,casaos" {
		t.Fatalf("started back in reverse: %v", units.started)
	}

	// and the manifest went last, under the box's own name
	last := copier.calls[len(copier.calls)-1]
	if !strings.HasSuffix(last.destination, SystemBackupName+"/"+restoreStamp+"/manifest.json") {
		t.Fatalf("%+v", last)
	}
}

func TestAServiceThatWillNotStopMeansNoCopyAndTheOthersComeBack(t *testing.T) {
	logInTempDir(t)
	units := &fakeUnits{failStop: "casaos-user-service"}
	copier := &fakeCopier{}

	_, err := RunSystemBackup(context.Background(), copier, units, SystemBackupOptions{
		Destination: "offsite", Stamp: restoreStamp, HoldStill: true,
	})
	if err == nil || !strings.Contains(err.Error(), "did not run") {
		t.Fatalf("want the refusal, got %v", err)
	}
	if len(copier.calls) != 0 {
		t.Fatalf("nothing copied: %+v", copier.calls)
	}
	if strings.Join(units.started, ",") != "casaos" {
		t.Fatalf("what went down comes back: %v", units.started)
	}
}

// A restore puts things at this box's paths, never the manifest's, stops the
// services for it, and restarts rclone last when its configuration came back.
func TestABoxRestoreUsesThisBoxesPathsAndRestartsRcloneLast(t *testing.T) {
	logInTempDir(t)

	// a manifest as another box wrote it, with its own idea of where things were
	manifest := PlanBackup(SystemBackupName, systemLegacyEntries(func(string) bool { return true }), nil)
	for i := range manifest.Operations {
		manifest.Operations[i].Directory = !strings.HasSuffix(manifest.Operations[i].Source, ".conf")
		manifest.Operations[i].Source = "/elsewhere" + manifest.Operations[i].Source
	}
	raw, _ := stdjson.Marshal(manifest)
	root := RootFor(SystemBackupName, restoreStamp)
	restorer := &fakeRestorer{files: map[string][]byte{path.Join(root, ManifestFileName): raw}}
	units := &fakeUnits{}

	report, err := RestoreSystemBackup(context.Background(), restorer, units, SystemRestoreOptions{Destination: "offsite", Stamp: restoreStamp})
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Restored) != len(systemBackupPaths()) || report.Restored[len(report.Restored)-1].Source != VerificationSchedulePath {
		t.Fatalf("everything the backup held, the verification schedule included: %+v", report)
	}
	for _, call := range restorer.restored {
		if strings.HasPrefix(call.host, "/elsewhere") {
			t.Fatalf("the manifest's path was used: %+v", call)
		}
	}
	if strings.Join(units.stopped, ",") != "casaos,casaos-user-service,casaos-local-storage" || len(units.started) != 3 {
		t.Fatalf("held still and started again: stopped %v started %v", units.stopped, units.started)
	}
	if strings.Join(units.restarted, ",") != "rclone" {
		t.Fatalf("rclone restarted, last: %v", units.restarted)
	}
}

// A backup of an app is not a backup of the box, whatever it is asked to be.
func TestABackupOfAnAppIsNotPouredIntoTheBox(t *testing.T) {
	logInTempDir(t)

	manifest := BackupManifest{FormatVersion: BackupFormatVersion, App: "nextcloud"}
	raw, _ := stdjson.Marshal(manifest)
	root := RootFor(SystemBackupName, restoreStamp)
	restorer := &fakeRestorer{files: map[string][]byte{path.Join(root, ManifestFileName): raw}}

	_, err := RestoreSystemBackup(context.Background(), restorer, &fakeUnits{}, SystemRestoreOptions{Destination: "offsite", Stamp: restoreStamp})
	if err == nil || !strings.Contains(err.Error(), "not of this box") {
		t.Fatalf("want the refusal, got %v", err)
	}
}

// The numbers in a backup are given out over the parts that were present when
// it was taken. A box without a Samba config had its database folder as
// binds/2; paired by number with this box's second path, that folder went where
// the Samba config goes and the next one over the database. Each part goes back
// to its own path, whatever number the copy landed under.
func TestEachPartOfTheBoxGoesBackToItsOwnPathWhateverNumberItLandedUnder(t *testing.T) {
	logInTempDir(t)

	// taken on a box with no Samba config: eight parts, numbered 1..8
	present := func(p string) bool { return p != "/etc/samba/smb.casa.conf" }
	manifest := PlanBackup(SystemBackupName, systemLegacyEntries(present), nil)
	for i := range manifest.Operations {
		manifest.Operations[i].Directory = !strings.HasSuffix(manifest.Operations[i].Source, ".conf")
	}
	raw, _ := stdjson.Marshal(manifest)
	root := RootFor(SystemBackupName, restoreStamp)
	restorer := &fakeRestorer{files: map[string][]byte{path.Join(root, ManifestFileName): raw}}

	report, err := RestoreSystemBackup(context.Background(), restorer, &fakeUnits{}, SystemRestoreOptions{Destination: "offsite", Stamp: restoreStamp})
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Restored) != 8 || len(report.Missing) != 0 {
		t.Fatalf("eight parts back, nothing missing: %+v", report)
	}
	for _, call := range restorer.restored {
		// binds/2 in this backup is the database folder, and it must land there
		if strings.HasSuffix(call.remote, "binds/2") && call.host != "/var/lib/casaos/db" {
			t.Fatalf("binds/2 went to %s", call.host)
		}
		if call.host == "/etc/samba/smb.casa.conf" {
			t.Fatalf("nothing was in the backup for the Samba config, so nothing may be written there: %+v", call)
		}
		if strings.HasSuffix(call.host, "rclone.conf") && call.kind != "file" {
			t.Fatalf("a file comes back as a file: %+v", call)
		}
	}
}

// atTheDefaultDataRoot puts the data folder where the manifests under test say it is:
// a restore only writes inside the box, so where the box is matters.
func atTheDefaultDataRoot(t *testing.T) {
	t.Helper()

	was := systemDataRoot
	systemDataRoot = "/var/lib/casaos"
	t.Cleanup(func() { systemDataRoot = was })
}

func TestARestoreRefusesWhatIsNotTheBox(t *testing.T) {
	atTheDefaultDataRoot(t)

	for target, want := range map[string]bool{
		"/etc/casaos": true, "/var/lib/casaos/db": true, "/var/lib/casaos/fleet": true, "/var/lib/casaos/tls": true,
		"/var/lib/casaos/backups/schedules.json": true, "/var/lib/casaos/backups/sync.json": true,
		"/var/lib/casaos/gateway/routes.json": true, "/root/.config/rclone/rclone.conf": true,
		"/var/lib/casaos/git_apps/app.key": true,
		"/etc/samba/smb.casa.conf":         true,
		"/var/lib/casaos":                  false, "/var/lib/casaos/backups": false, "/var/lib/casaos/gateway": false,
		"/var/lib/casaos/gateway/guard-state.json": false, "/var/lib/casaos/apps": false, "/var/lib/casaos/apps/x": false,
		"/var/lib/casaos/files": false, "/var/lib/casaos/files/x": false,
		"/var/lib/casaos/git_apps": false, "/var/lib/casaos/git_apps/work": false, "/var/lib/casaos/git_apps/work/x": false,
		"/var/lib/casaos/fleet/a/b": false, "/var/lib/casaos/x/y": false,
		"/var/lib/casaos/backups/restic": false, "/var/lib/casaos/backups/restic/password": false,
		"/var/lib/casaos/../../etc/passwd": false, "/var/lib/casaos/db/": false, "var/lib/casaos/db": false,
		"/etc/passwd": false, "/etc/casaos/../passwd": false, "/var/lib/casaosx/db": false, "": false,
	} {
		if got := systemRestorable(target); got != want {
			t.Errorf("%q: got %v, want %v", target, got, want)
		}
	}
}

func manifestOf(targets ...string) []byte {
	manifest := BackupManifest{FormatVersion: BackupFormatVersion, App: SystemBackupName}
	for i, target := range targets {
		manifest.Operations = append(manifest.Operations, BackupOperation{
			Kind: BackupKindBind, Source: "/elsewhere" + target, Target: target,
			Destination: "binds/" + string(rune('1'+i)), Directory: true,
		})
	}
	raw, _ := stdjson.Marshal(manifest)

	return raw
}

// The manifest comes from a destination, so from outside: it says what to put
// back, this box says where it may go.
func TestABoxRestoreFollowsTheManifestAndReportsWhatItRefused(t *testing.T) {
	logInTempDir(t)
	atTheDefaultDataRoot(t)

	raw := manifestOf("/var/lib/casaos/fleet", "/var/lib/casaos/never-heard-of.json", "/etc/passwd", "/var/lib/casaos/backups/restic")
	restorer := &fakeRestorer{files: map[string][]byte{path.Join(RootFor(SystemBackupName, restoreStamp), ManifestFileName): raw}}

	report, err := RestoreSystemBackup(context.Background(), restorer, &fakeUnits{}, SystemRestoreOptions{Destination: "offsite", Stamp: restoreStamp})
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Restored) != 2 || report.Restored[0].Source != "/var/lib/casaos/fleet" || report.Restored[1].Source != "/var/lib/casaos/never-heard-of.json" {
		t.Fatalf("the two that belong to the box, at their targets, in manifest order: %+v", report.Restored)
	}
	if len(report.Missing) != 2 || report.Missing[0].Target != "/etc/passwd" || report.Missing[1].Target != "/var/lib/casaos/backups/restic" {
		t.Fatalf("the others are refused and said so: %+v", report.Missing)
	}
	if len(restorer.restored) != 2 {
		t.Fatalf("nothing is written for a refused target: %+v", restorer.restored)
	}
	for _, call := range restorer.restored {
		if call.host == "/etc/passwd" || strings.HasPrefix(call.host, "/var/lib/casaos/backups") || strings.HasPrefix(call.host, "/elsewhere") {
			t.Fatalf("written where it must not be: %+v", call)
		}
	}
}

func TestTheGatewayIsRestartedLastWhenItsFilesCameBack(t *testing.T) {
	atTheDefaultDataRoot(t)

	for _, tc := range []struct {
		name    string
		targets []string
		want    string
	}{
		{"its folder's file", []string{"/root/.config/rclone/rclone.conf", "/var/lib/casaos/gateway/routes.json"}, "rclone,casaos-gateway"},
		{"the certificates", []string{"/var/lib/casaos/tls"}, "casaos-gateway"},
		{"neither", []string{"/var/lib/casaos/fleet", "/var/lib/casaos/gateway/guard-state.json"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logInTempDir(t)
			atTheDefaultDataRoot(t)
			restorer := &fakeRestorer{files: map[string][]byte{path.Join(RootFor(SystemBackupName, restoreStamp), ManifestFileName): manifestOf(tc.targets...)}}
			units := &fakeUnits{}

			if _, err := RestoreSystemBackup(context.Background(), restorer, units, SystemRestoreOptions{Destination: "offsite", Stamp: restoreStamp}); err != nil {
				t.Fatal(err)
			}

			if got := strings.Join(units.restarted, ","); got != tc.want {
				t.Fatalf("restarted %q, want %q", got, tc.want)
			}
		})
	}
}

// The fixed list is restorable wherever the data folder is: its paths do not
// follow it, so the data folder's rules cannot be what lets them through.
func TestTheFixedListIsRestorableWhateverTheDataFolderIs(t *testing.T) {
	logInTempDir(t)
	was := systemDataRoot
	systemDataRoot = "/srv/casa"
	t.Cleanup(func() { systemDataRoot = was })

	manifest := PlanBackup(SystemBackupName, systemLegacyEntries(func(string) bool { return true }), nil)
	for i := range manifest.Operations {
		manifest.Operations[i].Directory = !strings.HasSuffix(manifest.Operations[i].Source, ".conf")
	}
	raw, _ := stdjson.Marshal(manifest)
	restorer := &fakeRestorer{files: map[string][]byte{path.Join(RootFor(SystemBackupName, restoreStamp), ManifestFileName): raw}}

	report, err := RestoreSystemBackup(context.Background(), restorer, &fakeUnits{}, SystemRestoreOptions{Destination: "offsite", Stamp: restoreStamp})
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Missing) != 0 || len(report.Restored) != len(systemBackupPaths()) {
		t.Fatalf("every part of the fixed list back: %+v", report)
	}
}

// A link under the data folder leads the copy out of the box: the target that is
// one, or has one for a parent, is refused and nothing is written.
func TestARestoreRefusesATargetThatIsOrLiesUnderALink(t *testing.T) {
	logInTempDir(t)
	atTheDefaultDataRoot(t)
	was := isSymlink
	isSymlink = func(p string) bool { return p == "/var/lib/casaos/fleet" || p == "/var/lib/casaos/backups" }
	t.Cleanup(func() { isSymlink = was })

	raw := manifestOf("/var/lib/casaos/fleet", "/var/lib/casaos/backups/schedules.json", "/var/lib/casaos/tls")
	restorer := &fakeRestorer{files: map[string][]byte{path.Join(RootFor(SystemBackupName, restoreStamp), ManifestFileName): raw}}

	report, err := RestoreSystemBackup(context.Background(), restorer, &fakeUnits{}, SystemRestoreOptions{Destination: "offsite", Stamp: restoreStamp})
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Missing) != 2 || len(report.Restored) != 1 || report.Restored[0].Source != "/var/lib/casaos/tls" {
		t.Fatalf("the link and what lies under one are refused: %+v", report)
	}
	if len(restorer.restored) != 1 || restorer.restored[0].host != "/var/lib/casaos/tls" {
		t.Fatalf("nothing is written through a link: %+v", restorer.restored)
	}
}

// What came back may carry the default modes: the data folder's targets and the
// rclone configuration are tightened, /etc/casaos is not.
func TestARestoreTightensTheModesOfWhatCameBackExceptTheConfigFolder(t *testing.T) {
	logInTempDir(t)
	atTheDefaultDataRoot(t)
	var tightened []string
	was := tightenRestored
	tightenRestored = func(p string) error { tightened = append(tightened, p); return nil }
	t.Cleanup(func() { tightenRestored = was })

	raw := manifestOf("/var/lib/casaos/tls", "/root/.config/rclone/rclone.conf", "/etc/casaos", "/etc/samba/smb.casa.conf")
	restorer := &fakeRestorer{files: map[string][]byte{path.Join(RootFor(SystemBackupName, restoreStamp), ManifestFileName): raw}}

	if _, err := RestoreSystemBackup(context.Background(), restorer, &fakeUnits{}, SystemRestoreOptions{Destination: "offsite", Stamp: restoreStamp}); err != nil {
		t.Fatal(err)
	}

	if strings.Join(tightened, ",") != "/var/lib/casaos/tls,/root/.config/rclone/rclone.conf" {
		t.Fatalf("tightened %v", tightened)
	}
}

func TestTightenRestoredTakesGroupAndOtherOffFilesAndFoldersAndLeavesLinksAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes do not apply")
	}
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	tree := filepath.Join(dir, "tls")
	for _, d := range []string{outside, filepath.Join(tree, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	key := filepath.Join(tree, "sub", "gateway.key")
	if err := os.WriteFile(key, []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "f"), []byte("f"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tree, "link")); err != nil {
		t.Fatal(err)
	}

	if err := tightenRestored(tree); err != nil {
		t.Fatal(err)
	}

	for p, want := range map[string]os.FileMode{tree: 0o700, filepath.Join(tree, "sub"): 0o700, key: 0o600, outside: 0o755, filepath.Join(outside, "f"): 0o644} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s: %v, want %v", p, info.Mode().Perm(), want)
		}
	}
}
