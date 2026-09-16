package v2

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/labstack/echo/v4"
	"github.com/samber/lo"
)

// Apps deployed from their git repository. See service/git_apps.go and git_deploy.go.

// gitAppOK is `GitAppOK`. The service builds the `GitApp` itself, to the contract.
type gitAppOK struct {
	Data    *service.GitAppView `json:"data"`
	Message *string             `json:"message,omitempty"`
}

func (a *AppManagement) CreateGitApp(ctx echo.Context) error {
	var body codegen.GitAppCreateRequest
	if err := ctx.Bind(&body); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	view, err := service.CreateGitApp(ctx.Request().Context(), service.GitAppRegistration{
		Name: body.Name, URL: body.Url, Branch: lo.FromPtr(body.Branch), Access: body.Access, Token: lo.FromPtr(body.Token),
	})

	return gitAppAnswer(ctx, http.StatusOK, view, err)
}

func (a *AppManagement) GetGitApp(ctx echo.Context, app codegen.GitAppName) error {
	view, err := service.GetGitApp(ctx.Request().Context(), app)

	return gitAppAnswer(ctx, http.StatusOK, view, err)
}

func (a *AppManagement) UpdateGitApp(ctx echo.Context, app codegen.GitAppName) error {
	var body codegen.GitAppUpdateRequest
	if err := ctx.Bind(&body); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	view, err := service.UpdateGitApp(ctx.Request().Context(), app, service.GitAppChanges{
		Branch: body.Branch, AutoDeploy: body.AutoDeploy, Access: body.Access, Token: body.Token,
	})

	return gitAppAnswer(ctx, http.StatusOK, view, err)
}

func (a *AppManagement) DeleteGitApp(ctx echo.Context, app codegen.GitAppName) error {
	if err := service.DeleteGitApp(ctx.Request().Context(), app); err != nil {
		return gitAppError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, codegen.ResponseOK{Message: utils.Ptr(fmt.Sprintf("`%s` is removed", app))})
}

func (a *AppManagement) CheckGitApp(ctx echo.Context, app codegen.GitAppName) error {
	view, err := service.CheckGitApp(ctx.Request().Context(), app)

	return gitAppAnswer(ctx, http.StatusAccepted, view, err)
}

func (a *AppManagement) DeployGitApp(ctx echo.Context, app codegen.GitAppName) error {
	// the body is optional: absent, it deploys the remote's latest
	var body codegen.GitAppDeployRequest
	if err := ctx.Bind(&body); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	view, err := service.DeployGitApp(ctx.Request().Context(), app, lo.FromPtr(body.Commit), body.Env)

	return gitAppAnswer(ctx, http.StatusAccepted, view, err)
}

func gitAppAnswer(ctx echo.Context, status int, view *service.GitAppView, err error) error {
	if err != nil {
		return gitAppError(ctx, err)
	}

	return ctx.JSON(status, gitAppOK{Data: view})
}

// gitAppError answers what the service refused with the status the API promises.
func gitAppError(ctx echo.Context, err error) error {
	message := err.Error()

	switch {
	case errors.As(err, new(service.GitRequestError)):
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	case errors.Is(err, service.ErrGitAppNotFound):
		return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
	case errors.Is(err, service.ErrGitAppNameTaken), errors.Is(err, service.ErrGitAppDeployed):
		return ctx.JSON(http.StatusConflict, codegen.ResponseConflict{Message: &message})
	}

	return conflictOrServerError(ctx, err)
}
