package v2

import (
	"testing"

	"github.com/inkly/CasaOS-AppManagement/codegen"
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
				out[service] = append(out[service], codegen.ContainerSummary{State: state})
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
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, composeAppStatus(states(c.containers)), c.want)
		})
	}

	// no containers at all: nothing to claim
	assert.Equal(t, composeAppStatus(map[string][]codegen.ContainerSummary{}), "")
}
