package service

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/types"
	"gotest.tools/v3/assert"
)

func TestBeginRefusesASecondOperationOnTheSameApp(t *testing.T) {
	end, err := Begin("jarvis", "deploy")
	assert.NilError(t, err)

	_, err = Begin("jarvis", "update")
	var busy ErrAppBusy
	assert.Assert(t, errors.As(err, &busy), err)
	assert.Equal(t, busy, ErrAppBusy{App: "jarvis", Running: "deploy"})
	assert.Equal(t, err.Error(), "`jarvis` is busy: deploy in progress")

	other, err := Begin("nextcloud", "backup")
	assert.NilError(t, err, "another app is not held")
	other()

	end()
	end() // twice is harmless

	again, err := Begin("jarvis", "update")
	assert.NilError(t, err)
	again()
}

// holding is what a running deployment looks like to everything else.
func holding(t *testing.T, app string) {
	t.Helper()

	end, err := Begin(app, "deploy")
	assert.NilError(t, err)
	t.Cleanup(end)
}

func assertBusy(t *testing.T, err error) {
	t.Helper()

	assert.Assert(t, errors.As(err, new(ErrAppBusy)), "want ErrAppBusy, got %v", err)
}

func TestASettingsOrEnvSaveIsRefusedWhileTheAppIsBusy(t *testing.T) {
	logger.LogInitConsoleOnly()
	dir := writeStack(t, map[string]string{"docker-compose.yml": handWrittenStack, ".env": "GREETING=hello\n"})
	app := &ComposeApp{Name: "jarvis", WorkingDir: dir, ComposeFiles: []string{filepath.Join(dir, "docker-compose.yml")}}
	holding(t, "jarvis")

	assertBusy(t, app.Apply(context.Background(), []byte(handWrittenStack)))
	assertBusy(t, app.ApplyEnv(context.Background(), []byte("GREETING=bye\n")))
}

func TestAnUpdateIsRefusedWhileTheAppIsBusy(t *testing.T) {
	app := &ComposeApp{Name: "jarvis", ComposeFiles: []string{"/nonexistent/docker-compose.yml"}}
	holding(t, "jarvis")

	assertBusy(t, app.Update(context.Background()))
	assertBusy(t, app.UpdateNow(context.Background()))
}

func TestAnInstallIsRefusedWhileTheAppIsBusy(t *testing.T) {
	holding(t, "jarvis")

	assertBusy(t, NewComposeService().Install(context.Background(), &ComposeApp{Name: "jarvis"}))
}

// An uninstall is refused before it touches anything. The name is one no real compose
// project has: an uninstall that went ahead would reach the daemon the tests run against.
func TestAnUninstallIsRefusedWhileTheAppIsBusy(t *testing.T) {
	holding(t, "casaos-test-uninstall")

	err := NewComposeService().Uninstall(context.Background(), &ComposeApp{Name: "casaos-test-uninstall"}, true)
	assert.Error(t, err, "`casaos-test-uninstall` is busy: deploy in progress")
}

// A backup is the one operation that queues. It is asked for by a timer, or in the
// second after an install whose hold has not been let go of yet, so it waits.
func TestABackupThatHoldsTheAppStillWaitsForItsTurn(t *testing.T) {
	logInTempDir(t)
	app, _ := appWithOneBindAndOneVolume(t)
	docker := &fakeBackupDocker{mountpoints: demoMountpoints()}

	end, err := Begin(app.Name, "install")
	assert.NilError(t, err)
	go func() {
		time.Sleep(2 * howOftenToAskAgain)
		end()
	}()

	manifest, err := RunBackup(context.Background(), app, docker, &fakeCopier{}, BackupOptions{
		Destination: "offsite", Stamp: restoreStamp, HoldStill: true, Containers: running("c1"),
	})
	assert.NilError(t, err)
	assert.Assert(t, manifest.ContainersStopped)
	assert.Equal(t, strings.Join(docker.stopped, ","), "c1", "it held the app still once its turn came")
}

