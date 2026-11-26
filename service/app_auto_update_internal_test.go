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
	"gotest.tools/v3/assert"
)

// a Wednesday afternoon, the night before it, and the next one
var (
	afternoon = time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
	lastNight = time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC)
	tonight   = time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC)
)

// nightBox has its file in a folder of its own, jellyfin, immich and a repository app
// installed, no upgrade of the system open, and an update that records what it was asked
// for and answers what `answers` says.
func nightBox(t *testing.T, answers map[string]codegen.ComposeAppUpdateRunAppState) *[]string {
	t.Helper()
	wasPath, wasUpgrade, wasOne, wasInstalled, wasRemoved, wasBusy, wasCheck := AutoUpdatePath, osUpgradeOpenForAutoUpdate, autoUpdateOne, installedApps, appRemoved, appBusy, autoUpdateCheck
	AutoUpdatePath = filepath.Join(t.TempDir(), "auto-update.json")
	osUpgradeOpenForAutoUpdate = func() (bool, error) { return false, nil }
	installedApps = func(context.Context) ([]string, error) { return []string{"immich", "jellyfin"}, nil }
	appRemoved = func(app string) bool { return app == "seerr" }
	appBusy = func(string) bool { return false }
	// every app has an update, and none waits: the tests of the wait set their own
	autoUpdateCheck = func(context.Context, string) (bool, codegen.ComposeAppUpdateRunAppState, string) { return true, "", "" }
	zero := 0
	assert.NilError(t, writeAutoUpdate(autoUpdateState{Apps: []string{}, MinDays: &zero}))
	updated := &[]string{}
	autoUpdateOne = func(_ context.Context, app string) (codegen.ComposeAppUpdateRunAppState, string, []BlockedImage) {
		*updated = append(*updated, app)
		if state, ok := answers[app]; ok {
			if state == codegen.ComposeAppUpdateRunAppStateFailed {
				return state, "the app did not start: rolled back", nil
			}
			return state, "", nil
		}
		return codegen.ComposeAppUpdateRunAppStateCurrent, "no update", nil
	}
	t.Cleanup(func() {
		AutoUpdatePath, osUpgradeOpenForAutoUpdate, autoUpdateOne, installedApps, appRemoved, appBusy, autoUpdateCheck = wasPath, wasUpgrade, wasOne, wasInstalled, wasRemoved, wasBusy, wasCheck
	})

	return updated
}

func TestNoAppUpdatesByItselfUntilItsOwnerTurnsItOn(t *testing.T) {
	updated := nightBox(t, nil)

	view, err := AutoUpdate(afternoon)
	assert.NilError(t, err)
	assert.Equal(t, len(view.Apps), 0)
	assert.Assert(t, view.Next == nil)

	RunDueAutoUpdates(context.Background(), tonight.Add(time.Hour))
	assert.Equal(t, len(*updated), 0)
}

// Turned on in the afternoon, the first update is that night, never at once.
func TestTheFirstAppTurnedOnWaitsForTheNextNight(t *testing.T) {
	updated := nightBox(t, nil)

	view, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Apps, []string{"jellyfin"})
	assert.Assert(t, view.Next.Equal(tonight), "%v", view.Next)

	RunDueAutoUpdates(context.Background(), afternoon.Add(time.Hour))
	assert.Equal(t, len(*updated), 0)
}

// Only the apps turned on, one after another, and what became of each is written.
func TestANightUpdatesTheAppsTurnedOnAndWritesWhatBecameOfEach(t *testing.T) {
	updated := nightBox(t, map[string]codegen.ComposeAppUpdateRunAppState{
		"immich": codegen.ComposeAppUpdateRunAppStateFailed,
	})
	_, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)
	_, err = SetAppAutoUpdate("immich", true, afternoon)
	assert.NilError(t, err)

	RunDueAutoUpdates(context.Background(), tonight.Add(10*time.Minute))
	assert.DeepEqual(t, *updated, []string{"immich", "jellyfin"})

	view, err := AutoUpdate(tonight.Add(time.Hour))
	assert.NilError(t, err)
	assert.Equal(t, len(view.Runs), 1)
	run := view.Runs[0]
	assert.Equal(t, run.Apps[0].Id, "immich")
	assert.Equal(t, run.Apps[0].State, codegen.ComposeAppUpdateRunAppStateFailed)
	assert.Equal(t, *run.Apps[0].Message, "the app did not start: rolled back")
	assert.Equal(t, run.Apps[1].State, codegen.ComposeAppUpdateRunAppStateCurrent)
	// done for tonight: the next is tomorrow
	assert.Assert(t, view.Next.Equal(tonight.AddDate(0, 0, 1)), "%v", view.Next)

	// a second tick the same night does nothing
	RunDueAutoUpdates(context.Background(), tonight.Add(20*time.Minute))
	assert.Equal(t, len(*updated), 2)
}

