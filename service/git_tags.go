package service

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/Masterminds/semver/v3"
)

// Git apps that follow the tags of their repository instead of a branch: which tags they may
// deploy, and in which order. The commit stays the identity of a version everywhere; the tag
// is written down beside it.

// What a git app follows.
const (
	gitFollowBranch = "branch"
	gitFollowTags   = "tags"
)

// gitTagPatternCap is the longest tag pattern accepted.
const gitTagPatternCap = 100

// errGitTagsHaveNoBranch refuses a branch given to an app that follows tags.
const errGitTagsHaveNoBranch = GitRequestError("an app that follows tags has no branch: set follow to branch to give one")

// GitTag is a tag of the remote and the commit it names.
type GitTag struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

// follow is what the app follows: gitFollowTags, or gitFollowBranch for anything else, an
// older state's empty value included.
func (st *gitApp) follow() string {
	if st.followsTags() {
		return gitFollowTags
	}

	return gitFollowBranch
}

func (st *gitApp) followsTags() bool {
	return st.Follow == gitFollowTags
}

// checkGitFollow refuses a mode that is neither branch nor tags.
func checkGitFollow(follow string) error {
	if follow != gitFollowBranch && follow != gitFollowTags {
		return GitRequestError(fmt.Sprintf("follow is branch or tags, not `%s`", follow))
	}

	return nil
}

// checkGitTagPattern refuses a tag pattern path.Match cannot read, one too long, or one holding
// a space or a control character. Empty is every version tag.
func checkGitTagPattern(pattern string) error {
	_, err := path.Match(pattern, "")
	if err != nil || len(pattern) > gitTagPatternCap || strings.ContainsFunc(pattern, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return GitRequestError(fmt.Sprintf("the tag pattern is a glob such as v2.* (`*`, `?`, `[...]`), at most %d characters, with no space", gitTagPatternCap))
	}

	return nil
}

// gitNoTag opens the message of a check that reached the remote and found no eligible tag.
const gitNoTag = "no tag matches"

// gitNoTagMessage says that no tag is eligible, and under which filter.
func gitNoTagMessage(pattern string, prereleases bool) string {
	filter := "no pattern"
	if pattern != "" {
		filter = "pattern `" + pattern + "`"
	}
	releases := "pre-releases excluded"
	if prereleases {
		releases = "pre-releases included"
	}

	return fmt.Sprintf("%s (%s, %s)", gitNoTag, filter, releases)
}

// gitTagVersion is the version a tag names: strict semver once one leading `v` is dropped, so
// that `v2`, `latest` or `2024-09` name none.
func gitTagVersion(name string) (*semver.Version, bool) {
	version, err := semver.StrictNewVersion(strings.TrimPrefix(name, "v"))

	return version, err == nil
}

// gitTagHigher reports whether tag a names a strictly higher version than tag b; false when
// either names none.
func gitTagHigher(a, b string) bool {
	va, okA := gitTagVersion(a)
	vb, okB := gitTagVersion(b)

	return okA && okB && va.GreaterThan(vb)
}

// eligibleGitTags is the tags an app may deploy among tags, highest first: the name matches
// pattern when there is one, names a version, and names a pre-release only with prereleases.
// Equal versions (`v1.2.3` and `1.2.3`, or build metadata only) come by name, so that the
// choice is always the same.
func eligibleGitTags(tags map[string]string, pattern string, prereleases bool) []GitTag {
	type versioned struct {
		tag     GitTag
		version *semver.Version
	}

	eligible := []versioned{}
	for name, commit := range tags {
		version, ok := gitTagVersion(name)
		matched, _ := path.Match(pattern, name)
		if !ok || (pattern != "" && !matched) || (version.Prerelease() != "" && !prereleases) {
			continue
		}
		eligible = append(eligible, versioned{GitTag{Name: name, Commit: commit}, version})
	}

	slices.SortFunc(eligible, func(a, b versioned) int {
		if order := b.version.Compare(a.version); order != 0 {
			return order
		}

		return strings.Compare(a.tag.Name, b.tag.Name)
	})

	sorted := make([]GitTag, 0, len(eligible))
	for _, e := range eligible {
		sorted = append(sorted, e.tag)
	}

	return sorted
}

// gitTagMoved reports whether the last check found the deployed tag on another commit than the
// one deployed: an app that follows tags never deploys it again by itself.
func gitTagMoved(st *gitApp) bool {
	return st.followsTags() && st.Deployed != nil && st.Check != nil && st.Check.RemoteTag != "" &&
		st.Check.RemoteTag == st.Deployed.Tag && st.Check.RemoteCommit != st.Deployed.Commit
}
