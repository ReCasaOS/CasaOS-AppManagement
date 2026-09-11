package v2

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/labstack/echo/v4"
)

// statsDeadline bounds the whole sample. Two frames off the daemon's stream take
// about a second; anything past this is a daemon that has stopped answering, and
// a dashboard that waits on it forever is worse than one that says nothing.
const statsDeadline = 10 * time.Second

// ComposeAppContainerStats reports what an app's running containers are using.
//
// Only running ones: a stopped container uses nothing, and the daemon has no
// sample to give for it. The caller matches these back to the rows it already
// has by container id, so a container missing from this answer keeps whatever
// the row already said rather than being redrawn as idle.
func (a *AppManagement) ComposeAppContainerStats(ctx echo.Context, id codegen.ComposeAppID) error {
	if id == "" {
		message := ErrComposeAppIDNotProvided.Error()
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

	containerLists, err := composeApp.Containers(ctx.Request().Context())
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	serviceOf := map[string]string{}
	ids := []string{}
	for serviceName, list := range containerLists {
		for _, container := range list {
			if container.State != "running" || container.ID == "" {
				continue
			}

			serviceOf[container.ID] = serviceName
			ids = append(ids, container.ID)
		}
	}

	// A stable order, because this answer is redrawn every few seconds and a table
	// that reshuffles under the reader is unusable.
	sort.Strings(ids)

	sampleCtx, cancel := context.WithTimeout(ctx.Request().Context(), statsDeadline)
	defer cancel()

	sampled := service.MyService.Docker().SampleContainerStats(sampleCtx, ids)

	stats := make([]codegen.ContainerStats, 0, len(sampled))
	for _, containerID := range ids {
		s, ok := sampled[containerID]
		if !ok {
			continue
		}

		used, limit := docker.MemoryUsage(s)
		stats = append(stats, codegen.ContainerStats{
			ContainerId: containerID,
			Service:     serviceOf[containerID],
			CpuPercent:  docker.CPUPercent(s),
			MemoryUsed:  int64(used),
			MemoryLimit: int64(limit),
		})
	}

	return ctx.JSON(http.StatusOK, codegen.ComposeAppContainerStatsOK{
		Message: utils.Ptr("OK"),
		Data:    &stats,
	})
}
