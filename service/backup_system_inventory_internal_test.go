package service

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func boxFS(children map[string][]systemChild, sizes map[string]int64) systemFS {
	return systemFS{
		List: func(dir string) ([]systemChild, error) {
			c, ok := children[dir]
			if !ok {
				return nil, errors.New("unreadable")
			}

			return c, nil
		},
		Size:   func(p string) int64 { return sizes[p] },
		Exists: func(p string) bool { return true },
	}
}

func entryFor(entries []BackupEntry, p string) (BackupEntry, bool) {
	for _, e := range entries {
		if e.Path == p {
			return e, true
		}
	}

	return BackupEntry{}, false
}

func TestAnUnknownFileOfTheBoxIsBackedUpAndAnExcludedOneSaysWhy(t *testing.T) {
	fs := boxFS(map[string][]systemChild{
		"/var/lib/casaos":         {{Name: "db"}, {Name: "conf"}, {Name: "apps"}, {Name: "fleet"}, {Name: "never-heard-of.json"}, {Name: "backups"}, {Name: "gateway"}, {Name: "files"}, {Name: "git_apps"}},
		"/var/lib/casaos/git_apps": {{Name: "app.key"}, {Name: "work"}},
		"/var/lib/casaos/backups": {{Name: "schedules.json"}, {Name: "restic"}, {Name: "sync.json"}},
		"/var/lib/casaos/gateway": {{Name: "guard-state.json"}, {Name: "routes.json"}},
	}, nil)

	entries, err := SystemBackupInventory(fs)
	assert.NilError(t, err)

	for _, p := range []string{
		"/etc/casaos", "/etc/samba/smb.casa.conf", "/root/.config/rclone/rclone.conf",
		"/var/lib/casaos/db", "/var/lib/casaos/conf", "/var/lib/casaos/fleet", "/var/lib/casaos/never-heard-of.json",
		"/var/lib/casaos/backups/schedules.json", "/var/lib/casaos/backups/sync.json", "/var/lib/casaos/gateway/routes.json",
		"/var/lib/casaos/git_apps/app.key",
	} {
		e, ok := entryFor(entries, p)
		assert.Assert(t, ok, "%s is in the backup", p)
		assert.Equal(t, e.Skip, "", p)
	}
	for _, p := range []string{
		"/var/lib/casaos/apps", "/var/lib/casaos/backups/restic", "/var/lib/casaos/backups", "/var/lib/casaos/gateway",
		"/var/lib/casaos/gateway/guard-state.json", "/var/lib/casaos/files", "/var/lib/casaos/git_apps", "/var/lib/casaos/git_apps/work",
	} {
		_, ok := entryFor(entries, p)
		assert.Assert(t, !ok, "%s has no entry at all", p)
	}

	// what the fixed list already holds is not listed a second time
	count := 0
	for _, e := range entries {
		if e.Path == "/var/lib/casaos/db" || e.Path == "/var/lib/casaos/backups/schedules.json" {
			count++
		}
	}
	assert.Equal(t, count, 2)

	// the fixed list comes first, in its order
	assert.Equal(t, entries[0].Path, "/etc/casaos")

	fleet, _ := entryFor(entries, "/var/lib/casaos/fleet")
	assert.Equal(t, fleet.Class, "identity")
	unknown, _ := entryFor(entries, "/var/lib/casaos/never-heard-of.json")
	assert.Equal(t, unknown.Class, "config")
	assert.Assert(t, systemExcludedReason("apps") != "")
	assert.Assert(t, systemExcludedReason("backups/restic") != "")
	assert.Assert(t, systemExcludedReason("anything.notified") != "")
	assert.Equal(t, systemExcludedReason("db"), "")
}

func TestASplitFolderThatIsNotThereIsAbsentNotAnError(t *testing.T) {
	fs := boxFS(nil, nil)
	fs.List = func(dir string) ([]systemChild, error) {
		if dir == "/var/lib/casaos" {
			return []systemChild{{Name: "fleet"}, {Name: "backups"}, {Name: "gateway"}}, nil
		}

		return nil, os.ErrNotExist
	}

	entries, err := SystemBackupInventory(fs)
	assert.NilError(t, err)
	_, ok := entryFor(entries, "/var/lib/casaos/fleet")
	assert.Assert(t, ok)
	// nothing of the absent folders but what the fixed list names itself
	for _, e := range entries {
		inSplit := strings.Contains(e.Path, "/backups/") || strings.Contains(e.Path, "/gateway/") ||
			e.Path == "/var/lib/casaos/backups" || e.Path == "/var/lib/casaos/gateway"
		assert.Assert(t, !inSplit || slices.Contains(systemBackupPaths(), e.Path), e.Path)
	}
}

