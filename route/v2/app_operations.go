package v2

import (
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
)

// AppOperations answers what runs on each app right now. The core asks before it updates the
// box by itself, as an internal request.
func (a *AppManagement) AppOperations(ctx echo.Context) error {
	visible, owner := visibleFilter(ctx), isOwner(ctx)
	operations := []codegen.AppOperation{}
	for _, operation := range runningOperations() {
		if !visible(operation.App) {
			continue
		}
		app := operation.App
		// a sync is listed, the core counts it, but its task's name is the owner's
		if operation.Kind == "sync" && !owner {
			app = "sync"
		}
		operations = append(operations, codegen.AppOperation{App: app, Kind: operation.Kind})
	}

	// an upgrade that cannot be ruled out is told as open, as the scheduler reads it
	open, _ := osUpgradeOpen()

	return ctx.JSON(http.StatusOK, codegen.AppOperationsOK{Operations: operations, OsUpgradeOpen: open})
}

// osUpgradeOpen is a variable so a test can open an upgrade without the core's file.
var osUpgradeOpen = service.OSUpgradeOpen

// runningOperations is a variable so a test can run a sync without a pass.
var runningOperations = service.RunningOperations
