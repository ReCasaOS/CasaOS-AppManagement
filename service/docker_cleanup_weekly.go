package service

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// A cleanup that runs by itself, once a week, and what every cleanup freed.
//
// By itself it only ever takes the two things that hold the data of no app: the old
// versions an update replaced, and the build cache. Images and volumes of no app stay a
// choice made in the window, after a list; nothing here can widen that.

// CleanupStatePath is where the switch and the last runs are kept. A variable so a test
// can put it somewhere of its own.
var CleanupStatePath = "/var/lib/casaos/cleanup.json"

// cleanupRunsKept is how many runs are remembered: half a year of weeks, with room for
// what was freed by hand in between.
const cleanupRunsKept = 40

// The kinds of run the history tells apart.
const (
	CleanupRunWeekly      = "weekly"
	CleanupRunOldVersions = "old_versions"
	CleanupRunBuildCache  = "build_cache"
	CleanupRunImages      = "images"
	CleanupRunVolumes     = "volumes"
)

// cleanupSlot is when the weekly cleanup comes round: Sunday at four, on the box's clock,
// when a home server has the least to do.
var cleanupSlot = BackupSchedule{Every: ScheduleWeekly, Weekday: time.Sunday, At: "04:00"}

// osUpgradeOpenForCleanup is a variable so a test can open an upgrade without the core's file.
var osUpgradeOpenForCleanup = osUpgradeOpen

type cleanupState struct {
	Weekly bool `json:"weekly"`
	// the slot last run, or the one that was current when the switch was turned on
	LastSlot time.Time            `json:"last_slot"`
	Runs     []codegen.CleanupRun `json:"runs"`
}

var cleanupStateLock sync.Mutex

func readCleanupState() (cleanupState, error) {
	state := cleanupState{Runs: []codegen.CleanupRun{}}
	content, err := os.ReadFile(CleanupStatePath)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := stdjson.Unmarshal(content, &state); err != nil {
		return state, err
	}
	if state.Runs == nil {
		state.Runs = []codegen.CleanupRun{}
	}

	return state, nil
}

// Written beside and renamed, like the backups' files: a file cut short by a power cut
// would turn the switch off without anybody knowing.
func writeCleanupState(state cleanupState) error {
	encoded, err := stdjson.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(CleanupStatePath), 0o700); err != nil {
		return err
	}
	temp := CleanupStatePath + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return err
	}

	return os.Rename(temp, CleanupStatePath)
}

// CleanupSchedule is the switch, when the next run is due, and the last runs, newest first.
func CleanupSchedule(now time.Time) (*codegen.CleanupSchedule, error) {
	cleanupStateLock.Lock()
	defer cleanupStateLock.Unlock()

	state, err := readCleanupState()
	if err != nil {
		return nil, err
	}

	return cleanupScheduleOf(state, now), nil
}

func cleanupScheduleOf(state cleanupState, now time.Time) *codegen.CleanupSchedule {
	schedule := &codegen.CleanupSchedule{Weekly: state.Weekly, Runs: []codegen.CleanupRun{}}
	for i := len(state.Runs) - 1; i >= 0; i-- {
		schedule.Runs = append(schedule.Runs, state.Runs[i])
	}
	if state.Weekly {
		if slot, err := mostRecentSlot(cleanupSlot, now); err == nil {
			next := time.Date(slot.Year(), slot.Month(), slot.Day()+7, slot.Hour(), slot.Minute(), 0, 0, slot.Location())
			if slot.After(state.LastSlot) {
				// a slot that passed while the box was off or busy: due at the next tick
				next = slot
			}
			schedule.Next = &next
		}
	}

	return schedule
}

// SetCleanupWeekly turns the weekly cleanup on or off. Turned on, the first run is the
// next Sunday: the slot that just passed is not caught up, or flipping the switch on a
// Monday would delete at once what the owner has not looked at.
func SetCleanupWeekly(weekly bool, now time.Time) (*codegen.CleanupSchedule, error) {
	cleanupStateLock.Lock()
	defer cleanupStateLock.Unlock()

	state, err := readCleanupState()
	if err != nil {
		return nil, err
	}
	if weekly && !state.Weekly {
		slot, err := mostRecentSlot(cleanupSlot, now)
		if err != nil {
			return nil, err
		}
		state.LastSlot = slot
	}
	state.Weekly = weekly
	if err := writeCleanupState(state); err != nil {
		return nil, err
	}

	return cleanupScheduleOf(state, now), nil
}