// While something runs on an app, or an upgrade of the system is open, the night waits
// and is still due.
func TestANightWaitsWhileTheBoxIsBusy(t *testing.T) {
	updated := nightBox(t, nil)
	_, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)

	end, err := Begin("immich", "backup")
	assert.NilError(t, err)
	RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
	end()
	assert.Equal(t, len(*updated), 0)

	osUpgradeOpenForAutoUpdate = func() (bool, error) { return true, nil }
	RunDueAutoUpdates(context.Background(), tonight.Add(11*time.Minute))
	assert.Equal(t, len(*updated), 0)

	view, err := AutoUpdate(tonight.Add(12 * time.Minute))
	assert.NilError(t, err)
	assert.Assert(t, view.Next.Equal(tonight), "still due: %v", view.Next)

	osUpgradeOpenForAutoUpdate = func() (bool, error) { return false, nil }
	RunDueAutoUpdates(context.Background(), tonight.Add(21*time.Minute))
	assert.DeepEqual(t, *updated, []string{"jellyfin"})
}

// An app removed since it was turned on is not updated, and is forgotten.
func TestAnAppThatIsGoneIsForgotten(t *testing.T) {
	updated := nightBox(t, nil)
	_, err := SetAppAutoUpdate("seerr", true, afternoon)
	assert.NilError(t, err)
	_, err = SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)

	RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
	assert.DeepEqual(t, *updated, []string{"jellyfin"})

	view, err := AutoUpdate(tonight.Add(time.Hour))
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Apps, []string{"jellyfin"})
}

// When the apps cannot be listed, nothing is updated and the night is still due.
func TestANightThatCannotListTheAppsIsTriedAgain(t *testing.T) {
	updated := nightBox(t, nil)
	_, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)
	installedApps = func(context.Context) ([]string, error) { return nil, errors.New("daemon down") }

	RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
	assert.Equal(t, len(*updated), 0)

	state, err := readAutoUpdate()
	assert.NilError(t, err)
	assert.Assert(t, state.LastSlot.Equal(lastNight), "%v", state.LastSlot)
}

// Turned off, an app is not updated; turned off last, nothing is due.
func TestAnAppTurnedOffIsLeftAlone(t *testing.T) {
	updated := nightBox(t, nil)
	_, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)
	view, err := SetAppAutoUpdate("jellyfin", false, afternoon)
	assert.NilError(t, err)
	assert.Equal(t, len(view.Apps), 0)
	assert.Assert(t, view.Next == nil)

	RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
	assert.Equal(t, len(*updated), 0)
}

// The job ticks every ten minutes, and a night of many apps lasts longer: a tick that
// comes while a night runs does not start it again.
func TestATickDuringANightDoesNotStartItAgain(t *testing.T) {
	nightBox(t, nil)
	_, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)

	calls := 0
	release := make(chan struct{})
	started := make(chan struct{})
	autoUpdateOne = func(context.Context, string) (codegen.ComposeAppUpdateRunAppState, string, []BlockedImage) {
		calls++
		close(started)
		<-release
		return codegen.ComposeAppUpdateRunAppStateUpdated, "", nil
	}

	done := make(chan struct{})
	go func() {
		RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
		close(done)
	}()
	<-started
	// the second tick, ten minutes later, while the first night still runs
	RunDueAutoUpdates(context.Background(), tonight.Add(11*time.Minute))
	close(release)
	<-done

	assert.Equal(t, calls, 1)
}

// A box off at two and back at nine skips the night: an app is never updated in the day.
func TestANightThatCannotStartWithinItsWindowIsSkipped(t *testing.T) {
	updated := nightBox(t, nil)
	_, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)

	RunDueAutoUpdates(context.Background(), tonight.Add(7*time.Hour))
	assert.Equal(t, len(*updated), 0)

	view, err := AutoUpdate(tonight.Add(7 * time.Hour))
	assert.NilError(t, err)
	assert.Assert(t, view.Next.Equal(tonight.AddDate(0, 0, 1)), "the next night, not a past one: %v", view.Next)
	assert.Equal(t, len(view.Runs), 0)
}

// An app Docker Compose does not list tonight, one waiting for its storage, keeps its
// switch: only an app whose folder is gone is forgotten.
func TestAnAppNotListedTonightKeepsItsSwitch(t *testing.T) {
	updated := nightBox(t, nil)
	_, err := SetAppAutoUpdate("waiting", true, afternoon)
	assert.NilError(t, err)

	RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
	assert.Equal(t, len(*updated), 0)

	view, err := AutoUpdate(tonight.Add(time.Hour))
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Apps, []string{"waiting"})
}

