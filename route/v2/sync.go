package v2

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/rclone"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/labstack/echo/v4"
)

// replaceable in tests
var (
	runSyncNow         = service.RunSyncNow
	runSyncFirstPass   = service.RunSyncFirstPass
	confirmSyncDeletes = service.ConfirmSyncDeletes
	listFolders        = func(fs, remote string) ([]string, error) { return rclone.NewClient().Folders(fs, remote) }
)

// syncTasksOut is the envelope of the neighbouring /backup/* routes, with the stored tasks as they are.
type syncTasksOut struct {
	Message *string            `json:"message,omitempty"`
	Data    []service.SyncTask `json:"data"`
}

// SyncTasks lists the scheduled folder syncs (owner only).
func (a *AppManagement) SyncTasks(ctx echo.Context) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}
	tasks, err := service.SyncTasks()
	if err != nil {
		return backupError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, syncTasksOut{Message: utils.Ptr("OK"), Data: tasks})
}

// SetSyncTasks replaces them. The body is read strictly, as the backup exclusions are: a
// misspelt field would otherwise save an empty list, and the box would stop syncing.
func (a *AppManagement) SetSyncTasks(ctx echo.Context) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}

	var body struct {
		Tasks *[]service.SyncTask `json:"tasks"`
	}
	decoder := json.NewDecoder(ctx.Request().Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&body)
	if err == nil && decoder.Decode(&struct{}{}) != io.EOF {
		err = errors.New("trailing data")
	}
	if err != nil || body.Tasks == nil {
		return backupBadRequest(ctx, errors.New("sync tasks are JSON with tasks, a list of sync tasks"))
	}

	saved, err := service.SaveSyncTasks(*body.Tasks)
	var refusal service.SyncRefusal
	if errors.As(err, &refusal) {
		return backupBadRequest(ctx, err)
	}
	if err != nil {
		return backupError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, syncTasksOut{Message: utils.Ptr("OK"), Data: saved})
}

// RunSyncTask runs one pass of a task now.
func (a *AppManagement) RunSyncTask(ctx echo.Context, id codegen.SyncTaskID) error {
	return startSync(ctx, runSyncNow, id)
}

// FirstPassSyncTask runs the first pass of a task that waits for it.
func (a *AppManagement) FirstPassSyncTask(ctx echo.Context, id codegen.SyncTaskID) error {
	return startSync(ctx, runSyncFirstPass, id)
}

// ConfirmSyncTaskDeletes lets a task delete what it held back.
func (a *AppManagement) ConfirmSyncTaskDeletes(ctx echo.Context, id codegen.SyncTaskID) error {
	return startSync(ctx, confirmSyncDeletes, id)
}

func startSync(ctx echo.Context, start func(id string) error, id string) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}

	err := start(id)
	switch {
	case err == nil:
		return ctx.JSON(http.StatusAccepted, codegen.BaseResponse{Message: utils.Ptr("Sync started")})
	case errors.Is(err, service.ErrSyncNotFound):
		return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: utils.Ptr(err.Error())})
	case errors.Is(err, service.ErrSyncRunning), errors.Is(err, service.ErrSyncUpgrade), errors.Is(err, service.ErrSyncWrongState):
		return ctx.JSON(http.StatusConflict, codegen.ResponseConflict{Message: utils.Ptr(err.Error())})
	}

	return backupError(ctx, err)
}

// BackupDestinationFolders lists the folders directly under a path of a destination, for the owner to choose one.
func (a *AppManagement) BackupDestinationFolders(ctx echo.Context, name codegen.BackupDestinationName, params codegen.BackupDestinationFoldersParams) error {
	if !isOwner(ctx) {
		return forbidden(ctx)
	}

	remote := ""
	if params.Path != nil {
		remote = strings.Trim(*params.Path, "/")
	}
	for _, part := range strings.Split(remote, "/") {
		if part == ".." {
			return backupBadRequest(ctx, errors.New("a path cannot go up"))
		}
	}

	folders, err := listFolders(rclone.Destination(name, ""), remote)
	if err != nil {
		return backupBadRequest(ctx, err)
	}

	return ctx.JSON(http.StatusOK, codegen.SyncFoldersOK{Message: utils.Ptr("OK"), Data: &codegen.SyncFolders{Folders: folders}})
}

// syncUsing names a sync task that uses the destination; an error when the tasks cannot be read.
func syncUsing(destination string) (string, error) {
	tasks, err := service.SyncTasks()
	if err != nil {
		return "", fmt.Errorf("the sync tasks cannot be read, so the destination is kept: %w", err)
	}
	for _, task := range tasks {
		if task.Destination == destination {
			return task.Name, nil
		}
	}

	return "", nil
}
