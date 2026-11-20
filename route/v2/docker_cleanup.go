package v2

import (
	"context"
	"errors"
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// The cleanup is the owner's: it lists the images and the volumes of every app, hidden
// ones included, and it deletes.

func (a *AppManagement) DockerCleanup(ctx echo.Context) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}
	report, err := service.DockerCleanup(ctx.Request().Context())
	if err != nil {
		message := err.Error()
		logger.Error("failed to measure what a cleanup would free", zap.Error(err))
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	return ctx.JSON(http.StatusOK, codegen.DockerCleanupOK{Data: report})
}

func (a *AppManagement) PruneBuildCache(ctx echo.Context) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}
	freed, err := service.PruneBuildCache(ctx.Request().Context())
	if held, refused := cleanupRefused(ctx, err); refused {
		return held
	}
	if err != nil {
		message := err.Error()
		logger.Error("failed to prune the build cache", zap.Error(err))
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}
	logger.Info("pruned the build cache", zap.Int64("bytes", freed))

	return ctx.JSON(http.StatusOK, codegen.CleanupResultOK{
		Data: &codegen.CleanupResult{Removed: []string{}, Refused: map[string]string{}, Size: freed},
	})
}

func (a *AppManagement) RemoveLeftoverImages(ctx echo.Context) error {
	return removeLeftovers(ctx, "images", service.RemoveLeftoverImages)
}

func (a *AppManagement) RemoveLeftoverVolumes(ctx echo.Context) error {
	return removeLeftovers(ctx, "volumes", service.RemoveLeftoverVolumes)
}

func (a *AppManagement) RemoveOrphanFolders(ctx echo.Context) error {
	return removeLeftovers(ctx, "folders", service.RemoveOrphanFolders)
}

// removeLeftovers is the two deletions: the owner's, a list that only narrows what the
// report offers, a refusal while what the apps claim cannot be told.
func removeLeftovers(ctx echo.Context, what string, remove func(context.Context, []string) (*codegen.CleanupResult, error)) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}
	var selection codegen.RemoveLeftoverImagesJSONRequestBody
	if err := ctx.Bind(&selection); err != nil || len(selection.Names) == 0 {
		message := "names: the list of what to delete is required"
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	result, err := remove(ctx.Request().Context(), selection.Names)
	if held, refused := cleanupRefused(ctx, err); refused {
		return held
	}
	if err != nil {
		message := err.Error()
		logger.Error("failed to delete leftovers", zap.String("what", what), zap.Error(err))
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}
	logger.Info("deleted leftovers", zap.String("what", what), zap.Strings("removed", result.Removed), zap.Int64("bytes", result.Size))

	return ctx.JSON(http.StatusOK, codegen.CleanupResultOK{Data: result})
}

// cleanupRefused answers 409 for a cleanup that is held, with why; refused is false for
// any other error, and for none.
func cleanupRefused(ctx echo.Context, err error) (answer error, refused bool) {
	var held service.ErrCleanupHeld
	if !errors.As(err, &held) {
		return nil, false
	}
	message := held.Error()

	return ctx.JSON(http.StatusConflict, codegen.ResponseConflict{Message: &message}), true
}
