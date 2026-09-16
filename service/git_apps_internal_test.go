package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

func assertBadRequest(t *testing.T, err error, contains string) {
	t.Helper()

	var bad GitRequestError
	assert.Assert(t, errors.As(err, &bad), "want a GitRequestError, got %v", err)
	assert.ErrorContains(t, err, contains)
}

// checkTestApp runs "Check now" and waits for it, and for what it started, to end.
func checkTestApp(t *testing.T, name string) *gitApp {
	t.Helper()

	_, err := CheckGitApp(context.Background(), name)
	assert.NilError(t, err)

	return waitForGitApp(t, name)
}

func TestRegisteringAGitAppChecksWhatItIsGiven(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	register := func(r GitAppRegistration) error {
		_, err := CreateGitApp(ctx, r)
		return err
	}

	assertBadRequest(t, register(GitAppRegistration{Name: "Jarvis", URL: "https://github.com/o/j.git", Access: "none"}), "not an app name")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: "https://o:p@github.com/o/j.git", Access: "none"}), "without a password")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: "https://github.com/o/j.git", Access: "key"}), "SSH URL")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: "git@github.com:o/j.git", Access: "token", Token: "t"}), "HTTPS URL")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: "https://github.com/o/j.git", Access: "token"}), "needs the token")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: "https://github.com/o/j.git", Access: "none", Branch: "--orphan"}), "not a branch name")

	fake.projects["nextcloud"] = "/var/lib/casaos/apps/nextcloud"
	assert.ErrorIs(t, register(GitAppRegistration{Name: "nextcloud", URL: "https://github.com/o/j.git", Access: "none"}), ErrGitAppNameTaken)

	view, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: "git@github.com:owner/jarvis.git", Access: "key"})
	assert.NilError(t, err)
	assert.Equal(t, view.Origin, "created")
	assert.Equal(t, view.Dir, filepath.Join(gitAppsDataRoot, "jarvis"))
	assert.Equal(t, view.Cloned, false)
	assert.Assert(t, strings.HasSuffix(view.PublicKey, " casaos-jarvis"))
	_, err = os.Stat(view.Dir)
	assert.Assert(t, os.IsNotExist(err), "nothing is cloned before a check")

	assert.ErrorIs(t, register(GitAppRegistration{Name: "jarvis", URL: "https://github.com/o/j.git", Access: "none"}), ErrGitAppNameTaken)
}

func TestACheckClonesAndReviewsARegisteredAppInTheBackground(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	pushTestCommit(t, work, ".env.example", "GREETING=hello\n")
	head := gitShell(t, work, "git rev-parse HEAD")

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none"})
	assert.NilError(t, err)

	started, err := CheckGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, started.Operation.Kind, gitOperationCheck)

	waitForGitApp(t, "jarvis")
	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Assert(t, view.Operation == nil)
	assert.Equal(t, view.Branch, "main", "the branch the remote's HEAD points to")
	assert.Equal(t, view.Cloned, true)
	assert.Equal(t, view.Check.RemoteCommit, head)
	assert.Equal(t, view.Head.Commit, head)
	assert.Equal(t, view.Compose.Services[0].Name, "web")
	assert.Equal(t, *view.EnvTemplate, "GREETING=hello\n")
	assert.Equal(t, view.State, "idle")
	assert.Equal(t, view.NewCommits, false, "nothing runs yet")
	_, err = os.Stat(filepath.Join(gitAppsDataRoot, ".jarvis.clone"))
	assert.Assert(t, os.IsNotExist(err), "the clone was moved in whole")
}

func TestARepositoryWithoutAComposeFileEndsWithAnExampleAndNoClone(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	gitShell(t, work, "git rm -q compose.yaml && echo 'FROM busybox' > Dockerfile && git add Dockerfile && git commit -qm dockerfile && git push -q origin main")

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none"})
	assert.NilError(t, err)
	checkTestApp(t, "jarvis")

	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.Cloned, false)
	assert.Assert(t, strings.Contains(view.Check.Error, "no compose.yaml"), view.Check.Error)
	assert.Equal(t, *view.ComposeExample, GitComposeExample)
	_, err = os.Stat(filepath.Join(gitAppsDataRoot, "jarvis"))
	assert.Assert(t, os.IsNotExist(err), "the clone is removed")

	// the owner adds one: the next check clones, and the example goes
	pushTestCommit(t, work, "compose.yaml", testComposeFile)
	checkTestApp(t, "jarvis")
	view, err = GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.Cloned, true)
	assert.Assert(t, view.ComposeExample == nil)
	assert.Equal(t, view.Check.Error, "")
}

