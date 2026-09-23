package service

import (
	"context"
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
