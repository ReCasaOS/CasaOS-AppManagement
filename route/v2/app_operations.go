package v2

import (
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
)

// AppOperations answers what holds each app right now. The core asks before it updates the
// box by itself, as an internal request.
func (a *AppManagement) AppOperations(ctx echo.Context) error {
	operations := []codegen.AppOperation{}
	for _, operation := range service.RunningOperations() {
		operations = append(operations, codegen.AppOperation{App: operation.App, Kind: operation.Kind})
	}

	return ctx.JSON(http.StatusOK, codegen.AppOperationsOK{Operations: operations})
}