func TestAFolderInTheWayIsNotClonedInto(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, _ := newTestRemote(t)
	assert.NilError(t, os.MkdirAll(filepath.Join(gitAppsDataRoot, "jarvis"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(gitAppsDataRoot, "jarvis", "notes.txt"), []byte("mine"), 0o644))

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none"})
	assert.NilError(t, err)
	st := checkTestApp(t, "jarvis")
	assert.Assert(t, strings.Contains(st.Check.Error, "not empty"), st.Check.Error)

	notes, err := os.ReadFile(filepath.Join(gitAppsDataRoot, "jarvis", "notes.txt"))
	assert.NilError(t, err)
	assert.Equal(t, string(notes), "mine")

	// and removing the app leaves the folder that was there
	assert.NilError(t, DeleteGitApp(ctx, "jarvis"))
	_, err = os.Stat(filepath.Join(gitAppsDataRoot, "jarvis", "notes.txt"))
	assert.NilError(t, err)
}

func TestAnUnreachableRepositoryIsRecordedAndNothingElseChanges(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	head := gitShell(t, work, "git rev-parse HEAD")

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none"})
	assert.NilError(t, err)
	checkTestApp(t, "jarvis")

	remote := strings.TrimPrefix(url, "file://")
	assert.NilError(t, os.Rename(remote, remote+".gone"))

	st := checkTestApp(t, "jarvis")
	assert.Equal(t, gitAppStateOf(st), "unreachable")
	assert.Assert(t, st.Check.Error != "")
	assert.Equal(t, st.Check.RemoteCommit, head, "what was last seen stays")
	assert.Equal(t, st.Cloned, true)
}

// A stack started by hand in a folder that is a git work tree is a git app to adopt, and
// the first thing the owner does with it adopts it.
func TestAStackStartedByHandInAGitFolderIsAdoptedByTheFirstAction(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	head := gitShell(t, work, "git rev-parse HEAD")
	dir := filepath.Join(t.TempDir(), "jarvis")
	assert.NilError(t, git.Clone(ctx, url, "main", dir, git.Auth{}))
	fake.projects["jarvis"] = dir

	_, err := GetGitApp(ctx, "nextcloud")
	assert.ErrorIs(t, err, ErrGitAppNotFound)

	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.Origin, "adoptable")
	assert.Equal(t, view.Remote, url)
	assert.Assert(t, view.Deployed == nil)
	_, err = loadGitApp("jarvis")
	assert.ErrorIs(t, err, ErrGitAppNotFound, "looking adopts nothing")

	on := true
	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{AutoDeploy: &on})
	assert.NilError(t, err)
	assert.Equal(t, view.Origin, "adopted")
	assert.Equal(t, view.AutoDeploy, true)
	assert.Equal(t, view.Deployed.Commit, head)
	assert.Equal(t, view.History[0].Outcome, "adopted")
	assert.DeepEqual(t, fake.Calls(), []string{"tag running " + head[:12]})

	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, st.Deployed.Images, map[string]string{"web": "jarvis-web:git-" + head[:12]})
}

