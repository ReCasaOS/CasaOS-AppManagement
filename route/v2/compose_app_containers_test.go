package v2

import (
	"encoding/json"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/samber/lo"
	"gotest.tools/v3/assert"
)

// The response used to collapse each service to its first container, so the replicas of
// a scaled service were invisible and a service with no container at all panicked the
// handler. This pins the wire shape the dashboard reads: service -> list of containers.
func TestComposeAppContainersCarriesEveryContainer(t *testing.T) {
	containers := map[string][]codegen.ContainerSummary{
		"gluetun": {{ID: "aaa", Service: "gluetun", State: "running"}},
		"qbit":    {{ID: "bbb", Service: "qbit", State: "running"}, {ID: "ccc", Service: "qbit", State: "exited"}},
		"down":    {},
	}

	encoded, err := json.Marshal(codegen.ComposeAppContainers{
		Main:       lo.ToPtr("gluetun"),
		Containers: &containers,
	})
	assert.NilError(t, err)

	var decoded struct {
		Main       string                       `json:"main"`
		Containers map[string][]json.RawMessage `json:"containers"`
	}
	assert.NilError(t, json.Unmarshal(encoded, &decoded))

	assert.Equal(t, decoded.Main, "gluetun")
	assert.Equal(t, len(decoded.Containers), 3)
	assert.Equal(t, len(decoded.Containers["gluetun"]), 1)
	// the second replica is the one the old shape threw away
	assert.Equal(t, len(decoded.Containers["qbit"]), 2)
	// a declared service that is down is reported as an empty list, not omitted
	assert.Equal(t, len(decoded.Containers["down"]), 0)
}

// The one state the dashboard shows for a multi-container app. An app is only as
// healthy as its unhealthiest part, so the fold has to pick the worst container of
// any service -- the bug being that a stack whose VPN sidecar is dead read as
// `running` because the service listed first was up.
func TestComposeAppStatusReportsTheWorstContainer(t *testing.T) {
	for _, c := range []struct {
		name   string
		states map[string][]string
		want   string
	}{
		{"everything up", map[string][]string{"a": {"running"}, "b": {"running", "running"}}, "running"},
		{"one replica exited", map[string][]string{"a": {"running"}, "b": {"running", "exited"}}, "exited"},
		{"dead outranks exited", map[string][]string{"a": {"exited"}, "b": {"dead"}}, "dead"},
		{"restarting outranks paused", map[string][]string{"a": {"paused"}, "b": {"restarting"}}, "restarting"},
		// an answer we do not recognise is not a reason to report `running`
		{"unfamiliar state wins", map[string][]string{"a": {"running"}, "b": {"hibernating"}}, "hibernating"},
		// a declared service with no container is the empty list the handler now sends
		{"nothing is up", map[string][]string{"a": {}}, "unknown"},
		{"no services at all", map[string][]string{}, "unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			containers := map[string][]codegen.ContainerSummary{}
			for service, states := range c.states {
				containers[service] = lo.Map(states, func(state string, _ int) codegen.ContainerSummary {
					return codegen.ContainerSummary{Service: service, State: state}
				})
			}
			assert.Equal(t, composeAppStatus(containers), c.want)
		})
	}
}
