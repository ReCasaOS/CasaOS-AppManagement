package service

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

// While an operating system upgrade is open nothing of a git app runs by itself: not the
// check every five minutes, not a push, not the deployment a check finds to do. What somebody
// clicks stays available, and a check by hand still reads.
//
// These tests need no git binary: the apps are written down as a check left them, and one
// that is not cloned is refused by a deployment, which is how a test sees that one started.

var (
	// the commit a seeded app runs, and the one its last check saw
	runningCommit = strings.Repeat("a", 40)
	aheadCommit   = strings.Repeat("b", 40)
)

// seedGitApp writes down an app whose last check saw a commit it does not run, with the
// automatic rebuild on, and whose remote cannot be reached: a check of it ends in an error
// that is recorded, which is how a test sees that one ran.
func seedGitApp(t *testing.T, name string) *gitApp {
	t.Helper()

	st := &gitApp{
		App: name, Origin: gitOriginCreated, Dir: filepath.Join(gitAppsDataRoot, name),
		Remote: "file:///nonexistent/" + name + ".git", Branch: "main", Access: "none", AutoDeploy: true,
		Deployed: &gitDeployment{Commit: runningCommit, At: time.Now().UTC(), Images: map[string]string{}},
		Check:    &gitCheck{At: time.Now().UTC().Add(-time.Hour), RemoteCommit: aheadCommit},
		History:  []gitHistoryEntry{}, Attempted: []string{},
	}
	assert.NilError(t, saveGitApp(st))

	return st
}

func gitAppFileBytes(t *testing.T, name string) string {
	t.Helper()

	content, err := os.ReadFile(gitAppFile(name, ".json"))
	assert.NilError(t, err)

	return string(content)
}

// The funnel every automatic deployment goes through: the periodic check, a push, and a check
// by hand. The commit stays untried, so that the first check after the upgrade is closed
// deploys it.
func TestAnAutomaticDeploymentWaitsForTheUpgrade(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	flag := upgradeFlagIn(t)
	ctx := context.Background()
	seedGitApp(t, "jarvis")

	deploy := func() {
		end, err := Begin("jarvis", gitOperationCheck)
		assert.NilError(t, err)
		deployGitAppAutomatically(ctx, "jarvis", end)
	}

	flag.present = true
	deploy()

	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, st.Attempted, []string{})
	assert.Equal(t, len(st.History), 0)
	assert.Equal(t, len(flag.said), 1, "it says what waits")
	assert.DeepEqual(t, fake.Calls(), []string{})
	end, err := Begin("jarvis", "test")
	assert.NilError(t, err, "the app is let go")
	end()

	// the same call with no upgrade open does try: the app is not cloned, so the deployment is
	// refused, and a refusal is what an automatic deployment writes down
	flag.present = false
	deploy()

	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, st.Attempted, []string{aheadCommit})
	assert.Equal(t, len(st.History), 1)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Equal(t, len(flag.said), 1, "a free box says nothing")
}

// Nothing is checked either: the check reads, but the system git binary it runs is one the
// upgrade replaces, and what it would write down is an error about that.
func TestThePeriodicCheckOfGitAppsWaitsForTheUpgrade(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	flag := upgradeFlagIn(t)
	ctx := context.Background()
	seedGitApp(t, "jarvis")
	seedGitApp(t, "nextcloud")
	before := map[string]string{"jarvis": gitAppFileBytes(t, "jarvis"), "nextcloud": gitAppFileBytes(t, "nextcloud")}

	flag.present = true
	CheckGitApps(ctx)

	for name, was := range before {
		assert.Equal(t, gitAppFileBytes(t, name), was, "%s is left as it was: no operation, no check", name)
	}
	assert.Equal(t, len(flag.said), 1, "one line for the cycle, not one per app")

	flag.present = false
	CheckGitApps(ctx)

	for name := range before {
		st := waitForGitApp(t, name)
		assert.Assert(t, st.Check.At.After(time.Now().Add(-time.Minute)), "%s is checked once the upgrade is closed", name)
		assert.Assert(t, st.Check.Error != "", "%s: its remote is not there", name)
	}
	assert.Equal(t, len(flag.said), 1)
}

// A push is an automatic check: it starts nothing, says so to the forge, and does not use up
// the window in which a following push would be taken for a repeat.
func TestAWebhookDuringTheUpgradeChecksNothing(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	freshGitWebhookRuns(t)
	flag := upgradeFlagIn(t)
	seeded := seedGitApp(t, "jarvis")
	secret := strings.Repeat("5e", 32)
	assert.NilError(t, saveGitWebhook("jarvis", &gitWebhook{Secret: secret}))
	body := []byte(`{"ref":"refs/heads/main"}`)
	before := gitAppFileBytes(t, "jarvis")

	flag.present = true
	answer, err := deliver(signedDelivery(secret, "push", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookHeld)
	assert.Equal(t, lastDelivery(t).Result, "held")
	assert.Equal(t, gitAppFileBytes(t, "jarvis"), before, "no check ran")
	assert.Equal(t, len(flag.said), 1)

	// the push after the upgrade is closed is the first one: a check, not a repeat of a check
	flag.present = false
	answer, err = deliver(signedDelivery(secret, "push", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookChecking)
	st := waitForGitCheckAfter(t, "jarvis", seeded.Check.At)
	assert.Assert(t, st.Check.Error != "", "its remote is not there")
}

// The hold as the service wires it: the flag read from the disk, with nothing faked but the
// place the flag is. A default that answers "not there" whatever the disk says passes every
// test that fakes it.
func TestAGitAppIsHeldAsTheServiceWiresIt(t *testing.T) {
	gitAppsIn(t)
	ctx := context.Background()
	seedGitApp(t, "jarvis")

	wasPath := osUpgradeBusyPath
	osUpgradeBusyPath = filepath.Join(t.TempDir(), "busy")
	t.Cleanup(func() { osUpgradeBusyPath = wasPath })
	if runtime.GOOS == "windows" {
		assert.NilError(t, writeRaw(osUpgradeBusyPath, "{}"))
	} else {
		// not even a file: a link to nothing, which the core takes for an upgrade too
		assert.NilError(t, os.Symlink(filepath.Join(t.TempDir(), "gone"), osUpgradeBusyPath))
	}

	logger.LogInitConsoleOnly() // the line that says what waits is written to the real log
	end, err := Begin("jarvis", gitOperationCheck)
	assert.NilError(t, err)
	deployGitAppAutomatically(ctx, "jarvis", end)

	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, st.Attempted, []string{})
}

// A check by hand is somebody asking, and reads; the deployment it would start waits like any
// other automatic one, and the next check after the upgrade is closed does it.
func TestACheckByHandStillReadsButItsDeploymentWaitsForTheUpgrade(t *testing.T) {
	fake, work, first := deployedTestApp(t, true)
	second := pushTestCommit(t, work, "index.html", "v2")
	flag := upgradeFlagIn(t)

	flag.present = true
	st := checkTestApp(t, "jarvis")

	assert.Equal(t, st.Check.RemoteCommit, second, "the check read the remote")
	assert.Equal(t, st.Deployed.Commit, first)
	assert.DeepEqual(t, st.Attempted, []string{})
	assert.DeepEqual(t, fake.Calls(), []string{})

	flag.present = false
	st = checkTestApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Commit, second)
}
