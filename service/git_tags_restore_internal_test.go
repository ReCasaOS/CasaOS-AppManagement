package service

import (
	"context"
	stdjson "encoding/json"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

// A branch app's origin is what older versions wrote, byte for byte; an app that follows tags
// adds its mode, its tag and its filter.
func TestABackupOfAnAppThatFollowsTagsRecordsItsTag(t *testing.T) {
	logInTempDir(t)
	gitAppsIn(t)
	app, _ := appWithOneBindAndOneVolume(t)
	options := BackupOptions{Destination: "offsite", Stamp: restoreStamp}
	const commit = "4f5f60c16eba0123456789abcdef0123456789ab"
	backup := func() string {
		t.Helper()
		manifest, err := RunBackup(context.Background(), app, &fakeBackupDocker{mountpoints: demoMountpoints()}, &fakeCopier{}, options)
		assert.NilError(t, err)
		raw, err := stdjson.Marshal(manifest.Git)
		assert.NilError(t, err)
		return string(raw)
	}

	// on a branch, its tag filter kept from a time on tags
	assert.NilError(t, saveGitApp(&gitApp{
		App: "demo", Remote: "git@github.com:owner/demo.git", Branch: "main", TagPattern: "v1.*", Prereleases: true,
		Deployed: &gitDeployment{Commit: commit},
	}))
	assert.Equal(t, backup(), `{"remote":"git@github.com:owner/demo.git","branch":"main","commit":"`+commit+`"}`)

	assert.NilError(t, saveGitApp(&gitApp{
		App: "demo", Remote: "git@github.com:owner/demo.git", Follow: gitFollowTags, TagPattern: "v1.*", Prereleases: true,
		Deployed: &gitDeployment{Commit: commit, Tag: "v1.4.2"},
	}))
	assert.Equal(t, backup(), `{"remote":"git@github.com:owner/demo.git","branch":"","commit":"`+commit+`","follow":"tags","tag":"v1.4.2","tag_pattern":"v1.*","prereleases":true}`)
}

func TestAnAppThatFollowsTagsIsRestoredAtItsTag(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	first := pushTestTag(t, work, "v1.0.0")
	pushTestCommit(t, work, "index.html", "v2")
	second := pushTestTag(t, work, "v1.1.0")

	_, err := installGitAppFromBackup(ctx, "jarvis", BackupGit{Remote: url, Commit: first, Follow: gitFollowTags, Tag: "v1.0.0", TagPattern: "v1.*"}, nil)
	assert.NilError(t, err)

	assert.DeepEqual(t, fake.Calls(), []string{"build " + first[:12], "retag " + first[:12], "start " + first[:12]})
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, st.Follow, gitFollowTags)
	assert.Equal(t, st.Branch, "")
	assert.Equal(t, st.TagPattern, "v1.*")
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, st.Deployed.Tag, "v1.0.0")
	info, err := git.Describe(ctx, st.Dir)
	assert.NilError(t, err)
	assert.Equal(t, info.Branch, "", "in detached HEAD, as an app that follows tags is")
	assert.Equal(t, info.Head, first)
	// v1.1.0 contains v1.0.0's commit: only what the clone holds shows which tag it was made at
	assert.Assert(t, !git.HasCommit(ctx, st.Dir, second), "cloned at v1.0.0, not at the highest tag")
}