// RecordCleanupRun adds one run to the history, and with slot set, says that slot is done.
func RecordCleanupRun(run codegen.CleanupRun, slot *time.Time) error {
	cleanupStateLock.Lock()
	defer cleanupStateLock.Unlock()

	state, err := readCleanupState()
	if err != nil {
		return err
	}
	state.Runs = append(state.Runs, run)
	if len(state.Runs) > cleanupRunsKept {
		state.Runs = state.Runs[len(state.Runs)-cleanupRunsKept:]
	}
	if slot != nil {
		state.LastSlot = *slot
	}

	return writeCleanupState(state)
}

// RunDueCleanup runs the weekly cleanup when its slot has come and the switch is on.
// What is not due costs one file read.
func RunDueCleanup(ctx context.Context, now time.Time) {
	cleanupStateLock.Lock()
	state, err := readCleanupState()
	cleanupStateLock.Unlock()
	if err != nil {
		logger.Error("weekly cleanup: its settings cannot be read", zap.Error(err))
		return
	}
	if !state.Weekly {
		return
	}
	slot, err := mostRecentSlot(cleanupSlot, now)
	if err != nil || !slot.After(state.LastSlot) {
		return
	}
	// an upgrade of the system that cannot be ruled out is told as open: the cleanup waits
	if open, _ := osUpgradeOpenForCleanup(); open {
		return
	}

	cli, err := newCleanupDaemon()
	if err != nil {
		logger.Error("weekly cleanup: the daemon cannot be reached", zap.Error(err))
		return
	}
	defer cli.Close()

	runWeeklyCleanup(ctx, cli, slot, now)
}

// runWeeklyCleanup frees the old versions and the build cache, and writes what it did.
// Held by something that runs, it writes nothing and leaves the slot undone: the next
// tick tries again, and the run happens once the box is quiet.
func runWeeklyCleanup(ctx context.Context, cli cleanupDaemon, slot, now time.Time) {
	run := codegen.CleanupRun{At: now, What: CleanupRunWeekly, Scheduled: true}

	old, err := pruneDanglingImages(ctx, cli)
	var held ErrCleanupHeld
	if errors.As(err, &held) && held.Reason == CleanupHeldBusy {
		return
	}
	if err == nil {
		run.Count, run.Size = old.Count, old.Size

		var cache int64
		if cache, err = pruneBuildCache(ctx, cli); err == nil {
			run.Size += cache
		}
	}
	if err != nil {
		// an app that cannot be read, or a daemon that refused: said in the history, and
		// the slot is done, or the same failure would be written every ten minutes
		message := err.Error()
		run.Error = &message
		logger.Error("weekly cleanup failed", zap.Error(err))
	} else {
		logger.Info("weekly cleanup", zap.Int("old versions", run.Count), zap.Int64("bytes", run.Size))
	}

	if err := RecordCleanupRun(run, &slot); err != nil {
		logger.Error("weekly cleanup: its run cannot be written", zap.Error(err))
	}
	// for the alerts: what the weekly cleanup freed, or why it could not
	if run.Error != nil {
		publishEvent(ctx, common.EventTypeCleanupError, map[string]string{common.PropertyTypeMessage.Name: *run.Error})
	} else {
		publishEvent(ctx, common.EventTypeCleanupEnd, map[string]string{
			common.PropertyTypeCleanupCount.Name: strconv.Itoa(run.Count),
			common.PropertyTypeCleanupSize.Name:  strconv.FormatInt(run.Size, 10),
		})
	}
}

// RecordManualCleanup writes what the owner freed by hand in the window. A history that
// cannot be written is logged and nothing more: the deletion is done either way.
func RecordManualCleanup(what string, count int, size int64) {
	if count == 0 && size == 0 {
		return
	}
	run := codegen.CleanupRun{At: BoxNow(), What: what, Count: count, Size: size}
	if err := RecordCleanupRun(run, nil); err != nil {
		logger.Error("cleanup: its run cannot be written", zap.Error(err))
	}
}
