package service

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"path"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
)

// A destination as a restore sees it: files that can be fetched, and a record
// of everything it was told to put back and where.
type restoredCall struct {
	kind   string // "dir" or "file"
	remote string
	host   string
}

type fakeRestorer struct {
	files    map[string][]byte
	restored []restoredCall
	failOn   string
}

func (f *fakeRestorer) Fetch(_ context.Context, _, remotePath string) ([]byte, error) {
	content, ok := f.files[remotePath]
	if !ok {
		return nil, errors.New("rclone: object not found")
	}

	return content, nil
}

func (f *fakeRestorer) RestoreDirectory(_ context.Context, _, remotePath, hostPath string) error {
	if f.failOn == remotePath {
		return errors.New("destination refused it")
	}
	f.restored = append(f.restored, restoredCall{"dir", remotePath, hostPath})

	return nil
}

func (f *fakeRestorer) RestoreFile(_ context.Context, _, remotePath, hostPath string) error {
	if f.failOn == remotePath {
		return errors.New("destination refused it")
	}
	f.restored = append(f.restored, restoredCall{"file", remotePath, hostPath})

	return nil
}

const restoreStamp = "2026-09-13T02-29-56Z"

// where Docker keeps the demo app's named volume on this box
const demoVolumePath = "/var/lib/docker/volumes/demo_data/_data"

// what the docker fake answers, keyed by the docker name
func demoMountpoints() map[string]string {
	return map[string]string{"demo_data": demoVolumePath}
}

// backupOf is what a backup of the app would have left at the destination: its
// manifest, planned the way RunBackup plans, with the host paths of the box that
// took it -- which are not this box's, on purpose.
func backupOf(t *testing.T, app *ComposeApp) (*fakeRestorer, BackupManifest) {
	t.Helper()

	// keyed by the compose name, as RunBackup hands them to PlanBackup
	manifest := PlanBackup(app.Name, app.BackupInventory(), map[string]string{"data": demoVolumePath})
	manifest.MarkBindFiles(isRegularFile)
	for i := range manifest.Operations {
		manifest.Operations[i].Source = "/somewhere/else/" + path.Base(manifest.Operations[i].Source)
	}

	raw, err := stdjson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	root := RootFor(app.Name, restoreStamp)
	files := map[string][]byte{path.Join(root, ManifestFileName): raw}
	for _, operation := range manifest.Operations {
		files[path.Join(root, operation.Destination)] = []byte("content of " + operation.Destination)
	}

	return &fakeRestorer{files: files}, manifest
}

func running(ids ...string) func(context.Context) (map[string][]codegen.ContainerSummary, error) {
	return func(context.Context) (map[string][]codegen.ContainerSummary, error) {
		out := map[string][]codegen.ContainerSummary{}
		for i, id := range ids {
			out[string(rune('a'+i))] = []codegen.ContainerSummary{{ID: id, State: "running"}}
		}

		return out, nil
	}
}

func noInstall(context.Context, string, []byte, []byte) (*ComposeApp, error) {
	return nil, errors.New("install was not supposed to be called")
}