// An app turned off while the night runs is left alone.
func TestAnAppTurnedOffDuringTheNightIsLeftAlone(t *testing.T) {
	updated := nightBox(t, nil)
	_, err := SetAppAutoUpdate("immich", true, afternoon)
	assert.NilError(t, err)
	_, err = SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)
	autoUpdateOne = func(_ context.Context, app string) (codegen.ComposeAppUpdateRunAppState, string, []BlockedImage) {
		*updated = append(*updated, app)
		// the owner turns jellyfin off while immich updates
		_, err := SetAppAutoUpdate("jellyfin", false, tonight)
		assert.NilError(t, err)
		return codegen.ComposeAppUpdateRunAppStateUpdated, "", nil
	}

	RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
	assert.DeepEqual(t, *updated, []string{"immich"})
}

// Each failure stops the app for a while, and the next night would fail the same way:
// after two nights in a row it is left alone, until its switch goes off and on.
func TestAnAppThatFailedTwoNightsInARowIsLeftAlone(t *testing.T) {
	updated := nightBox(t, map[string]codegen.ComposeAppUpdateRunAppState{"jellyfin": codegen.ComposeAppUpdateRunAppStateFailed})
	_, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)

	RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
	RunDueAutoUpdates(context.Background(), tonight.AddDate(0, 0, 1).Add(time.Minute))
	RunDueAutoUpdates(context.Background(), tonight.AddDate(0, 0, 2).Add(time.Minute))
	assert.Equal(t, len(*updated), 2)

	view, err := AutoUpdate(tonight.AddDate(0, 0, 2).Add(time.Hour))
	assert.NilError(t, err)
	assert.Equal(t, len(view.Runs), 3)
	assert.Assert(t, view.Runs[0].Apps[0].Message != nil)
	assert.Assert(t, strings.Contains(*view.Runs[0].Apps[0].Message, "two nights in a row"), *view.Runs[0].Apps[0].Message)

	// off and on: tried again the next night
	_, err = SetAppAutoUpdate("jellyfin", false, afternoon)
	assert.NilError(t, err)
	_, err = SetAppAutoUpdate("jellyfin", true, tonight.AddDate(0, 0, 2).Add(time.Hour))
	assert.NilError(t, err)
	RunDueAutoUpdates(context.Background(), tonight.AddDate(0, 0, 3).Add(time.Minute))
	assert.Equal(t, len(*updated), 3)
}

// The box's own update waits for nothing but what is listed: the night is listed from
// its first look at an app to its last.
func TestTheNightIsListedWhileItRuns(t *testing.T) {
	nightBox(t, nil)
	_, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)
	listed := false
	autoUpdateOne = func(context.Context, string) (codegen.ComposeAppUpdateRunAppState, string, []BlockedImage) {
		for _, operation := range RunningOperations() {
			listed = listed || operation.App == autoUpdateMark
		}
		return codegen.ComposeAppUpdateRunAppStateUpdated, "", nil
	}

	RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
	assert.Assert(t, listed)
	for _, operation := range RunningOperations() {
		assert.Assert(t, operation.App != autoUpdateMark, "the mark goes with the night")
	}
}

// The night and an update of every app exclude each other.
func TestTheNightAndAnUpdateOfEveryAppExcludeEachOther(t *testing.T) {
	updated := nightBox(t, nil)
	_, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)

	// an update of every app runs: the night waits, still due
	updateAll.Lock()
	was := updateAll.run
	updateAll.run = &codegen.ComposeAppUpdateRun{StartedAt: tonight}
	updateAll.Unlock()
	RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
	assert.Equal(t, len(*updated), 0)
	updateAll.Lock()
	updateAll.run = was
	updateAll.Unlock()

	// the night runs: an update of every app is refused
	autoUpdateOne = func(context.Context, string) (codegen.ComposeAppUpdateRunAppState, string, []BlockedImage) {
		_, err := StartUpdateAll([]string{"immich"}, func(context.Context, string) (codegen.ComposeAppUpdateRunAppState, string, []BlockedImage) {
			return codegen.ComposeAppUpdateRunAppStateUpdated, "", nil
		})
		assert.ErrorIs(t, err, ErrUpdateAllRunning)
		return codegen.ComposeAppUpdateRunAppStateUpdated, "", nil
	}
	RunDueAutoUpdates(context.Background(), tonight.Add(11*time.Minute))
}

// An app busy with something else tonight is not counted as a failure.
func TestAnAppBusyTonightIsTriedTheNextNightWithoutAFailure(t *testing.T) {
	updated := nightBox(t, nil)
	_, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)
	appBusy = func(app string) bool { return app == "jellyfin" }

	RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
	assert.Equal(t, len(*updated), 0)
	state, err := readAutoUpdate()
	assert.NilError(t, err)
	assert.Equal(t, state.Failures["jellyfin"], 0)
}

