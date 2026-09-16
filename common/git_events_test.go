package common_test

import (
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"gotest.tools/v3/assert"
)

// The dashboard listens for these by name: each is registered, with what it carries.
func TestGitAppEventsAreRegisteredWithTheirProperties(t *testing.T) {
	registered := map[string][]string{}
	for _, eventType := range common.EventTypes {
		properties := []string{}
		for _, property := range eventType.PropertyTypeList {
			properties = append(properties, property.Name)
		}
		registered[eventType.Name] = properties
	}

	for name, properties := range map[string][]string{
		"app:git-build-begin":    {"app:name"},
		"app:git-build-progress": {"app:name", "message"},
		"app:git-build-end":      {"app:name"},
		"app:git-build-error":    {"app:name", "message"},
		"app:git-deploy-end":     {"app:name"},
		"app:git-deploy-error":   {"app:name", "message"},
	} {
		assert.DeepEqual(t, registered[name], properties)
	}
}
