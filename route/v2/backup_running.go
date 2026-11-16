package v2

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/backupprogress"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/labstack/echo/v4"
)

// BackupRunning answers what runs and how far it has got, without an envelope, as /operations.
func (a *AppManagement) BackupRunning(ctx echo.Context) error {
	return ctx.JSON(http.StatusOK, runningFor(ctx, service.LiveBackups(), service.BackupPasses()))
}

// runningFor is runningOut for the caller of ctx: the apps it may see, and the paths for the owner only
// (a backup reads the box's folders and files, which a user account does not browse).
func runningFor(ctx echo.Context, lives []service.LiveBackup, passes []service.BackupPass) codegen.BackupRunningOK {
	// a sync is the owner's: its App is a task name, never an app to filter by
	if !isOwner(ctx) {
		lives = slices.DeleteFunc(slices.Clone(lives), func(l service.LiveBackup) bool { return l.Kind == "sync" })
	}
	out := runningOut(visibleFilter(ctx), lives, passes)
	if !isOwner(ctx) {
		for i := range out.Backups {
			out.Backups[i].Source = ""
			if out.Backups[i].Stats != nil {
				out.Backups[i].Stats.Current = nil
			}
		}
		// the engine's error text can name the box's folders: the state says an item failed
		for i := range out.Passes {
			for j := range out.Passes[i].Items {
				out.Passes[i].Items[j].Error = nil
			}
		}
	}

	return out
}

// runningOut is what runs, as a caller who sees the apps visible sees it.
func runningOut(visible func(app string) bool, lives []service.LiveBackup, passes []service.BackupPass) codegen.BackupRunningOK {
	out := codegen.BackupRunningOK{Backups: []codegen.LiveBackup{}, Passes: []codegen.BackupPass{}}

	// a path is shown only when no folder on it is named after an app the caller may not see
	shown := func(p string) bool {
		return !slices.ContainsFunc(strings.Split(p, "/"), func(part string) bool { return part != "" && !visible(part) })
	}
	for _, live := range lives {
		isSync := live.Kind == "sync"
		// a sync is not an app: the filter and the hidden-app paths do not apply (runningFor keeps it from non-owners)
		if !isSync && !visible(live.App) {
			continue
		}
		backup := codegen.LiveBackup{
			Kind: "backup", Id: live.ID, App: live.App, Destination: live.Destination, Stamp: live.Stamp,
			Origin: string(live.Origin), StartedAt: live.StartedAt,
			Operation: live.Operation, Operations: live.Operations, Source: live.Source,
		}
		if isSync {
			backup.Kind = "sync"
		} else if !shown(live.Source) {
			backup.Source = ""
		}
		if live.Stats != nil {
			backup.Stats = liveStatsOut(*live.Stats, live)
			if backup.Stats.Current != nil && !isSync && !shown(*backup.Stats.Current) {
				backup.Stats.Current = nil
			}
		}
		out.Backups = append(out.Backups, backup)
	}

	for _, pass := range passes {
		passOut := codegen.BackupPass{
			Origin: string(pass.Origin), StartedAt: pass.StartedAt, FinishedAt: pass.FinishedAt,
			Items: []codegen.BackupPassItem{},
		}
		for _, item := range pass.Items {
			if !visible(item.App) {
				continue
			}
			itemOut := codegen.BackupPassItem{
				App: item.App, Destination: item.Destination, State: string(item.State),
				StartedAt: item.StartedAt, FinishedAt: item.FinishedAt,
			}
			if item.FinishedAt != nil {
				itemOut.Bytes = utils.Ptr(item.Bytes)
			}
			if item.Error != "" {
				itemOut.Error = utils.Ptr(item.Error)
			}
			passOut.Items = append(passOut.Items, itemOut)
		}
		// a pass of hidden apps alone is, for this caller, no pass
		if len(pass.Items) > 0 && len(passOut.Items) == 0 {
			continue
		}
		out.Passes = append(out.Passes, passOut)
	}

	return out
}

// liveStatsOut leaves out what the engine has not said: a zero total is a total not known yet.
func liveStatsOut(stats backupprogress.Stats, live service.LiveBackup) *codegen.LiveBackupStats {
	out := &codegen.LiveBackupStats{Bytes: stats.Bytes, Files: stats.Files, Eta: stats.ETA, UpdatedAt: live.StatsAt}
	if stats.TotalBytes > 0 {
		out.TotalBytes = utils.Ptr(stats.TotalBytes)
	}
	if stats.TotalFiles > 0 {
		out.TotalFiles = utils.Ptr(stats.TotalFiles)
	}
	if stats.Speed > 0 {
		out.Speed = utils.Ptr(stats.Speed)
	}
	if stats.Current != "" {
		out.Current = utils.Ptr(stats.Current)
	}

	return out
}

// StopRunningBackup stops one backup; the pass it belongs to goes on.
func (a *AppManagement) StopRunningBackup(ctx echo.Context, backupID string) error {
	if err := service.StopLiveBackup(backupID); err != nil {
		if errors.Is(err, service.ErrLiveBackupNotFound) {
			return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: utils.Ptr(err.Error())})
		}

		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: utils.Ptr(err.Error())})
	}

	return ctx.JSON(http.StatusAccepted, codegen.BaseResponse{Message: utils.Ptr("Stop requested")})
}
