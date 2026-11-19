package v2

import (
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	containertypes "github.com/moby/moby/api/types/container"
	"gotest.tools/v3/assert"
)

// The dashboard told a stopped app from a running one and nothing more: an app whose
// database failed its check looked exactly like one in good shape.
func TestComposeAppHealthIsTheWorstOfTheRunningContainers(t *testing.T) {
	type c = codegen.ContainerSummary
	one := func(state, health string) c {
		return c{State: containertypes.ContainerState(state), Health: containertypes.HealthStatus(health)}
	}

	for name, tc := range map[string]struct {
		containers map[string][]c
		want       string
	}{
		"no check anywhere":         {map[string][]c{"app": {one("running", "")}}, ""},
		"all healthy":               {map[string][]c{"app": {one("running", "healthy")}, "db": {one("running", "healthy")}}, "healthy"},
		"one still starting":        {map[string][]c{"app": {one("running", "healthy")}, "db": {one("running", "starting")}}, "starting"},
		"one unhealthy":             {map[string][]c{"app": {one("running", "starting")}, "db": {one("running", "unhealthy")}}, "unhealthy"},
		"a replica unhealthy":       {map[string][]c{"app": {one("running", "healthy"), one("running", "unhealthy")}}, "unhealthy"},
		"a check beside none":       {map[string][]c{"app": {one("running", "")}, "db": {one("running", "healthy")}}, "healthy"},
		"an unfamiliar word is not": {map[string][]c{"app": {one("running", "something-new")}}, ""},
		// Docker checks a running container only: the verdict a stopped one kept is old news,
		// and the status already says the app is stopped
		"a stopped container's last verdict": {map[string][]c{"app": {one("running", "healthy")}, "job": {one("exited", "unhealthy")}}, "healthy"},
		"nothing at all":                     {map[string][]c{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, composeAppHealth(tc.containers), tc.want)
		})
	}
}

func inspectedWithCheck() *types.ContainerJSON {
	at := time.Date(2026, 11, 19, 18, 40, 7, 0, time.UTC)

	return &types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{State: &dockercontainer.State{
			Status: "running",
			Health: &dockercontainer.Health{
				Status:        "unhealthy",
				FailingStreak: 2,
				Log: []*dockercontainer.HealthcheckResult{
					{Start: at, End: at.Add(40 * time.Millisecond), ExitCode: 0, Output: "accepting connections\n"},
					{Start: at.Add(30 * time.Second), End: at.Add(35 * time.Second), ExitCode: 2, Output: "no response\n"},
					nil,
				},
			},
		}},
		Config: &dockercontainer.Config{Healthcheck: &dockercontainer.HealthConfig{
			Test:        []string{"CMD-SHELL", "pg_isready -U postgres"},
			Interval:    10 * time.Second,
			StartPeriod: time.Minute,
		}},
	}
}

func TestDescribeHealthSaysTheCheckAndItsLastRuns(t *testing.T) {
	health := describeHealth(true, inspectedWithCheck())

	assert.Equal(t, *health.Status, "unhealthy")
	assert.Equal(t, *health.FailingStreak, 2)
	assert.Equal(t, *health.Command, "pg_isready -U postgres")
	assert.Equal(t, *health.Interval, 10)
	// what the compose file leaves out is Docker's default, not zero
	assert.Equal(t, *health.Timeout, 30)
	assert.Equal(t, *health.Retries, 3)
	assert.Equal(t, *health.StartPeriod, 60)

	// newest first, and an entry Docker left empty is skipped
	checks := *health.Checks
	assert.Equal(t, len(checks), 2)
	assert.Equal(t, *checks[0].ExitCode, 2)
	assert.Equal(t, *checks[0].Output, "no response")
	assert.Equal(t, checks[0].StartedAt.Format(time.RFC3339), "2026-11-19T18:40:37Z")
	assert.Equal(t, *checks[1].ExitCode, 0)
}

func TestDescribeHealthJoinsAnExecCommand(t *testing.T) {
	inspected := inspectedWithCheck()
	inspected.Config.Healthcheck.Test = []string{"CMD", "curl", "-f", "http://localhost/"}

	assert.Equal(t, *describeHealth(true, inspected).Command, "curl -f http://localhost/")
}

// compose fills a check's command from .env, and its output is whatever the command
// printed: both can carry a password, so only the owner reads them.
func TestDescribeHealthKeepsTheCommandAndItsOutputFromOtherAccounts(t *testing.T) {
	health := describeHealth(false, inspectedWithCheck())

	assert.Equal(t, *health.Status, "unhealthy")
	assert.Equal(t, *health.Command, "")
	checks := *health.Checks
	assert.Equal(t, len(checks), 2)
	assert.Equal(t, *checks[0].ExitCode, 2)
	assert.Equal(t, *checks[0].Output, "")
}

func TestDescribeHealthOfAContainerWithNoCheck(t *testing.T) {
	for name, inspected := range map[string]*types.ContainerJSON{
		"nothing known": nil,
		"no check declared": {
			ContainerJSONBase: &types.ContainerJSONBase{State: &dockercontainer.State{Status: "running"}},
			Config:            &dockercontainer.Config{},
		},
		"a check switched off": {
			ContainerJSONBase: &types.ContainerJSONBase{State: &dockercontainer.State{Status: "running"}},
			Config:            &dockercontainer.Config{Healthcheck: &dockercontainer.HealthConfig{Test: []string{"NONE"}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			health := describeHealth(true, inspected)

			assert.Equal(t, *health.Status, "")
			assert.Equal(t, *health.Command, "")
			assert.Equal(t, len(*health.Checks), 0)
		})
	}
}
