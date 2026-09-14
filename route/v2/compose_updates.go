package v2

import (
	"errors"
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// Every app at once: the plan, the run, and what became of it. See
// service/compose_update_all.go.

func (a *AppManagement) ComposeAppUpdatePlan(ctx echo.Context, params codegen.ComposeAppUpdatePlanParams) error {
	requestCtx := ctx.Request().Context()

	// The plan is only as fresh as the last image check, and a person about to
	// update everything wants it fresh: the same sweep the `Check for image
	// updates` button runs, on the request's context so that a closed tab stops
	// it. A registry that does not answer is not fatal; the catalogue still has
	// something to say.
	if params.Check != nil && *params.Check {
		if _, err := service.CheckImageUpdates(requestCtx); err != nil {
			logger.Info("could not check images before planning an update of every app", zap.Error(err))
		}
	}

	apps, err := service.MyService.Compose().List(requestCtx)
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	store := service.MyService.AppStoreManagement()
	if params.Check != nil && *params.Check {
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

func (a *AppManagement) ComposeAppUpdateRun(ctx echo.Context) error {
	run := service.CurrentUpdateAllRun()
	if run == nil {
		message := "no update of every app has run since this service started"
		return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
	}

	return ctx.JSON(http.StatusOK, codegen.ComposeAppUpdateRunOK{Data: run})
}
