package service

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func dashboardHiddenFile(t *testing.T) {
	t.Helper()
	was := DashboardHiddenPath
	DashboardHiddenPath = filepath.Join(t.TempDir(), "dashboard-hidden.json")
	t.Cleanup(func() { DashboardHiddenPath = was })
}

func TestDashboardHiddenHidesAndShowsAgain(t *testing.T) {
	dashboardHiddenFile(t)
	assert.DeepEqual(t, DashboardHidden.List(), []string{})

	assert.NilError(t, DashboardHidden.Set("cloudflared", true))
	assert.NilError(t, DashboardHidden.Set("adguard", true))
	assert.NilError(t, DashboardHidden.Set("adguard", true)) // twice is once
	assert.DeepEqual(t, DashboardHidden.List(), []string{"adguard", "cloudflared"})

	assert.NilError(t, DashboardHidden.Set("adguard", false))
	assert.NilError(t, DashboardHidden.Set("never-hidden", false))
	assert.DeepEqual(t, DashboardHidden.List(), []string{"cloudflared"})
}

// A file that will not parse hides nothing, and is not replaced by a write.
func TestDashboardHiddenLeavesABrokenFileAlone(t *testing.T) {
	dashboardHiddenFile(t)
	assert.NilError(t, os.WriteFile(DashboardHiddenPath, []byte("{not a list"), 0o600))

	assert.DeepEqual(t, DashboardHidden.List(), []string{})
	assert.Assert(t, DashboardHidden.Set("cloudflared", true) != nil)
	content, err := os.ReadFile(DashboardHiddenPath)
	assert.NilError(t, err)
	assert.Equal(t, string(content), "{not a list")
}