// Where things go is decided here, from the app as installed now, and never read
// from the manifest: a manifest is a file on a remote that anyone with the
// credentials can edit.
func TestARestorePutsThingsWhereThisBoxHasThemNotWhereTheManifestSays(t *testing.T) {
	logInTempDir(t)

	app, dir := appWithOneBindAndOneVolume(t)
	docker := &fakeBackupDocker{mountpoints: demoMountpoints()}
	restorer, _ := backupOf(t, app)

	report, err := RestoreBackup(context.Background(), app, docker, restorer, noInstall, RestoreOptions{
		Destination: "offsite", App: "demo", Stamp: restoreStamp, Containers: running("c1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// the bind and the volume, at this box's paths
	if len(restorer.restored) != 2 {
		t.Fatalf("want the bind and the volume, got %+v", restorer.restored)
	}
	for _, call := range restorer.restored {
		if strings.HasPrefix(call.host, "/somewhere/else/") {
			t.Fatalf("the manifest's path was used as a destination: %+v", call)
		}
	}
	hosts := restorer.restored[0].host + " " + restorer.restored[1].host
	if !strings.Contains(hosts, dir+"/config") || !strings.Contains(hosts, demoVolumePath) {
		t.Fatalf("this box's bind and volume: %s", hosts)
	}

	if len(report.Restored) != 2 || len(report.Missing) != 0 || report.Installed {
		t.Fatalf("%+v", report)
	}
}

// The compose file an installed app runs now is kept: only its data comes back.
func TestARestoreOfAnInstalledAppLeavesItsComposeFileAlone(t *testing.T) {
	logInTempDir(t)

	app, _ := appWithOneBindAndOneVolume(t)
	restorer, _ := backupOf(t, app)

	if _, err := RestoreBackup(context.Background(), app, &fakeBackupDocker{mountpoints: demoMountpoints()}, restorer, noInstall, RestoreOptions{
		Destination: "offsite", App: "demo", Stamp: restoreStamp, Containers: running("c1"),
	}); err != nil {
		t.Fatal(err)
	}

	for _, call := range restorer.restored {
		if strings.Contains(call.remote, "/compose/") {
			t.Fatalf("the definition is not data: %+v", call)
		}
	}
}

// The app is held still for the copy and started again afterwards, the same
// way a backup holds it.
func TestARestoreStopsTheAppFirstAndStartsItAfter(t *testing.T) {
	logInTempDir(t)

	app, _ := appWithOneBindAndOneVolume(t)
	docker := &fakeBackupDocker{mountpoints: demoMountpoints()}
	restorer, _ := backupOf(t, app)

	if _, err := RestoreBackup(context.Background(), app, docker, restorer, noInstall, RestoreOptions{
		Destination: "offsite", App: "demo", Stamp: restoreStamp, Containers: running("c1", "c2"),
	}); err != nil {
		t.Fatal(err)
	}

	if strings.Join(docker.stopped, ",") != "c1,c2" || strings.Join(docker.started, ",") != "c2,c1" {
		t.Fatalf("stopped %v, started %v", docker.stopped, docker.started)
	}
}

// What the backup holds and the app no longer declares is reported, not written
// somewhere it was not asked for.
func TestWhatTheAppNoLongerDeclaresIsReportedNotRestored(t *testing.T) {
	logInTempDir(t)

	app, _ := appWithOneBindAndOneVolume(t)
	restorer, _ := backupOf(t, app)

	// the app has since dropped its named volume
	delete(app.Volumes, "data")
	for name, service := range app.Services {
		kept := service.Volumes[:0]
		for _, volume := range service.Volumes {
			if volume.Type != "volume" {
				kept = append(kept, volume)
			}
		}
		service.Volumes = kept
		app.Services[name] = service
	}

	report, err := RestoreBackup(context.Background(), app, &fakeBackupDocker{}, restorer, noInstall, RestoreOptions{
		Destination: "offsite", App: "demo", Stamp: restoreStamp, Containers: running("c1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Restored) != 1 || len(report.Missing) != 1 || report.Missing[0].Volume != "data" {
		t.Fatalf("the bind restored, the volume reported: %+v", report)
	}
	for _, call := range restorer.restored {
		if strings.Contains(call.remote, "volumes/") {
			t.Fatalf("a volume with nowhere to go was written somewhere: %+v", call)
		}
	}
}

// An app that is not installed is installed first, from the backup's own
// compose file, and only then is anything put back into it.
func TestAnAppThatIsNotInstalledIsInstalledFromItsBackupFirst(t *testing.T) {
	logInTempDir(t)

	app, _ := appWithOneBindAndOneVolume(t)
	restorer, _ := backupOf(t, app)

	installedWith := map[string][]byte{}
	install := func(_ context.Context, name string, compose, env []byte) (*ComposeApp, error) {
		installedWith["name"] = []byte(name)
		installedWith["compose"] = compose
		installedWith["env"] = env
		if len(restorer.restored) != 0 {
			t.Fatal("nothing may be restored before the app exists")
		}

		return app, nil
	}

	report, err := RestoreBackup(context.Background(), nil, &fakeBackupDocker{mountpoints: demoMountpoints()}, restorer, install, RestoreOptions{
		Destination: "offsite", App: "demo", Stamp: restoreStamp, Containers: running("c1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if string(installedWith["name"]) != "demo" || string(installedWith["compose"]) != "content of compose/docker-compose.yml" {
		t.Fatalf("installed with %q", installedWith)
	}
	if string(installedWith["env"]) != "content of compose/.env" {
		t.Fatalf("the .env travels with the compose file: %q", installedWith["env"])
	}
	if !report.Installed || len(report.Restored) != 2 {
		t.Fatalf("%+v", report)
	}
}

// A backup of one app must not be poured into another, whatever the request says.
func TestABackupOfAnotherAppIsRefused(t *testing.T) {
	logInTempDir(t)

	app, _ := appWithOneBindAndOneVolume(t)
	restorer, manifest := backupOf(t, app)

	// the same backup, filed under another app's name
	manifest.App = "other"
	raw, _ := stdjson.Marshal(manifest)
	restorer.files[path.Join(RootFor("demo", restoreStamp), ManifestFileName)] = raw

	_, err := RestoreBackup(context.Background(), app, &fakeBackupDocker{mountpoints: demoMountpoints()}, restorer, noInstall, RestoreOptions{
		Destination: "offsite", App: "demo", Stamp: restoreStamp, Containers: running("c1"),
	})
	if err == nil || !strings.Contains(err.Error(), "is of `other`") {
		t.Fatalf("want the refusal, got %v", err)
	}
	if len(restorer.restored) != 0 {
		t.Fatalf("nothing may be written: %+v", restorer.restored)
	}
}

// A backup written by a later version of this code is refused rather than
// half-understood.
func TestABackupInAFormatThisVersionDoesNotReadIsRefused(t *testing.T) {
	logInTempDir(t)

	app, _ := appWithOneBindAndOneVolume(t)
	restorer, manifest := backupOf(t, app)

	manifest.FormatVersion = BackupFormatVersion + 1
	raw, _ := stdjson.Marshal(manifest)
	restorer.files[path.Join(RootFor("demo", restoreStamp), ManifestFileName)] = raw

	_, err := RestoreBackup(context.Background(), app, &fakeBackupDocker{mountpoints: demoMountpoints()}, restorer, noInstall, RestoreOptions{
		Destination: "offsite", App: "demo", Stamp: restoreStamp, Containers: running("c1"),
	})
	if err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("want the refusal, got %v", err)
	}
}

// A restore that fails part-way still starts the app again, and is written down
// with its reason, marked as a restore.
func TestAFailedRestoreStartsTheAppAgainAndIsWrittenDown(t *testing.T) {
	logInTempDir(t)

	app, _ := appWithOneBindAndOneVolume(t)
	docker := &fakeBackupDocker{mountpoints: demoMountpoints()}
	restorer, _ := backupOf(t, app)
	restorer.failOn = path.Join(RootFor("demo", restoreStamp), "volumes/data")

	_, err := RestoreOnDemand(context.Background(), app, docker, restorer, noInstall, RestoreOptions{
		Destination: "offsite", App: "demo", Stamp: restoreStamp, Containers: running("c1"),
	})
	if err == nil {
		t.Fatal("want the failure back")
	}

	if strings.Join(docker.started, ",") != "c1" {
		t.Fatalf("the app comes back either way: started %v", docker.started)
	}

	runs, err := BackupRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || !runs[0].Restore || runs[0].Error == "" {
		t.Fatalf("a restore, failed, with its reason: %+v", runs)
	}
}
