package service

import (
	stdjson "encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Folders the owner of the box never wants in a backup, whichever app mounts them: a
// share on a NAS an app only reads from, a media library that is kept somewhere else.
// One list for the box, because the same share is usually mounted by several apps, and
// forgetting it in one of them is a night spent copying it.

var (
	ErrInvalidBackupExclusion = errors.New("invalid backup exclusion")
	BackupExclusionsPath      = "/var/lib/casaos/backups/exclusions.json"

	backupExclusionsLock sync.Mutex
)

// backupExclusionSkip is the reason the manifest gives for a folder left out this way.
const backupExclusionSkip = "left out by this box's backup settings"

// BackupExclusions reads the list; a missing file is an empty one. A file that cannot be
// understood is an error, and whoever asked copies nothing: carrying on without the list
// would copy exactly what the owner asked to leave out.
func BackupExclusions() ([]string, error) {
	backupExclusionsLock.Lock()
	defer backupExclusionsLock.Unlock()

	content, err := os.ReadFile(BackupExclusionsPath)
	if os.IsNotExist(err) || (err == nil && len(content) == 0) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}

	paths := []string{}
	if err := stdjson.Unmarshal(content, &paths); err != nil {
		return nil, fmt.Errorf("the backup exclusions cannot be read; they were left untouched: %w", err)
	}

	// a file edited by hand is held to what PUT refuses
	return cleanBackupExclusions(paths)
}

// SetBackupExclusions replaces the list with what the owner chose.
func SetBackupExclusions(paths []string) error {
	clean, err := cleanBackupExclusions(paths)
	if err != nil {
		return err
	}

	backupExclusionsLock.Lock()
	defer backupExclusionsLock.Unlock()

	encoded, err := stdjson.MarshalIndent(clean, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(BackupExclusionsPath), 0o700); err != nil {
		return err
	}
	temp := BackupExclusionsPath + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return err
	}

	return os.Rename(temp, BackupExclusionsPath)
}

// cleanBackupExclusions is the list as it is kept: absolute host paths, written the short
// way, each once, sorted.
func cleanBackupExclusions(paths []string) ([]string, error) {
	clean := []string{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if !strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf("%w: %q is not a full path, which starts with /", ErrInvalidBackupExclusion, p)
		}

		p = path.Clean(p)
		if p == "/" {
			return nil, fmt.Errorf("%w: leaving out / would leave out everything", ErrInvalidBackupExclusion)
		}
		if !slices.Contains(clean, p) {
			clean = append(clean, p)
		}
	}
	sort.Strings(clean)

	return clean, nil
}

// withoutExcluded marks as skipped the folders an app mounts from inside an excluded one.
//
// shortcut: paths are compared as written, so a symlink to an excluded folder and a named
// volume kept on one are still copied; resolve both when somebody is bitten by it.
func withoutExcluded(entries []BackupEntry, excluded []string) []BackupEntry {
	for i, entry := range entries {
		if entry.Kind != BackupKindBind || entry.Skip != "" {
			continue
		}

		for _, folder := range excluded {
			if entry.Path == folder || strings.HasPrefix(entry.Path, folder+"/") {
				entries[i].Skip = backupExclusionSkip
				break
			}
		}
	}

	return entries
}

// BackupInventoryHere is the app's inventory less what this box's settings leave out. A
// backup and a restore both go by it, so an excluded folder is neither copied nor written
// over by an older backup that still holds it.
func (a *ComposeApp) BackupInventoryHere() ([]BackupEntry, error) {
	excluded, err := BackupExclusions()
	if err != nil {
		return nil, err
	}

	return withoutExcluded(a.BackupInventory(), excluded), nil
}
