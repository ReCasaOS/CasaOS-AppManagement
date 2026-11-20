package service

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// Apps that update by themselves, at night, one app at a time.
//
// Off for every app until its owner turns it on, app by app: an update is the one thing
// here that can break an app with nobody watching. What runs is the update button's own
// update, rollback included: an app that does not start again is put back on the files
// and the images it ran, and the failure raises the alert every failed update raises.

// AutoUpdatePath is where the apps that update by themselves and the last runs are
// kept. A variable so a test can put it somewhere of its own.
var AutoUpdatePath = "/var/lib/casaos/auto-update.json"

// autoUpdateRunsKept is how many nights are remembered.
const autoUpdateRunsKept = 30

// autoUpdateSlot is when the apps update: every night at three, on the box's clock.
var autoUpdateSlot = BackupSchedule{Every: ScheduleDaily, At: "03:00"}

type autoUpdateState struct {
	Apps []string `json:"apps"`
	// the night last run, or the one that was current when the first app was turned on
	LastSlot time.Time               `json:"last_slot"`
	Runs     []codegen.AutoUpdateRun `json:"runs"`
}

var autoUpdateLock sync.Mutex

func readAutoUpdate() (autoUpdateState, error) {
	state := autoUpdateState{Apps: []string{}, Runs: []codegen.AutoUpdateRun{}}
	content, err := os.ReadFile(AutoUpdatePath)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := stdjson.Unmarshal(content, &state); err != nil {
		return state, err
	}
	if state.Apps == nil {
		state.Apps = []string{}
	}
	if state.Runs == nil {
		state.Runs = []codegen.AutoUpdateRun{}
	}

	return state, nil
}

// Written beside and renamed: a file cut short would turn every app's switch off.
func writeAutoUpdate(state autoUpdateState) error {
	encoded, err := stdjson.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(AutoUpdatePath), 0o700); err != nil {
		return err
	}
	temp := AutoUpdatePath + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return err
	}

	return os.Rename(temp, AutoUpdatePath)
}

func autoUpdateOf(state autoUpdateState, now time.Time) *codegen.AutoUpdate {
	view := &codegen.AutoUpdate{Apps: append([]string{}, state.Apps...), Runs: []codegen.AutoUpdateRun{}}
	for i := len(state.Runs) - 1; i >= 0; i-- {
		view.Runs = append(view.Runs, state.Runs[i])
	}
	if len(state.Apps) > 0 {
		if slot, err := mostRecentSlot(autoUpdateSlot, now); err == nil {
			next := time.Date(slot.Year(), slot.Month(), slot.Day()+1, slot.Hour(), slot.Minute(), 0, 0, slot.Location())
			if slot.After(state.LastSlot) {
				// a night that passed while the box was off or busy: due at the next tick
				next = slot
			}
			view.Next = &next
		}
	}

	return view
}

// AutoUpdate is the apps that update by themselves, when the next night is, and the
// last nights, newest first.
func AutoUpdate(now time.Time) (*codegen.AutoUpdate, error) {
	autoUpdateLock.Lock()
	defer autoUpdateLock.Unlock()

	state, err := readAutoUpdate()
	if err != nil {
		return nil, err
	}

	return autoUpdateOf(state, now), nil
}

// SetAppAutoUpdate turns the nightly update of one app on or off. The first app turned
// on waits for the next night: the one that just passed is not caught up, or the switch
// would update at once.
func SetAppAutoUpdate(app string, on bool, now time.Time) (*codegen.AutoUpdate, error) {
	autoUpdateLock.Lock()
	defer autoUpdateLock.Unlock()

	state, err := readAutoUpdate()
	if err != nil {
		return nil, err
	}
	has := slices.Contains(state.Apps, app)
	switch {
	case on && !has:
		if len(state.Apps) == 0 {
			slot, err := mostRecentSlot(autoUpdateSlot, now)
			if err != nil {
				return nil, err
			}
			state.LastSlot = slot
		}
		state.Apps = append(state.Apps, app)
		sort.Strings(state.Apps)
	case !on && has:
		state.Apps = slices.DeleteFunc(state.Apps, func(name string) bool { return name == app })
	}
	if err := writeAutoUpdate(state); err != nil {
		return nil, err
	}

	return autoUpdateOf(state, now), nil
}

