package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
	"gotest.tools/v3/assert"
)

// weeklyBox is aBox with its cleanup file in a folder of its own, two old versions on the
// daemon, and no upgrade of the system open.
func weeklyBox(t *testing.T) *fakeCleanupDaemon {
	t.Helper()
	wasPath, wasUpgrade := CleanupStatePath, osUpgradeOpenForCleanup
	CleanupStatePath = filepath.Join(t.TempDir(), "cleanup.json")
	osUpgradeOpenForCleanup = func() (bool, error) { return false, nil }
	t.Cleanup(func() { CleanupStatePath, osUpgradeOpenForCleanup = wasPath, wasUpgrade })

	daemon := aBox(t)
	daemon.dangling = []image.Summary{{ID: "left", Size: 300}, {ID: "left-too", Size: 200}}

	return daemon
}

// a Wednesday, and the Sunday at four before it
var (
	wednesday  = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	lastSunday = time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)
	nextSunday = time.Date(2026, 11, 22, 4, 0, 0, 0, time.UTC)
)

func TestTheWeeklyCleanupIsOffUntilTheOwnerTurnsItOn(t *testing.T) {
	weeklyBox(t)

	schedule, err := CleanupSchedule(wednesday)
	assert.NilError(t, err)
	assert.Assert(t, !schedule.Weekly)
	assert.Assert(t, schedule.Next == nil)
	assert.Equal(t, len(schedule.Runs), 0)
}

// Flipped on a Wednesday, the switch must not delete at once what the owner has not
// looked at: the Sunday that passed is not caught up.
func TestTurnedOnTheFirstRunIsTheNextSunday(t *testing.T) {
	weeklyBox(t)

	schedule, err := SetCleanupWeekly(true, wednesday)
	assert.NilError(t, err)
	assert.Assert(t, schedule.Weekly)
	assert.Assert(t, schedule.Next.Equal(nextSunday), "%v", schedule.Next)

	state, err := readCleanupState()
	assert.NilError(t, err)
	assert.Assert(t, state.LastSlot.Equal(lastSunday), "%v", state.LastSlot)
}

func TestTheWeeklyRunFreesTheOldVersionsAndTheBuildCacheOnly(t *testing.T) {
	daemon := weeklyBox(t)

	runWeeklyCleanup(context.Background(), daemon, nextSunday, nextSunday.Add(time.Minute))

	// by id, the two old versions; no tagged image, no volume
	assert.DeepEqual(t, daemon.images, []string{"left", "left-too"})
	assert.Equal(t, len(daemon.volumes), 0)
	assert.Assert(t, daemon.pruned != nil && daemon.pruned.All)
	assert.Assert(t, !daemon.forced)

	schedule, err := CleanupSchedule(nextSunday.Add(time.Hour))
	assert.NilError(t, err)
	assert.Equal(t, len(schedule.Runs), 1)
	run := schedule.Runs[0]
	assert.Equal(t, run.What, CleanupRunWeekly)
	assert.Assert(t, run.Scheduled)
	assert.Equal(t, run.Count, 2)
	// the two images, and the 42 bytes the fake cache gives back
	assert.Equal(t, run.Size, int64(542))
	assert.Assert(t, run.Error == nil)
}

// A run that finds the box busy writes nothing and leaves its slot undone: the next tick
// tries again.
func TestAWeeklyRunWaitsWhileSomethingRunsOnAnApp(t *testing.T) {
	daemon := weeklyBox(t)
	_, err := SetCleanupWeekly(true, wednesday)
	assert.NilError(t, err)
	end, err := Begin("jellyfin", "update")
	assert.NilError(t, err)

	runWeeklyCleanup(context.Background(), daemon, nextSunday, nextSunday.Add(time.Minute))
	assert.Equal(t, len(daemon.images), 0)
	schedule, err := CleanupSchedule(nextSunday.Add(time.Hour))
	assert.NilError(t, err)
	assert.Equal(t, len(schedule.Runs), 0)
	// still due, and said as due now rather than in a week
	assert.Assert(t, schedule.Next.Equal(nextSunday), "%v", schedule.Next)

	end()
	runWeeklyCleanup(context.Background(), daemon, nextSunday, nextSunday.Add(20*time.Minute))
	assert.Equal(t, len(daemon.images), 2)
	schedule, err = CleanupSchedule(nextSunday.Add(time.Hour))
	assert.NilError(t, err)
	assert.Equal(t, len(schedule.Runs), 1)
	assert.Assert(t, schedule.Next.Equal(nextSunday.AddDate(0, 0, 7)), "%v", schedule.Next)
}

