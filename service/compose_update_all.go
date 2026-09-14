package service

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// Every app at once.
//
// The grid updates one app at a time: a click, a card, a toast. A box with fifteen
// apps that all moved is fifteen clicks, and fifteen chances to miss one. This runs
// the same update for a list of apps in one go, one after another -- pulls are
// heavy and two at once help nobody -- and on the box itself rather than from a
// browser tab, so a laptop lid closed halfway does not leave half the apps updated
// and no record of which. What the run is doing is kept here, so the dashboard can
// show it, or come back to it.
//
// First the plan: what updating everything would do, app by app, image by image,
// so that a person confirms a list and not a word. Then the run.

// ErrUpdateAllRunning is answered to a second run asked for while one is going.
var ErrUpdateAllRunning = errors.New("an update of every app is already running")

// PlanUpdateAll says what updating every app at once would do, from what is already
// known: the catalogue, and the last image check. It asks no registry; the caller
// does first, if it wants the answer fresh. `available` is the update button's own
// answer for an app, `storeCompose` the catalogue's compose file for a store app.
func PlanUpdateAll(apps map[string]*ComposeApp, available func(*ComposeApp) bool, storeCompose func(string) (*ComposeApp, error)) []codegen.ComposeAppUpdatePlanApp {
	plan := []codegen.ComposeAppUpdatePlanApp{}

	names := make([]string, 0, len(apps))
	for name := range apps {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		app := apps[name]
		if !available(app) {
			continue
		}

		entry := codegen.ComposeAppUpdatePlanApp{
			Id:       name,
			Title:    name,
			Kind:     codegen.Image,
			Services: []codegen.ComposeAppUpdatePlanService{},
		}

		var store *ComposeApp
		if info, err := app.StoreInfo(false); err == nil && info != nil {
			if title := info.Title["en_us"]; title != "" {
				entry.Title = title
			}
			entry.Icon = info.Icon
			if info.StoreAppID != nil && *info.StoreAppID != "" {
				if s, err := storeCompose(string(*info.StoreAppID)); err == nil {
					store = s
				}
			}
		}

		// The catalogue moves an image: that is the update, service by service. When
		// it moves none, the update is a re-pull of the tags the app already names,
		// because the last image check found a newer build under one of them; the
		// check answers per app, so every service is listed as it is.
		changed := []codegen.ComposeAppUpdatePlanService{}
		all := []codegen.ComposeAppUpdatePlanService{}
		for _, service := range sortedServiceNames(app.Services) {
			image := app.Services[service].Image
			all = append(all, codegen.ComposeAppUpdatePlanService{Name: service, From: image, To: image})
			if store == nil {
				continue
			}
			storeService, ok := store.Services[service]
			if !ok || storeService.Image == image || !updateWritesStoreImage(image, storeService.Image) {
				continue
			}
			changed = append(changed, codegen.ComposeAppUpdatePlanService{Name: service, From: image, To: storeService.Image})
		}
		if len(changed) > 0 {
			entry.Kind = codegen.Catalogue
			entry.Services = changed
		} else {
			entry.Services = all
		}

		plan = append(plan, entry)
	}

	return plan
}

// The run in progress, or the last one. One at a time: a second list while the
// first is going would only queue behind it, and two lists is what a person clicked
// twice, not what they meant.
var updateAll struct {
	sync.Mutex
	run *codegen.ComposeAppUpdateRun
}

// CurrentUpdateAllRun is a copy of the run in progress or of the last one, or nil
// when none has run since this service started.
func CurrentUpdateAllRun() *codegen.ComposeAppUpdateRun {
	updateAll.Lock()
	defer updateAll.Unlock()

	return copyOfUpdateAllRun()
}

func copyOfUpdateAllRun() *codegen.ComposeAppUpdateRun {
	if updateAll.run == nil {
		return nil
	}

	run := *updateAll.run
	run.Apps = append([]codegen.ComposeAppUpdateRunApp(nil), updateAll.run.Apps...)

	return &run
}

// StartUpdateAll runs the apps named, in that order, one after another, and returns
// the run as it starts. `update` does one app and says what became of it.
func StartUpdateAll(ids []string, update func(ctx context.Context, id string) (codegen.ComposeAppUpdateRunAppState, string)) (*codegen.ComposeAppUpdateRun, error) {
	updateAll.Lock()
	defer updateAll.Unlock()

	if updateAll.run != nil && updateAll.run.FinishedAt == nil {
		return nil, ErrUpdateAllRunning
	}

	run := &codegen.ComposeAppUpdateRun{StartedAt: time.Now(), Apps: make([]codegen.ComposeAppUpdateRunApp, 0, len(ids))}
	for _, id := range ids {
		run.Apps = append(run.Apps, codegen.ComposeAppUpdateRunApp{Id: id, State: codegen.ComposeAppUpdateRunAppStateQueued})
	}
	updateAll.run = run

	go runUpdateAll(run, update)

	return copyOfUpdateAllRun(), nil
}

func runUpdateAll(run *codegen.ComposeAppUpdateRun, update func(ctx context.Context, id string) (codegen.ComposeAppUpdateRunAppState, string)) {
	for i := range run.Apps {
		setUpdateAllState(run, i, codegen.ComposeAppUpdateRunAppStateUpdating, "")
		state, message := update(context.Background(), run.Apps[i].Id)
		setUpdateAllState(run, i, state, message)
	}

	updateAll.Lock()
	defer updateAll.Unlock()
	now := time.Now()
	run.FinishedAt = &now
}

func setUpdateAllState(run *codegen.ComposeAppUpdateRun, i int, state codegen.ComposeAppUpdateRunAppState, message string) {
	updateAll.Lock()
	defer updateAll.Unlock()

	run.Apps[i].State = state
	if message == "" {
		run.Apps[i].Message = nil
	} else {
		run.Apps[i].Message = &message
	}
}

// UpdateOneForRun is what the run does to one app: what the update button does,
// the check its name promises included, waited for; and the reason, when nothing
// happens.
func UpdateOneForRun(ctx context.Context, id string) (codegen.ComposeAppUpdateRunAppState, string) {
	apps, err := MyService.Compose().List(ctx)
	if err != nil {
		return codegen.ComposeAppUpdateRunAppStateFailed, err.Error()
	}

	app, ok := apps[id]
	if !ok {
		return codegen.ComposeAppUpdateRunAppStateFailed, "not installed"
	}

	if err := CheckImageUpdatesForApp(ctx, app); err != nil {
		logger.Info("could not check images before updating", zap.Error(err), zap.String("appID", id))
	}
	// the cached answer is an hour old at most, which outlives the check just made
	MyService.AppStoreManagement().ForgetUpgradable(id)

	if available, reason := MyService.AppStoreManagement().UpdateAvailability(app); !available {
		return codegen.ComposeAppUpdateRunAppStateCurrent, reason
	}

	// the events of this update name the app, as the update button's do
	if err := app.UpdateNow(common.WithProperties(ctx, map[string]string{})); err != nil {
		return codegen.ComposeAppUpdateRunAppStateFailed, err.Error()
	}

	return codegen.ComposeAppUpdateRunAppStateUpdated, ""
}