// osUpgradeOpenForAutoUpdate is a variable so a test can open an upgrade without the
// core's file.
var osUpgradeOpenForAutoUpdate = osUpgradeOpen

// autoUpdateOne updates one app as the run of every app does. A variable so a test runs
// no update.
var autoUpdateOne = UpdateOneForRun

// installedApps is the apps the night can update: the compose apps, without the ones
// built from a repository, which deploy by themselves already. A variable so a test
// installs what it likes.
var installedApps = func(ctx context.Context) ([]string, error) {
	apps, err := MyService.Compose().List(ctx)
	if err != nil {
		return nil, err
	}
	gitApps, err := GitAppNames()
	if err != nil {
		return nil, err
	}
	names := []string{}
	for name := range apps {
		if !slices.Contains(gitApps, name) {
			names = append(names, name)
		}
	}

	return names, nil
}

// RunDueAutoUpdates updates the apps that are turned on when their night has come.
// What is not due costs one file read.
func RunDueAutoUpdates(ctx context.Context, now time.Time) {
	autoUpdateLock.Lock()
	state, err := readAutoUpdate()
	autoUpdateLock.Unlock()
	if err != nil {
		logger.Error("automatic updates: their settings cannot be read", zap.Error(err))
		return
	}
	if len(state.Apps) == 0 {
		return
	}
	slot, err := mostRecentSlot(autoUpdateSlot, now)
	if err != nil || !slot.After(state.LastSlot) {
		return
	}
	// The night waits, slot undone, for what must not be crossed: an upgrade of the
	// system, an operation on an app, a cleanup, a move of the data of Docker. The next
	// tick tries again.
	if open, _ := osUpgradeOpenForAutoUpdate(); open {
		return
	}
	if cleanupHeld() != "" {
		return
	}

	runAutoUpdates(ctx, state.Apps, slot, now)
}

// runAutoUpdates updates the apps asked for that are installed, one after another, and
// writes what became of each. An app that is gone is forgotten.
func runAutoUpdates(ctx context.Context, apps []string, slot, now time.Time) {
	installed, err := installedApps(ctx)
	if err != nil {
		// nothing can be told of the apps: the night is tried again at the next tick
		logger.Error("automatic updates: the apps cannot be listed", zap.Error(err))
		return
	}

	run := codegen.AutoUpdateRun{At: now, Apps: []codegen.ComposeAppUpdateRunApp{}}
	for _, app := range apps {
		if !slices.Contains(installed, app) {
			continue
		}
		state, message := autoUpdateOne(ctx, app)
		entry := codegen.ComposeAppUpdateRunApp{Id: app, State: state}
		if message != "" {
			entry.Message = &message
		}
		run.Apps = append(run.Apps, entry)
		logger.Info("automatic update", zap.String("app", app), zap.String("state", string(state)), zap.String("message", message))
	}

	autoUpdateLock.Lock()
	defer autoUpdateLock.Unlock()
	state, err := readAutoUpdate()
	if err != nil {
		logger.Error("automatic updates: their night cannot be written", zap.Error(err))
		return
	}
	state.LastSlot = slot
	state.Apps = slices.DeleteFunc(state.Apps, func(name string) bool { return !slices.Contains(installed, name) })
	state.Runs = append(state.Runs, run)
	if len(state.Runs) > autoUpdateRunsKept {
		state.Runs = state.Runs[len(state.Runs)-autoUpdateRunsKept:]
	}
	if err := writeAutoUpdate(state); err != nil {
		logger.Error("automatic updates: their night cannot be written", zap.Error(err))
	}
}

// InstalledAppsForAutoUpdate is the apps the night can update.
func InstalledAppsForAutoUpdate(ctx context.Context) ([]string, error) { return installedApps(ctx) }
