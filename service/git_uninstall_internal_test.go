package service

import (
	"context"
	"os"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/types"
	"gotest.tools/v3/assert"
)

// An adopted folder is its owner's and never goes; a created app's clone goes with the
// config folder only; any other app is uninstalled as before.
func TestUninstallingAGitAppDeletesOnlyWhatIsCasaOSToDelete(t *testing.T) {
	volumes := func(sources ...string) types.Services {
		service := types.ServiceConfig{Name: "web"}
		for _, source := range sources {
			service.Volumes = append(service.Volumes, types.ServiceVolumeConfig{Source: source, Target: "/data"})
		}
		return types.Services{"web": service}
	}

	byHand := &ComposeApp{Name: "jarvis", WorkingDir: "/home/gary/jarvis", Services: volumes("/home/gary/jarvis/data", "/DATA/AppData/jarvis/cache")}
	adopted := &gitApp{Origin: gitOriginAdopted, Dir: "/home/gary/jarvis"}

	assert.DeepEqual(t, byHand.uninstalledFolders(nil, false), []string{"/home/gary/jarvis"})
	assert.DeepEqual(t, byHand.uninstalledFolders(nil, true), []string{"/home/gary/jarvis", "/DATA/AppData/jarvis"})
	assert.DeepEqual(t, byHand.uninstalledFolders(adopted, false), []string{})
	assert.DeepEqual(t, byHand.uninstalledFolders(adopted, true), []string{"/DATA/AppData/jarvis"})

	cloned := &ComposeApp{Name: "jarvis", WorkingDir: "/DATA/AppData/jarvis", Services: volumes("/DATA/AppData/jarvis/data")}
	created := &gitApp{Origin: gitOriginCreated, Dir: "/DATA/AppData/jarvis"}

	assert.DeepEqual(t, cloned.uninstalledFolders(created, false), []string{})
	assert.DeepEqual(t, cloned.uninstalledFolders(created, true), []string{"/DATA/AppData/jarvis"})
}

// An uninstall is refused while the app deploys, naming the deployment, which goes on. The
// name is one no real compose project has: an uninstall that went ahead would reach the
// daemon the tests run against.
func TestUninstallingAGitAppIsRefusedWhileItDeploys(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	url, work := newTestRemote(t)
	ctx := context.Background()
	const name = "casaos-test-uninstall"

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: name, URL: url, Access: "none"})
	assert.NilError(t, err)
	assert.Equal(t, checkTestApp(t, name).Cloned, true)
	fake.gate = make(chan struct{})

	_, err = DeployGitApp(ctx, name, "", nil)
	assert.NilError(t, err)
	<-fake.gate // the deployment is building

	err = NewComposeService().Uninstall(ctx, &ComposeApp{Name: name}, true)
	fake.gate <- struct{}{}

	assert.Error(t, err, "`casaos-test-uninstall` is busy: deploy in progress")
	st := waitForGitApp(t, name)
	assert.Equal(t, st.Deployed.Commit, gitShell(t, work, "git rev-parse HEAD"), "the deployment went on and the app is still known")
}

func TestAnUninstalledGitAppIsForgottenWithItsImages(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	st := &gitApp{
		App:      "jarvis",
		Deployed: &gitDeployment{Commit: "b", Images: map[string]string{"web": "jarvis-web:git-b"}},
		History: []gitHistoryEntry{
			{Commit: "b", Images: map[string]string{"web": "jarvis-web:git-b"}},
			{Commit: "a", Images: map[string]string{"web": "jarvis-web:git-a"}},
		},
	}
	assert.NilError(t, saveGitApp(st))
	_, err := generateGitKey("jarvis")
	assert.NilError(t, err)

	forgetUninstalledGitApp(context.Background(), st)

	assert.DeepEqual(t, fake.removed, []string{"jarvis-web:git-a", "jarvis-web:git-b"})
	_, err = loadGitApp("jarvis")
	assert.ErrorIs(t, err, ErrGitAppNotFound)
	assert.Assert(t, !pathExists(gitAppFile("jarvis", ".key")))
}

// A git state that cannot be read may be an adopted app's, whose folder is its owner's: the
// uninstall deletes nothing then, and keeps the state for the owner to look at. Any other
// app's folder goes as before.
func TestAnUnreadableGitStateKeepsEveryFolder(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()

	adopted := t.TempDir()
	assert.NilError(t, os.MkdirAll(gitAppsDir, 0o700))
	assert.NilError(t, os.WriteFile(gitAppFile("jarvis", ".json"), []byte("{torn"), 0o600))
	(&ComposeApp{Name: "jarvis", WorkingDir: adopted}).removeUninstalledFolders(ctx, true)
	_, err := os.Stat(adopted)
	assert.NilError(t, err, "the folder is kept")
	assert.Assert(t, pathExists(gitAppFile("jarvis", ".json")), "and so is the state")

	other := t.TempDir()
	(&ComposeApp{Name: "nextcloud", WorkingDir: other}).removeUninstalledFolders(ctx, false)
	_, err = os.Stat(other)
	assert.Assert(t, os.IsNotExist(err), "any other app's folder goes as before")
}
