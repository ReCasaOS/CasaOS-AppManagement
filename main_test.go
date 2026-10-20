package main

import (
	"os"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// The sampler publishes events as soon as it starts (alerts rebuilt from the history),
// so it must start after the event types are registered. main cannot run in a test; this
// pins the order in the source.
func TestTheSamplerStartsAfterTheEventTypesAreRegistered(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	assert.NilError(t, err)
	src := string(raw)
	register := strings.Index(src, "RegisterEventTypesWithResponse(")
	start := strings.Index(src, "service.StartStats(")
	assert.Assert(t, register > 0 && start > 0, "main.go no longer registers the events or starts the sampler")
	assert.Assert(t, register < start, "StartStats runs before the event types are registered")
}
