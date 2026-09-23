package service

import (
	"context"
	"sort"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"gotest.tools/v3/assert"
)

func TestTheAutomaticDeploymentOfTagsOnlyEverGoesUp(t *testing.T) {
	ready := func(follow, deployedTag, remoteTag string) *gitApp {
		return &gitApp{
			Follow: follow, AutoDeploy: true, Attempted: []string{},
			Deployed: &gitDeployment{Commit: "a", Tag: deployedTag},
			Check:    &gitCheck{RemoteCommit: "b", RemoteTag: remoteTag},
		}
	}
	refused := func(change func(st *gitApp)) bool {
		st := ready(gitFollowTags, "v1.0.0", "v1.1.0")
		change(st)
		return !gitMayDeployAutomatically(st)
	}

	assert.Assert(t, gitMayDeployAutomatically(ready(gitFollowTags, "v1.0.0", "v1.1.0")))
	assert.Assert(t, !gitMayDeployAutomatically(ready(gitFollowTags, "v1.1.0", "v1.0.0")), "a lower tag: the higher one deleted, or the pattern narrowed")
	assert.Assert(t, !gitMayDeployAutomatically(ready(gitFollowTags, "v1.1.0", "v1.1.0")), "the deployed tag, moved")
	assert.Assert(t, !gitMayDeployAutomatically(ready(gitFollowTags, "", "v1.1.0")), "no deployment in this mode yet")
	assert.Assert(t, gitMayDeployAutomatically(ready(gitFollowBranch, "", "")))
	assert.Assert(t, !gitMayDeployAutomatically(ready(gitFollowBranch, "v1.1.0", "")), "no deployment on the branch yet")

	assert.Assert(t, refused(func(st *gitApp) { st.AutoDeploy = false }), "off")
	assert.Assert(t, refused(func(st *gitApp) { st.AutoPaused = true }), "paused")
	assert.Assert(t, refused(func(st *gitApp) { st.Blocked = true }), "blocked")
	assert.Assert(t, refused(func(st *gitApp) { st.Check.Error = "offline" }), "a failed check")
	assert.Assert(t, refused(func(st *gitApp) { st.Attempted = []string{"b"} }), "tried already")
	assert.Assert(t, refused(func(st *gitApp) { st.Check.RemoteCommit = "a" }), "what runs")
	assert.Assert(t, refused(func(st *gitApp) { st.Deployed = nil }), "never deployed")
}

func TestAnAppThatFollowsTagsIsUpgradedAutomaticallyAndNeverDowngraded(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, true)
	ctx := context.Background()

	// a higher tag is deployed
	second := pushTestCommit(t, work, "index.html", "v2")
	pushTestTag(t, work, "v1.1.0")
	st := checkTestApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Tag, "v1.1.0")

	// a pre-release is not
	fake.calls = nil
	pushTestCommit(t, work, "index.html", "v3")
	pushTestTag(t, work, "v1.2.0-rc.1")
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.1.0")

	// nor the lower tag a narrowed pattern leaves
	pattern := "v1.0.*"
	_, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.0.0")
	assert.Equal(t, gitNewCommits(st), false)
	pattern = ""
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)

	// a moved tag is reported, never redeployed by itself
	moved := pushTestCommit(t, work, "index.html", "v4")
	gitShell(t, work, "git tag -f -a -m v1.1.0 v1.1.0 >/dev/null && git push -q -f origin v1.1.0")
	checkTestApp(t, "jarvis")
	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.Check.RemoteCommit, moved)
	assert.Equal(t, view.Check.TagMoved, true)
	assert.Equal(t, view.NewCommits, false)
	assert.DeepEqual(t, fake.Calls(), []string{})

	// by hand, the moved tag builds its new commit
	_, err = DeployGitAppTag(ctx, "jarvis", "v1.1.0", nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + moved[:12], "retag " + moved[:12], "start " + moved[:12]})
	assert.Equal(t, st.AutoPaused, false, "the same version: nothing to pause")

	// a deleted tag brings nothing back
	fake.calls = nil
	gitShell(t, work, "git push -q origin :refs/tags/v1.1.0")
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.0.0")
	assert.Equal(t, st.Check.RemoteCommit, first)
	assert.DeepEqual(t, fake.Calls(), []string{})
	assert.Equal(t, st.Deployed.Commit, moved)
	assert.Equal(t, st.Deployed.Tag, "v1.1.0")
}

// After a change of mode nothing deploys by itself until one deployment by hand in the new
// mode, which may be the running commit under its tag.
func TestAfterAChangeOfModeTheFirstDeploymentIsByHand(t *testing.T) {
	fake, work, first := deployedTestApp(t, true)
	ctx := context.Background()
	pushTestTag(t, work, "v1.0.0")
	tags, branch := gitFollowTags, gitFollowBranch

	_, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	second := pushTestCommit(t, work, "index.html", "v2")
	pushTestTag(t, work, "v1.1.0")
	st := checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.1.0")
	assert.DeepEqual(t, fake.Calls(), []string{})
	assert.Equal(t, gitNewCommits(st), true, "a version to deploy by hand")

	// the running commit, deployed by hand under its tag, starts again from its images
	_, err = DeployGitAppTag(ctx, "jarvis", "v1.0.0", nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"retag " + first[:12], "start " + first[:12]})
	assert.Equal(t, st.Deployed.Tag, "v1.0.0")

	// and automatic deployment resumes
	fake.calls = nil
	st = checkTestApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Tag, "v1.1.0")

	// back on the branch: nothing deploys by itself until a deployment by hand there
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch})
	assert.NilError(t, err)
	third := pushTestCommit(t, work, "index.html", "v3")
	fake.calls = nil
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteCommit, third)
	assert.DeepEqual(t, fake.Calls(), []string{})

	st = deployTestApp(t)
	assert.Equal(t, st.Deployed.Commit, third)
	assert.Equal(t, st.Deployed.Tag, "")
	fourth := pushTestCommit(t, work, "index.html", "v4")
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Deployed.Commit, fourth, "and automatic deployment resumes")

	// on tags again, the first deployment by hand goes back to a version that ran: a revert,
	// which pauses nothing, and automatic deployment resumes after it
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	fake.calls = nil
	_, err = DeployGitAppTag(ctx, "jarvis", "v1.1.0", nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Tag, "v1.1.0")
	assert.Equal(t, st.AutoPaused, false)
	fifth := pushTestCommit(t, work, "index.html", "v5")
	pushTestTag(t, work, "v1.2.0")
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Deployed.Commit, fifth)
	assert.Equal(t, st.Deployed.Tag, "v1.2.0")
}

