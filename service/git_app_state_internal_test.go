package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"golang.org/x/crypto/ssh"
	"gotest.tools/v3/assert"
)

// gitAppsIn points the git apps' state, secrets and created clones at a test's folders.
func gitAppsIn(t *testing.T) {
	t.Helper()

	dir, data := gitAppsDir, gitAppsDataRoot
	gitAppsDir = filepath.Join(t.TempDir(), "git_apps")
	gitAppsDataRoot = t.TempDir()
	t.Cleanup(func() { gitAppsDir, gitAppsDataRoot = dir, data })
}

func TestAGitAppIsKeptInAFileOnlyRootReads(t *testing.T) {
	gitAppsIn(t)

	st := &gitApp{App: "jarvis", Origin: gitOriginCreated, Dir: "/DATA/AppData/jarvis", Remote: "https://example.invalid/jarvis.git", Branch: "main", Access: "none"}
	assert.NilError(t, saveGitApp(st))

	loaded, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, loaded, st)

	info, err := os.Stat(gitAppFile("jarvis", ".json"))
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))
	folder, err := os.Stat(gitAppsDir)
	assert.NilError(t, err)
	assert.Equal(t, folder.Mode().Perm(), os.FileMode(0o700))

	names, err := GitAppNames()
	assert.NilError(t, err)
	assert.DeepEqual(t, names, []string{"jarvis"})

	_, err = loadGitApp("nextcloud")
	assert.ErrorIs(t, err, ErrGitAppNotFound)

	assert.NilError(t, forgetGitApp("jarvis"))
	names, err = GitAppNames()
	assert.NilError(t, err)
	assert.DeepEqual(t, names, []string{})
}

// A token reaches git through a helper written beside the state, which tests point
// elsewhere, never under /tmp.
func TestAGitAppWithATokenHasItsAskpassHelperBesideTheState(t *testing.T) {
	gitAppsIn(t)
	assert.NilError(t, writeGitFile(gitAppFile("jarvis", ".token"), []byte("ghp_secret\n")))

	auth, err := gitAuth(&gitApp{App: "jarvis", Access: git.AccessToken})
	assert.NilError(t, err)
	assert.DeepEqual(t, auth, git.Auth{Mode: git.AccessToken, Token: "ghp_secret", AskpassDir: gitAppsDir})
}

func TestADeployKeyIsEd25519WithItsPublicHalfCommented(t *testing.T) {
	gitAppsIn(t)

	public, err := generateGitKey("jarvis")
	assert.NilError(t, err)
	assert.Assert(t, strings.HasPrefix(public, "ssh-ed25519 AAAA"), public)
	assert.Assert(t, strings.HasSuffix(public, " casaos-jarvis"), public)

	private, err := os.ReadFile(gitAppFile("jarvis", ".key"))
	assert.NilError(t, err)
	signer, err := ssh.ParsePrivateKey(private)
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))+" casaos-jarvis", public)

	for _, suffix := range []string{".key", ".key.pub", ".known_hosts"} {
		info, err := os.Stat(gitAppFile("jarvis", suffix))
		assert.NilError(t, err, suffix)
		assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600), suffix)
	}
}

func TestHistoryKeepsThreeAndGivesBackWhatNothingNames(t *testing.T) {
	entry := func(commit, outcome string, images map[string]string) gitHistoryEntry {
		return gitHistoryEntry{Commit: commit, Outcome: outcome, At: time.Now(), Images: images}
	}
	st := &gitApp{Deployed: &gitDeployment{Commit: "a", Images: map[string]string{"web": "jarvis-web:git-a"}}}

	assert.Assert(t, len(st.record(entry("a", gitOutcomeDeployed, map[string]string{"web": "jarvis-web:git-a"}))) == 0)
	assert.Assert(t, len(st.record(entry("b", gitOutcomeRolledBack, map[string]string{"web": "jarvis-web:git-b"}))) == 0)
	assert.Assert(t, len(st.record(entry("c", gitOutcomeBuildFailed, nil))) == 0)

	// a leaves the history, but it is what runs
	assert.DeepEqual(t, st.record(entry("d", gitOutcomeBuildFailed, nil)), []string{})
	// b leaves it, and nothing names its image any more
	assert.DeepEqual(t, st.record(entry("e", gitOutcomeBuildFailed, nil)), []string{"jarvis-web:git-b"})
	assert.Equal(t, len(st.History), 3)
	assert.Equal(t, st.History[0].Commit, "e")
}

func TestAttemptedCommitsAreBoundedAndUnique(t *testing.T) {
	st := &gitApp{}
	for i := 0; i < 25; i++ {
		st.attempt(strings.Repeat(string(rune('a'+i)), 40))
	}
	st.attempt(strings.Repeat("y", 40))

	assert.Equal(t, len(st.Attempted), gitAttemptedLength)
	assert.Equal(t, st.Attempted[0], strings.Repeat("f", 40))
	assert.Equal(t, st.Attempted[gitAttemptedLength-1], strings.Repeat("y", 40))
}

func TestAGitURLIsOneGitCanBeGivenSafely(t *testing.T) {
	for raw, kind := range map[string]string{
		"https://github.com/owner/jarvis.git":      "http",
		"http://gitea.local:3000/owner/jarvis.git": "http",
		"git@github.com:owner/jarvis.git":          "ssh",
		"ssh://git@gitea.local:2222/owner/j.git":   "ssh",
		"ssh://git@[::1]:2222/owner/j.git":         "ssh",
		"https://[2001:db8::1]/owner/j.git":        "http",
		"file:///opt/jarvis.git":                   "file",
		"https://owner:hunter2@github.com/j.git":   "",
		"--upload-pack=touch /tmp/pwned":           "",
		"ext::sh -c touch% /tmp/pwned":             "",
		"ext::id":                                  "",
		"ext::sh${IFS}-c${IFS}id":                  "",
		"fd::0":                                    "",
		"ftp://example.com/jarvis.git":             "",
		"":                                         "",
	} {
		assert.Equal(t, gitURLKind(raw), kind, raw)
	}
	assert.Equal(t, redactGitURL("https://owner:hunter2@github.com/j.git"), "https://owner@github.com/j.git")
}

func TestABranchIsANameGitCannotReadAsSomethingElse(t *testing.T) {
	for branch, valid := range map[string]bool{
		"":                   true,
		"main":               true,
		"feature/voice-wake": true,
		"release-1.2":        true,
		"-upload-pack":       false,
		"main..dev":          false,
		"@":                  false,
		"@{upstream}":        false,
		"main@{-1}":          false,
		"main~1":             false,
		"main^":              false,
		"a b":                false,
		"a:b":                false,
		"a?b":                false,
		"a*b":                false,
		"a[b":                false,
		"a\\b":               false,
		"a\x1bb":             false,
		"a\x7fb":             false,
	} {
		assert.Equal(t, validGitBranch(branch), valid, "%q", branch)
	}
}
