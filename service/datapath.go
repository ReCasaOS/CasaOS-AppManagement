package service

import (
	"path/filepath"

	"github.com/ReCasaOS/CasaOS-Common/utils/constants"
)

// ApplyDataPath points stats, git-app state, backups and image-update
// cache at root. Empty root is /var/lib/casaos. gitAppsDataRoot and
// os-upgrade/busy stay on the box paths.
func ApplyDataPath(root string) string {
	if root == "" {
		root = constants.DefaultDataPath
	}
	StatsDir = filepath.Join(root, "stats", "app_management")
	gitAppsDir = filepath.Join(root, "git_apps")
	BackupLogPath = filepath.Join(root, "backups", "runs.json")
	BackupHoldPath = filepath.Join(root, "backups", "holding.json")
	BackupSchedulePath = filepath.Join(root, "backups", "schedules.json")
	imageUpdateStatePath = filepath.Join(root, "image_updates.json")
	return root
}
