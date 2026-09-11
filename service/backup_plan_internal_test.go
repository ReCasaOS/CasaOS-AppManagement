package service

import (
	stdjson "encoding/json"
	"strings"
	"testing"
)

func planFor(entries []BackupEntry, mountpoints map[string]string) BackupManifest {
	return PlanBackup("nextcloud", entries, mountpoints)
}

func TestAPlanPutsEveryFileWhereAPersonCouldPutItBack(t *testing.T) {
	manifest := planFor([]BackupEntry{
		{Kind: BackupKindCompose, Path: appDir + "/docker-compose.yml"},
		{Kind: BackupKindEnv, Path: appDir + "/.env"},
		{Kind: BackupKindVolume, Volume: "backend-storage", Service: "app", Target: "/data"},
		{Kind: BackupKindBind, Path: "/DATA/Media", Service: "app", Target: "/media"},
		{Kind: BackupKindBind, Path: "/DATA/Photos", Service: "web", Target: "/photos"},
	}, map[string]string{"backend-storage": "/var/lib/docker/volumes/nextcloud_backend-storage/_data"})

	want := []string{
		"compose/docker-compose.yml",
		"compose/.env",
		"volumes/backend-storage",
		"binds/1",
		"binds/2",
	}

	if len(manifest.Operations) != len(want) {
		t.Fatalf("want %d operations, got %d", len(want), len(manifest.Operations))
	}
	for i, destination := range want {
		if manifest.Operations[i].Destination != destination {
			t.Errorf("%d: want %q, got %q", i, destination, manifest.Operations[i].Destination)
		}
	}

	// a volume is copied from where Docker actually keeps it, not from its name
	if manifest.Operations[2].Source != "/var/lib/docker/volumes/nextcloud_backend-storage/_data" {
		t.Fatalf("wrong volume source: %q", manifest.Operations[2].Source)
	}
}

// A guessed path that happens to name an empty folder produces a backup that
// reports success and restores nothing.
func TestAVolumeDockerCannotPlaceIsSkippedRatherThanGuessed(t *testing.T) {
	manifest := planFor([]BackupEntry{
		{Kind: BackupKindVolume, Volume: "backend-storage", Service: "app", Target: "/data"},
	}, map[string]string{})

	if len(manifest.Operations) != 0 {
		t.Fatalf("nothing can be copied for it: %+v", manifest.Operations)
	}
	if len(manifest.Skipped) != 1 || !strings.Contains(manifest.Skipped[0].Skip, "where this volume lives") {
		t.Fatalf("and the reason is recorded: %+v", manifest.Skipped)
	}

	// an empty mountpoint is the same answer as none
	empty := planFor([]BackupEntry{
		{Kind: BackupKindVolume, Volume: "v", Service: "app"},
	}, map[string]string{"v": ""})
	if len(empty.Operations) != 0 {
		t.Fatal("an empty mountpoint is not a path")
	}
}

// The reasons an entry was left out travel with the backup: the person restoring
// finds out here rather than by noticing something missing a week later.
func TestWhatWasLeftOutTravelsWithTheBackup(t *testing.T) {
	manifest := planFor([]BackupEntry{
		{Kind: BackupKindCompose, Path: appDir + "/docker-compose.yml"},
		{Kind: BackupKindBind, Path: "/var/run/docker.sock", Skip: "the Docker socket is a door, not data"},
		{Kind: BackupKindVolume, Service: "app", Skip: "an anonymous volume has no name to restore it under"},
	}, nil)

	if len(manifest.Operations) != 1 {
		t.Fatalf("one thing to copy: %+v", manifest.Operations)
	}
	if len(manifest.Skipped) != 2 {
		t.Fatalf("two things to explain: %+v", manifest.Skipped)
	}

	// and it survives being written out, which is the only way anyone will read it
	encoded, err := stdjson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "a door, not data") {
		t.Fatalf("the reason did not survive the manifest: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"format_version":1`) {
		t.Fatalf("a backup outlives the code that wrote it: %s", encoded)
	}
}

// `./php.ini` and `./config` are indistinguishable in a compose file, and rclone
// copies a file and a directory with different calls. Copying a file as though it
// were a directory copies its PARENT -- on /DATA/photos/one.jpg, the whole library.
func TestABindThatTurnsOutToBeASingleFile(t *testing.T) {
	manifest := planFor([]BackupEntry{
		{Kind: BackupKindBind, Path: "/DATA/AppData/app/php.ini", Service: "app", Target: "/etc/php.ini"},
		{Kind: BackupKindBind, Path: "/DATA/Media", Service: "app", Target: "/media"},
	}, nil)

	for _, operation := range manifest.Operations {
		if !operation.Directory {
			t.Fatal("nothing is known about the disk until something looks")
		}
	}

	manifest.MarkBindFiles(func(p string) bool { return p == "/DATA/AppData/app/php.ini" })

	file := manifest.Operations[0]
	if file.Directory {
		t.Fatal("this one is a file")
	}
	// it keeps its own name at the destination, so a restore can put it back
	if file.Destination != "binds/1/php.ini" {
		t.Fatalf("want binds/1/php.ini, got %q", file.Destination)
	}

	folder := manifest.Operations[1]
	if !folder.Directory || folder.Destination != "binds/2" {
		t.Fatalf("the folder is untouched: %+v", folder)
	}
}

func TestTwoServicesMountingTheSameFolderIsOneThingToCopy(t *testing.T) {
	manifest := planFor([]BackupEntry{
		{Kind: BackupKindBind, Path: "/DATA/Media", Service: "web", Target: "/media"},
		{Kind: BackupKindBind, Path: "/DATA/Media", Service: "worker", Target: "/media"},
		{Kind: BackupKindBind, Path: "/DATA/Other", Service: "web", Target: "/other"},
	}, nil)

	sources := manifest.TotalBySource()
	if len(sources) != 2 || sources[0] != "/DATA/Media" || sources[1] != "/DATA/Other" {
		t.Fatalf("want two distinct sources in order, got %v", sources)
	}
}

// One app's history sits together, and a destination holding several apps stays
// readable in a file browser -- which is how somebody will read it on the day it
// matters.
func TestWhereOneRunGoesOnADestination(t *testing.T) {
	if got := RootFor("nextcloud", "2026-09-11T02-30-00Z"); got != "nextcloud/2026-09-11T02-30-00Z" {
		t.Fatalf("got %q", got)
	}

	// a name is not allowed to climb out of its own folder
	got := RootFor("../../etc", "2026/09/11")
	if strings.Contains(got, "/../") || strings.HasPrefix(got, "/") {
		t.Fatalf("a destination path must stay where it was put: %q", got)
	}
	if got != "..-..-etc/2026-09-11" {
		t.Fatalf("got %q", got)
	}
}
