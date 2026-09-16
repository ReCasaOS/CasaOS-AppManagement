package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

func folderHead(t *testing.T, dir string) string {
	t.Helper()

	info, err := git.Describe(context.Background(), dir)
	assert.NilError(t, err)

	return info.Head
}

// clonedTestApp is a created app, checked and cloned, not deployed yet.
func clonedTestApp(t *testing.T) (fake *fakeGitRuntime, work string) {
	t.Helper()

	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake = withFakeGitDocker(t)
	url, work := newTestRemote(t)

	_, err := CreateGitApp(context.Background(), GitAppRegistration{Name: "jarvis", URL: url, Access: "none"})
	assert.NilError(t, err)
	assert.Equal(t, checkTestApp(t, "jarvis").Cloned, true)

	return fake, work
}

// deployTestApp deploys the remote's latest by hand and waits for the deployment to end.
func deployTestApp(t *testing.T) *gitApp {
	t.Helper()

	_, err := DeployGitApp(context.Background(), "jarvis", "", nil)
	assert.NilError(t, err)

	return waitForGitApp(t, "jarvis")
}

// deployedTestApp is a created app whose first version runs, with its automatic rebuild
// set as asked.
func deployedTestApp(t *testing.T, autoDeploy bool) (fake *fakeGitRuntime, work, first string) {
	t.Helper()

	fake, work = clonedTestApp(t)
	first = gitShell(t, work, "git rev-parse HEAD")
	st := deployTestApp(t)
	assert.Equal(t, st.Deployed.Commit, first)

	st.AutoDeploy = autoDeploy
	assert.NilError(t, saveGitApp(st))
	fake.calls = nil

	return fake, work, first
}

func TestAFirstDeploymentWritesEnvBuildsAndStarts(t *testing.T) {
	fake, work := clonedTestApp(t)
	first := gitShell(t, work, "git rev-parse HEAD")
	env := "GREETING=hello\n"

	view, err := DeployGitApp(context.Background(), "jarvis", "", &env)
	assert.NilError(t, err)
	assert.Equal(t, view.Operation.Kind, gitOperationBuild)
	assert.Equal(t, view.Operation.Commit, first, "the commit is known as the deployment starts")
	assert.Equal(t, view.State, "building")

	st := waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + first[:12], "retag " + first[:12], "start " + first[:12]})
	assert.Assert(t, st.Operation == nil)
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, st.Deployed.Subject, "change compose.yaml")
	assert.DeepEqual(t, st.Deployed.Images, map[string]string{"web": "jarvis-web:git-" + first[:12]})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeDeployed)

	written, err := os.ReadFile(filepath.Join(st.Dir, ".env"))
	assert.NilError(t, err)
	assert.Equal(t, string(written), env)

	log, err := os.ReadFile(gitAppFile("jarvis", ".build.log"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(log), "building "+first[:12]))

	entries, err := os.ReadDir(filepath.Join(gitAppsDir, "work"))
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 0, "the build's worktree is gone")
}

func TestANewCommitIsBuiltThenSwitchedIn(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")

	st := deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Commit, second)
	assert.Equal(t, folderHead(t, st.Dir), second)
	assert.Equal(t, len(st.History), 2)
	assert.Equal(t, st.History[1].Commit, first)
}

// The running version is never touched by a build that fails.
func TestAFailedBuildChangesNothing(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")
	fake.buildErr = errors.New("RUN make: exit status 2")

	st := deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12]})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeBuildFailed)
	assert.Assert(t, strings.Contains(st.History[0].Reason, "exit status 2"))
	assert.DeepEqual(t, st.Attempted, []string{second})
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first, "the folder did not move")
	assert.Equal(t, gitAppStateOf(st), "build_failed")

	log, err := os.ReadFile(gitAppFile("jarvis", ".build.log"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(log), "exit status 2"))
}

