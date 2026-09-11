package service

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
)

const appDir = "/var/lib/casaos/apps/nextcloud"

func volumes(list ...types.ServiceVolumeConfig) types.Services {
	return types.Services{"app": types.ServiceConfig{Name: "app", Volumes: list}}
}

func onlyEntry(t *testing.T, v types.ServiceVolumeConfig) BackupEntry {
	t.Helper()

	entries := backupEntriesForServices(volumes(v), appDir)
	if len(entries) != 1 {
		t.Fatalf("want one entry, got %d", len(entries))
	}

	return entries[0]
}

// A compose file mounts more than an app's data, and the difference is not
// cosmetic: restoring a Docker socket or a device node onto another machine is how
// a backup does damage on the day it is finally used.
func TestWhatIsNotAnAppsData(t *testing.T) {
	cases := []struct {
		source string
		reason string
	}{
		{"/var/run/docker.sock", "the Docker socket is a door, not data"},
		{"/run/docker.sock", "the Docker socket is a door, not data"},
		{"/etc/localtime", "the host's clock belongs to the host"},
		{"/etc/timezone", "the host's clock belongs to the host"},
		{"/dev/dri", "kernel interface, with no contents to copy"},
		{"/dev", "kernel interface, with no contents to copy"},
		{"/sys/fs/cgroup", "kernel interface, with no contents to copy"},
		{"/proc", "kernel interface, with no contents to copy"},
		{"/", "the whole filesystem is not an app's data"},
		// the same paths written the long way round
		{"/var/run/../run/docker.sock", "the Docker socket is a door, not data"},
		{"/dev/../dev/net/tun", "kernel interface, with no contents to copy"},
	}

	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			entry := onlyEntry(t, types.ServiceVolumeConfig{Type: "bind", Source: c.source, Target: "/x"})

			if entry.Skip != c.reason {
				t.Fatalf("want skip %q, got %q", c.reason, entry.Skip)
			}
			if entry.IsIncluded() {
				t.Fatal("a skipped entry is not in the backup")
			}
		})
	}
}

// `/devices` is not `/dev`, and an app whose data lives there would have been
// dropped by a prefix test written without the separator.
func TestAPathThatMerelyStartsLikeAKernelPathIsKept(t *testing.T) {
	for _, source := range []string{"/devices/data", "/system/app", "/procedures", "/DATA/dev-notes"} {
		entry := onlyEntry(t, types.ServiceVolumeConfig{Type: "bind", Source: source, Target: "/x"})
		if !entry.IsIncluded() {
			t.Errorf("%s: %s", source, entry.Skip)
		}
	}
}

// Compose resolves a relative source against the folder holding the compose file,
// not against the working directory of whoever is asking.
func TestARelativeBindResolvesAgainstTheAppsOwnFolder(t *testing.T) {
	entry := onlyEntry(t, types.ServiceVolumeConfig{Type: "bind", Source: "./backend/docker/php.ini", Target: "/x"})

	want := appDir + "/backend/docker/php.ini"
	if entry.Path != want {
		t.Fatalf("want %q, got %q", want, entry.Path)
	}

	up := onlyEntry(t, types.ServiceVolumeConfig{Type: "bind", Source: "../shared", Target: "/x"})
	if up.Path != "/var/lib/casaos/apps/shared" {
		t.Fatalf("want the sibling folder, got %q", up.Path)
	}

	// the daemon has no home directory to expand `~` against
	home := onlyEntry(t, types.ServiceVolumeConfig{Type: "bind", Source: "~/stuff", Target: "/x"})
	if home.IsIncluded() {
		t.Fatalf("~ is the shell's, got %q", home.Path)
	}
}

func TestWhatEachKindOfMountContributes(t *testing.T) {
	named := onlyEntry(t, types.ServiceVolumeConfig{Type: "volume", Source: "backend-storage", Target: "/var/www/html/storage"})
	if named.Kind != BackupKindVolume || named.Volume != "backend-storage" {
		t.Fatalf("a named volume carries its name: %+v", named)
	}
	// only the daemon knows where it put it, which is a separate question
	if named.Path != "" {
		t.Fatalf("the path is not known yet, got %q", named.Path)
	}
	if named.Skip != "" {
		t.Fatalf("a named volume is in the backup: %q", named.Skip)
	}

	anonymous := onlyEntry(t, types.ServiceVolumeConfig{Type: "volume", Target: "/cache"})
	if anonymous.Skip == "" {
		t.Fatal("an anonymous volume cannot be restored under any name")
	}

	tmpfs := onlyEntry(t, types.ServiceVolumeConfig{Type: "tmpfs", Target: "/tmp"})
	if tmpfs.Skip == "" {
		t.Fatal("tmpfs is empty at every start")
	}

	// the short syntax produces an entry with no type at all
	untyped := onlyEntry(t, types.ServiceVolumeConfig{Source: "/DATA/Media", Target: "/media"})
	if untyped.Kind != BackupKindBind || untyped.Path != "/DATA/Media" {
		t.Fatalf("an entry with no type is a bind: %+v", untyped)
	}
}