func TestASplitFolderThatCannotBeListedIsAnErrorNotAPartialBackup(t *testing.T) {
	fs := boxFS(nil, nil)
	fs.List = func(dir string) ([]systemChild, error) {
		if dir == "/var/lib/casaos" {
			return []systemChild{{Name: "fleet"}, {Name: "backups"}}, nil
		}

		return nil, os.ErrPermission
	}

	_, err := SystemBackupInventory(fs)
	assert.Assert(t, errors.Is(err, os.ErrPermission), "%v", err)
}

func TestTheClassIsDecidedOnTheTopLevelNameUnderTheDataFolder(t *testing.T) {
	for p, want := range map[string]string{
		"/var/lib/casaos/fleet":            "identity",
		"/var/lib/casaos/tls":              "identity",
		"/var/lib/casaos/docker_root":      "machine",
		"/var/lib/casaos/db":               "config",
		"/var/lib/casaos/gateway/x.json":   "config",
		"/etc/casaos":                      "config",
		"/root/.config/rclone/rclone.conf": "config",
	} {
		assert.Equal(t, systemClass(p), want, p)
	}
}

func TestALinkIsNeverFollowed(t *testing.T) {
	fs := boxFS(map[string][]systemChild{"/var/lib/casaos": {{Name: "linked", Link: true}}}, nil)

	entries, err := SystemBackupInventory(fs)
	assert.NilError(t, err)
	e, ok := entryFor(entries, "/var/lib/casaos/linked")
	assert.Assert(t, ok)
	assert.Assert(t, strings.Contains(e.Skip, "link"), e.Skip)
}

func TestANewEntryOverTheCapIsSkippedAndAnOldOneNever(t *testing.T) {
	fs := boxFS(map[string][]systemChild{"/var/lib/casaos": {{Name: "huge"}, {Name: "db"}, {Name: "small"}}},
		map[string]int64{"/var/lib/casaos/huge": systemEntryCap + 1, "/var/lib/casaos/db": systemEntryCap + 1, "/var/lib/casaos/small": systemEntryCap})

	entries, err := SystemBackupInventory(fs)
	assert.NilError(t, err)
	huge, _ := entryFor(entries, "/var/lib/casaos/huge")
	assert.Assert(t, strings.Contains(huge.Skip, "50 MB") && strings.Contains(huge.Skip, "size"), huge.Skip)
	db, _ := entryFor(entries, "/var/lib/casaos/db")
	assert.Equal(t, db.Skip, "")
	small, _ := entryFor(entries, "/var/lib/casaos/small")
	assert.Equal(t, small.Skip, "")
}

func TestABoxThatCannotBeReadIsAnErrorNotAnEmptyBackup(t *testing.T) {
	_, err := SystemBackupInventory(boxFS(nil, nil))
	assert.Assert(t, err != nil)

	_, err = PlanSystemBackup(boxFS(nil, nil))
	assert.Assert(t, err != nil)
}

func TestThePlanCarriesTheClass(t *testing.T) {
	fs := boxFS(map[string][]systemChild{"/var/lib/casaos": {{Name: "fleet"}}}, nil)
	entries, err := SystemBackupInventory(fs)
	assert.NilError(t, err)

	for _, op := range PlanBackup(SystemBackupName, entries, nil).Operations {
		if op.Source == "/var/lib/casaos/fleet" {
			assert.Equal(t, op.Class, "identity")

			return
		}
	}
	t.Fatal("fleet is planned")
}

func TestASocketOrAPipeIsSkippedNotCopied(t *testing.T) {
	fs := boxFS(map[string][]systemChild{"/var/lib/casaos": {{Name: "x.sock", Special: true}, {Name: "db"}}}, nil)

	entries, err := SystemBackupInventory(fs)
	assert.NilError(t, err)
	e, ok := entryFor(entries, "/var/lib/casaos/x.sock")
	assert.Assert(t, ok)
	assert.Assert(t, strings.Contains(e.Skip, "not a file or a folder"), e.Skip)
}
