package v2

import (
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

func (a *AppManagement) CheckImageUpdates(ctx echo.Context) error {
	// Deliberately the request's context: this is a button the user pressed and is
	// waiting on, so if they navigate away the registry calls should stop too.
	result, err := service.CheckImageUpdates(ctx.Request().Context())
	if err != nil {
		message := err.Error()
		logger.Error("failed to check image updates", zap.Error(err))
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	return ctx.JSON(http.StatusOK, codegen.ImageUpdateCheckOK{Data: result})
}