// An image published broken is usually replaced within a day or two: an update waits until
// it has been known for the days the owner chose, counted from the night that found it.
func TestAnUpdateWaitsUntilItHasBeenKnownLongEnough(t *testing.T) {
	updated := nightBox(t, nil)
	_, err := SetAutoUpdateMinDays(3, afternoon)
	assert.NilError(t, err)
	_, err = SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)

	// found the first night, waits two more
	for night := 0; night < 3; night++ {
		RunDueAutoUpdates(context.Background(), tonight.AddDate(0, 0, night).Add(time.Minute))
	}
	assert.Equal(t, len(*updated), 0)
	view, err := AutoUpdate(tonight.AddDate(0, 0, 2).Add(time.Hour))
	assert.NilError(t, err)
	assert.Equal(t, view.MinDays, 3)
	assert.Equal(t, view.Runs[0].Apps[0].State, codegen.ComposeAppUpdateRunAppStateQueued)
	assert.Assert(t, strings.Contains(*view.Runs[0].Apps[0].Message, "applied from 2026-11-22"), *view.Runs[0].Apps[0].Message)

	// the fourth night, three days after it was found
	RunDueAutoUpdates(context.Background(), tonight.AddDate(0, 0, 3).Add(time.Minute))
	assert.DeepEqual(t, *updated, []string{"jellyfin"})

	// and the next update waits afresh
	state, err := readAutoUpdate()
	assert.NilError(t, err)
	_, still := state.Seen["jellyfin"]
	assert.Assert(t, !still)
}

// An app found up to date forgets the update it was waiting for: one that comes back
// later waits the whole time again.
func TestAnAppFoundUpToDateForgetsTheWait(t *testing.T) {
	nightBox(t, nil)
	_, err := SetAutoUpdateMinDays(3, afternoon)
	assert.NilError(t, err)
	_, err = SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)

	RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
	autoUpdateCheck = func(context.Context, string) (bool, codegen.ComposeAppUpdateRunAppState, string) {
		return false, codegen.ComposeAppUpdateRunAppStateCurrent, "up to date"
	}
	RunDueAutoUpdates(context.Background(), tonight.AddDate(0, 0, 1).Add(time.Minute))

	state, err := readAutoUpdate()
	assert.NilError(t, err)
	_, still := state.Seen["jellyfin"]
	assert.Assert(t, !still)
}

func TestTheWaitIsThreeDaysUntilChosenAndBounded(t *testing.T) {
	nightBox(t, nil)
	assert.NilError(t, os.Remove(AutoUpdatePath))

	view, err := AutoUpdate(afternoon)
	assert.NilError(t, err)
	assert.Equal(t, view.MinDays, 3)

	_, err = SetAutoUpdateMinDays(31, afternoon)
	assert.ErrorIs(t, err, ErrAutoUpdateMinDays)
	_, err = SetAutoUpdateMinDays(-1, afternoon)
	assert.ErrorIs(t, err, ErrAutoUpdateMinDays)
	view, err = SetAutoUpdateMinDays(0, afternoon)
	assert.NilError(t, err)
	assert.Equal(t, view.MinDays, 0)
}

// An app the automatic restart holds when its update starts is not a failure, and keeps
// the night its update was first found.
func TestAnAppTakenWhenItsUpdateStartsIsNoFailure(t *testing.T) {
	updated := nightBox(t, map[string]codegen.ComposeAppUpdateRunAppState{"jellyfin": codegen.ComposeAppUpdateRunAppStateFailed})
	_, err := SetAppAutoUpdate("jellyfin", true, afternoon)
	assert.NilError(t, err)
	RunDueAutoUpdates(context.Background(), tonight.Add(time.Minute))
	state, err := readAutoUpdate()
	assert.NilError(t, err)
	assert.Equal(t, state.Failures["jellyfin"], 1)
	seen := tonight.Add(-time.Hour)
	state.Seen = map[string]time.Time{"jellyfin": seen}
	assert.NilError(t, writeAutoUpdate(state))

	autoUpdateOne = func(_ context.Context, app string) (codegen.ComposeAppUpdateRunAppState, string, []BlockedImage) {
		*updated = append(*updated, app)
		return updateOutcome(ErrAppBusy{App: app, Running: "auto-restart"})
	}
	RunDueAutoUpdates(context.Background(), tonight.AddDate(0, 0, 1).Add(time.Minute))
	assert.Equal(t, len(*updated), 2)
	state, err = readAutoUpdate()
	assert.NilError(t, err)
	assert.Equal(t, state.Failures["jellyfin"], 1)
	assert.Assert(t, state.Seen["jellyfin"].Equal(seen), "%v", state.Seen["jellyfin"])
}
