package v2

import (
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	containertypes "github.com/moby/moby/api/types/container"
	"gotest.tools/v3/assert"
)

// The dashboard draws one dot per app. It used to come from the main service's first
// container alone, so an app whose database had died showed green -- the one thing
// that view exists to tell you, wrong.
func TestComposeAppStatusIsTheWorstOfEveryContainer(t *testing.T) {
	states := func(byService map[string][]string) map[string][]codegen.ContainerSummary {
		out := map[string][]codegen.ContainerSummary{}
		for service, list := range byService {
			for _, state := range list {
				out[service] = append(out[service], codegen.ContainerSummary{State: containertypes.ContainerState(state)})
			}
		}
		return out
	}

	for name, c := range map[string]struct {
		containers map[string][]string
		want       string
	}{
		"everything up":                 {map[string][]string{"app": {"running"}, "db": {"running"}}, "running"},
		"the database died":             {map[string][]string{"app": {"running"}, "db": {"exited"}}, "exited"},
		"a sidecar is restarting":       {map[string][]string{"app": {"running"}, "side": {"restarting"}}, "restarting"},
		"dead outranks exited":          {map[string][]string{"a": {"exited"}, "b": {"dead"}}, "dead"},
		"one replica of a service down": {map[string][]string{"app": {"running", "exited"}}, "exited"},
		"paused is not running":         {map[string][]string{"app": {"paused"}}, "paused"},
		"a single healthy container":    {map[string][]string{"app": {"running"}}, "running"},
		// an answer we do not recognise is not a reason to report `running`
		"an unfamiliar state wins": {map[string][]string{"app": {"running"}, "b": {"something-new"}}, "something-new"},
		// the main service being down is not the app being unanswerable: the caller
		// used to give up here and report `unknown` for a stack that was half up
		"the main service has no container": {map[string][]string{"db": {"running"}}, "running"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, composeAppStatus(states(c.containers)), c.want)
		})
	}

	// no containers at all: nothing any container claimed, and the app still needs
	// the one word the dashboard renders
	assert.Equal(t, composeAppStatus(map[string][]codegen.ContainerSummary{}), "unknown")
}

// A one-shot service that finished cleanly is not a reason to show the app stopped.
func TestComposeAppStatusIgnoresAServiceThatFinishedItsJob(t *testing.T) {
	c := func(state, status string) codegen.ContainerSummary {
		return codegen.ContainerSummary{State: containertypes.ContainerState(state), Status: status}
	}
	for name, tc := range map[string]struct {
		containers map[string][]codegen.ContainerSummary
		want       string
	}{
		"finished init, the app runs": {map[string][]codegen.ContainerSummary{"init": {c("exited", "Exited (0) 3 minutes ago")}, "app": {c("running", "Up 3 minutes")}}, "running"},
		"crashed init, the app runs":  {map[string][]codegen.ContainerSummary{"init": {c("exited", "Exited (1) 3 minutes ago")}, "app": {c("running", "Up 3 minutes")}}, "exited"},
		"stopped by hand, app runs":   {map[string][]codegen.ContainerSummary{"db": {c("exited", "Exited (137) 3 minutes ago")}, "app": {c("running", "Up 3 minutes")}}, "exited"},
		"nothing runs, finished init": {map[string][]codegen.ContainerSummary{"init": {c("exited", "Exited (0) 3 minutes ago")}, "app": {c("exited", "Exited (0) 3 minutes ago")}}, "exited"},
		"a restarting service counts": {map[string][]codegen.ContainerSummary{"init": {c("exited", "Exited (0) 3 minutes ago")}, "app": {c("running", "Up")}, "side": {c("restarting", "Restarting")}}, "restarting"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, composeAppStatus(tc.containers), tc.want)
		})
	}
}
