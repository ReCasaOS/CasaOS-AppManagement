package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

// deployedTaggedTestApp is taggedTestApp deployed by hand at v1.0.0, with its automatic
// deployment set as asked.
func deployedTaggedTestApp(t *testing.T, autoDeploy bool) (fake *fakeGitRuntime, work, first string) {
	t.Helper()

	fake, work, first = taggedTestApp(t)
	st := deployTestApp(t)
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, st.Deployed.Tag, "v1.0.0")

	st.AutoDeploy = autoDeploy
	assert.NilError(t, saveGitApp(st))
	fake.calls = nil

	return fake, work, first
}

func TestAnyEligibleTagIsDeployedByHand(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, true)
	ctx := context.Background()
	second := pushTestCommit(t, work, "index.html", "v2")
	pushTestTag(t, work, "v1.1.0")
	// a fix of 1.0 on a line of its own: tags do not form a line
	hotfix := gitShell(t, work, "git checkout -q -b hotfix "+first+" && echo fix > fix.txt && git add fix.txt && git commit -qm fix && git tag -a -m v1.0.1 v1.0.1 && git push -q origin v1.0.1 && git rev-parse HEAD && git checkout -q main")

	// the highest, when no tag is given
	view, err := DeployGitApp(ctx, "jarvis", "", nil)
	assert.NilError(t, err)
	assert.Equal(t, view.Operation.Tag, "v1.1.0", "the tag is known as the deployment starts")
	st := waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Commit, second)
	assert.Equal(t, st.Deployed.Tag, "v1.1.0")
	assert.Equal(t, st.History[0].Tag, "v1.1.0")
	assert.Equal(t, st.AutoPaused, false)

	// an older tag that never ran is built from its own line, and pauses the automatic deployment
	fake.calls = nil
	_, err = DeployGitAppTag(ctx, "jarvis", "v1.0.1", nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + hotfix[:12], "retag " + hotfix[:12], "start " + hotfix[:12]})
	assert.Equal(t, st.Deployed.Tag, "v1.0.1")
	assert.Equal(t, folderHead(t, st.Dir), hotfix)
	assert.Equal(t, st.AutoPaused, true, "or the next check would upgrade it again")

	// a tag whose commit ran is a revert: nothing is built
	fake.calls = nil
	_, err = DeployGitAppTag(ctx, "jarvis", "v1.1.0", nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Commit, second)
	assert.Equal(t, st.Deployed.Tag, "v1.1.0")

	// a commit of the history, given as such, runs again under the tag it ran as
	fake.calls = nil
	_, err = DeployGitApp(ctx, "jarvis", hotfix, nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"retag " + hotfix[:12], "start " + hotfix[:12]})
	assert.Equal(t, st.Deployed.Tag, "v1.0.1")

	// first left the history: a commit no version of the history ran is refused
	_, err = DeployGitApp(ctx, "jarvis", first, nil)
	assertBadRequest(t, err, "no version of the history that ran")
}

func TestADeploymentOfATagRefusesWhatItCannotDo(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, false)
	ctx := context.Background()
	pushTestCommit(t, work, "index.html", "v2")
	gitShell(t, work, "git tag -a -m rc v1.1.0-rc.1 && git tag latest && git push -q origin --tags")

	// what runs, by its tag, as the highest tag, or by its commit: nothing new to build
	_, err := DeployGitAppTag(ctx, "jarvis", "v1.0.0", nil)
	assertBadRequest(t, err, "is already deployed")
	_, err = DeployGitApp(ctx, "jarvis", "", nil)
	assertBadRequest(t, err, "is already deployed")
	_, err = DeployGitApp(ctx, "jarvis", first, nil)
	assertBadRequest(t, err, "is already deployed")

	for _, tag := range []string{"v9.9.9", "v1.1.0-rc.1", "latest", "-v1.0.0"} {
		_, err := DeployGitAppTag(ctx, "jarvis", tag, nil)
		assertBadRequest(t, err, "not one of the eligible tags")
	}

	pattern := "v2.*"
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)
	_, err = DeployGitApp(ctx, "jarvis", "", nil)
	assertBadRequest(t, err, "no tag matches (pattern `v2.*`, pre-releases excluded)")

	// a folder put on a branch by hand
	pattern = ""
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	gitShell(t, st.Dir, "git checkout -q -b mine")
	_, err = DeployGitApp(ctx, "jarvis", "", nil)
	assertBadRequest(t, err, "is on the branch mine")

	assert.DeepEqual(t, fake.Calls(), []string{})
}

