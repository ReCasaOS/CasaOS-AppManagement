package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"gotest.tools/v3/assert"
)

func TestAnAppThatFollowsTagsIsRegisteredWithoutABranch(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	register := func(r GitAppRegistration) error {
		_, err := CreateGitApp(ctx, r)
		return err
	}
	url := "https://github.com/owner/jarvis.git"

	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: "tag"}), "follow is branch or tags")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: gitFollowTags, Branch: "main"}), "has no branch")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: gitFollowTags, TagPattern: "v2.["}), "tag pattern")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: url, Access: "none", TagPattern: strings.Repeat("v", 101)}), "tag pattern")

	view, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: gitFollowTags, TagPattern: "v2.*", Prereleases: true})
	assert.NilError(t, err)
	assert.Equal(t, view.Follow, gitFollowTags)
	assert.Equal(t, view.Branch, "")
	assert.Equal(t, view.TagPattern, "v2.*")
	assert.Equal(t, view.Prereleases, true)

	// not cloned yet: the mode and the branch change freely
	view, err = CreateGitApp(ctx, GitAppRegistration{Name: "other", URL: url, Access: "none", Branch: "develop"})
	assert.NilError(t, err)
	assert.Equal(t, view.Follow, gitFollowBranch)
	tags, branch, main := gitFollowTags, gitFollowBranch, "main"
	view, err = UpdateGitApp(ctx, "other", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	assert.Equal(t, view.Branch, "")
	view, err = UpdateGitApp(ctx, "other", GitAppChanges{Follow: &branch, Branch: &main})
	assert.NilError(t, err)
	assert.Equal(t, view.Follow, gitFollowBranch)
	assert.Equal(t, view.Branch, "main")
}

// A cloned app's folder takes the shape its mode deploys from, and none of its files moves:
// detached to follow tags, on the branch to follow one.
func TestTheModeOfAClonedAppChangesAndItsFolderFollows(t *testing.T) {
	clonedTestApp(t)
	ctx := context.Background()
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	head := folderHead(t, st.Dir)
	describe := func() git.Info {
		t.Helper()
		info, err := git.Describe(ctx, st.Dir)
		assert.NilError(t, err)
		return info
	}
	tags, branch, main, develop, pattern, on := gitFollowTags, gitFollowBranch, "main", "develop", "v2.*", true

	view, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	assert.Equal(t, view.Follow, gitFollowTags)
	assert.Equal(t, view.Branch, "")
	info := describe()
	assert.Equal(t, info.Branch, "", "detached")
	assert.Equal(t, info.Head, head, "on the commit it was on")

	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern, Prereleases: &on})
	assert.NilError(t, err)
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Branch: &main})
	assertBadRequest(t, err, "has no branch")

	// back on a branch, the remote's default, with the tag filter kept
	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch})
	assert.NilError(t, err)
	assert.Equal(t, view.Follow, gitFollowBranch)
	assert.Equal(t, view.Branch, "main")
	assert.Equal(t, view.TagPattern, "v2.*")
	assert.Equal(t, view.Prereleases, true)
	info = describe()
	assert.Equal(t, info.Branch, "main")
	assert.Equal(t, info.Head, head)

	// the branch of a cloned app changes with its mode only
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Branch: &develop})
	assertBadRequest(t, err, "check another branch out there instead")
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch, Branch: &develop})
	assert.NilError(t, err)
	assert.Equal(t, view.Branch, "develop")
	assert.Equal(t, describe().Branch, "develop")

	bad, badPattern := "tag", "["
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &bad})
	assertBadRequest(t, err, "follow is branch or tags")
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &badPattern})
	assertBadRequest(t, err, "tag pattern")
}

// A check of one mode says nothing about the other: a change of mode forgets the last check, in
// both directions, and the app is not checked yet in its new mode. A change that keeps the mode
// keeps it.
func TestAChangeOfModeForgetsTheLastCheck(t *testing.T) {
	_, work := clonedTestApp(t)
	ctx := context.Background()
	pushTestTag(t, work, "v1.0.0")
	tags, branch := gitFollowTags, gitFollowBranch

	view, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch})
	assert.NilError(t, err)
	assert.Assert(t, view.Check != nil, "the same mode")

	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	assert.Assert(t, view.Check == nil, "a branch check, after a switch to tags")
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Assert(t, st.Check == nil)

	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.0.0")
	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch})
	assert.NilError(t, err)
	assert.Assert(t, view.Check == nil, "a tag check, after a switch to a branch")
	assert.Equal(t, view.NewCommits, false)
}