// Waiting is not waiting forever: a hold nobody lets go of is still the refusal, and
// nothing of the app was touched on the way to it.
func TestABackupGivesUpWhenTheAppNeverComesFree(t *testing.T) {
	logInTempDir(t)
	app, _ := appWithOneBindAndOneVolume(t)
	docker := &fakeBackupDocker{mountpoints: demoMountpoints()}
	holding(t, app.Name)

	ctx, cancel := context.WithTimeout(context.Background(), 2*howOftenToAskAgain)
	defer cancel()

	_, err := RunBackup(ctx, app, docker, &fakeCopier{}, BackupOptions{
		Destination: "offsite", Stamp: restoreStamp, HoldStill: true, Containers: running("c1"),
	})
	assertBusy(t, err)
	assert.Equal(t, strings.Join(docker.stopped, ","), "", "nothing was stopped")
}

func TestARestoreIsRefusedWhileTheAppIsBusy(t *testing.T) {
	logInTempDir(t)
	app, _ := appWithOneBindAndOneVolume(t)
	restorer, _ := backupOf(t, app)
	holding(t, app.Name)

	_, err := RestoreBackup(context.Background(), app, &fakeBackupDocker{mountpoints: demoMountpoints()}, restorer, noInstall, RestoreOptions{
		Destination: "offsite", App: "demo", Stamp: restoreStamp, Containers: running("c1"),
	})
	assertBusy(t, err)
	assert.Equal(t, len(restorer.restored), 0)
}

// The core reads this before it updates the box by itself: every held app once, by name
// whatever order the holds came in, and nothing once they are let go of.
func TestRunningOperationsListWhatBeginHolds(t *testing.T) {
	assert.DeepEqual(t, RunningOperations(), []AppOperation{})

	var ends []func()
	for _, hold := range []AppOperation{{"jarvis", "deploy"}, {"adguard", "backup"}, {"nextcloud", "update"}, {"bitwarden", "restore"}} {
		end, err := Begin(hold.App, hold.Kind)
		assert.NilError(t, err)
		ends = append(ends, end)
	}

	want := []AppOperation{{"adguard", "backup"}, {"bitwarden", "restore"}, {"jarvis", "deploy"}, {"nextcloud", "update"}}
	assert.DeepEqual(t, RunningOperations(), want)
	assert.DeepEqual(t, RunningOperations(), want)

	ends[0]()
	assert.DeepEqual(t, RunningOperations(), []AppOperation{{"adguard", "backup"}, {"bitwarden", "restore"}, {"nextcloud", "update"}})

	for _, end := range ends {
		end()
	}
	assert.DeepEqual(t, RunningOperations(), []AppOperation{})
}

// A mark is listed like a hold and refuses nothing: a backup copied from the running app
// lets a settings save through, as it always did. One entry per app, the hold's kind over
// the mark's, and the app stays listed until its last mark is done.
func TestAMarkIsListedAndRefusesNothing(t *testing.T) {
	first := markInProgress("demo", "backup")
	second := markInProgress("demo", "backup")
	assert.DeepEqual(t, RunningOperations(), []AppOperation{{"demo", "backup"}})

	end, err := Begin("demo", "save")
	assert.NilError(t, err, "a mark is not a hold")
	assert.DeepEqual(t, RunningOperations(), []AppOperation{{"demo", "save"}})
	end()

	first()
	first() // twice is harmless
	// the second backup still runs
	assert.DeepEqual(t, RunningOperations(), []AppOperation{{"demo", "backup"}})

	second()
	assert.DeepEqual(t, RunningOperations(), []AppOperation{})
}

// What GET /operations would have answered at each step of a copy.
type watchingCopier struct {
	fakeCopier
	seen [][]AppOperation
}

func (w *watchingCopier) CopyDirectory(ctx context.Context, source, destinationName, destination string) error {
	w.seen = append(w.seen, RunningOperations())

	return w.fakeCopier.CopyDirectory(ctx, source, destinationName, destination)
}

func (w *watchingCopier) CopyFile(ctx context.Context, source, destinationName, destination string) error {
	w.seen = append(w.seen, RunningOperations())

	return w.fakeCopier.CopyFile(ctx, source, destinationName, destination)
}

type watchingRestorer struct {
	fakeRestorer
	seen [][]AppOperation
}

func (w *watchingRestorer) RestoreDirectory(ctx context.Context, destination, remotePath, hostPath string) error {
	w.seen = append(w.seen, RunningOperations())

	return w.fakeRestorer.RestoreDirectory(ctx, destination, remotePath, hostPath)
}

func (w *watchingRestorer) RestoreFile(ctx context.Context, destination, remotePath, hostPath string) error {
	w.seen = append(w.seen, RunningOperations())

	return w.fakeRestorer.RestoreFile(ctx, destination, remotePath, hostPath)
}