func TestAVersionThatDoesNotStartIsRolledBack(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")
	fake.startErrs = []error{errors.New("container jarvis-web-1 of web exited with code 1")}

	st := deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{
		"build " + second[:12], "retag " + second[:12], "start " + second[:12],
		"retag " + first[:12], "start " + first[:12],
	})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeRolledBack)
	assert.Assert(t, strings.Contains(st.History[0].Reason, "exited with code 1"))
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first)
	assert.DeepEqual(t, st.Attempted, []string{second})
	assert.Equal(t, st.Blocked, false)
	assert.Equal(t, gitAppStateOf(st), "rolled_back")
}

func TestAFirstDeploymentThatFailsHasNothingToRollBackTo(t *testing.T) {
	fake, work := clonedTestApp(t)
	first := gitShell(t, work, "git rev-parse HEAD")
	fake.startErrs = []error{errors.New("container jarvis-web-1 of web is restarting")}

	st := deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{"build " + first[:12], "retag " + first[:12], "start " + first[:12], "stop"})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Assert(t, st.Deployed == nil)
	assert.Equal(t, st.Blocked, false)

	// .env is still accepted: no deployment has succeeded
	env := "GREETING=again\n"
	_, err := DeployGitApp(context.Background(), "jarvis", "", &env)
	assert.NilError(t, err)
	waitForGitApp(t, "jarvis")
}

func TestARollbackThatFailsBlocksEveryAutomaticAction(t *testing.T) {
	fake, work, first := deployedTestApp(t, true)
	pushTestCommit(t, work, "index.html", "v2")
	fake.startErrs = []error{errors.New("container exited with code 1"), errors.New("port 8080 is already allocated")}

	st := deployTestApp(t)

	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Assert(t, strings.Contains(st.History[0].Reason, "exited with code 1"))
	assert.Assert(t, strings.Contains(st.History[0].Reason, "already allocated"))
	assert.Equal(t, st.Blocked, true)
	assert.Equal(t, gitAppStateOf(st), "failed")

	// a third commit, with the automatic rebuild on: nothing happens while blocked
	pushTestCommit(t, work, "index.html", "v3")
	fake.calls = nil
	checkTestApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{})

	// a manual deployment that works lifts it
	st = deployTestApp(t)
	assert.Equal(t, st.Blocked, false)
	assert.Assert(t, st.Deployed.Commit != first)
}

// Adopting tags what the stack runs, so the first deployment CasaOS makes of it has a
// version to roll back to.
func TestAnAdoptedStackRollsBackToTheImagesItRanWhenAdopted(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	first := gitShell(t, work, "git rev-parse HEAD")
	dir := filepath.Join(t.TempDir(), "jarvis")
	assert.NilError(t, git.Clone(ctx, url, "main", dir, git.Auth{}))
	fake.projects["jarvis"] = dir
	second := pushTestCommit(t, work, "index.html", "v2")
	fake.startErrs = []error{errors.New("container exited with code 1")}

	st := deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{
		"tag running " + first[:12],
		"build " + second[:12], "retag " + second[:12], "start " + second[:12],
		"retag " + first[:12], "start " + first[:12],
	})
	assert.Equal(t, st.Origin, gitOriginAdopted)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeRolledBack)
	assert.Equal(t, st.History[1].Outcome, gitOutcomeAdopted)
}

func TestADeploymentOfAFolderInDetachedHeadSaysWhy(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	dir := filepath.Join(t.TempDir(), "jarvis")
	assert.NilError(t, git.Clone(ctx, url, "main", dir, git.Auth{}))
	gitShell(t, dir, "git checkout -q --detach "+gitShell(t, work, "git rev-parse HEAD"))
	fake.projects["jarvis"] = dir

	_, err := DeployGitApp(ctx, "jarvis", "", nil)
	assertBadRequest(t, err, "detached HEAD")
	assert.DeepEqual(t, fake.Calls(), []string{})
}

