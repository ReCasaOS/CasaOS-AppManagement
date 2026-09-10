package v2

import (
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

func (a *AppManagement) DanglingImages(ctx echo.Context) error {
	result, err := service.DanglingImages(ctx.Request().Context())
	if err != nil {
		message := err.Error()
		logger.Error("failed to measure dangling images", zap.Error(err))
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	return ctx.JSON(http.StatusOK, codegen.DanglingImagesOK{Data: result})
}

// Synchronous, unlike the recreate and pull endpoints that hand work to a goroutine and
// report progress over the message bus. Those have no answer to give back; this one's
// whole point is the number of bytes freed, and dangling-only bounds the work to what
// the GET just measured -- a handful of layers on this kind of box. The request's
// context carries the cancellation, so navigating away stops it.
func (a *AppManagement) PruneDanglingImages(ctx echo.Context) error {
	result, err := service.PruneDanglingImages(ctx.Request().Context())
	if err != nil {
		message := err.Error()
		logger.Error("failed to prune dangling images", zap.Error(err))
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	logger.Info("pruned dangling images", zap.Int("count", result.Count), zap.Int64("bytes", result.Size))

	return ctx.JSON(http.StatusOK, codegen.DanglingImagesOK{Data: result})
}
