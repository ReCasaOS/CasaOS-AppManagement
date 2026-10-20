package dockerstats

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"gotest.tools/v3/assert"
)

// Common creates its directory 0750 and its files 0640; these hold container names.
func TestStatsFilesArePrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "stats")
	store, err := stats.Open(dir, &fakeClock{t0})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	eval, err := stats.NewEvaluator(store, dir, func(stats.Event) {})
	assert.NilError(t, err)
	assert.NilError(t, store.Record(alertItem, t0, stats.Sample{"cpu/user": 1}))
	assert.NilError(t, eval.SetRules(alertItem, []stats.Rule{cpuRule(2)}))
	fi, err := os.Stat(dir)
	assert.NilError(t, err)
	assert.Equal(t, fi.Mode().Perm(), os.FileMode(0o750))
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	assert.Assert(t, len(files) >= 2, "%v", files)
	for _, f := range files {
		fi, err := os.Stat(f)
		assert.NilError(t, err)
		assert.Equal(t, fi.Mode().Perm(), os.FileMode(0o640), f)
	}
}
