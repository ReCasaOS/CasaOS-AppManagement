package common_test

import (
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"gotest.tools/v3/assert"
)

// The core's alerts and the dashboard listen for the resource alerts by name.
func TestResourceAlertEventsAreRegistered(t *testing.T) {
	registered := map[string][]string{}
	for _, eventType := range common.EventTypes {
		assert.Equal(t, eventType.SourceID, common.AppManagementServiceName)
		properties := []string{}
		for _, property := range eventType.PropertyTypeList {
			properties = append(properties, property.Name)
		}
		registered[eventType.Name] = properties
	}

	want := []string{"item", "name", "kind", "situation", "value", "minutes", "since", "source"}
	assert.DeepEqual(t, registered["resource:alert-firing"], want)
	assert.DeepEqual(t, registered["resource:alert-resolved"], want)
}
