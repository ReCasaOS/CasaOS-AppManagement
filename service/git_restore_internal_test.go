package service

import (
	"context"
	stdjson "encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

func TestABackupOfAGitAppRecordsWhereItsCodeComesFrom(t *testing.T) {
	logInTempDir(t)
	gitAppsIn(t)
	app, _ := appWithOneBindAndOneVolume(t)
	options := BackupOptions{Destination: "offsite", Stamp: restoreStamp}

	manifest, err := RunBackup(context.Background(), app, &fakeBackupDocker{mountpoints: demoMountpoints()}, &fakeCopier{}, options)
	assert.NilError(t, err)
	assert.Assert(t, manifest.Git == nil, "an app not deployed from git")

	const commit = "4f5f60c16eba0123456789abcdef0123456789ab"
	assert.NilError(t, saveGitApp(&gitApp{App: "demo", Remote: "git@github.com:owner/demo.git", Branch: "main", Deployed: &gitDeployment{Commit: commit}}))

	manifest, err = RunBackup(context.Background(), app, &fakeBackupDocker{mountpoints: demoMountpoints()}, &fakeCopier{}, options)
	assert.NilError(t, err)
	assert.DeepEqual(t, manifest.Git, &BackupGit{Remote: "git@github.com:owner/demo.git", Branch: "main", Commit: commit})
}

func TestARestoreOfAGitAppNotInstalledComesBackFromItsRepository(t *testing.T) {
	logInTempDir(t)
	app, _ := appWithOneBindAndOneVolume(t)
	restorer, manifest := backupOf(t, app)
	origin := BackupGit{Remote: "https://example.invalid/owner/demo.git", Branch: "main", Commit: strings.Repeat("a", 40)}
	manifest.Git = &origin
	raw, err := stdjson.Marshal(manifest)
	assert.NilError(t, err)
	restorer.files[path.Join(RootFor("demo", restoreStamp), ManifestFileName)] = raw

	var restoredWith BackupGit
	var envWith []byte
	previous := restoreGitApp
	restoreGitApp = func(_ context.Context, name string, git BackupGit, env []byte) (*ComposeApp, error) {
		restoredWith, envWith = git, env
		return app, nil
	}
	t.Cleanup(func() { restoreGitApp = previous })

	report, err := RestoreBackup(context.Background(), nil, &fakeBackupDocker{mountpoints: demoMountpoints()}, restorer, noInstall, RestoreOptions{
		Destination: "offsite", App: "demo", Stamp: restoreStamp, Containers: running("c1"),
	})
	assert.NilError(t, err)
	assert.DeepEqual(t, restoredWith, origin)
	assert.Equal(t, string(envWith), "content of compose/.env")
	assert.Assert(t, report.Installed)
	assert.Equal(t, len(report.Restored), 2)
}

func TestAGitAppIsInstalledFromItsBackupAtTheBackedUpCommit(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	url, work := newTestRemote(t)
	first := gitShell(t, work, "git rev-parse HEAD")
	pushTestCommit(t, work, "index.html", "v2")

	app, err := installGitAppFromBackup(context.Background(), "jarvis", BackupGit{Remote: url, Branch: "main", Commit: first}, []byte("GREETING=hello\n"))
	assert.NilError(t, err)

	assert.Equal(t, app.Name, "jarvis")
	assert.Equal(t, app.WorkingDir, filepath.Join(gitAppsDataRoot, "jarvis"))
	assert.DeepEqual(t, fake.Calls(), []string{"build " + first[:12], "retag " + first[:12], "start " + first[:12]})

	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first)
	env, err := os.ReadFile(filepath.Join(st.Dir, ".env"))
	assert.NilError(t, err)
	assert.Equal(t, string(env), "GREETING=hello\n")
}

// A restore of the version the app's state already names starts it from the images that
// version kept, and builds nothing.
func TestARestoreOfTheDeployedCommitStartsItFromItsImages(t *testing.T) {
	fake, _, first := deployedTestApp(t, false)
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)

	_, err = installGitAppFromBackup(context.Background(), "jarvis", BackupGit{Remote: st.Remote, Branch: st.Branch, Commit: first}, nil)
	assert.NilError(t, err)

	assert.DeepEqual(t, fake.Calls(), []string{"retag " + first[:12], "start " + first[:12]})
}

// A backup holds no key and no token: a repository the restore cannot reach leaves the app
// registered with what it needs, and the restore says what to do.
func TestARestoreThatCannotReachTheRepositoryAsksForAccess(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	commit := strings.Repeat("a", 40)

	_, err := installGitAppFromBackup(context.Background(), "jarvis", BackupGit{Remote: "ssh://git@127.0.0.1:1/owner/jarvis.git", Branch: "main", Commit: commit}, nil)
	assert.ErrorContains(t, err, "access required: add this deploy key")
	assert.ErrorContains(t, err, "ssh-ed25519 ")

	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, st.Access, git.AccessKey)
	assert.Assert(t, st.Deployed == nil)

	_, err = installGitAppFromBackup(context.Background(), "private", BackupGit{Remote: "https://127.0.0.1:1/owner/private.git", Branch: "main", Commit: commit}, nil)
	assert.ErrorContains(t, err, "give `private` a token")
}

// A manifest is a file anyone with the destination's credentials can edit: what it says of
// the code is checked before git is given it, and nothing is registered.
func TestARestoreRefusesAnOriginGitCannotBeGiven(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	commit := strings.Repeat("a", 40)

	for origin, message := range map[BackupGit]string{
		{Remote: "https://127.0.0.1:1/owner/demo.git", Branch: "main", Commit: "4f5f60c"}:     "not a full commit hash",
		{Remote: "ext::id", Branch: "main", Commit: commit}:                                   "not a URL CasaOS can clone",
		{Remote: "https://127.0.0.1:1/owner/demo.git", Branch: "@{upstream}", Commit: commit}: "not a branch name",
	} {
		_, err := installGitAppFromBackup(ctx, "jarvis", origin, nil)
		assert.ErrorContains(t, err, message)
	}
	_, err := loadGitApp("jarvis")
	assert.ErrorIs(t, err, ErrGitAppNotFound, "nothing is registered")
}

// A repository that tracks .env restores its own: the backed-up one is not written over it.
func TestARestoreNeverWritesATrackedEnv(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	url, work := newTestRemote(t)
	commit := pushTestCommit(t, work, ".env", "GREETING=tracked\n")

	_, err := installGitAppFromBackup(context.Background(), "jarvis", BackupGit{Remote: url, Branch: "main", Commit: commit}, []byte("GREETING=edited\n"))
	assert.NilError(t, err)

	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	env, err := os.ReadFile(filepath.Join(st.Dir, ".env"))
	assert.NilError(t, err)
	assert.Equal(t, string(env), "GREETING=tracked\n")
	assert.Equal(t, st.Deployed.Commit, commit)
}