// The backed-up commit comes back whatever its tag did: fetched by its hash when the tag moved
// or went, and never another in its place.
func TestARestoreFetchesTheBackedUpCommitWhenItsTagMovedOrWent(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	base := gitShell(t, work, "git rev-parse HEAD")
	backedUp := pushTestCommit(t, work, "index.html", "v1")
	pushTestTag(t, work, "v1.0.0")
	// v1.0.0 moved to a commit of another line, which does not contain the backed-up one; that
	// one is still on main
	// (`git tag -f` prints `Updated tag ...` on stdout: silenced, so that the output is the hash)
	moved := gitShell(t, work, "git checkout -q -b other "+base+" && echo other > other.txt && git add other.txt && git commit -qm other && git tag -f -a -m v1.0.0 v1.0.0 >/dev/null && git push -q -f origin v1.0.0 && git rev-parse HEAD && git checkout -q main")

	_, err := installGitAppFromBackup(ctx, "jarvis", BackupGit{Remote: url, Commit: backedUp, Follow: gitFollowTags, Tag: "v1.0.0"}, nil)
	assert.NilError(t, err)
	assert.DeepEqual(t, fake.Calls(), []string{"build " + backedUp[:12], "retag " + backedUp[:12], "start " + backedUp[:12]})
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, st.Deployed.Commit, backedUp)
	assert.Equal(t, st.Deployed.Tag, "v1.0.0")
	// the next check finds the tag elsewhere, and says so
	checkTestApp(t, "jarvis")
	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.Check.RemoteCommit, moved)
	assert.Equal(t, view.Check.TagMoved, true)

	// a tag that went: cloned at the highest eligible one, the commit fetched all the same
	fake.calls = nil
	_, err = installGitAppFromBackup(ctx, "gone", BackupGit{Remote: url, Commit: backedUp, Follow: gitFollowTags, Tag: "v0.9.0"}, nil)
	assert.NilError(t, err)
	assert.DeepEqual(t, fake.Calls(), []string{"build " + backedUp[:12], "retag " + backedUp[:12], "start " + backedUp[:12]})

	// a commit the forge does not give: refused, and nothing deployed instead
	unpushed := gitShell(t, work, "echo local > local.txt && git add local.txt && git commit -qm local && git rev-parse HEAD")
	fake.calls = nil
	_, err = installGitAppFromBackup(ctx, "lost", BackupGit{Remote: url, Commit: unpushed, Follow: gitFollowTags, Tag: "v1.0.0"}, nil)
	assert.ErrorContains(t, err, "the tag v1.0.0 no longer points at the backed-up commit, and the forge does not give that commit")
	assert.DeepEqual(t, fake.Calls(), []string{})
	st, err = loadGitApp("lost")
	assert.NilError(t, err)
	assert.Assert(t, st.Deployed == nil)
}

// A backup taken before the first deployment in tag mode names no tag, and the remote may have
// no eligible tag left: the app comes back in tag mode all the same, cloned at the highest
// eligible tag or else at the default branch, detached at the backed-up commit.
func TestAnAppThatFollowsTagsIsRestoredWithoutItsTag(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	pushTestTag(t, work, "v1.0.0")
	// on main, under no tag: a clone at v1.0.0 does not hold it
	backedUp := pushTestCommit(t, work, "index.html", "v2")

	for _, c := range []struct {
		name   string
		origin BackupGit
	}{
		// cloned at v1.0.0, the commit fetched by its hash
		{"untagged", BackupGit{Remote: url, Commit: backedUp, Follow: gitFollowTags}},
		// its tag gone and no tag eligible: cloned at the default branch
		{"unmatched", BackupGit{Remote: url, Commit: backedUp, Follow: gitFollowTags, Tag: "v0.9.0", TagPattern: "v9.*"}},
	} {
		fake.calls = nil
		_, err := installGitAppFromBackup(ctx, c.name, c.origin, nil)
		assert.NilError(t, err, c.name)
		assert.DeepEqual(t, fake.Calls(), []string{"build " + backedUp[:12], "retag " + backedUp[:12], "start " + backedUp[:12]})

		st, err := loadGitApp(c.name)
		assert.NilError(t, err)
		assert.Equal(t, st.Follow, gitFollowTags, c.name)
		assert.Equal(t, st.Branch, "", c.name)
		assert.Equal(t, st.Deployed.Commit, backedUp, c.name)
		assert.Equal(t, st.Deployed.Tag, c.origin.Tag, c.name)
		info, err := git.Describe(ctx, st.Dir)
		assert.NilError(t, err)
		assert.Equal(t, info.Branch, "", "%s: in detached HEAD, as an app that follows tags is", c.name)
		assert.Equal(t, info.Head, backedUp, c.name)
	}
}

// A manifest is a file anyone with the destination's credentials can edit: its tag fields are
// checked before git is given them, and nothing is registered.
func TestARestoreRefusesATagOriginGitCannotBeGiven(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	commit := strings.Repeat("a", 40)
	remote := "https://127.0.0.1:1/owner/demo.git"

	for _, c := range []struct {
		origin  BackupGit
		message string
	}{
		{BackupGit{Remote: remote, Commit: commit, Follow: "tag"}, "neither branch nor tags"},
		{BackupGit{Remote: remote, Commit: commit, Follow: gitFollowTags, Tag: "-v1.0.0"}, "is not a tag name"},
		{BackupGit{Remote: remote, Commit: commit, Follow: gitFollowTags, Tag: "v1.0.0", TagPattern: "["}, "tag pattern"},
	} {
		_, err := installGitAppFromBackup(ctx, "jarvis", c.origin, nil)
		assert.ErrorContains(t, err, c.message)
	}
	_, err := loadGitApp("jarvis")
	assert.ErrorIs(t, err, ErrGitAppNotFound, "nothing is registered")
}
