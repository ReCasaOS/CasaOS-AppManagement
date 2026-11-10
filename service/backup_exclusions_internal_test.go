package service

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
)

func exclusionsInTempDir(t *testing.T) string {
	t.Helper()

	was := BackupExclusionsPath
	BackupExclusionsPath = filepath.Join(t.TempDir(), "exclusions.json")
	t.Cleanup(func() { BackupExclusionsPath = was })

	return BackupExclusionsPath
}

// The list is kept as full paths, each once, and what would empty every backup is refused.
func TestBackupExclusionsAreFullPathsKeptOnceAndSorted(t *testing.T) {
	exclusionsInTempDir(t)

	if got, err := BackupExclusions(); err != nil || len(got) != 0 {
		t.Fatalf("no file is an empty list: %v %v", got, err)
	}

	for _, bad := range [][]string{{"mnt/nas"}, {"/"}, {"/mnt/.."}, {""}, {"/mnt/nas", "~/films"}} {
		if err := SetBackupExclusions(bad); !errors.Is(err, ErrInvalidBackupExclusion) {
			t.Fatalf("%q: want a refusal, got %v", bad, err)
		}
	}
	if got, _ := BackupExclusions(); len(got) != 0 {
		t.Fatalf("a refused list saves nothing: %v", got)
	}

	if err := SetBackupExclusions([]string{" /mnt/nas/ ", "/DATA/Media", "/mnt/x/../nas"}); err != nil {
		t.Fatal(err)
	}
	got, err := BackupExclusions()
	if err != nil || !slices.Equal(got, []string{"/DATA/Media", "/mnt/nas"}) {
		t.Fatalf("cleaned, once each, sorted: %v %v", got, err)
	}
}

// What an app mounts from inside an excluded folder is left out with the reason, and
// nothing else is: not a neighbour whose name starts the same way, not a named volume.
func TestAFolderInsideAnExcludedOneIsLeftOut(t *testing.T) {
	exclusionsInTempDir(t)
	if err := SetBackupExclusions([]string{"/mnt/nas", "/dev"}); err != nil {
		t.Fatal(err)
	}

	app := &ComposeApp{WorkingDir: appDir, Services: volumes(
		types.ServiceVolumeConfig{Type: "bind", Source: "/mnt/nas", Target: "/a"},
		types.ServiceVolumeConfig{Type: "bind", Source: "/mnt/nas/films", Target: "/b"},
		types.ServiceVolumeConfig{Type: "bind", Source: "/mnt/nas2/films", Target: "/c"},
		types.ServiceVolumeConfig{Type: "bind", Source: "/DATA/AppData/app", Target: "/d"},
		types.ServiceVolumeConfig{Type: "bind", Source: "/dev/dri", Target: "/e"},
		types.ServiceVolumeConfig{Type: "volume", Source: "data", Target: "/f"},
	)}

	entries, err := app.BackupInventoryHere()
	if err != nil {
		t.Fatal(err)
	}

	skips := map[string]string{}
	for _, entry := range entries {
		skips[entry.Target] = entry.Skip
	}
	want := map[string]string{
		"/a": backupExclusionSkip,
		"/b": backupExclusionSkip,
		"/c": "",
		"/d": "",
		"/e": "kernel interface, with no contents to copy", // its own reason stays
		"/f": "",
	}
	for target, skip := range want {
		if skips[target] != skip {
			t.Fatalf("%s: want skip %q, got %q", target, skip, skips[target])
		}
	}
}

// A list that cannot be read stops the backup: carrying on would copy what was left out.
func TestAnUnreadableExclusionListIsAnErrorNotAnEmptyList(t *testing.T) {
	file := exclusionsInTempDir(t)

	for _, content := range []string{"{not a list", `["relative"]`} {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := BackupExclusions(); err == nil {
			t.Fatalf("%s: want an error", content)
		}
		if _, err := (&ComposeApp{}).BackupInventoryHere(); err == nil {
			t.Fatalf("%s: the inventory must refuse too", content)
		}
	}
}
