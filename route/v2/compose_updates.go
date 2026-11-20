package v2

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

// Every app at once: the plan, the run, and what became of it. See
// service/compose_update_all.go.

// CheckImageUpdates is the registry sweep `check=true` asks for; a var so a test can see it run.
var CheckImageUpdates = service.CheckImageUpdates

func (a *AppManagement) ComposeAppUpdatePlan(ctx echo.Context, params codegen.ComposeAppUpdatePlanParams) error {
	requestCtx := ctx.Request().Context()
	// a sweep acts on every app and writes the shared cache: only the owner starts one
	check := params.Check != nil && *params.Check && isOwner(ctx)

	// The plan is only as fresh as the last image check, and a person about to
	// update everything wants it fresh: the same sweep the `Check for image
	// updates` button runs, on the request's context so that a closed tab stops
	// it. A registry that does not answer is not fatal; the catalogue still has
	// something to say.
	if check {
		if _, err := CheckImageUpdates(requestCtx); err != nil {
			logger.Info("could not check images before planning an update of every app", zap.Error(err))
		}
	}

	apps, err := service.MyService.Compose().List(requestCtx)
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}
	apps = visibleApps(ctx, apps)

	store := service.MyService.AppStoreManagement()
	if check {
		// the cached answers are an hour old at most, which outlives the check just made
		for id := range apps {
			store.ForgetUpgradable(id)
		}
	}

	plan := service.PlanUpdateAll(apps, store.IsUpdateAvailable, store.ComposeApp)

	return ctx.JSON(http.StatusOK, codegen.ComposeAppUpdatePlanOK{Data: &codegen.ComposeAppUpdatePlan{Apps: plan}})
}

func (a *AppManagement) UpdateComposeApps(ctx echo.Context) error {
	var request codegen.ComposeAppUpdateRequest
	if err := ctx.Bind(&request); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}
	if len(request.Ids) == 0 {
		message := "nothing to update: the list of apps is empty"
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	run, err := service.StartUpdateAll(request.Ids, service.UpdateOneForRun)
	if err != nil {
		message := err.Error()
		if errors.Is(err, service.ErrUpdateAllRunning) {
			return ctx.JSON(http.StatusConflict, codegen.ResponseConflict{Message: &message})
		}

		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	return ctx.JSON(http.StatusAccepted, codegen.ComposeAppUpdateRunOK{Data: run})
}

var noUpdateRun = "no update of every app has run since this service started"

func (a *AppManagement) ComposeAppUpdateRun(ctx echo.Context) error {
	run := service.CurrentUpdateAllRun()
	if run == nil {
		return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &noUpdateRun})
	}
	visible := visibleFilter(ctx)
	run.Apps = lo.Filter(run.Apps, func(app codegen.ComposeAppUpdateRunApp, _ int) bool { return visible(app.Id) })
	// a run of hidden apps alone is, for this caller, no run at all
	if len(run.Apps) == 0 {
		return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &noUpdateRun})
	}

	return ctx.JSON(http.StatusOK, codegen.ComposeAppUpdateRunOK{Data: run})
}

// AutoUpdate is the apps that update by themselves and the last nights.
func (a *AppManagement) AutoUpdate(ctx echo.Context) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}
	view, err := service.AutoUpdate(service.BoxNow())
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	return ctx.JSON(http.StatusOK, codegen.AutoUpdateOK{Data: view})
}

func (a *AppManagement) SetAppAutoUpdate(ctx echo.Context, id codegen.ComposeAppID) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}
	// a pointer: a body that leaves `enabled` out must not read as "turn it off"
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := ctx.Bind(&body); err != nil || body.Enabled == nil {
		message := "enabled: true or false is required"
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	// turning off is always allowed, so an app that is gone can still be taken off
	if *body.Enabled {
		installed, err := service.InstalledAppsForAutoUpdate(ctx.Request().Context())
		if err != nil {
			message := err.Error()
			return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
		}
		if !slices.Contains(installed, id) {
			message := fmt.Sprintf("`%s` is not an app the night can update: it is not installed, or it is built from a repository", id)
			return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
		}
	}

	view, err := service.SetAppAutoUpdate(id, *body.Enabled, service.BoxNow())
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}
	logger.Info("automatic update switched", zap.String("app", id), zap.Bool("on", *body.Enabled))

	return ctx.JSON(http.StatusOK, codegen.AutoUpdateOK{Data: view})
}