func TestARevertPausesTheAutomaticRebuild(t *testing.T) {
	fake, work, first := deployedTestApp(t, true)
	second := pushTestCommit(t, work, "index.html", "v2")
	checkTestApp(t, "jarvis")
	fake.calls = nil

	view, err := DeployGitApp(context.Background(), "jarvis", first, nil)
	assert.NilError(t, err)
	assert.Equal(t, view.State, "deploying")
	st := waitForGitApp(t, "jarvis")

	// nothing is built
	assert.DeepEqual(t, fake.Calls(), []string{"retag " + first[:12], "start " + first[:12]})
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first)
	assert.Equal(t, st.AutoPaused, true)

	// the check sees the branch ahead, and leaves it
	fake.calls = nil
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, gitNewCommits(st), true)
	assert.DeepEqual(t, fake.Calls(), []string{})

	// turning the switch on again resumes it
	on := true
	_, err = UpdateGitApp(context.Background(), "jarvis", GitAppChanges{AutoDeploy: &on})
	assert.NilError(t, err)
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Deployed.Commit, second)
}

// A check that finds a new commit ends, then the automatic rebuild starts: the owner sees
// the check end and the build begin.
func TestACheckStartsTheAutomaticRebuildOnceItHasEnded(t *testing.T) {
	fake, work, _ := deployedTestApp(t, true)
	second := pushTestCommit(t, work, "index.html", "v2")

	st := checkTestApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Commit, second)
	assert.Equal(t, st.AutoPaused, false)
}

func TestAnAttemptedCommitIsNotRetriedAutomatically(t *testing.T) {
	fake, work, _ := deployedTestApp(t, true)
	second := pushTestCommit(t, work, "index.html", "v2")
	fake.buildErr = errors.New("no space left on device")

	checkTestApp(t, "jarvis")
	fake.buildErr = nil

	checkTestApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12]})
}

// Every precondition refuses before anything moves; an automatic deployment records why.
func TestADeploymentRefusesWhatWouldBreakTheApp(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	ctx := context.Background()
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	env := "GREETING=hello\n"

	_, err = DeployGitApp(ctx, "jarvis", "", &env)
	assertBadRequest(t, err, "first deployment only")
	_, err = DeployGitApp(ctx, "jarvis", "4f5f60c", nil)
	assertBadRequest(t, err, "not a full commit hash")
	_, err = DeployGitApp(ctx, "jarvis", first, nil)
	assertBadRequest(t, err, "cannot be reverted to")

	assert.NilError(t, os.WriteFile(filepath.Join(st.Dir, "compose.yaml"), []byte("services: {}\n"), 0o644))
	_, err = DeployGitApp(ctx, "jarvis", "", nil)
	assertBadRequest(t, err, "tracked files were modified")

	st.AutoDeploy = true
	assert.NilError(t, saveGitApp(st))
	second := pushTestCommit(t, work, "index.html", "v2")
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Assert(t, strings.Contains(st.History[0].Reason, "tracked files were modified"))
	assert.DeepEqual(t, st.Attempted, []string{second})
	assert.DeepEqual(t, fake.Calls(), []string{})
}

// A rebuild of what runs would tag the running version's images anew, and leave a rollback
// nothing to go back to.
func TestTheDeployedCommitIsNotBuiltAgain(t *testing.T) {
	fake, _, _ := deployedTestApp(t, false)

	_, err := DeployGitApp(context.Background(), "jarvis", "", nil)
	assertBadRequest(t, err, "already deployed")
	assert.DeepEqual(t, fake.Calls(), []string{})
}

// A rollback that failed blocks the app. Once the broken commit is off the branch, "Fetch and
// rebuild" finds the deployed commit there and deploys that version again, from its images.
func TestABlockedAppIsRepairedFromTheImagesOfItsDeployedVersion(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	pushTestCommit(t, work, "index.html", "v2")
	fake.startErrs = []error{errors.New("container exited with code 1"), errors.New("port 8080 is already allocated")}
	st := deployTestApp(t)
	assert.Equal(t, st.Blocked, true)
	gitShell(t, work, "git push -q -f origin "+first+":main")
	st.AutoPaused = true
	assert.NilError(t, saveGitApp(st))
	fake.calls = nil

	st = deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{"retag " + first[:12], "start " + first[:12]})
	assert.Equal(t, st.Blocked, false)
	assert.Equal(t, st.AutoPaused, true, "a repair leaves the automatic rebuild as it was")
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, gitAppStateOf(st), "idle")
}

