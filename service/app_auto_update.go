package service

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
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

// autoUpdateFailuresHeld is how many nights in a row an app may fail before the night
// leaves it alone: each failure stops the app for a while, and the next night would
// fail the same way.
const autoUpdateFailuresHeld = 2

// autoUpdateSlot is when the apps update: every night at two, on the box's clock, before
// the window in which the box updates itself.
var autoUpdateSlot = BackupSchedule{Every: ScheduleDaily, At: "02:00"}

// autoUpdateWindow is how long after its slot a night may still run. A box that was off
// or busy all that time skips the night: an app is never updated in the afternoon.
const autoUpdateWindow = 2 * time.Hour

// autoUpdateMinDaysDefault is how many days an update must have been known before a
// night applies it, when the owner chose nothing. An image published broken is usually
// replaced within a day or two; three days lets that happen elsewhere first.
const autoUpdateMinDaysDefault = 3

// autoUpdateMinDaysMax is the longest wait the owner can choose.
const autoUpdateMinDaysMax = 30

// autoUpdateMark is the name the night is listed under while it runs, so that the box's
// own update waits for it.
const autoUpdateMark = "auto-update"

// blockedUpdate is what stopped an app's update: the images the scan refused, and the words
// the night says of it. The app waits for the registry to publish another image.
type blockedUpdate struct {
	Images  []BlockedImage `json:"images"`
	Message string         `json:"message"`
}

type autoUpdateState struct {
	Apps []string `json:"apps"`
	// the night last run or skipped, or the one current when the first app was turned on
	LastSlot time.Time `json:"last_slot"`
	// nights in a row each app failed; at autoUpdateFailuresHeld it is left alone
	Failures map[string]int `json:"failures,omitempty"`
	// when the night first found each app had an update, which the wait counts from
	Seen map[string]time.Time `json:"seen,omitempty"`
	// days an update must have been known before a night applies it; nil is the default
	MinDays *int `json:"min_days,omitempty"`
	// the apps whose update the scan stopped, and on what; the owner was told when it was written
	Blocked map[string]blockedUpdate `json:"blocked,omitempty"`
	// whether an update is checked by the scan module before it is applied; nil is yes
	ScanBeforeUpdate *bool                   `json:"scan_before_update,omitempty"`
	Runs             []codegen.AutoUpdateRun `json:"runs"`
}

func (s autoUpdateState) scanBeforeUpdate() bool { return s.ScanBeforeUpdate == nil || *s.ScanBeforeUpdate }

// minDays is the wait the owner chose, or the default.
func (s autoUpdateState) minDays() int {
	if s.MinDays == nil {
		return autoUpdateMinDaysDefault
	}

	return *s.MinDays
}

var autoUpdateLock sync.Mutex

// autoUpdateNight is held while a night runs. The job ticks every ten minutes and a night
// of many apps lasts longer: a second tick must not start the same night again while the
// first has not written that it is done.
var autoUpdateNight sync.Mutex

