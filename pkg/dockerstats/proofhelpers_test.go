package dockerstats

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"gotest.tools/v3/assert"
)

// Helpers of the Docker proof (proof_linux_test.go), kept here so they are
// tested on any host.

// composeFile is the proof's compose project: one service that burns CPU
// under a 0.5 core limit and restarts by policy.
const composeFile = `services:
  web:
    image: busybox
    command: ["sh", "-c", "while :; do :; done"]
    restart: always
    cpus: 0.5
    labels:
      stats-proof: "1"
`

// writeCompose writes composeFile as dir/compose.yaml and returns its path.
func writeCompose(dir string) (string, error) {
	path := filepath.Join(dir, "compose.yaml")
	return path, os.WriteFile(path, []byte(composeFile), 0o644)
}

// pointsOf returns the points of key ("cpu/user", "network/rx:eth0") in res,
// nil when the series is absent.
func pointsOf(res stats.Result, key string) []stats.Point {
	chart, name := stats.SplitKey(key)
	for _, sd := range res.Series {
		if sd.Chart == chart && sd.Name == name {
			return sd.Points
		}
	}
	return nil
}

// lastValue is the latest non-null average of key in res.
func lastValue(res stats.Result, key string) (float64, bool) {
	v := values(pointsOf(res, key))
	if len(v) == 0 {
		return 0, false
	}
	return v[len(v)-1], true
}

// written returns the times of the points of key that hold a value.
func written(res stats.Result, key string) []int64 {
	var out []int64
	for _, p := range pointsOf(res, key) {
		if p.Avg != nil {
			out = append(out, p.T)
		}
	}
	return out
}

func TestTheProofComposeFileDeclaresItsLimits(t *testing.T) {
	path, err := writeCompose(t.TempDir())
	assert.NilError(t, err)
	raw, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, filepath.Base(path), "compose.yaml")
	assert.Equal(t, string(raw), composeFile)
	for _, want := range []string{"restart: always", "cpus: 0.5", `stats-proof: "1"`, "image: busybox"} {
		assert.Assert(t, strings.Contains(string(raw), want), want)
	}
}

func TestTheProofSeriesHelpers(t *testing.T) {
	one, two := 1.0, 2.0
	res := stats.Result{Series: []stats.SeriesData{
		{Chart: "network", Name: "rx:eth0", Points: []stats.Point{{T: 10, Avg: &one}, {T: 15}, {T: 20, Avg: &two}, {T: 25}}},
		{Chart: "cpu", Name: "user", Points: []stats.Point{{T: 10}}},
	}}
	assert.Equal(t, len(pointsOf(res, "network/rx:eth0")), 4)
	assert.Assert(t, pointsOf(res, "memory/limit") == nil)
	v, ok := lastValue(res, "network/rx:eth0")
	assert.Assert(t, ok)
	assert.Equal(t, v, 2.0)
	_, ok = lastValue(res, "cpu/user") // only gaps
	assert.Assert(t, !ok)
	_, ok = lastValue(res, "memory/limit")
	assert.Assert(t, !ok)
	assert.DeepEqual(t, written(res, "network/rx:eth0"), []int64{10, 20})
	assert.Assert(t, written(res, "cpu/user") == nil)
}