// A revert AppManagement did not live to finish leaves the folder on the older commit and the
// newer version deployed: asked for again, the deployed commit is deployed from its images, and
// the folder is put back on it.
func TestAFolderAnInterruptedRevertMovedIsPutBackOnTheDeployedCommit(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")
	st := deployTestApp(t)
	gitShell(t, st.Dir, "git reset -q --keep "+first)
	fake.calls = nil

	st = deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{"retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, folderHead(t, st.Dir), second)
	assert.Equal(t, st.Deployed.Commit, second)

	// the deployed commit given as such is the same repair, not a revert that pauses anything
	gitShell(t, st.Dir, "git reset -q --keep "+first)
	fake.calls = nil

	_, err := DeployGitApp(context.Background(), "jarvis", second, nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{"retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, folderHead(t, st.Dir), second)
	assert.Equal(t, st.AutoPaused, false)
}

// With the deployed version's images gone, a repair has nothing to start from: it builds that
// version, whose images nothing runs any more.
func TestARepairWhoseImagesAreGoneBuildsThem(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")
	st := deployTestApp(t)
	gitShell(t, st.Dir, "git reset -q --keep "+first)
	fake.missing["jarvis-web:git-"+second[:12]] = true
	fake.calls = nil

	st = deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, folderHead(t, st.Dir), second)
	assert.Equal(t, st.Deployed.Commit, second)
}

// A repair puts the folder back on the deployed commit: the commits made there, which that
// commit does not contain, would be dropped, and it is refused.
func TestARepairOverCommitsOfTheFolderIsRefused(t *testing.T) {
	fake, _, _ := deployedTestApp(t, false)
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	mine := gitShell(t, st.Dir, "echo mine > notes.txt && git add notes.txt && git commit -qm mine && git rev-parse HEAD")

	_, err = DeployGitApp(context.Background(), "jarvis", "", nil)

	assertBadRequest(t, err, "push the commits made there")
	assert.DeepEqual(t, fake.Calls(), []string{})
	assert.Equal(t, folderHead(t, st.Dir), mine)
}

// A folder with no commit any more, as `git init` leaves one, is refused before anything reads
// the commit it is on.
func TestADeploymentOfAFolderWithNoCommitSaysWhy(t *testing.T) {
	fake, _, first := deployedTestApp(t, false)
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	gitShell(t, st.Dir, "git update-ref -d refs/heads/main && git read-tree --empty")

	for _, commit := range []string{first, ""} {
		_, err = DeployGitApp(context.Background(), "jarvis", commit, nil)
		assertBadRequest(t, err, "no commit")
	}
	assert.DeepEqual(t, fake.Calls(), []string{})
}

// Commits made in the folder by hand are never rolled back over: the deployment stops
// before building, and the folder keeps them.
func TestAFolderWithCommitsOfItsOwnIsLeftAsItIs(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	mine := gitShell(t, st.Dir, "echo mine > notes.txt && git add notes.txt && git commit -qm mine && git rev-parse HEAD")
	second := pushTestCommit(t, work, "index.html", "v2")

	st = deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{})
	assert.Equal(t, st.History[0].Commit, second)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Assert(t, strings.Contains(st.History[0].Reason, "push the commits made there"), st.History[0].Reason)
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), mine)
	notes, err := os.ReadFile(filepath.Join(st.Dir, "notes.txt"))
	assert.NilError(t, err)
	assert.Equal(t, string(notes), "mine\n")
}

// A move git refuses leaves the running version as it was: there is nothing to roll back.
func TestAMoveGitRefusesChangesNothing(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	// an untracked file where the new commit puts a tracked one: the fast-forward stops
	assert.NilError(t, os.WriteFile(filepath.Join(st.Dir, "index.html"), []byte("mine"), 0o644))

	st = deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12]})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first)
	mine, err := os.ReadFile(filepath.Join(st.Dir, "index.html"))
	assert.NilError(t, err)
	assert.Equal(t, string(mine), "mine")
}

func TestARevertOverCommitsOfTheFolderIsRefused(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	pushTestCommit(t, work, "index.html", "v2")
	st := deployTestApp(t)
	gitShell(t, st.Dir, "echo mine > notes.txt && git add notes.txt && git commit -qm mine")
	fake.calls = nil

	_, err := DeployGitApp(context.Background(), "jarvis", first, nil)
	assertBadRequest(t, err, "a revert would drop the commits made there")
	assert.DeepEqual(t, fake.Calls(), []string{})
}

