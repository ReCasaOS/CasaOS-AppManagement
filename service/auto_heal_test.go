package service

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func autoHealAt(t *testing.T) string {
	was := AutoHealPath
	AutoHealPath = filepath.Join(t.TempDir(), "sub", "auto-heal.json")
	t.Cleanup(func() { AutoHealPath = was })

	return AutoHealPath
}

func TestAutoHealOnWithoutFile(t *testing.T) {
	autoHealAt(t)
	assert.Assert(t, AutoHealEnabled())
}

func TestAutoHealOnWhenUnreadable(t *testing.T) {
	path := autoHealAt(t)
	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	assert.NilError(t, os.WriteFile(path, []byte("{not json"), 0o644))
	assert.Assert(t, AutoHealEnabled(), "a malformed file reads as on")
	content, _ := os.ReadFile(path)
	assert.Equal(t, string(content), "{not json", "a read does not rewrite it")

	// a directory where the file should be: unreadable
	assert.NilError(t, os.Remove(path))
	assert.NilError(t, os.Mkdir(path, 0o755))
	assert.Assert(t, AutoHealEnabled())
}

func TestSetAutoHealRoundTrip(t *testing.T) {
	path := autoHealAt(t)

	assert.NilError(t, SetAutoHeal(false))
	assert.Assert(t, !AutoHealEnabled())
	assert.NilError(t, SetAutoHeal(true))
	assert.Assert(t, AutoHealEnabled())
	assert.NilError(t, SetAutoHeal(false))
	assert.Assert(t, !AutoHealEnabled())

	entries, err := os.ReadDir(filepath.Dir(path))
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 1, "no temp file is left beside it")
}