func readAutoUpdate() (autoUpdateState, error) {
	state := autoUpdateState{Apps: []string{}, Runs: []codegen.AutoUpdateRun{}, Failures: map[string]int{}, Seen: map[string]time.Time{}, Blocked: map[string]blockedUpdate{}}
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
	if state.Failures == nil {
		state.Failures = map[string]int{}
	}
	if state.Seen == nil {
		state.Seen = map[string]time.Time{}
	}
	if state.Blocked == nil {
		state.Blocked = map[string]blockedUpdate{}
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
	view := &codegen.AutoUpdate{
		Apps: append([]string{}, state.Apps...), MinDays: state.minDays(), Runs: []codegen.AutoUpdateRun{},
		ScanBeforeUpdate: state.scanBeforeUpdate(), ScanAvailable: scanModulePresent(),
	}
	for i := len(state.Runs) - 1; i >= 0; i-- {
		view.Runs = append(view.Runs, state.Runs[i])
	}
	if len(state.Apps) > 0 {
		if slot, err := mostRecentSlot(autoUpdateSlot, now); err == nil {
			next := time.Date(slot.Year(), slot.Month(), slot.Day()+1, slot.Hour(), slot.Minute(), 0, 0, slot.Location())
			if slot.After(state.LastSlot) && now.Before(slot.Add(autoUpdateWindow)) {
				// tonight's, not run yet: due at the next tick
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
// would update at once. Turning an app on again clears the failures that held it.
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
		delete(state.Failures, app)
	case !on && has:
		state.Apps = slices.DeleteFunc(state.Apps, func(name string) bool { return name == app })
		delete(state.Failures, app)
		delete(state.Seen, app)
		delete(state.Blocked, app)
	default:
		// nothing changes: nothing is written
		return autoUpdateOf(state, now), nil
	}
	if err := writeAutoUpdate(state); err != nil {
		return nil, err
	}

	return autoUpdateOf(state, now), nil
}

// SetAutoUpdateMinDays sets how many days an update must have been known before a night
// applies it.
func SetAutoUpdateMinDays(days int, now time.Time) (*codegen.AutoUpdate, error) {
	if days < 0 || days > autoUpdateMinDaysMax {
		return nil, ErrAutoUpdateMinDays
	}

	autoUpdateLock.Lock()
	defer autoUpdateLock.Unlock()

	state, err := readAutoUpdate()
	if err != nil {
		return nil, err
	}
	state.MinDays = &days
	if err := writeAutoUpdate(state); err != nil {
		return nil, err
	}

	return autoUpdateOf(state, now), nil
}

// ScanBeforeUpdate says whether an update asks the scan module first. On unless the owner
// turned it off: a file that cannot be read does not turn the check off.
func ScanBeforeUpdate() bool {
	autoUpdateLock.Lock()
	defer autoUpdateLock.Unlock()

	state, err := readAutoUpdate()

	return err != nil || state.scanBeforeUpdate()
}

// SetScanBeforeUpdate turns the check on or off for every update: the button, the update
// of every app and the night. Turning it off forgets the blocks, which the night would
// otherwise keep honouring for an image the owner now accepts.
func SetScanBeforeUpdate(on bool, now time.Time) (*codegen.AutoUpdate, error) {
	autoUpdateLock.Lock()
	defer autoUpdateLock.Unlock()

	state, err := readAutoUpdate()
	if err != nil {
		return nil, err
	}
	state.ScanBeforeUpdate = &on
	if !on {
		state.Blocked = map[string]blockedUpdate{}
	}
	if err := writeAutoUpdate(state); err != nil {
		return nil, err
	}

	return autoUpdateOf(state, now), nil
}

// ErrAutoUpdateMinDays is a wait out of bounds.
var ErrAutoUpdateMinDays = errors.New("min_days: from 0 to 30 days")

// osUpgradeOpenForAutoUpdate is a variable so a test can open an upgrade without the
// core's file.
var osUpgradeOpenForAutoUpdate = osUpgradeOpen

// autoUpdateCheck says whether an app has an update, asking the registries again. A
// variable so a test asks none.
var autoUpdateCheck = UpdateAvailableForRun

// registryDigestOf is what the registry publishes for an image now. A variable so a test
// asks no registry.
var registryDigestOf = docker.RegistryDigest

// updateTargetImages is the image each service of an app would be given by an update run
// now, read from the catalogue without writing anything. A variable so a test asks no
// catalogue.
var updateTargetImages = func(ctx context.Context, app string) (map[string]string, error) {
	apps, err := MyService.Compose().List(ctx)
	if err != nil {
		return nil, err
	}
	running, ok := apps[app]
	if !ok {
		return nil, fmt.Errorf("%s is not installed", app)
	}
	// a git app's update writes back its own file: its images stay
	if _, err := loadGitApp(app); err == nil {
		return targetImages(running, nil, nil), nil
	}
	store, err := running.storeComposeAppForUpdate()
	if err != nil || store == nil {
		return targetImages(running, nil, nil), err
	}
	if len(running.ComposeFiles) == 0 {
		return nil, ErrComposeFileNotFound
	}
	// the update decides on the images as written, `.env` references kept, as updatedComposeYAML does
	keys, err := running.EnvKeys()
	if err != nil {
		return nil, err
	}
	written, err := LoadComposeAppForEditing(app, running.ComposeFiles[0], keys)
	if err != nil {
		return nil, err
	}

	return targetImages(running, written, store), nil
}

// targetImages is what updatedComposeYAML writes, service by service: the catalogue's image
// where the update writes it, the app's own (as it runs) everywhere else. Whether it writes is
// decided on the image as written in the file (`app:${TAG}`), when written has the service.
// A nil store keeps every image.
// shortcut: an override file beside the main one that sets an image is not read; the ref
// then differs from the one blocked, and the night tries rather than wrongly skips.
func targetImages(running, written, store *ComposeApp) map[string]string {
	images := map[string]string{}
	for name, service := range running.Services {
		images[name] = service.Image
		if store == nil {
			continue
		}
		asWritten := service.Image
		if written != nil {
			if w, ok := written.Services[name]; ok {
				asWritten = w.Image
			}
		}
		if fromStore, ok := store.Services[name]; ok && updateWritesStoreImage(asWritten, fromStore.Image) {
			images[name] = fromStore.Image
		}
	}

	return images
}

// stillBlocked says tonight's update would pull, for every service that blocked it, the
// very reference that was blocked, and the registry still publishes the very image that
// blocked it: trying again would be refused again. Any doubt, a registry that does not
// answer or a target that cannot be read included, says no, so that the night tries.
func stillBlocked(ctx context.Context, app string, images []BlockedImage) bool {
	if len(images) == 0 {
		return false
	}
	target, err := updateTargetImages(ctx, app)
	if err != nil {
		return false
	}
	for _, image := range images {
		if target[image.Service] != image.Image {
			return false
		}
		digest, err := registryDigestOf(image.Image)
		if err != nil || !docker.ContainsDigest(image.RepoDigests, digest) {
			return false
		}
	}

	return true
}

// sameBlock says a block is on the very images another was: told already.
func sameBlock(a, b []BlockedImage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Service != b[i].Service || a[i].ID != b[i].ID {
			return false
		}
	}

	return true
}

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

// appRemoved says an app is uninstalled: its folder among the apps is gone. An app Docker
// Compose does not list right now, one waiting for its storage, is not.
var appRemoved = func(app string) bool {
	_, err := os.Stat(filepath.Join(config.AppInfo.AppsPath, app))
	return errors.Is(err, os.ErrNotExist)
}

// appBusy is a variable so a test can hold an app without an operation.
var appBusy = held

// RunDueAutoUpdates updates the apps that are turned on when their night has come.
// What is not due costs one file read.
func RunDueAutoUpdates(ctx context.Context, now time.Time) {
	if !autoUpdateNight.TryLock() {
		return
	}
	defer autoUpdateNight.Unlock()

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
	// Past its window, a night is skipped rather than run in the day.
	if !now.Before(slot.Add(autoUpdateWindow)) {
		logger.Info("automatic updates: the night was missed, the box was off or busy", zap.Time("night", slot))
		finishAutoUpdateNight(slot, nil, nil, nil, nil, nil, nil)
		return
	}
	// The night waits, slot undone, for what must not be crossed: an upgrade of the
	// system, an operation on an app, a cleanup, a move of the data of Docker, an update
	// of every app. The next tick tries again.
	if open, _ := osUpgradeOpenForAutoUpdate(); open {
		return
	}
	if cleanupHeld() != "" {
		return
	}
	if !startAutoUpdateNight() {
		return
	}
	defer endAutoUpdateNight()

	// Listed from the first look at an app to the last, so that the box's own update,
	// which waits for nothing else, waits for the night.
	done := markInProgress(autoUpdateMark, "update")
	defer done()

	runAutoUpdates(ctx, slot, now)
}

// runAutoUpdates updates the apps turned on that are installed, one after another, and
// writes what became of each. The list is read again before each app: one turned off
// while the night runs is left alone.
func runAutoUpdates(ctx context.Context, slot, now time.Time) {
	installed, err := installedApps(ctx)
	if err != nil {
		// nothing can be told of the apps: the night is tried again at the next tick
		logger.Error("automatic updates: the apps cannot be listed", zap.Error(err))
		return
	}

	autoUpdateLock.Lock()
	state, err := readAutoUpdate()
	autoUpdateLock.Unlock()
	if err != nil {
		logger.Error("automatic updates: their settings cannot be read", zap.Error(err))
		return
	}

	run := codegen.AutoUpdateRun{At: now, Apps: []codegen.ComposeAppUpdateRunApp{}}
	removed := []string{}
	failures := map[string]int{}
	// the apps blocked tonight; newlyBlocked is the ones whose block is new: the owner is told those
	blocks := map[string]blockedUpdate{}
	newlyBlocked := []string{}
	// the apps whose earlier block is lifted
	unblocked := map[string]bool{}
	// when each app's update was first found; a zero time forgets it
	seen := map[string]time.Time{}
	wait := time.Duration(state.minDays()) * 24 * time.Hour
	for _, app := range state.Apps {
		autoUpdateLock.Lock()
		current, err := readAutoUpdate()
		autoUpdateLock.Unlock()
		if err != nil || !slices.Contains(current.Apps, app) {
			continue
		}
		// an upgrade of the system opened during the night stops it here
		if open, _ := osUpgradeOpenForAutoUpdate(); open {
			logger.Info("automatic updates: an upgrade of the system opened, the night stops", zap.String("before", app))
			break
		}
		if !slices.Contains(installed, app) {
			if appRemoved(app) {
				removed = append(removed, app)
			}
			continue
		}
		// busy with something else tonight: no failure, the next night tries again
		if appBusy(app) {
			continue
		}
		entry := codegen.ComposeAppUpdateRunApp{Id: app}
		if current.Failures[app] >= autoUpdateFailuresHeld {
			message := "left alone: it failed two nights in a row; turn its switch off and on again to try once more"
			entry.State, entry.Message = codegen.ComposeAppUpdateRunAppStateFailed, &message
			run.Apps = append(run.Apps, entry)
			continue
		}

		available, result, message := autoUpdateCheck(ctx, app)
		if !available {
			entry.State = result
			if message != "" {
				entry.Message = &message
			}
			run.Apps = append(run.Apps, entry)
			// nothing to wait for any more
			seen[app] = time.Time{}
			// only a real "no update" lifts a block: a failed check says nothing of the image
			if result == codegen.ComposeAppUpdateRunAppStateCurrent {
				unblocked[app] = true
			}
			continue
		}
		// The wait counts from the night that first found the update: the box cannot
		// tell when the image was published, only when it saw it.
		first, known := current.Seen[app]
		if !known {
			first = now
			seen[app] = now
		}
		if due := first.Add(wait); now.Before(due) {
			waiting := "waiting: a new version was found on " + first.Format("2006-01-02") + "; applied from " + due.Format("2006-01-02")
			entry.State, entry.Message = codegen.ComposeAppUpdateRunAppStateQueued, &waiting
			run.Apps = append(run.Apps, entry)
			continue
		}

		// blocked by the very image the registry publishes still: not tried, not told again.
		// A block stands on a check that can run: without the module, or with the check off,
		// it is forgotten and the update runs.
		if kept, ok := current.Blocked[app]; ok {
			if !scanModulePresent() || !current.scanBeforeUpdate() {
				unblocked[app] = true
			} else if stillBlocked(ctx, app, kept.Images) {
				entry.State, entry.Message = codegen.ComposeAppUpdateRunAppStateBlocked, &kept.Message
				entry.Blocked = blockedView(kept.Images)
				run.Apps = append(run.Apps, entry)
				continue
			}
		}

		var blocked []BlockedImage
		result, message, blocked = autoUpdateOne(ctx, app)
		entry.State = result
		if message != "" {
			entry.Message = &message
		}
		if result == codegen.ComposeAppUpdateRunAppStateBlocked {
			entry.Blocked = blockedView(blocked)
		}
		run.Apps = append(run.Apps, entry)
		switch result {
		case codegen.ComposeAppUpdateRunAppStateBlocked:
			// not a failure, and the wait is not restarted: Failures and Seen stay as they are
			blocks[app] = blockedUpdate{Images: blocked, Message: message}
			if kept, ok := current.Blocked[app]; !ok || !sameBlock(kept.Images, blocked) {
				newlyBlocked = append(newlyBlocked, app)
			}
		case codegen.ComposeAppUpdateRunAppStateQueued:
			// taken by something else between appBusy and the update: as busy, nothing changes
		case codegen.ComposeAppUpdateRunAppStateFailed:
			failures[app] = current.Failures[app] + 1
			unblocked[app] = true
		default:
			failures[app] = 0
			unblocked[app] = true
			// updated, or up to date by the time it ran: the next update waits afresh
			seen[app] = time.Time{}
		}
		logger.Info("automatic update", zap.String("app", app), zap.String("state", string(result)), zap.String("message", message))
	}

	finishAutoUpdateNight(slot, &run, removed, failures, seen, blocks, unblocked)
	tellAutoUpdateNight(ctx, run, newlyBlocked)
}

// publishEvent is how the night, the newer versions, the cleanup and an app's update and
// apply tell the bus: a variable so a test hears them, and leaves no goroutine behind that
// reads MyService.
var publishEvent = PublishEventWrapper

// tellAutoUpdateNight says what the night did, for the alerts: the apps it updated and the
// ones whose update failed. A night that changed nothing says nothing.
func tellAutoUpdateNight(ctx context.Context, run codegen.AutoUpdateRun, blocked []string) {
	var updated, failed []string
	for _, app := range run.Apps {
		switch app.State {
		case codegen.ComposeAppUpdateRunAppStateUpdated:
			updated = append(updated, app.Id)
		case codegen.ComposeAppUpdateRunAppStateFailed:
			// left alone after two failed nights: told when it failed, not every night after
			if app.Message == nil || !strings.HasPrefix(*app.Message, "left alone") {
				failed = append(failed, app.Id)
			}
		}
	}
	if len(updated) == 0 && len(failed) == 0 && len(blocked) == 0 {
		return
	}
	publishEvent(ctx, common.EventTypeAutoUpdateNightEnd, map[string]string{
		common.PropertyTypeAutoUpdateUpdated.Name: strings.Join(updated, ","),
		common.PropertyTypeAutoUpdateFailed.Name:  strings.Join(failed, ","),
		common.PropertyTypeAutoUpdateBlocked.Name: strings.Join(blocked, ","),
	})
}

// finishAutoUpdateNight writes that the night is done, with its run when there was one,
// the apps that are gone forgotten, and the count of failures of each app updated.
func finishAutoUpdateNight(slot time.Time, run *codegen.AutoUpdateRun, removed []string, failures map[string]int, seen map[string]time.Time, blocks map[string]blockedUpdate, unblocked map[string]bool) {
	autoUpdateLock.Lock()
	defer autoUpdateLock.Unlock()

	state, err := readAutoUpdate()
	if err != nil {
		logger.Error("automatic updates: their night cannot be written", zap.Error(err))
		return
	}
	state.LastSlot = slot
	state.Apps = slices.DeleteFunc(state.Apps, func(name string) bool { return slices.Contains(removed, name) })
	for app, count := range failures {
		if count == 0 {
			delete(state.Failures, app)
		} else {
			state.Failures[app] = count
		}
	}
	for app, at := range seen {
		if at.IsZero() {
			delete(state.Seen, app)
		} else {
			state.Seen[app] = at
		}
	}
	for app := range unblocked {
		delete(state.Blocked, app)
	}
	// not for an app turned off, nor once the check is, while the night ran
	for app, block := range blocks {
		if slices.Contains(state.Apps, app) && state.scanBeforeUpdate() {
			state.Blocked[app] = block
		}
	}
	for _, app := range removed {
		delete(state.Failures, app)
		delete(state.Seen, app)
		delete(state.Blocked, app)
	}
	if run != nil {
		state.Runs = append(state.Runs, *run)
		if len(state.Runs) > autoUpdateRunsKept {
			state.Runs = state.Runs[len(state.Runs)-autoUpdateRunsKept:]
		}
	}
	if err := writeAutoUpdate(state); err != nil {
		logger.Error("automatic updates: their night cannot be written", zap.Error(err))
	}
}

// startAutoUpdateNight claims the updates for the night, or says an update of every app
// runs. The night and that run exclude each other: two pulls at once, and an app found
// busy by the one that lost the race, is what a person sees as a failure.
func startAutoUpdateNight() bool {
	updateAll.Lock()
	defer updateAll.Unlock()

	if updateAll.run != nil && updateAll.run.FinishedAt == nil {
		return false
	}
	updateAll.night = true

	return true
}

func endAutoUpdateNight() {
	updateAll.Lock()
	defer updateAll.Unlock()
	updateAll.night = false
}

// InstalledAppsForAutoUpdate is the apps the night can update.
func InstalledAppsForAutoUpdate(ctx context.Context) ([]string, error) { return installedApps(ctx) }