func TestAFolderInDetachedHeadOrWithoutARemoteIsShownButNotAdopted(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	head := gitShell(t, work, "git rev-parse HEAD")

	detached := filepath.Join(t.TempDir(), "detached")
	assert.NilError(t, git.Clone(ctx, url, "main", detached, git.Auth{}))
	gitShell(t, detached, "git checkout -q --detach "+head)
	fake.projects["detached"] = detached

	alone := filepath.Join(t.TempDir(), "alone")
	assert.NilError(t, git.Clone(ctx, url, "main", alone, git.Auth{}))
	gitShell(t, alone, "git remote remove origin")
	fake.projects["alone"] = alone

	view, err := GetGitApp(ctx, "detached")
	assert.NilError(t, err)
	assert.Equal(t, view.Branch, "")
	view, err = GetGitApp(ctx, "alone")
	assert.NilError(t, err)
	assert.Equal(t, view.Remote, "")

	on := true
	_, err = CheckGitApp(ctx, "detached")
	assertBadRequest(t, err, "detached HEAD")
	_, err = UpdateGitApp(ctx, "alone", GitAppChanges{AutoDeploy: &on})
	assertBadRequest(t, err, "no remote")

	_, err = loadGitApp("detached")
	assert.ErrorIs(t, err, ErrGitAppNotFound)
	_, err = loadGitApp("alone")
	assert.ErrorIs(t, err, ErrGitAppNotFound)
}

func TestTheAccessOfAGitAppChangesAndItsTokenIsNeverShown(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: "https://github.com/owner/jarvis.git", Access: "token", Token: "ghp_secret"})
	assert.NilError(t, err)
	info, err := os.Stat(gitAppFile("jarvis", ".token"))
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))

	empty := ""
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Token: &empty})
	assertBadRequest(t, err, "cannot be empty")

	other := "ghp_other"
	view, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{Token: &other})
	assert.NilError(t, err)
	assert.Equal(t, view.TokenSet, true)
	token, err := os.ReadFile(gitAppFile("jarvis", ".token"))
	assert.NilError(t, err)
	assert.Equal(t, string(token), other)

	none := "none"
	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Access: &none})
	assert.NilError(t, err)
	assert.Equal(t, view.TokenSet, false, "a token that is not used any more is forgotten")

	key := "key"
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Access: &key})
	assertBadRequest(t, err, "SSH URL")

	branch := "develop"
	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Branch: &branch})
	assert.NilError(t, err, "a branch changes until the app is cloned")
	assert.Equal(t, view.Branch, "develop")
}

// Removing is for an app no deployment succeeded for, whatever its history: what a failed
// first deployment left goes with it.
func TestRemovingAGitAppThatNeverRan(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, _ := newTestRemote(t)

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none"})
	assert.NilError(t, err)
	st := checkTestApp(t, "jarvis")
	st.History = []gitHistoryEntry{{Commit: st.Check.RemoteCommit, Outcome: gitOutcomeFailed, Images: map[string]string{"web": "jarvis-web:git-0123456789ab"}}}

	st.Deployed = &gitDeployment{Commit: st.Check.RemoteCommit}
	assert.NilError(t, saveGitApp(st))
	assert.ErrorIs(t, DeleteGitApp(ctx, "jarvis"), ErrGitAppDeployed)

	st.Deployed = nil
	assert.NilError(t, saveGitApp(st))
	assert.NilError(t, DeleteGitApp(ctx, "jarvis"))
	assert.DeepEqual(t, fake.Calls(), []string{"remove"})
	assert.DeepEqual(t, fake.removed, []string{"jarvis-web:git-0123456789ab"})
	_, err = os.Stat(st.Dir)
	assert.Assert(t, os.IsNotExist(err), "the clone goes")
	_, err = loadGitApp("jarvis")
	assert.ErrorIs(t, err, ErrGitAppNotFound)
}

// A name from a request is an app's name or nothing: never a path out of the state folder.
func TestAGitAppNameIsNeverAPath(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	outside := filepath.Join(filepath.Dir(gitAppsDir), "outside.json")
	assert.NilError(t, os.MkdirAll(gitAppsDir, 0o700))
	assert.NilError(t, os.WriteFile(outside, []byte("{}"), 0o600))

	_, err := GetGitApp(context.Background(), "../outside")
	assert.ErrorIs(t, err, ErrGitAppNotFound)
	assert.ErrorIs(t, DeleteGitApp(context.Background(), "../outside"), ErrGitAppNotFound)
	_, err = os.Stat(outside)
	assert.NilError(t, err, "the file beside the state folder is still there")
}
