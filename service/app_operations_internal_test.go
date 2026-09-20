package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
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
