package v2

import (
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/labstack/echo/v4"
)

// Stubs for the fleet-backup routes, so the contracts compile before each
// route has its implementation. Each later task deletes its own stub.

func notImplemented(ctx echo.Context) error {
	message := "not implemented"

	return ctx.JSON(http.StatusNotImplemented, codegen.BaseResponse{Message: &message})
}

func (a *AppManagement) ForceBackupSchedules(ctx echo.Context) error { return notImplemented(ctx) }

func (a *AppManagement) BackupVerificationSchedule(ctx echo.Context) error {
	return notImplemented(ctx)
}

func (a *AppManagement) SetBackupVerificationSchedule(ctx echo.Context) error {
	return notImplemented(ctx)
}

func (a *AppManagement) BackupVerifications(ctx echo.Context) error { return notImplemented(ctx) }

func (a *AppManagement) StartBackupVerification(ctx echo.Context) error {
	return notImplemented(ctx)
}

func (a *AppManagement) CancelBackupVerification(ctx echo.Context, verificationId string) error {
	return notImplemented(ctx)
}