// Back on a branch after a tag of another line ran, the first deployment by hand moves the
// folder to the branch's head, which does not descend from that tag, and automatic
// deployment resumes after it. The running version given by hand stays what its tag made it.
func TestBackOnABranchAfterATagOfAnotherLineTheBranchIsFollowedAgain(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, true)
	ctx := context.Background()
	branch := gitFollowBranch

	// a fix of 1.0 on a line of its own, higher: deployed by itself
	hotfix := gitShell(t, work, "git checkout -q -b hotfix "+first+" && echo fix > fix.txt && git add fix.txt && git commit -qm fix && git tag -a -m v1.0.1 v1.0.1 && git push -q origin v1.0.1 && git rev-parse HEAD && git checkout -q main")
	st := checkTestApp(t, "jarvis")
	assert.Equal(t, st.Deployed.Commit, hotfix)
	assert.Equal(t, st.Deployed.Tag, "v1.0.1")

	_, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch})
	assert.NilError(t, err)
	second := pushTestCommit(t, work, "index.html", "v2")
	fake.calls = nil
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteCommit, second)
	// nothing deploys by itself after a change of mode
	assert.DeepEqual(t, fake.Calls(), []string{})

	// the running version given by hand deploys nothing, and keeps its tag
	_, err = DeployGitApp(ctx, "jarvis", hotfix, nil)
	assertBadRequest(t, err, "it is the running version")
	st, err = loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, st.Deployed.Tag, "v1.0.1")

	// the branch's head, by hand: built and moved to, though it does not descend from the fix
	st = deployTestApp(t)
	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Commit, second)
	assert.Equal(t, st.Deployed.Tag, "")
	assert.Equal(t, st.AutoPaused, false)
	assert.Equal(t, folderHead(t, st.Dir), second)

	// and automatic deployment resumes on the branch
	fake.calls = nil
	third := pushTestCommit(t, work, "index.html", "v3")
	st = checkTestApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + third[:12], "retag " + third[:12], "start " + third[:12]})
	assert.Equal(t, st.Deployed.Commit, third)
}

// A revert goes back to a commit whose tag is gone: the folder keeps a ref for every version
// the history offers, and a commit git no longer holds is offered no more.
func TestTheCommitsOfTheHistoryAreKeptWhateverTheTagsDo(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, false)
	ctx := context.Background()
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	kept := func() []string {
		t.Helper()
		commits, err := git.KeptCommits(ctx, st.Dir)
		assert.NilError(t, err)
		sort.Strings(commits)
		return commits
	}
	sorted := func(commits ...string) []string {
		sort.Strings(commits)
		return commits
	}
	// what git collects once the last fetch, the last move and every reflog are forgotten:
	// whatever no ref names
	collect := func() {
		t.Helper()
		gitShell(t, st.Dir, "rm -f .git/FETCH_HEAD .git/ORIG_HEAD && git reflog expire --expire=now --all && git gc -q --prune=now")
	}
	assert.DeepEqual(t, kept(), []string{first})

	// a fix of 1.0 on a line of its own, then 1.1 on the main line
	hotfix := gitShell(t, work, "git checkout -q -b hotfix "+first+" && echo fix > fix.txt && git add fix.txt && git commit -qm fix && git tag -a -m v1.0.1 v1.0.1 && git push -q origin v1.0.1 && git rev-parse HEAD && git checkout -q main")
	_, err = DeployGitAppTag(ctx, "jarvis", "v1.0.1", nil)
	assert.NilError(t, err)
	waitForGitApp(t, "jarvis")
	second := pushTestCommit(t, work, "index.html", "v2")
	pushTestTag(t, work, "v1.1.0")
	deployTestApp(t)
	assert.DeepEqual(t, kept(), sorted(first, hotfix, second))

	// the fix's tag deleted: nothing in the folder names its commit but the kept ref
	gitShell(t, work, "git push -q origin :refs/tags/v1.0.1")
	collect()
	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.History[1].Commit, hotfix)
	assert.Equal(t, view.History[1].Revertable, true)

	fake.calls = nil
	_, err = DeployGitApp(ctx, "jarvis", hotfix, nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"retag " + hotfix[:12], "start " + hotfix[:12]})
	assert.Equal(t, folderHead(t, st.Dir), hotfix)
	// first left the history with this deployment, and its ref with it
	assert.DeepEqual(t, kept(), sorted(hotfix, second))

	// a commit git no longer holds is no version to revert to
	assert.NilError(t, git.DropKeptCommit(ctx, st.Dir, second))
	collect()
	view, err = GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.History[1].Commit, second)
	assert.Equal(t, view.History[1].Revertable, false)
	_, err = DeployGitApp(ctx, "jarvis", second, nil)
	assertBadRequest(t, err, "cannot be reverted to")
}