// A revert that failed leaves an entry for its commit newer than the one of the version that
// ran: the history still offers that version, and a revert to it is accepted.
func TestAVersionARevertFailedToReachCanStillBeRevertedTo(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")
	deployTestApp(t)
	fake.startErrs = []error{errors.New("container exited with code 1")}
	_, err := DeployGitApp(context.Background(), "jarvis", first, nil)
	assert.NilError(t, err)
	st := waitForGitApp(t, "jarvis")
	for i, want := range []string{first + " rolled_back", second + " deployed", first + " deployed"} {
		assert.Equal(t, st.History[i].Commit+" "+st.History[i].Outcome, want)
	}
	assert.Equal(t, newGitAppView(context.Background(), st).History[2].Revertable, true)
	fake.calls = nil

	_, err = DeployGitApp(context.Background(), "jarvis", first, nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{"retag " + first[:12], "start " + first[:12]})
	assert.Equal(t, st.Deployed.Commit, first)
}

func TestATrackedEnvIsNeverWritten(t *testing.T) {
	_, work := clonedTestApp(t)
	pushTestCommit(t, work, ".env", "GREETING=tracked\n")
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	_, err = git.Fetch(context.Background(), st.Dir, st.Remote, st.Branch, git.Auth{})
	assert.NilError(t, err)
	assert.NilError(t, git.FastForward(context.Background(), st.Dir, gitShell(t, work, "git rev-parse HEAD")))

	env := "GREETING=mine\n"
	_, err = DeployGitApp(context.Background(), "jarvis", "", &env)
	assertBadRequest(t, err, "tracks .env")
}

func TestTheGuardRefusesAnOverlap(t *testing.T) {
	deployedTestApp(t, false)
	holding(t, "jarvis")
	ctx := context.Background()
	on := true

	_, err := DeployGitApp(ctx, "jarvis", "", nil)
	assertBusy(t, err)
	_, err = CheckGitApp(ctx, "jarvis")
	assertBusy(t, err)
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{AutoDeploy: &on})
	assertBusy(t, err)
	assertBusy(t, DeleteGitApp(ctx, "jarvis"))
}

// The check's hold passes to the deployment it starts under that deployment's name: what is
// refused meanwhile is told a deployment runs, never a check.
func TestTheGuardRefusesInTheNameOfTheDeploymentACheckStarted(t *testing.T) {
	fake, work, _ := deployedTestApp(t, true)
	second := pushTestCommit(t, work, "index.html", "v2")
	fake.gate = make(chan struct{})

	_, err := CheckGitApp(context.Background(), "jarvis")
	assert.NilError(t, err)
	<-fake.gate // the automatic deployment is building

	_, err = Begin("jarvis", "update")
	fake.gate <- struct{}{}

	assert.Error(t, err, "`jarvis` is busy: deploy in progress")
	assert.Equal(t, waitForGitApp(t, "jarvis").Deployed.Commit, second)
}

func TestTheBuildLogKeepsItsLastMegabyte(t *testing.T) {
	gitAppsIn(t)
	assert.NilError(t, os.MkdirAll(gitAppsDir, 0o700))
	file, err := os.OpenFile(gitAppFile("jarvis", ".build.log"), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	assert.NilError(t, err)
	out := &gitBuildLog{ctx: context.Background(), file: file, lastSent: time.Now().Add(time.Hour)}

	line := strings.Repeat("x", 1023) + "\n"
	for i := 0; i < 3000; i++ {
		_, err := out.Write([]byte(line))
		assert.NilError(t, err)
	}
	_, err = out.Write([]byte("the last line\n"))
	assert.NilError(t, err)
	assert.NilError(t, out.Close())

	info, err := os.Stat(gitAppFile("jarvis", ".build.log"))
	assert.NilError(t, err)
	assert.Assert(t, info.Size() <= 2*gitBuildLogCap, info.Size())
	assert.Assert(t, strings.HasSuffix(readGitBuildLogTail("jarvis"), "x\nthe last line\n"))
}
