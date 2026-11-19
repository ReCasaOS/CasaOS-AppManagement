package v2

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
	"github.com/labstack/echo/v4"
)

// The health check of an app, for somebody who did not write its compose file.
//
// The Containers tab said Healthy or Unhealthy and stopped there: which command decides,
// how often it runs and what it last answered took `docker inspect` in a terminal.

// healthSeverity ranks what a health check can say, worst last. A word it does not know
// ranks with no check at all.
var healthSeverity = map[string]int{"healthy": 1, "starting": 2, "unhealthy": 3}

// composeAppHealth folds the health checks of the app's running containers into the one
// word the dashboard draws: the worst of them, or nothing when none has a check.
//
// Docker checks a running container only. The verdict a stopped one kept is old, and the
// app's status already says that it is stopped.
func composeAppHealth(containerLists map[string][]codegen.ContainerSummary) string {
	worst := ""
	for _, containers := range containerLists {
		for _, c := range containers {
			if c.State != "running" {
				continue
			}
			if health := string(c.Health); healthSeverity[health] > healthSeverity[worst] {
				worst = health
			}
		}
	}

	return worst
}

// Docker's own defaults, for what a check leaves out: it stores zero and means these.
const (
	defaultHealthInterval = 30 * time.Second
	defaultHealthTimeout  = 30 * time.Second
	defaultHealthRetries  = 3
)

// describeHealth is the container's check and its last runs, newest first. The command
// and what it printed are emptied unless owner: compose fills a command from .env, and a
// command prints what it likes.
func describeHealth(owner bool, inspected *types.ContainerJSON) codegen.ContainerHealth {
	status, command, runs := "", "", []codegen.ContainerHealthRun{}
	health := codegen.ContainerHealth{Status: &status, Command: &command, Checks: &runs}
	if inspected == nil || inspected.Config == nil {
		return health
	}

	check := inspected.Config.Healthcheck
	if check == nil || len(check.Test) == 0 || check.Test[0] == "NONE" {
		return health
	}
	if owner {
		command = strings.Join(check.Test[1:], " ") // the first word is CMD or CMD-SHELL
	}

	seconds := func(d, unset time.Duration) *int {
		if d <= 0 {
			d = unset
		}

		return utils.Ptr(int(d / time.Second))
	}
	health.Interval = seconds(check.Interval, defaultHealthInterval)
	health.Timeout = seconds(check.Timeout, defaultHealthTimeout)
	health.StartPeriod = seconds(check.StartPeriod, 0)
	retries := check.Retries
	if retries <= 0 {
		retries = defaultHealthRetries
	}
	health.Retries = &retries

	if inspected.State == nil || inspected.State.Health == nil {
		return health
	}
	reported := inspected.State.Health
	status = reported.Status
	if status == dockercontainer.NoHealthcheck {
		status = ""
	}
	health.FailingStreak = &reported.FailingStreak

	for i := len(reported.Log) - 1; i >= 0; i-- {
		run := reported.Log[i]
		if run == nil {
			continue
		}
		output := ""
		if owner {
			output = strings.TrimSpace(run.Output)
		}
		started, exitCode := run.Start, run.ExitCode
		runs = append(runs, codegen.ContainerHealthRun{StartedAt: &started, ExitCode: &exitCode, Output: &output})
	}

	return health
}

// ComposeAppContainerHealth answers for one container of a compose app.
func (a *AppManagement) ComposeAppContainerHealth(ctx echo.Context, id codegen.ComposeAppID, containerID codegen.ComposeContainerID) error {
	composeApps, err := service.MyService.Compose().List(ctx.Request().Context())
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	composeApp, ok := composeApps[id]
	if !ok || !visibleTo(ctx, id) {
		message := fmt.Sprintf("compose app `%s` not found", id)
		return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
	}

	// The app in the path is what authorises the container in the path: without this,
	// the check of any container on the host could be read through an app the caller sees.
	containerLists, err := composeApp.Containers(ctx.Request().Context())
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}
	if !containerBelongsTo(containerLists, string(containerID)) {
		message := fmt.Sprintf("compose app `%s` has no container `%s`", id, containerID)
		return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
	}

	inspected, err := service.MyService.Docker().DescribeContainer(ctx.Request().Context(), string(containerID))
	if err != nil {
		message := err.Error()
		if errdefs.IsNotFound(err) {
			return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
		}

		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	health := describeHealth(isOwner(ctx), inspected)

	return ctx.JSON(http.StatusOK, codegen.ContainerHealthOK{Message: utils.Ptr("OK"), Data: &health})
}
