package v2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// The compose list drops a stack whose files do not load and only logs why. The grid now
// carries that reason on the stack's containers, so the owner is told what to change.
func TestComposeLoadErrorNamesWhatStopsTheStack(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.yml")
	good := filepath.Join(dir, "good.yml")
	assert.NilError(t, os.WriteFile(broken, []byte("services:\n  web:\n    image: nginx\n    not_a_compose_key: 1\n"), 0o644))
	assert.NilError(t, os.WriteFile(good, []byte("services:\n  web:\n    image: nginx\n"), 0o644))

	reasons := composeLoadErrorCache{}

	reason := reasons.of("broken", broken)
	assert.Assert(t, reason != nil)
	assert.Assert(t, strings.Contains(*reason, "not_a_compose_key"), *reason)

	assert.Assert(t, reasons.of("good", good) == nil)

	// a Portainer or Dockge stack: its containers name a file inside that tool's container
	missing := reasons.of("elsewhere", "/data/compose/1/docker-compose.yml")
	assert.Assert(t, missing != nil)
	assert.Assert(t, strings.Contains(*missing, "/data/compose/1/docker-compose.yml"), *missing)

	// asked once per project for one grid
	assert.NilError(t, os.Remove(broken))
	assert.Equal(t, reasons.of("broken", broken), reason)
}