// Back on a branch, a local branch holding commits the folder is not on is refused: putting the
// folder on it would reset it and drop them. Once the owner checks it out there, the folder goes
// on it as it is.
func TestBackOnABranchThatHoldsOtherCommitsIsRefused(t *testing.T) {
	clonedTestApp(t)
	ctx := context.Background()
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	tags, branch, main := gitFollowTags, gitFollowBranch, "main"
	// a commit of the owner's on the folder's branch, as an adopted folder may hold, then the
	// detached folder moved back, as a deployment of a tag moves it
	mine := gitShell(t, st.Dir, "echo mine > notes.txt && git add notes.txt && git commit -qm mine && git rev-parse HEAD")
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	gitShell(t, st.Dir, "git checkout -q --detach HEAD~1")

	for _, changes := range []GitAppChanges{{Follow: &branch}, {Follow: &branch, Branch: &main}} {
		_, err = UpdateGitApp(ctx, "jarvis", changes)
		assertBadRequest(t, err, "main holds commits the folder is not on: check it out there first")
	}
	assert.Equal(t, gitShell(t, st.Dir, "git rev-parse refs/heads/main"), mine)
	info, err := git.Describe(ctx, st.Dir)
	assert.NilError(t, err)
	assert.Equal(t, info.Branch, "", "still detached")
	after, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, after.Follow, gitFollowTags)

	gitShell(t, st.Dir, "git checkout -q main")
	view, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch})
	assert.NilError(t, err)
	assert.Equal(t, view.Branch, "main")
	assert.Equal(t, folderHead(t, st.Dir), mine)
}

// Back on a branch without one, the remote's default is read with the access the request gives,
// before any access file is written or removed: a remote that cannot be read leaves the token that
// was kept, and the app as it was.
func TestAChangeOfModeThatCannotReadTheRemoteLeavesTheAccessAsItWas(t *testing.T) {
	taggedTestApp(t)
	ctx := context.Background()
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	// reached over HTTPS by token, and answering nothing
	st.Remote, st.Access = "https://127.0.0.1:1/owner/jarvis.git", "token"
	assert.NilError(t, saveGitApp(st))
	assert.NilError(t, writeGitFile(gitAppFile("jarvis", ".token"), []byte("ghp_kept")))
	branch, token, none, other := gitFollowBranch, "token", "none", "ghp_other"

	for _, changes := range []GitAppChanges{{Access: &token, Token: &other, Follow: &branch}, {Access: &none, Follow: &branch}} {
		_, err = UpdateGitApp(ctx, "jarvis", changes)
		assertBadRequest(t, err, "the remote's default branch cannot be read")
		kept, err := os.ReadFile(gitAppFile("jarvis", ".token"))
		assert.NilError(t, err)
		assert.Equal(t, string(kept), "ghp_kept")
		after, err := loadGitApp("jarvis")
		assert.NilError(t, err)
		assert.Equal(t, after.Follow, gitFollowTags)
		assert.Equal(t, after.Access, "token")
	}
}

// Back on a branch without one, the remote names its default: a remote that cannot be read, or
// that names no valid branch, is a 400 that leaves the app following tags and its folder
// detached. With the branch given, the remote is not asked.
func TestBackOnABranchWithoutOneTheRemoteMustNameItsDefault(t *testing.T) {
	_, work := clonedTestApp(t)
	ctx := context.Background()
	remote := filepath.Join(filepath.Dir(work), "remote.git")
	tags, branch, main := gitFollowTags, gitFollowBranch, "main"
	_, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	stillOnTags := func() {
		t.Helper()
		info, err := git.Describe(ctx, st.Dir)
		assert.NilError(t, err)
		assert.Equal(t, info.Branch, "", "still detached")
		after, err := loadGitApp("jarvis")
		assert.NilError(t, err)
		assert.Equal(t, after.Follow, gitFollowTags)
		assert.Equal(t, after.Branch, "")
	}

	// a name git would take for an option never reaches git checkout -B
	gitShell(t, remote, "git update-ref refs/heads/-bad refs/heads/main && git symbolic-ref HEAD refs/heads/-bad")
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch})
	assertBadRequest(t, err, "`-bad` is not a branch name")
	stillOnTags()

	assert.NilError(t, os.Rename(remote, remote+".gone"))
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch})
	assertBadRequest(t, err, "give the branch to follow")
	stillOnTags()

	view, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch, Branch: &main})
	assert.NilError(t, err)
	assert.Equal(t, view.Follow, gitFollowBranch)
	assert.Equal(t, view.Branch, "main")
	info, err := git.Describe(ctx, st.Dir)
	assert.NilError(t, err)
	assert.Equal(t, info.Branch, "main")
}
