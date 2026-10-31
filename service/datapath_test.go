package service

import (
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/constants"
)

func TestApplyDataPath(t *testing.T) {
	prevStats, prevGit, prevLog, prevHold, prevSched, prevImg := StatsDir, gitAppsDir, BackupLogPath, BackupHoldPath, BackupSchedulePath, imageUpdateStatePath
	t.Cleanup(func() {
		StatsDir, gitAppsDir, BackupLogPath, BackupHoldPath, BackupSchedulePath, imageUpdateStatePath = prevStats, prevGit, prevLog, prevHold, prevSched, prevImg
	})

	root := t.TempDir()
	got := ApplyDataPath(root)
	if got != root {
		t.Fatalf("ApplyDataPath = %q, want %q", got, root)
	}
	want := map[string]string{
		"StatsDir":             filepath.Join(root, "stats", "app_management"),
		"gitAppsDir":           filepath.Join(root, "git_apps"),
		"BackupLogPath":        filepath.Join(root, "backups", "runs.json"),
		"BackupHoldPath":       filepath.Join(root, "backups", "holding.json"),
		"BackupSchedulePath":   filepath.Join(root, "backups", "schedules.json"),
		"imageUpdateStatePath": filepath.Join(root, "image_updates.json"),
	}
	gotPaths := map[string]string{
		"StatsDir":             StatsDir,
		"gitAppsDir":           gitAppsDir,
		"BackupLogPath":        BackupLogPath,
		"BackupHoldPath":       BackupHoldPath,
		"BackupSchedulePath":   BackupSchedulePath,
		"imageUpdateStatePath": imageUpdateStatePath,
	}
	for name, w := range want {
		if gotPaths[name] != w {
			t.Errorf("%s = %q, want %q", name, gotPaths[name], w)
		}
	}
	if gitAppsDataRoot != "/DATA/AppData" {
		t.Errorf("gitAppsDataRoot changed: %q", gitAppsDataRoot)
	}
	if osUpgradeBusyPath != "/var/lib/casaos/os-upgrade/busy" {
		t.Errorf("osUpgradeBusyPath changed: %q", osUpgradeBusyPath)
	}
}

func TestApplyDataPathEmptyUsesDefault(t *testing.T) {
	prev := StatsDir
	t.Cleanup(func() { StatsDir = prev })
	got := ApplyDataPath("")
	if got != constants.DefaultDataPath {
		t.Fatalf("root = %q, want %q", got, constants.DefaultDataPath)
	}
	want := filepath.Join(constants.DefaultDataPath, "stats", "app_management")
	if StatsDir != want {
		t.Fatalf("StatsDir = %q, want %q", StatsDir, want)
	}
}

func TestApplyGitAppsDataRoot(t *testing.T) {
	prev := gitAppsDataRoot
	t.Cleanup(func() { gitAppsDataRoot = prev })

	ApplyGitAppsDataRoot("/tmp/x")
	if gitAppsDataRoot != "/tmp/x" {
		t.Fatalf("got %q", gitAppsDataRoot)
	}
	ApplyGitAppsDataRoot("")
	if gitAppsDataRoot != "/DATA/AppData" {
		t.Fatalf("empty got %q", gitAppsDataRoot)
	}
}
