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
	manifest, err := service.BackupOnDemand(ctx, composeApp, service.MyService.Docker(), rclone.NewClient(), service.BackupOptions{
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

// BackupSchedules lists the standing arrangements.
func (a *AppManagement) BackupSchedules(ctx echo.Context) error {
	schedules, err := service.BackupSchedules()
	if err != nil {
		return backupError(ctx, err)
	}

	out := make([]codegen.BackupSchedule, 0, len(schedules))
	for _, schedule := range schedules {
		out = append(out, toCodegenSchedule(schedule))
	}

	return ctx.JSON(http.StatusOK, codegen.BackupSchedulesOK{
		Message: utils.Ptr("OK"), Data: &out,
	})
}

// SetBackupSchedules replaces the lot.
//
// Every schedule is checked before any is saved. A list saved half-valid is a box
// where some backups run and some silently never will, and nothing on the screen
// tells the two apart.
func (a *AppManagement) SetBackupSchedules(ctx echo.Context) error {
	var incoming []codegen.BackupSchedule
	if err := ctx.Bind(&incoming); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	// What is already on disk, so a LastRun the screen never saw is not lost --
	// losing it makes every schedule fire again at the next tick.
	existing, err := service.BackupSchedules()
	if err != nil {
		return backupError(ctx, err)
	}

	lastRuns := map[string]time.Time{}
	for _, schedule := range existing {
		lastRuns[schedule.App+"\x00"+schedule.Destination] = schedule.LastRun
	}

	schedules := make([]service.BackupSchedule, 0, len(incoming))
	for _, item := range incoming {
		schedule := fromCodegenSchedule(item)
		schedule.LastRun = lastRuns[schedule.App+"\x00"+schedule.Destination]

		if _, err := service.IsDue(schedule, time.Now()); err != nil {
			message := err.Error()
			return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
		}

		schedules = append(schedules, schedule)
	}

	if err := service.SaveBackupSchedules(schedules); err != nil {
		return backupError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, codegen.BaseResponse{
		Message: utils.Ptr(fmt.Sprintf("%d schedule(s) saved", len(schedules))),
	})
}

// BackupRuns is what happened, newest first, failures included.
func (a *AppManagement) BackupRuns(ctx echo.Context) error {
	records, err := service.BackupRuns()
	if err != nil {
		return backupError(ctx, err)
	}

	out := make([]codegen.BackupRunRecord, 0, len(records))
	for _, record := range records {
		out = append(out, codegen.BackupRunRecord{
			App: &record.App, Destination: &record.Destination, Stamp: &record.Stamp,
			StartedAt: &record.StartedAt, FinishedAt: &record.FinishedAt,
			Scheduled: &record.Scheduled, ContainersStopped: &record.ContainersStopped,
			Copied: &record.Copied, SkippedCount: &record.SkippedCount,
			Error: &record.Error,
		})
	}

	return ctx.JSON(http.StatusOK, codegen.BackupRunsOK{
		Message: utils.Ptr("OK"), Data: &out,
	})
}

func toCodegenSchedule(schedule service.BackupSchedule) codegen.BackupSchedule {
	weekday := int(schedule.Weekday)
	every := string(schedule.Every)
	lastRun := schedule.LastRun

	return codegen.BackupSchedule{
		App: schedule.App, Destination: schedule.Destination,
		Every: codegen.BackupScheduleEvery(every), At: schedule.At,
		Weekday: &weekday, Keep: &schedule.Keep,
		HoldStill: &schedule.HoldStill, Enabled: &schedule.Enabled,
		LastRun: &lastRun,
	}
}

func fromCodegenSchedule(item codegen.BackupSchedule) service.BackupSchedule {
	schedule := service.BackupSchedule{
		App: item.App, Destination: item.Destination,
		Every: service.ScheduleEvery(item.Every), At: item.At,
	}

	if item.Weekday != nil {
		schedule.Weekday = time.Weekday(*item.Weekday)
	}
	if item.Keep != nil {
		schedule.Keep = *item.Keep
	}
	if item.HoldStill != nil {
		schedule.HoldStill = *item.HoldStill
	}
	if item.Enabled != nil {
		schedule.Enabled = *item.Enabled
	}

	return schedule
}