func TestABranchAppIsDeployedByCommitNotByTag(t *testing.T) {
	fake, _, _ := deployedTestApp(t, false)

	_, err := DeployGitAppTag(context.Background(), "jarvis", "v1.0.0", nil)

	assertBadRequest(t, err, "follows a branch")
	assert.DeepEqual(t, fake.Calls(), []string{})
}

// A deployment builds what its check or its request saw: a tag moved in between is refused,
// and the commit is not tried again automatically.
func TestATagThatMovedSinceTheCheckIsRefused(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, false)
	ctx := context.Background()
	second := pushTestCommit(t, work, "index.html", "v2")
	pushTestTag(t, work, "v1.1.0")
	st := checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.1.0")
	assert.Equal(t, st.Check.RemoteCommit, second)
	moved := pushTestCommit(t, work, "index.html", "v3")
	gitShell(t, work, "git tag -f -a -m v1.1.0 v1.1.0 >/dev/null && git push -q -f origin v1.1.0")

	st.AutoDeploy = true
	assert.NilError(t, saveGitApp(st))
	end, err := Begin("jarvis", gitOperationCheck)
	assert.NilError(t, err)
	deployGitAppAutomatically(ctx, "jarvis", end)
	st = waitForGitApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Equal(t, st.History[0].Tag, "v1.1.0")
	assert.Assert(t, strings.Contains(st.History[0].Reason, "moved since the check"), st.History[0].Reason)
	assert.Assert(t, strings.Contains(st.History[0].Reason, moved[:12]), st.History[0].Reason)
	assert.DeepEqual(t, st.Attempted, []string{second})
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first)

	// by hand the same: the request found v1.1.0 on second, and the tag moved before the
	// fetch. This is what the deployment DeployGitAppTag starts runs, holding the app.
	end, err = Begin("jarvis", gitOperationDeploy)
	assert.NilError(t, err)
	st.Operation = &gitOperation{Kind: gitOperationBuild, Commit: second, Tag: "v1.1.0", StartedAt: time.Now().UTC()}
	assert.NilError(t, saveGitApp(st))
	runGitDeploy(ctx, st, second, "v1.1.0", false, gitTriggerManual)
	end()
	st = waitForGitApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Equal(t, st.History[0].Commit, second)
	assert.Assert(t, strings.Contains(st.History[0].Reason, "moved since the check"), st.History[0].Reason)
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first)
}

// A repair rebuilds the running version given by hand, even once it left the history and
// its tag moved on the remote: the folder holds its commit, and no tag is fetched.
func TestARepairOfAnAppThatFollowsTagsNeedsNeitherItsHistoryNorItsTag(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, false)
	pushTestCommit(t, work, "index.html", "v2")
	gitShell(t, work, "git tag -f -a -m v1.0.0 v1.0.0 >/dev/null && git push -q -f origin v1.0.0")
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	// failed attempts pushed it out of the history, a rollback failed, and its images went
	st.History, st.Blocked = []gitHistoryEntry{}, true
	assert.NilError(t, saveGitApp(st))
	fake.missing["jarvis-web:git-"+first[:12]] = true

	_, err = DeployGitApp(context.Background(), "jarvis", first, nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{"build " + first[:12], "retag " + first[:12], "start " + first[:12]})
	assert.Equal(t, st.Blocked, false)
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, st.Deployed.Tag, "v1.0.0")
	assert.Equal(t, st.History[0].Outcome, gitOutcomeDeployed)
}

func TestAnInterruptedDeploymentOfATagKeepsItsTag(t *testing.T) {
	_, work, _ := deployedTaggedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	st.Operation = &gitOperation{Kind: gitOperationBuild, Commit: second, Tag: "v1.1.0", StartedAt: time.Now().UTC()}
	assert.NilError(t, saveGitApp(st))

	RecoverGitApps(context.Background())

	st, err = loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeInterrupted)
	assert.Equal(t, st.History[0].Commit, second)
	assert.Equal(t, st.History[0].Tag, "v1.1.0")
}
