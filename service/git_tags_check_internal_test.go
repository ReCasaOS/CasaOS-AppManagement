package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

// pushTestTag tags the work tree's HEAD with an annotated tag, pushes the tag, and returns the
// commit.
func pushTestTag(t *testing.T, work, tag string) string {
	t.Helper()

	return gitShell(t, work, "git tag -a -m "+tag+" "+tag+" && git push -q origin "+tag+" && git rev-parse HEAD")
}

// taggedTestApp is a created app that follows tags, checked and cloned at v1.0.0, the remote's
// first commit, and not deployed yet.
func taggedTestApp(t *testing.T) (fake *fakeGitRuntime, work, first string) {
	t.Helper()

	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake = withFakeGitDocker(t)
	url, work := newTestRemote(t)
	first = pushTestTag(t, work, "v1.0.0")

	_, err := CreateGitApp(context.Background(), GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: gitFollowTags})
	assert.NilError(t, err)
	st := checkTestApp(t, "jarvis")
	assert.Equal(t, st.Cloned, true, st.Check.Error)
	assert.Equal(t, st.Check.RemoteTag, "v1.0.0")

	return fake, work, first
}

func TestAnAppThatFollowsTagsIsClonedAtItsHighestTag(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	first := pushTestTag(t, work, "v1.0.0")
	second := pushTestCommit(t, work, "index.html", "v2")
	gitShell(t, work, "git tag v1.1.0 && git push -q origin v1.1.0")
	third := pushTestCommit(t, work, "index.html", "v3")
	gitShell(t, work, "git tag -a -m rc v1.2.0-rc.1 && git tag latest && git push -q origin --tags")

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: gitFollowTags})
	assert.NilError(t, err)
	st := checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.Error, "")
	assert.Equal(t, st.Branch, "", "the remote's default branch is never asked for")
	assert.Equal(t, st.Check.RemoteTag, "v1.1.0")
	assert.Equal(t, st.Check.RemoteCommit, second)
	assert.Equal(t, st.Cloned, true)
	info, err := git.Describe(ctx, st.Dir)
	assert.NilError(t, err)
	assert.Equal(t, info.Branch, "", "detached at the tag")
	assert.Equal(t, info.Head, second)

	// pre-releases, once included
	on := true
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Prereleases: &on})
	assert.NilError(t, err)
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.2.0-rc.1")
	assert.Equal(t, st.Check.RemoteCommit, third)

	// no tag matches: the check says so under which filter, and keeps what it saw last
	pattern := "v2.*"
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.Error, "no tag matches (pattern `v2.*`, pre-releases included)")
	assert.Equal(t, st.Check.RemoteTag, "v1.2.0-rc.1")
	assert.Equal(t, st.Check.RemoteCommit, third)
	assert.Equal(t, gitAppStateOf(st), "idle", "the repository was reached")

	pattern = ""
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)
	tags, err := GitAppTags(ctx, "jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, tags, []GitTag{{Name: "v1.2.0-rc.1", Commit: third}, {Name: "v1.1.0", Commit: second}, {Name: "v1.0.0", Commit: first}})
}

func TestTheTagsOfAnAppAreListedHighestFirstFiftyAtMost(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	head := gitShell(t, work, "git rev-parse HEAD")
	gitShell(t, work, "for i in $(seq 1 55); do git tag v0.1.$i; done && git tag -a -m one v1.0.0 && git tag v1.1.0-rc.1 && git push -q origin --tags")

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: gitFollowTags})
	assert.NilError(t, err)
	tags, err := GitAppTags(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, len(tags), 50)
	assert.Equal(t, tags[0], GitTag{Name: "v1.0.0", Commit: head}, "an annotated tag, peeled")
	assert.Equal(t, tags[1].Name, "v0.1.55")
	assert.Equal(t, tags[49].Name, "v0.1.7")

	pattern := "v9.*"
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)
	tags, err = GitAppTags(ctx, "jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, tags, []GitTag{})

	_, err = CreateGitApp(ctx, GitAppRegistration{Name: "other", URL: url, Access: "none"})
	assert.NilError(t, err)
	_, err = GitAppTags(ctx, "other")
	assertBadRequest(t, err, "follows a branch, not tags")
	_, err = GitAppTags(ctx, "nextcloud")
	assert.ErrorIs(t, err, ErrGitAppNotFound)
}

// The tags of a repository that cannot be reached are a 400 that says so.
func TestTheTagsOfARepositoryThatCannotBeReachedAreRefused(t *testing.T) {
	_, work, _ := taggedTestApp(t)
	remote := filepath.Join(filepath.Dir(work), "remote.git")
	assert.NilError(t, os.Rename(remote, remote+".gone"))

	_, err := GitAppTags(context.Background(), "jarvis")

	assertBadRequest(t, err, "the repository cannot be reached")
}