type watchingRunner struct {
	fakeRunner
	seen [][]AppOperation
}

func (w *watchingRunner) Purge(ctx context.Context, destination, remotePath string) error {
	w.seen = append(w.seen, RunningOperations())

	return w.fakeRunner.Purge(ctx, destination, remotePath)
}

// listedThroughout checks that every step of a copy saw the one operation, and that the
// list is empty again once it is over.
func listedThroughout(t *testing.T, seen [][]AppOperation, want AppOperation) {
	t.Helper()

	assert.Assert(t, len(seen) > 0, "nothing was copied")
	for _, operations := range seen {
		assert.DeepEqual(t, operations, []AppOperation{want})
	}
	assert.DeepEqual(t, RunningOperations(), []AppOperation{})
}

// The core reads the list before it updates the box, and the update stops app-management
// with whatever it is copying. So a backup is listed for as long as it copies, whether it
// holds the app still or copies it running: as it does when asked to, and always for an
// app that answers DNS, which is never stopped.
func TestABackupIsListedWhileItCopiesHeldStillOrNot(t *testing.T) {
	logger.LogInitConsoleOnly()
	logInTempDir(t)

	answersDNS := func(app *ComposeApp) {
		service := app.Services["app"]
		service.Ports = []types.ServicePortConfig{{Target: 53, Published: "53", Protocol: "udp"}}
		app.Services["app"] = service
	}

	for _, run := range []struct {
		name string
		hold bool
		dns  bool
	}{{"copied running", false, false}, {"held still", true, false}, {"answers DNS", true, true}} {
		t.Run(run.name, func(t *testing.T) {
			app, _ := appWithOneBindAndOneVolume(t)
			if run.dns {
				answersDNS(app)
			}
			copier := &watchingCopier{}

			manifest, err := RunBackup(context.Background(), app, &fakeBackupDocker{mountpoints: demoMountpoints()}, copier, BackupOptions{
				Destination: "offsite", Stamp: restoreStamp, HoldStill: run.hold, Containers: running("c1"),
			})
			assert.NilError(t, err)
			assert.Equal(t, manifest.ContainersStopped, run.hold && !run.dns)
			listedThroughout(t, copier.seen, AppOperation{"demo", "backup"})
		})
	}
}

// The box's own backup and restore hold services, not an app: listed all the same, under
// the name the box's backups are filed under.
func TestTheBoxsBackupAndRestoreAreListedWhileTheyCopy(t *testing.T) {
	logInTempDir(t)

	copier := &watchingCopier{}
	_, err := RunSystemBackup(context.Background(), copier, &fakeUnits{}, SystemBackupOptions{
		Destination: "offsite", Stamp: restoreStamp, HoldStill: true,
	})
	assert.NilError(t, err)
	listedThroughout(t, copier.seen, AppOperation{SystemBackupName, "backup"})

	raw, err := stdjson.Marshal(PlanBackup(SystemBackupName, SystemBackupInventory(func(string) bool { return true }), nil))
	assert.NilError(t, err)
	restorer := &watchingRestorer{fakeRestorer: fakeRestorer{files: map[string][]byte{
		path.Join(RootFor(SystemBackupName, restoreStamp), ManifestFileName): raw,
	}}}
	_, err = RestoreSystemBackup(context.Background(), restorer, &fakeUnits{}, SystemRestoreOptions{
		Destination: "offsite", Stamp: restoreStamp,
	})
	assert.NilError(t, err)
	listedThroughout(t, restorer.seen, AppOperation{SystemBackupName, "restore"})
}

// Retention deletes the old runs once a backup is written, after the backup has let go of
// its app: a purge cut short is a backup interrupted too.
func TestTheRetentionAfterAScheduledBackupIsListedWhileItDeletes(t *testing.T) {
	schedulerInTempDir(t)

	schedule := daily(time.Time{})
	schedule.Keep = 1
	assert.NilError(t, SaveBackupSchedules([]BackupSchedule{schedule}))

	runner := &watchingRunner{fakeRunner: fakeRunner{present: []string{"2026-09-09T03-00-00Z", "2026-09-10T03-00-00Z"}}}
	RunDueBackups(context.Background(), at(10, 7, 0), runner)

	assert.Equal(t, len(runner.purged), 1)
	listedThroughout(t, runner.seen, AppOperation{"nextcloud", "backup"})
}
