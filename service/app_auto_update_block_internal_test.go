package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"gotest.tools/v3/assert"
)

// blockedBox is a night whose update is blocked by the scan. The registry says `digest`, or
// fails with err, and the update records how often it was asked.
type blockedBox struct {
	asked  int
	digest string
	err    error
}

func nightWithABlock(t *testing.T) *blockedBox {
	t.Helper()
	nightBox(t, nil)
	box := &blockedBox{digest: "sha256:aa"}
	autoUpdateOne = func(context.Context, string) (codegen.ComposeAppUpdateRunAppState, string, []BlockedImage) {
		box.asked++
		return codegen.ComposeAppUpdateRunAppStateBlocked, "the new version has 2 more critical vulnerabilities than the one installed",
			[]BlockedImage{{Service: "app", Image: "img:1", RepoDigests: []string{"img@sha256:aa"}, ID: "sha256:new", Current: 1, Candidate: 3}}
	}
	was := registryDigestOf
	registryDigestOf = func(string) (string, error) { return box.digest, box.err }
	t.Cleanup(func() { registryDigestOf = was })
	_, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)

	return box
}

var (
	nightOne = tonight.Add(time.Minute)
	nightTwo = tonight.AddDate(0, 0, 1).Add(time.Minute)
)

// A block is not a failure: the failures are not counted, the wait is not restarted, and
// what blocked the update is written.
func TestNightBlockedIsNotAFailure(t *testing.T) {
	nightWithABlock(t)
	state, err := readAutoUpdate()
	assert.NilError(t, err)
	state.Failures["jellyfin"] = 1
	assert.NilError(t, writeAutoUpdate(state))

	RunDueAutoUpdates(context.Background(), nightOne)

	state, err = readAutoUpdate()
	assert.NilError(t, err)
	assert.Equal(t, state.Failures["jellyfin"], 1)
	assert.Equal(t, state.Seen["jellyfin"], nightOne)
	assert.Equal(t, len(state.Blocked["jellyfin"].Images), 1)
	assert.Assert(t, strings.Contains(state.Blocked["jellyfin"].Message, "2 more critical"))
	run := state.Runs[0].Apps[0]
	assert.Equal(t, run.State, codegen.ComposeAppUpdateRunAppStateBlocked)
	assert.Equal(t, len(*run.Blocked), 1)
	assert.Equal(t, (*run.Blocked)[0].Image, "img:1")
}

// The owner is told once; while the registry still publishes the blocked image the next
// nights do not try again, and say nothing.
func TestNightTellsABlockOnce(t *testing.T) {
	box := nightWithABlock(t)
	got := hear(t)

	RunDueAutoUpdates(context.Background(), nightOne)
	assert.Equal(t, len(*got), 1)
	assert.Equal(t, (*got)[0].name, "auto-update:night-end")
	assert.Equal(t, (*got)[0].properties["auto-update:blocked"], "jellyfin")

	RunDueAutoUpdates(context.Background(), nightTwo)
	assert.Equal(t, box.asked, 1)
	assert.Equal(t, len(*got), 1)
	view, err := AutoUpdate(nightTwo)
	assert.NilError(t, err)
	entry := view.Runs[0].Apps[0]
	assert.Equal(t, entry.State, codegen.ComposeAppUpdateRunAppStateBlocked)
	assert.Assert(t, strings.Contains(*entry.Message, "2 more critical"))
	assert.Equal(t, len(*entry.Blocked), 1)
}

func TestNightRetriesWhenRegistryMoved(t *testing.T) {
	box := nightWithABlock(t)
	RunDueAutoUpdates(context.Background(), nightOne)

	box.digest = "sha256:bb"
	RunDueAutoUpdates(context.Background(), nightTwo)
	assert.Equal(t, box.asked, 2)
}

// A registry that does not answer cannot say the image is the same: the night tries, and a
// block it finds again for the same image is not told again.
func TestNightRetriesWhenRegistryDoesNotAnswer(t *testing.T) {
	box := nightWithABlock(t)
	got := hear(t)
	RunDueAutoUpdates(context.Background(), nightOne)

	box.err = errors.New("registry unreachable")
	RunDueAutoUpdates(context.Background(), nightTwo)
	assert.Equal(t, box.asked, 2)
	assert.Equal(t, len(*got), 1)
}

func TestUpdatedClearsBlocked(t *testing.T) {
	box := nightWithABlock(t)
	RunDueAutoUpdates(context.Background(), nightOne)

	box.digest = "sha256:bb"
	autoUpdateOne = func(context.Context, string) (codegen.ComposeAppUpdateRunAppState, string, []BlockedImage) {
		return codegen.ComposeAppUpdateRunAppStateUpdated, "", nil
	}
	RunDueAutoUpdates(context.Background(), nightTwo)

	state, err := readAutoUpdate()
	assert.NilError(t, err)
	assert.Equal(t, len(state.Blocked), 0)
}

