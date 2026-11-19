package service

import (
	"fmt"
	"os"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/utils/constants"
)

// The box's own backup takes everything under the data folder except a named
// list, so a file a later version adds is backed up before anyone thinks of it.
// The fixed list (systemBackupPaths) stays: always copied, never capped.

// systemDataRoot is the data folder the inventory walks; ApplyDataPath moves it.
var systemDataRoot = constants.DefaultDataPath

// systemEntryCap is the size above which a new entry is left out and said so:
// a folder somebody dropped in the data folder must not turn the backup into a
// copy of their files.
const systemEntryCap int64 = 50 << 20

// systemSplit are the data folders whose children are listed one by one,
// because they hold both state and what must stay out.
var systemSplit = []string{"backups", "gateway"}

// systemExclusions are left out of the box backup, relative to the data folder
// (path.Match), each with the reason.
var systemExclusions = []struct{ pattern, reason string }{
	{"apps", "the apps are backed up on their own"},
	{"appstore", "downloaded again from the store"},
	{"www", "the web files are put back by the installer"},
	{"modules", "the modules are installed again by the installer"},
	{"geo", "a database downloaded again"},
	{"cache", "a cache, rebuilt on use"},
	{"stats", "statistics, rebuilt as they are taken"},
	{"os-upgrade", "state of an upgrade under way, meaningless on another box"},
	{"*.notified", "a notice already shown, not state"},
	{"upgraded-from", "a marker of this machine's last upgrade"},
	{"fork-release", "a marker of this machine's release"},
	{"manifest", "the installer's list of files on this machine"},
	{"image_updates.json", "an update check, redone at the next one"},
	{"backups/restic", "the repository keys of this machine's own backups"},
	{"backups/runs.json", "the log of runs, which describes this machine's past"},
	{"backups/verifications.json", "the log of verifications, which describes this machine's past"},
	{"backups/restic-checks.json", "the log of repository checks, which describes this machine's past"},
	{"backups/holding.json", "what is being held right now"},
	{"backups/bisync", "the state of a sync, rebuilt by the next one"},
	{"gateway/guard-state.json", "bans in progress, rewritten by the gateway from memory"},
}

// systemExcludedReason is why rel (relative to the data folder) is not backed
// up, "" when it is.
func systemExcludedReason(rel string) string {
	for _, e := range systemExclusions {
		if ok, _ := path.Match(e.pattern, rel); ok {
			return e.reason
		}
	}

	return ""
}

// systemClass says what a path is for, by its top-level folder under the data
// folder: "identity" is what makes this box this box, "machine" what belongs to
// this machine's disks, "config" the rest.
func systemClass(p string) string {
	rel, under := strings.CutPrefix(path.Clean(p), path.Clean(systemDataRoot)+"/")
	if !under {
		return "config"
	}
	switch strings.SplitN(rel, "/", 2)[0] {
	case "fleet", "tls":
		return "identity"
	case "docker_root":
		return "machine"
	}

	return "config"
}

type systemChild struct {
	Name string
	Link bool
}

// systemFS is what the inventory reads of the box, a seam for tests.
type systemFS struct {
	List   func(dir string) ([]systemChild, error)
	Size   func(p string) int64
	Exists func(p string) bool
}

func realSystemFS() systemFS {
	return systemFS{
		List: func(dir string) ([]systemChild, error) {
			items, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			children := make([]systemChild, 0, len(items))
			for _, item := range items {
				children = append(children, systemChild{Name: item.Name(), Link: item.Type()&os.ModeSymlink != 0})
			}

			return children, nil
		},
		Size: func(p string) int64 {
			// shortcut: what cannot be read counts for 0 bytes, so it is backed up
			// rather than left out; strict only if the cap must hold on bad files
			var total int64
			walkSize(p, &total)

			return total
		},
		Exists: pathExists,
	}
}

func walkSize(p string, total *int64) {
	info, err := os.Lstat(p)
	if err != nil {
		return
	}
	if !info.IsDir() {
		*total += info.Size()

		return
	}
	items, _ := os.ReadDir(p)
	for _, item := range items {
		walkSize(p+"/"+item.Name(), total)
	}
}

// systemLegacyEntries is the fixed list, as entries, absent ones skipped.
func systemLegacyEntries(exists func(string) bool) []BackupEntry {
	paths := systemBackupPaths()
	entries := make([]BackupEntry, 0, len(paths))
	for _, p := range paths {
		entry := BackupEntry{Kind: BackupKindBind, Path: p, Target: p, Class: systemClass(p)}
		if !exists(p) {
			entry.Skip = "not present on this box"
		}
		entries = append(entries, entry)
	}

	return entries
}

// SystemBackupInventory lists what the box is, as backup entries: the fixed
// list first, then every other child of the data folder not on the exclusion
// list. An excluded one has no entry at all, so the skipped count is only what
// is missing from the backup.
func SystemBackupInventory(fs systemFS) ([]BackupEntry, error) {
	entries := systemLegacyEntries(fs.Exists)
	have := map[string]bool{}
	for _, e := range entries {
		have[path.Clean(e.Path)] = true
	}

	children, err := fs.List(systemDataRoot)
	if err != nil {
		return nil, fmt.Errorf("the data folder %s could not be listed: %w", systemDataRoot, err)
	}

	type candidate struct {
		rel  string
		link bool
	}
	var found []candidate
	for _, c := range children {
		if !slices.Contains(systemSplit, c.Name) {
			found = append(found, candidate{c.Name, c.Link})

			continue
		}
		// a split folder absent from this box is just absent
		if grand, err := fs.List(path.Join(systemDataRoot, c.Name)); err == nil {
			for _, g := range grand {
				found = append(found, candidate{c.Name + "/" + g.Name, g.Link})
			}
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].rel < found[j].rel })

	for _, c := range found {
		p := path.Join(systemDataRoot, c.rel)
		if systemExcludedReason(c.rel) != "" || have[p] {
			continue
		}
		entry := BackupEntry{Kind: BackupKindBind, Path: p, Target: p, Class: systemClass(p)}
		switch {
		case c.link:
			entry.Skip = "a symbolic link is not followed"
		case fs.Size(p) > systemEntryCap:
			entry.Skip = fmt.Sprintf("left out because of its size: over %d MB, and not part of the box's fixed list", systemEntryCap>>20)
		}
		entries = append(entries, entry)
	}

	return entries, nil
}
