package v2

import (
	"fmt"
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// SetComposeAppContainerStatus starts, stops or restarts ONE container of a
// compose app.
//
// The app-level route next to this one acts on every service at once, which is
// the wrong tool for most of what goes wrong on a multi-service stack: a
// database that wedged, a sidecar that needs its config re-read. Taking the
// whole stack down to fix one of its parts is a bigger outage than the fault.
//
// This runs synchronously, unlike the app-level status change. One container is
// quick, the caller redraws the row as soon as it answers, and an error worth
// showing arrives as an error rather than as a row that silently never changes.
func (a *AppManagement) SetComposeAppContainerStatus(ctx echo.Context, id codegen.ComposeAppID, containerID codegen.ComposeContainerID) error {
	if id == "" {
		message := ErrComposeAppIDNotProvided.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	if containerID == "" {
		message := "container ID is not provided"
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	var action codegen.SetComposeAppContainerStatusJSONRequestBody
	if err := ctx.Bind(&action); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	composeApps, err := service.MyService.Compose().List(ctx.Request().Context())
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	composeApp, ok := composeApps[id]
	if !ok {
		message := fmt.Sprintf("compose app `%s` not found", id)
		return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
	}

	// The app in the path is what authorises the container in the path. Without
	// this, any container ID on the host could be stopped through the route of an
	// app that has nothing to do with it.
	containerLists, err := composeApp.Containers(ctx.Request().Context())
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	if !containerBelongsTo(containerLists, string(containerID)) {
		message := fmt.Sprintf("compose app `%s` has no container `%s`", id, containerID)
		return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
	}

	docker := service.MyService.Docker()

	switch codegen.SetComposeAppContainerStatusJSONBody(action) {
	case codegen.SetComposeAppContainerStatusJSONBodyStart:
		err = docker.StartContainer(string(containerID))
	case codegen.SetComposeAppContainerStatusJSONBodyStop:
		err = docker.StopContainer(string(containerID))
	case codegen.SetComposeAppContainerStatusJSONBodyRestart:
		err = docker.RestartContainer(string(containerID))
	default:
		message := fmt.Sprintf("`%s` is not a container action", action)
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	if err != nil {
		message := err.Error()
		logger.Error("failed to change container status",
			zap.Error(err), zap.String("app", id), zap.String("container", string(containerID)), zap.String("action", string(action)))
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	return ctx.JSON(http.StatusOK, codegen.BaseResponse{
		Message: utils.Ptr(fmt.Sprintf("container `%s` of `%s`: %s", containerID, id, action)),
	})
}

// containerBelongsTo reports whether a container ID is one of the app's own.
func containerBelongsTo(containerLists map[string][]codegen.ContainerSummary, containerID string) bool {
	for _, list := range containerLists {
		for _, container := range list {
			if container.ID == containerID {
				return true
			}
		}
	}

	return false
}