// The check turned off, or the app turned off, leaves no block behind to hold the app.
func TestTurningOffForgetsTheBlock(t *testing.T) {
	nightWithABlock(t)
	RunDueAutoUpdates(context.Background(), nightOne)

	_, err := SetScanBeforeUpdate(false, nightTwo)
	assert.NilError(t, err)
	state, err := readAutoUpdate()
	assert.NilError(t, err)
	assert.Equal(t, len(state.Blocked), 0)

	state.Blocked["jellyfin"] = blockedUpdate{Message: "x"}
	assert.NilError(t, writeAutoUpdate(state))
	_, err = SetAppAutoUpdate("jellyfin", false, nightTwo)
	assert.NilError(t, err)
	state, err = readAutoUpdate()
	assert.NilError(t, err)
	assert.Equal(t, len(state.Blocked), 0)
}

func TestScanBeforeUpdateDefaultsOn(t *testing.T) {
	nightBox(t, nil)
	assert.NilError(t, os.Remove(AutoUpdatePath))
	setRuntimePathForTest(t)
	assert.Assert(t, ScanBeforeUpdate())

	view, err := AutoUpdate(afternoon)
	assert.NilError(t, err)
	assert.Assert(t, view.ScanBeforeUpdate)
	assert.Assert(t, !view.ScanAvailable)

	assert.NilError(t, os.WriteFile(filepath.Join(config.CommonInfo.RuntimePath, vulnScanURLFile), []byte("http://127.0.0.1:1"), 0o600))
	view, err = SetScanBeforeUpdate(false, afternoon)
	assert.NilError(t, err)
	assert.Assert(t, !view.ScanBeforeUpdate && view.ScanAvailable)
	assert.Assert(t, !ScanBeforeUpdate())
}

// With the switch off, an update asks the scan nothing, whoever starts it.
func TestSwitchOffSkipsTheGate(t *testing.T) {
	r := newGateRig(t, blockedByTheScan())
	was := AutoUpdatePath
	AutoUpdatePath = filepath.Join(t.TempDir(), "auto-update.json")
	t.Cleanup(func() { AutoUpdatePath = was })
	_, err := SetScanBeforeUpdate(false, afternoon)
	assert.NilError(t, err)
	heard := hearUpdate(t)

	err = r.app.pullAndApply(context.Background(), []byte(gatedNext), nil, true)
	var blocked *UpdateBlockedError
	assert.Assert(t, !errors.As(err, &blocked), "%v", err)
	assert.Equal(t, r.checks(), 0)
	heard("docker:container:start-begin")
}

// A check that failed says nothing of the image: the block stays, and the night that finds
// the same block again does not tell it again.
func TestFailedCheckKeepsTheBlock(t *testing.T) {
	box := nightWithABlock(t)
	got := hear(t)
	RunDueAutoUpdates(context.Background(), nightOne)

	autoUpdateCheck = func(context.Context, string) (bool, codegen.ComposeAppUpdateRunAppState, string) {
		return false, codegen.ComposeAppUpdateRunAppStateFailed, "the apps cannot be listed"
	}
	RunDueAutoUpdates(context.Background(), nightTwo)
	state, err := readAutoUpdate()
	assert.NilError(t, err)
	assert.Equal(t, len(state.Blocked["jellyfin"].Images), 1)

	autoUpdateCheck = func(context.Context, string) (bool, codegen.ComposeAppUpdateRunAppState, string) { return true, "", "" }
	box.err = errors.New("registry unreachable")
	RunDueAutoUpdates(context.Background(), nightTwo.AddDate(0, 0, 1))
	assert.Equal(t, box.asked, 2)
	told := 0
	for _, e := range *got {
		if e.properties["auto-update:blocked"] != "" {
			told++
		}
	}
	assert.Equal(t, told, 1)
}

// What the owner dropped while the night ran is not written back by it.
func TestNightDoesNotKeepABlockTheOwnerJustDropped(t *testing.T) {
	for name, drop := range map[string]func(){
		"check off": func() { _, _ = SetScanBeforeUpdate(false, nightOne) },
		"app off":   func() { _, _ = SetAppAutoUpdate("jellyfin", false, nightOne) },
	} {
		t.Run(name, func(t *testing.T) {
			nightWithABlock(t)
			blockedOne := autoUpdateOne
			autoUpdateOne = func(ctx context.Context, app string) (codegen.ComposeAppUpdateRunAppState, string, []BlockedImage) {
				drop()
				return blockedOne(ctx, app)
			}
			RunDueAutoUpdates(context.Background(), nightOne)

			state, err := readAutoUpdate()
			assert.NilError(t, err)
			assert.Equal(t, len(state.Blocked), 0)
		})
	}
}
