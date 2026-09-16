package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"gotest.tools/v3/assert"
)

// The schedule checks every registered app: what the automatic rebuild may deploy starts,
// and an app waiting for its owner's first check is not cloned behind their back.
func TestThePeriodicCheckDeploysWhatItMayAndClonesNothing(t *testing.T) {
	fake, work, _ := deployedTestApp(t, true)
	second := pushTestCommit(t, work, "index.html", "v2")
	url, _ := newTestRemote(t)
	_, err := CreateGitApp(context.Background(), GitAppRegistration{Name: "waiting", URL: url, Access: "none"})
	assert.NilError(t, err)

	CheckGitApps(context.Background())
	st := waitForGitApp(t, "jarvis")

	assert.Equal(t, st.Deployed.Commit, second)
	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})

	waiting, err := loadGitApp("waiting")
	assert.NilError(t, err)
	assert.Assert(t, waiting.Check != nil && waiting.Check.Error == "", "a registered app is checked")
	assert.Equal(t, waiting.Cloned, false, "and not cloned: that is for its owner's check")
}

func TestStartupRecordsAnInterruptedOperationAndCleansUp(t *testing.T) {
	_, work, first := deployedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	st.Operation = &gitOperation{Kind: gitOperationBuild, Commit: second, StartedAt: time.Now().UTC()}
	assert.NilError(t, saveGitApp(st))
	_, err = git.Fetch(context.Background(), st.Dir, st.Remote, st.Branch, git.Auth{})
	assert.NilError(t, err)
	worktree := filepath.Join(gitAppsDir, "work", "jarvis-"+second[:12])
	assert.NilError(t, git.AddWorktree(context.Background(), st.Dir, second, worktree, git.Auth{}))
	partial := filepath.Join(gitAppsDataRoot, ".other.clone")
	assert.NilError(t, os.MkdirAll(partial, 0o755))
	assert.NilError(t, saveGitApp(&gitApp{App: "other", Origin: gitOriginCreated, Dir: filepath.Join(gitAppsDataRoot, "other")}))

	RecoverGitApps(context.Background())

	st, err = loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Assert(t, st.Operation == nil)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeInterrupted)
	assert.Equal(t, st.History[0].Commit, second)
	assert.DeepEqual(t, st.Attempted, []string{second})
	assert.DeepEqual(t, st.History[0].Images, map[string]string{"web": "jarvis-web:git-" + second[:12]})
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, gitAppStateOf(st), "interrupted")
	_, err = os.Stat(worktree)
	assert.Assert(t, os.IsNotExist(err))
	assert.Equal(t, gitShell(t, st.Dir, "git worktree list | wc -l"), "1")
	_, err = os.Stat(partial)
	assert.Assert(t, os.IsNotExist(err), "a clone cut short is removed")
}
