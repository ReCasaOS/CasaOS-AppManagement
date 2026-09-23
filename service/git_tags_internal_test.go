package service

import (
	"context"
	stdjson "encoding/json"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func tagNames(tags []GitTag) []string {
	names := []string{}
	for _, tag := range tags {
		names = append(names, tag.Name)
	}

	return names
}

func TestAnEligibleTagIsStrictSemverAfterOneLeadingV(t *testing.T) {
	remote := map[string]string{
		"v1.0.0": "c1", "1.0.1": "c2", "v1.1.0-rc.1": "c3", "v1.1.0-rc.10": "c4", "v1.1.0-rc.2": "c5",
		"v2.0.0": "c6", "v2.1.0+build.7": "c7",
		// no version: floating tags, a date, partial or loose versions, a prefix
		"latest": "x", "stable": "x", "2024-09": "x", "v2": "x", "v1.2": "x", "vv3.0.0": "x", "V3.0.0": "x",
		"v01.2.3": "x", "1.2.3.4": "x", "release-4.0.0": "x",
	}

	assert.DeepEqual(t, tagNames(eligibleGitTags(remote, "", false)), []string{"v2.1.0+build.7", "v2.0.0", "1.0.1", "v1.0.0"})
	assert.DeepEqual(t, tagNames(eligibleGitTags(remote, "", true)),
		[]string{"v2.1.0+build.7", "v2.0.0", "v1.1.0-rc.10", "v1.1.0-rc.2", "v1.1.0-rc.1", "1.0.1", "v1.0.0"})
	assert.DeepEqual(t, tagNames(eligibleGitTags(remote, "v1.*", true)), []string{"v1.1.0-rc.10", "v1.1.0-rc.2", "v1.1.0-rc.1", "v1.0.0"})
	assert.DeepEqual(t, tagNames(eligibleGitTags(remote, "v[0-9].0.?", false)), []string{"v2.0.0", "v1.0.0"})
	assert.DeepEqual(t, eligibleGitTags(remote, "v3.*", true), []GitTag{})
	assert.DeepEqual(t, eligibleGitTags(map[string]string{}, "", false), []GitTag{})
	assert.DeepEqual(t, eligibleGitTags(remote, "", false)[0], GitTag{Name: "v2.1.0+build.7", Commit: "c7"})
}

// Equal versions are ordered by name, so the same tag is chosen every time.
func TestEqualVersionsAreOrderedByName(t *testing.T) {
	remote := map[string]string{"v1.2.3": "a", "1.2.3": "b", "v1.2.3+b1": "c", "1.2.3+b2": "d"}

	for range 20 {
		assert.DeepEqual(t, tagNames(eligibleGitTags(remote, "", false)), []string{"1.2.3", "1.2.3+b2", "v1.2.3", "v1.2.3+b1"})
	}
}

func TestATagIsHigherOnlyByItsVersion(t *testing.T) {
	for _, c := range []struct {
		a, b   string
		higher bool
	}{
		{"v1.10.0", "v1.9.0", true},
		{"v2.0.0-rc.1", "v1.9.9", true},
		{"v1.0.0", "v1.0.0-rc.1", true},
		{"v1.0.0-rc.10", "v1.0.0-rc.2", true},
		{"v1.0.0", "v1.0.0", false},
		{"1.2.3", "v1.2.3", false},
		{"v1.2.3+b2", "v1.2.3+b1", false},
		{"v1.0.0", "v1.1.0", false},
		{"latest", "v1.0.0", false},
		{"v1.0.0", "", false},
		{"", "v1.0.0", false},
	} {
		assert.Equal(t, gitTagHigher(c.a, c.b), c.higher, "%s over %s", c.a, c.b)
	}
}

func TestAModeAndATagPatternAreChecked(t *testing.T) {
	for _, follow := range []string{"branch", "tags"} {
		assert.NilError(t, checkGitFollow(follow))
	}
	for _, follow := range []string{"", "tag", "Tags", "branches"} {
		assertBadRequest(t, checkGitFollow(follow), "follow is branch or tags")
	}

	for _, pattern := range []string{"", "v2.*", "v[0-9].*", "release-?.*", strings.Repeat("v", 100)} {
		assert.NilError(t, checkGitTagPattern(pattern), pattern)
	}
	for _, pattern := range []string{"[", "v2.[", "v2\\", "v 2.*", "v2.*\t", "v2\x00", strings.Repeat("v", 101)} {
		assertBadRequest(t, checkGitTagPattern(pattern), "tag pattern")
	}
}

func TestNoEligibleTagSaysWhichFilterIsInForce(t *testing.T) {
	assert.Equal(t, gitNoTagMessage("v2.*", false), "no tag matches (pattern `v2.*`, pre-releases excluded)")
	assert.Equal(t, gitNoTagMessage("", true), "no tag matches (no pattern, pre-releases included)")
}

// Only a higher tag is new to an app that follows tags: a lower or a moved one never lights
// the badge. Before its first deployment in that mode, any other commit is new.
func TestNewCommitsOfAnAppThatFollowsTagsIsAHigherTag(t *testing.T) {
	deployed := &gitDeployment{Commit: "a", Tag: "v1.1.0"}
	tagged := func(d *gitDeployment, commit, tag string) *gitApp {
		return &gitApp{Follow: gitFollowTags, Deployed: d, Check: &gitCheck{RemoteCommit: commit, RemoteTag: tag}}
	}

	assert.Assert(t, gitNewCommits(tagged(deployed, "b", "v1.2.0")))
	assert.Assert(t, !gitNewCommits(tagged(deployed, "b", "v1.0.0")), "a lower tag")
	assert.Assert(t, !gitNewCommits(tagged(deployed, "b", "v1.1.0")), "the deployed tag, moved")
	assert.Assert(t, !gitNewCommits(tagged(deployed, "a", "v1.2.0")), "a higher tag on what runs")
	assert.Assert(t, gitNewCommits(tagged(&gitDeployment{Commit: "a"}, "b", "v1.0.0")), "no tag deployed yet in this mode")
	assert.Assert(t, !gitNewCommits(tagged(&gitDeployment{Commit: "a"}, "b", "")), "a branch check left from before the switch")
	assert.Assert(t, gitNewCommits(&gitApp{Deployed: deployed, Check: &gitCheck{RemoteCommit: "b"}}), "a branch: any other commit")
}

// The view shows the mode and its filter, the tags beside the commits, and a deployed tag the
// remote moved.
func TestTheViewOfAnAppThatFollowsTagsSaysWhenItsTagMoved(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	st := &gitApp{
		App: "jarvis", Follow: gitFollowTags, TagPattern: "v1.*", Prereleases: true,
		Deployed:  &gitDeployment{Commit: "a", Tag: "v1.1.0"},
		Check:     &gitCheck{RemoteCommit: "b", RemoteTag: "v1.1.0"},
		Operation: &gitOperation{Kind: gitOperationBuild, Commit: "c", Tag: "v1.2.0"},
		History:   []gitHistoryEntry{{Commit: "a", Tag: "v1.1.0", Outcome: gitOutcomeDeployed}},
	}

	view := newGitAppView(ctx, st)
	assert.Equal(t, view.Follow, gitFollowTags)
	assert.Equal(t, view.TagPattern, "v1.*")
	assert.Equal(t, view.Prereleases, true)
	assert.Equal(t, view.Deployed.Tag, "v1.1.0")
	assert.Equal(t, view.History[0].Tag, "v1.1.0")
	assert.Equal(t, view.Operation.Tag, "v1.2.0")
	assert.DeepEqual(t, view.Check, &GitCheckView{RemoteCommit: "b", RemoteTag: "v1.1.0", TagMoved: true})
	// the operation's key, as the API answers it
	encoded, err := stdjson.Marshal(view)
	assert.NilError(t, err)
	var answered map[string]any
	assert.NilError(t, stdjson.Unmarshal(encoded, &answered))
	assert.Equal(t, answered["operation"].(map[string]any)["tag"], "v1.2.0")

	st.Check.RemoteCommit = "a"
	assert.Equal(t, newGitAppView(ctx, st).Check.TagMoved, false, "the tag on what runs")
	st.Check.RemoteTag, st.Check.RemoteCommit = "v1.2.0", "b"
	assert.Equal(t, newGitAppView(ctx, st).Check.TagMoved, false, "another tag")
	st.Follow, st.Check.RemoteTag = "", "v1.1.0"
	view = newGitAppView(ctx, st)
	assert.Equal(t, view.Follow, gitFollowBranch, "an older state follows a branch")
	assert.Equal(t, view.Check.TagMoved, false)
}
