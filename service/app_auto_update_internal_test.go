package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"gotest.tools/v3/assert"
)

// a Wednesday afternoon, the night before it, and the next one
var (
	afternoon = time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
	lastNight = time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)
	tonight   = time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
)

// nightBox has its file in a folder of its own, jellyfin, immich and a repository app
// installed, no upgrade of the system open, and an update that records what it was asked
// for and answers what `answers` says.
func nightBox(t *testing.T, answers map[string]codegen.ComposeAppUpdateRunAppState) *[]string {
	t.Helper()
	wasPath, wasUpgrade, wasOne, wasInstalled := AutoUpdatePath, osUpgradeOpenForAutoUpdate, autoUpdateOne, installedApps
	AutoUpdatePath = filepath.Join(t.TempDir(), "auto-update.json")
	osUpgradeOpenForAutoUpdate = func() (bool, error) { return false, nil }
	installedApps = func(context.Context) ([]string, error) { return []string{"immich", "jellyfin"}, nil }
	updated := &[]string{}
	autoUpdateOne = func(_ context.Context, app string) (codegen.ComposeAppUpdateRunAppState, string) {
		*updated = append(*updated, app)
		if state, ok := answers[app]; ok {
			if state == codegen.ComposeAppUpdateRunAppStateFailed {
				return state, "the app did not start: rolled back"
			}
			return state, ""
		}
		return codegen.ComposeAppUpdateRunAppStateCurrent, "no update"
	}
	t.Cleanup(func() {
		AutoUpdatePath, osUpgradeOpenForAutoUpdate, autoUpdateOne, installedApps = wasPath, wasUpgrade, wasOne, wasInstalled
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
