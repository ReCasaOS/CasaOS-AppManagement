package v2

import (
	"encoding/json"
	"testing"

	"github.com/inkly/CasaOS-AppManagement/codegen"
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
