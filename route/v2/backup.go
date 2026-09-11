package v2

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/rclone"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// stampLayout names a run inside a destination. Sortable, and with nothing in it
// a filesystem objects to -- a colon is not a character every destination accepts
// in a path, and this is read back by people in file browsers.
const stampLayout = "2006-01-02T15-04-05Z"

// BackupDestinations lists the destinations, by name.
//
// Names only. What a destination needs to be reached is rclone's to keep, and a
// secret that is never read back out through an API is a secret that cannot leak
// through one.
func (a *AppManagement) BackupDestinations(ctx echo.Context) error {
	names, err := rclone.NewClient().Destinations()
	if err != nil {
		return backupError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, codegen.BackupDestinationsOK{
		Message: utils.Ptr("OK"),
		Data:    &names,
	})
}

// SetBackupDestination adds or replaces one.
func (a *AppManagement) SetBackupDestination(ctx echo.Context, name codegen.BackupDestinationName) error {
	var spec codegen.BackupDestinationSpec
	if err := ctx.Bind(&spec); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	parameters := map[string]string{}
	if spec.Parameters != nil {
		parameters = *spec.Parameters
	}

	if err := rclone.NewClient().CreateDestination(string(name), spec.Backend, parameters); err != nil {
		return backupError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, codegen.BaseResponse{
		Message: utils.Ptr(fmt.Sprintf("destination `%s` saved", name)),
	})
}

// DeleteBackupDestination forgets one. What was copied there stays there.
func (a *AppManagement) DeleteBackupDestination(ctx echo.Context, name codegen.BackupDestinationName) error {
	if err := rclone.NewClient().DeleteDestination(string(name)); err != nil {
		return backupError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, codegen.BaseResponse{
		Message: utils.Ptr(fmt.Sprintf("destination `%s` forgotten; anything already copied there is untouched", name)),
	})
}

// CheckBackupDestination asks a destination whether it is reachable.
func (a *AppManagement) CheckBackupDestination(ctx echo.Context, name codegen.BackupDestinationName) error {
	space, err := rclone.NewClient().CheckDestination(string(name))
	if err != nil {
		return backupError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, codegen.BackupDestinationCheckOK{
		Message: utils.Ptr("OK"),
		Data: &codegen.BackupDestinationSpace{
			Used: space.Used, Free: space.Free, Total: space.Total,
		},
	})
}

// BackupComposeApp starts a backup and returns.
//
// A copy takes as long as it takes, so nothing waits for it here. What the caller
// gets back is that it started, and where it is going; the rest arrives on the
// message bus, like every other long operation in this service.
func (a *AppManagement) BackupComposeApp(ctx echo.Context, id codegen.ComposeAppID) error {
	if id == "" {
		message := ErrComposeAppIDNotProvided.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	var request codegen.BackupRequest
	if err := ctx.Bind(&request); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	if request.Destination == "" {
		message := "a backup needs a destination"
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

	holdStill := holdStillFrom(request)
	stamp := time.Now().UTC().Format(stampLayout)

	// Detached from the request: the copy outlives it by design, and a caller that
	// closes its connection must not cancel a backup half-written.
	backgroundCtx := common.WithProperties(context.Background(), PropertiesFromQueryParams(ctx))

	go runBackupInBackground(backgroundCtx, composeApp, request.Destination, stamp, holdStill)

	return ctx.JSON(http.StatusOK, codegen.BaseResponse{
		Message: utils.Ptr(fmt.Sprintf("backing `%s` up to `%s` as `%s`", id, request.Destination, stamp)),
	})
}

func runBackupInBackground(ctx context.Context, composeApp *service.ComposeApp, destination, stamp string, holdStill bool) {
	manifest, err := service.RunBackup(ctx, composeApp, service.MyService.Docker(), rclone.NewClient(), service.BackupOptions{
		Destination: destination,
		Stamp:       stamp,
		HoldStill:   holdStill,
	})
	if err != nil {
		logger.Error("backup failed",
			zap.Error(err), zap.String("app", composeApp.Name), zap.String("destination", destination), zap.String("stamp", stamp))

		return
	}

	logger.Info("backup finished",
		zap.String("app", composeApp.Name), zap.String("destination", destination), zap.String("stamp", stamp),
		zap.Int("copied", len(manifest.Operations)), zap.Int("skipped", len(manifest.Skipped)))
}

// backupError answers rclone's failures.
//
// The daemon not running and a destination being wrong are the same HTTP status
// and completely different problems, so the message has to carry the difference:
// one is fixed by starting a service, the other by correcting a bucket name.
func backupError(ctx echo.Context, err error) error {
	message := err.Error()

	return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
}

// holdStillFrom reads whether to stop the app, defaulting to stopping it.
//
// Copying a database while it is writing produces a backup that looks fine and
// does not restore. A field the request omits arrives nil, and nil has to mean
// the safe answer here rather than the zero value -- Go's zero for a bool is
// false, which would quietly turn every request that forgot the field into a hot
// copy.
func holdStillFrom(request codegen.BackupRequest) bool {
	if request.HoldStill == nil {
		return true
	}

	return *request.HoldStill
}