// An app that cannot be read fails the run once, in the history, and the slot is done:
// the same failure is not written every ten minutes for a week.
func TestAWeeklyRunThatFailsIsWrittenOnce(t *testing.T) {
	daemon := weeklyBox(t)
	appsProtection = func(context.Context) (cleanupProtection, error) {
		return cleanupProtection{}, errors.New("compose file of `odd`: yaml")
	}

	runWeeklyCleanup(context.Background(), daemon, nextSunday, nextSunday.Add(time.Minute))

	schedule, err := CleanupSchedule(nextSunday.Add(time.Hour))
	assert.NilError(t, err)
	assert.Equal(t, len(schedule.Runs), 1)
	assert.Assert(t, schedule.Runs[0].Error != nil)
	assert.Equal(t, len(daemon.images), 0)

	state, err := readCleanupState()
	assert.NilError(t, err)
	assert.Assert(t, state.LastSlot.Equal(nextSunday))
}

func TestNothingRunsByItselfWhileTheSwitchIsOffOrTheSlotHasNotCome(t *testing.T) {
	weeklyBox(t)

	// off: nothing, whatever the day
	RunDueCleanup(context.Background(), nextSunday.Add(time.Hour))
	schedule, err := CleanupSchedule(nextSunday.Add(2 * time.Hour))
	assert.NilError(t, err)
	assert.Equal(t, len(schedule.Runs), 0)

	// on, and the next slot has not come
	_, err = SetCleanupWeekly(true, wednesday)
	assert.NilError(t, err)
	RunDueCleanup(context.Background(), wednesday.Add(24*time.Hour))
	schedule, err = CleanupSchedule(wednesday.Add(25 * time.Hour))
	assert.NilError(t, err)
	assert.Equal(t, len(schedule.Runs), 0)
}

// An upgrade of the operating system that is open, or that cannot be ruled out, holds
// the run before the daemon is even reached.
func TestAWeeklyRunWaitsForAnUpgradeOfTheSystem(t *testing.T) {
	weeklyBox(t)
	_, err := SetCleanupWeekly(true, wednesday)
	assert.NilError(t, err)
	osUpgradeOpenForCleanup = func() (bool, error) { return true, nil }

	RunDueCleanup(context.Background(), nextSunday.Add(time.Hour))

	state, err := readCleanupState()
	assert.NilError(t, err)
	assert.Assert(t, state.LastSlot.Equal(lastSunday), "the slot stays undone: %v", state.LastSlot)
	assert.Equal(t, len(state.Runs), 0)
}

func TestWhatWasFreedByHandIsInTheHistoryNewestFirstAndBounded(t *testing.T) {
	weeklyBox(t)

	RecordManualCleanup(CleanupRunImages, 0, 0) // nothing freed, nothing to say
	for i := 0; i < cleanupRunsKept+5; i++ {
		RecordManualCleanup(CleanupRunVolumes, 1, int64(i))
	}

	schedule, err := CleanupSchedule(wednesday)
	assert.NilError(t, err)
	assert.Equal(t, len(schedule.Runs), cleanupRunsKept)
	assert.Equal(t, schedule.Runs[0].Size, int64(cleanupRunsKept+4))
	assert.Assert(t, !schedule.Runs[0].Scheduled)
}