// Two runs of the same app must list the same things in the same order, or a
// backup cannot be compared with the one before it.
func TestTheInventoryIsInAStableOrder(t *testing.T) {
	services := types.Services{
		"web":    types.ServiceConfig{Name: "web", Volumes: []types.ServiceVolumeConfig{{Type: "bind", Source: "/DATA/web", Target: "/w"}}},
		"db":     types.ServiceConfig{Name: "db", Volumes: []types.ServiceVolumeConfig{{Type: "volume", Source: "db-data", Target: "/d"}}},
		"cache":  types.ServiceConfig{Name: "cache", Volumes: []types.ServiceVolumeConfig{{Type: "tmpfs", Target: "/t"}}},
		"worker": types.ServiceConfig{Name: "worker", Volumes: []types.ServiceVolumeConfig{{Type: "bind", Source: "./work", Target: "/k"}}},
	}

	var first []string
	for run := 0; run < 20; run++ {
		got := []string{}
		for _, e := range backupEntriesForServices(services, appDir) {
			got = append(got, e.Service+":"+string(e.Kind))
		}

		if run == 0 {
			first = got
			continue
		}
		for i := range got {
			if got[i] != first[i] {
				t.Fatalf("run %d differs at %d: %q vs %q", run, i, got[i], first[i])
			}
		}
	}

	if len(first) != 4 || first[0] != "cache:volume" {
		t.Fatalf("services answer in name order: %v", first)
	}
}

// A compose file says `backend-storage`; Docker stores it as `<project>_backend-storage`
// unless the file names it or marks it external. Rebuilding that rule here is how a
// backup addresses a volume that does not exist and reports success having copied
// nothing -- so the answer compose-go already worked out at load time is read.
func TestTheNameDockerActuallyKnowsAVolumeBy(t *testing.T) {
	app := &ComposeApp{
		Name: "nextcloud",
		Volumes: types.Volumes{
			"backend-storage": types.VolumeConfig{Name: "nextcloud_backend-storage"},
			"shared":          types.VolumeConfig{Name: "shared-across-apps", External: true},
			"unnamed":         types.VolumeConfig{},
		},
	}

	cases := map[string]string{
		"backend-storage": "nextcloud_backend-storage",
		"shared":          "shared-across-apps",
		// nothing worked out: the compose name is the best answer there is
		"unnamed": "unnamed",
		// not declared at the top level at all
		"absent": "absent",
	}

	for compose, want := range cases {
		if got := app.DockerVolumeName(compose); got != want {
			t.Errorf("%s: want %q, got %q", compose, want, got)
		}
	}
}

// The two files an app cannot be rebuilt without come first, and a relative bind
// resolves against the project's working directory, which is what compose uses.
func TestAnAppsInventoryStartsWithWhatItCannotBeRebuiltWithout(t *testing.T) {
	app := &ComposeApp{
		Name:         "nextcloud",
		WorkingDir:   appDir,
		ComposeFiles: []string{appDir + "/docker-compose.yml"},
		Services: types.Services{
			"app": types.ServiceConfig{Name: "app", Volumes: []types.ServiceVolumeConfig{
				{Type: "bind", Source: "./config", Target: "/config"},
				{Type: "volume", Source: "backend-storage", Target: "/data"},
				{Type: "bind", Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
			}},
		},
	}

	entries := app.BackupInventory()
	if len(entries) != 5 {
		t.Fatalf("want 5 entries, got %d: %+v", len(entries), entries)
	}

	if entries[0].Kind != BackupKindCompose || entries[0].Path != appDir+"/docker-compose.yml" {
		t.Fatalf("the compose file comes first: %+v", entries[0])
	}
	if entries[1].Kind != BackupKindEnv || entries[1].Path != appDir+"/.env" {
		t.Fatalf("then the .env beside it: %+v", entries[1])
	}
	if entries[2].Path != appDir+"/config" {
		t.Fatalf("a relative bind resolves against the working dir: %+v", entries[2])
	}
	if !entries[3].IsIncluded() && entries[3].Volume != "backend-storage" {
		t.Fatalf("the named volume is in: %+v", entries[3])
	}
	if entries[4].IsIncluded() {
		t.Fatalf("the docker socket is not data: %+v", entries[4])
	}

	// what a person is told they are NOT getting
	skipped := 0
	for _, e := range entries {
		if e.Skip != "" {
			skipped++
		}
	}
	if skipped != 1 {
		t.Fatalf("want one skipped entry with its reason, got %d", skipped)
	}
}
