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

// gitAppTagsOK is `GitAppTagsOK`.
type gitAppTagsOK struct {
	Data    []service.GitTag `json:"data"`
	Message *string          `json:"message,omitempty"`
}

func (a *AppManagement) CreateGitApp(ctx echo.Context) error {
	var body codegen.GitAppCreateRequest
	if err := ctx.Bind(&body); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	view, err := service.CreateGitApp(ctx.Request().Context(), gitAppRegistration(body))

	return gitAppAnswer(ctx, http.StatusOK, view, err)
}

// gitAppRegistration is the app a POST asks the service to register.
func gitAppRegistration(body codegen.GitAppCreateRequest) service.GitAppRegistration {
	return service.GitAppRegistration{
		Name: body.Name, URL: body.Url, Branch: lo.FromPtr(body.Branch), Access: body.Access, Token: lo.FromPtr(body.Token),
		Follow: lo.FromPtr(body.Follow), TagPattern: lo.FromPtr(body.TagPattern), Prereleases: lo.FromPtr(body.Prereleases),
	}
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

	view, err := service.UpdateGitApp(ctx.Request().Context(), app, gitAppChanges(body))

	return gitAppAnswer(ctx, http.StatusOK, view, err)
}

// gitAppChanges is what a PUT asks the service to change.
func gitAppChanges(body codegen.GitAppUpdateRequest) service.GitAppChanges {
	return service.GitAppChanges{
		Branch: body.Branch, AutoDeploy: body.AutoDeploy, Access: body.Access, Token: body.Token,
		WebhookEnabled: body.WebhookEnabled, RegenerateWebhookSecret: body.RegenerateWebhookSecret,
		Follow: body.Follow, TagPattern: body.TagPattern, Prereleases: body.Prereleases,
	}
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

	var view *service.GitAppView
	var err error
	switch {
	case body.Tag != nil && body.Commit != nil:
		err = service.GitRequestError("give a tag or a commit, not both")
	case body.Tag != nil:
		view, err = service.DeployGitAppTag(ctx.Request().Context(), app, *body.Tag, body.Env)
	default:
		view, err = service.DeployGitApp(ctx.Request().Context(), app, lo.FromPtr(body.Commit), body.Env)
	}

	return gitAppAnswer(ctx, http.StatusAccepted, view, err)
}

func (a *AppManagement) GetGitAppTags(ctx echo.Context, app codegen.GitAppName) error {
	tags, err := service.GitAppTags(ctx.Request().Context(), app)

	return gitAppTagsAnswer(ctx, tags, err)
}

// gitAppTagsAnswer answers the tags of a git app, or what the service refused.
func gitAppTagsAnswer(ctx echo.Context, tags []service.GitTag, err error) error {
	if err != nil {
		return gitAppError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, gitAppTagsOK{Data: tags})
}

// ReceiveGitWebhook is a forge's delivery. It carries no token: route/v2.go lets this
// method and path through, and the service checks the signature instead.
func (a *AppManagement) ReceiveGitWebhook(ctx echo.Context, app codegen.GitAppName) error {
	answer, err := service.ReceiveGitWebhook(app, ctx.Request().Header, ctx.Request().Body)

	return gitWebhookAnswer(ctx, answer, err)
}

// gitWebhookAnswer tells the forge how its delivery went, and a caller not proven by a
// signature nothing more than which refusal it got.
func gitWebhookAnswer(ctx echo.Context, answer string, err error) error {
	status := http.StatusAccepted
	switch {
	case errors.Is(err, service.ErrGitWebhookNotFound):
		status, answer = http.StatusNotFound, err.Error()
	case errors.Is(err, service.ErrGitWebhookSignature):
		status, answer = http.StatusUnauthorized, err.Error()
	case errors.Is(err, service.ErrGitWebhookTooLarge):
		status, answer = http.StatusRequestEntityTooLarge, err.Error()
	case err != nil:
		// the service logged what failed
		status, answer = http.StatusInternalServerError, "internal error"
	case answer == service.GitWebhookPong:
		status = http.StatusOK
	}

	return ctx.JSON(status, codegen.ResponseOK{Message: &answer})
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
