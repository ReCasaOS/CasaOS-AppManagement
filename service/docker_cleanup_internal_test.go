package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"gotest.tools/v3/assert"
)

func emptyProtection() cleanupProtection {
	return cleanupProtection{
		Projects: map[string]struct{}{}, Images: map[string]struct{}{},
		Repos: map[string]struct{}{}, Volumes: map[string]struct{}{},
	}
}

func protectionOf(t *testing.T, projects ...*types.Project) cleanupProtection {
	t.Helper()
	protection := emptyProtection()
	for _, project := range projects {
		assert.NilError(t, protection.protect(project))
	}

	return protection
}

const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// an app with one image by tag, one by digest, one it builds, and two volumes
func jellyfin() *types.Project {
	return &types.Project{
		Name: "jellyfin",
		Services: types.Services{
			"web":   {Name: "web", Image: "linuxserver/jellyfin:10.9"},
			"db":    {Name: "db", Image: "postgres@sha256:" + sha},
			"proxy": {Name: "proxy", Build: &types.BuildConfig{Context: "."}},
		},
		Volumes: types.Volumes{"data": {Name: "jellyfin_data"}, "shared": {Name: "media", External: true}},
	}
}

func ids(images []codegen.CleanupImage) []string {
	out := []string{}
	for _, img := range images {
		out = append(out, img.Id)
	}

	return out
}

// Docker calls an image unused when no container holds it. Here an app without a
// container still owns its images: only what no installed app names is a leftover.
func TestLeftoverImagesAreWhatNoContainerHoldsAndNoAppNames(t *testing.T) {
	protection := protectionOf(t, jellyfin())

	for name, c := range map[string]struct {
		img  image.Summary
		left bool
	}{
		"an old tag of the image of an installed app": {image.Summary{RepoTags: []string{"linuxserver/jellyfin:10.8"}}, true},
		"an image of no app":                          {image.Summary{RepoTags: []string{"alpine:3.20"}}, true},
		"the tag an app names":                        {image.Summary{RepoTags: []string{"linuxserver/jellyfin:10.9"}}, false},
		"the same, spelled in full":                   {image.Summary{RepoTags: []string{"docker.io/linuxserver/jellyfin:10.9"}}, false},
		"one of two tags belongs to an app":           {image.Summary{RepoTags: []string{"alpine:3.20", "linuxserver/jellyfin:10.9"}}, false},
		"the digest an app pins":                      {image.Summary{RepoTags: []string{"postgres:16"}, RepoDigests: []string{"postgres@sha256:" + sha}}, false},
		"what compose built for a service":            {image.Summary{RepoTags: []string{"jellyfin-proxy:latest"}}, false},
		"a deployment from a repository":              {image.Summary{RepoTags: []string{"myapp-web:git-0123456789ab"}}, false},
		"a container holds it":                        {image.Summary{RepoTags: []string{"alpine:3.20"}, Containers: 1}, false},
		// -1 is "not counted", which is not zero
		"a count the daemon did not give": {image.Summary{RepoTags: []string{"alpine:3.20"}, Containers: -1}, false},
		// counted apart, as an old app version
		"a dangling image":           {image.Summary{RepoTags: []string{"<none>:<none>"}}, false},
		"no tag at all":              {image.Summary{RepoDigests: []string{"alpine@sha256:" + sha}}, false},
		"a name that cannot be read": {image.Summary{RepoTags: []string{"Not A Reference"}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			c.img.ID = "sha256:one"
			got := leftoverImages([]*image.Summary{&c.img, nil}, protection)
			assert.Equal(t, len(got) == 1, c.left, "%v", got)
		})
	}
}

func TestLeftoverImagesCountWhatTheyDoNotShareBiggestFirst(t *testing.T) {
	got := leftoverImages([]*image.Summary{
		{ID: "small", RepoTags: []string{"b:1", "a:1"}, Size: 900, SharedSize: 850},
		{ID: "big", RepoTags: []string{"c:1"}, Size: 300, SharedSize: -1},
	}, emptyProtection())

	assert.DeepEqual(t, ids(got), []string{"big", "small"})
	assert.Equal(t, got[0].Size, int64(300))
	assert.Equal(t, got[1].Size, int64(50))
	assert.DeepEqual(t, got[1].Tags, []string{"a:1", "b:1"})
}

func aVolume(name string, refs int64, labels map[string]string) *volume.Volume {
	return &volume.Volume{Name: name, Labels: labels, CreatedAt: "2026-11-01T10:00:00Z", UsageData: &volume.UsageData{Size: 100, RefCount: refs}}
}

func TestLeftoverVolumesAreWhatNoContainerHoldsAndNoAppOwns(t *testing.T) {
	protection := protectionOf(t, jellyfin())
	of := func(project string) map[string]string {
		return map[string]string{"com.docker.compose.project": project}
	}

	for name, c := range map[string]struct {
		volume *volume.Volume
		left   bool
	}{
		"of an app that was removed":        {aVolume("immich_pgdata", 0, of("immich")), true},
		"made by hand":                      {aVolume("scratch", 0, nil), true},
		"of an installed app, by its label": {aVolume("whatever", 0, of("jellyfin")), false},
		"declared by an installed app":      {aVolume("jellyfin_data", 0, nil), false},
		"an external one an app declares":   {aVolume("media", 0, nil), false},
		"a container holds it":              {aVolume("scratch", 1, nil), false},
		"a count the daemon did not give":   {aVolume("scratch", -1, nil), false},
		"no usage data at all":              {&volume.Volume{Name: "scratch"}, false},
		"nothing":                           {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := leftoverVolumes(dockertypes.DiskUsage{Volumes: []*volume.Volume{c.volume}}, protection)
			assert.Equal(t, len(got) == 1, c.left, "%v", got)
		})
	}

	// the app that made it is said, so the owner knows whose data this was
	got := leftoverVolumes(dockertypes.DiskUsage{Volumes: []*volume.Volume{aVolume("immich_pgdata", 0, of("immich"))}}, protection)
	assert.Equal(t, *got[0].App, "immich")
	assert.Equal(t, got[0].Created.Year(), 2026)
}

func TestAnImageAnAppNamesThatCannotBeReadIsAnError(t *testing.T) {
	protection := emptyProtection()
	err := protection.protect(&types.Project{Name: "odd", Services: types.Services{"web": {Name: "web", Image: "Not A Reference"}}})

	assert.ErrorContains(t, err, "odd")
}

func TestBuildCacheSizeCountsWhatNoBuildUses(t *testing.T) {
	assert.Equal(t, buildCacheSize([]*build.CacheRecord{
		{Size: 100}, {Size: 50, InUse: true}, {Size: 30, Shared: true}, nil,
	}), int64(100))
}

// fakeCleanupDaemon answers a disk usage and records what it was asked to delete: what
// reaches the daemon is the proof, not the list the screen showed.
type fakeCleanupDaemon struct {
	usage   dockertypes.DiskUsage
	images  []string
	forced  bool
	volumes []string
	pruned  *build.CachePruneOptions
	refuse  map[string]error
}

func (d *fakeCleanupDaemon) DiskUsage(context.Context, dockertypes.DiskUsageOptions) (dockertypes.DiskUsage, error) {
	return d.usage, nil
}

func (d *fakeCleanupDaemon) ImageRemove(_ context.Context, ref string, options image.RemoveOptions) ([]image.DeleteResponse, error) {
	d.forced = d.forced || options.Force
	if err := d.refuse[ref]; err != nil {
		return nil, err
	}
	d.images = append(d.images, ref)

	return nil, nil
}

func (d *fakeCleanupDaemon) VolumeRemove(_ context.Context, name string, force bool) error {
	d.forced = d.forced || force
	if err := d.refuse[name]; err != nil {
		return err
	}
	d.volumes = append(d.volumes, name)

	return nil
}

func (d *fakeCleanupDaemon) BuildCachePrune(_ context.Context, opts build.CachePruneOptions) (*build.CachePruneReport, error) {
	d.pruned = &opts

	return &build.CachePruneReport{SpaceReclaimed: 42}, nil
}

// aBox is a daemon with the image and the volume of an app, and a leftover of each.
func aBox(t *testing.T) *fakeCleanupDaemon {
	t.Helper()
	was := appsProtection
	protection := protectionOf(t, jellyfin())
	appsProtection = func(context.Context) (cleanupProtection, error) { return protection, nil }
	t.Cleanup(func() { appsProtection = was })

	return &fakeCleanupDaemon{usage: dockertypes.DiskUsage{
		Images: []*image.Summary{
			{ID: "app", RepoTags: []string{"linuxserver/jellyfin:10.9"}, Size: 500},
			{ID: "old", RepoTags: []string{"linuxserver/jellyfin:10.8", "jf:old"}, Size: 400},
		},
		Volumes: []*volume.Volume{aVolume("jellyfin_data", 0, nil), aVolume("scratch", 0, nil)},
	}}
}

func TestTheReportListsLeftoversOnly(t *testing.T) {
	report, err := dockerCleanup(context.Background(), aBox(t))
	assert.NilError(t, err)

	assert.Equal(t, report.Held, "")
	assert.DeepEqual(t, ids(report.Images), []string{"old"})
	assert.Equal(t, len(report.Volumes), 1)
	assert.Equal(t, report.Volumes[0].Name, "scratch")
}

// The body of a deletion can only narrow the list: the image or the volume of an app,
// asked for by its name, is refused and never reaches the daemon.
func TestADeletionNeverReachesWhatAnAppOwns(t *testing.T) {
	daemon := aBox(t)

	images, err := removeLeftoverImages(context.Background(), daemon, []string{"old", "app", "unknown"})
	assert.NilError(t, err)
	assert.DeepEqual(t, images.Removed, []string{"old"})
	assert.Equal(t, images.Size, int64(400))
	assert.Equal(t, len(images.Refused), 2)
	// by each of its names: by id an image with two names is refused unless forced
	assert.DeepEqual(t, daemon.images, []string{"jf:old", "linuxserver/jellyfin:10.8"})

	volumes, err := removeLeftoverVolumes(context.Background(), daemon, []string{"scratch", "jellyfin_data"})
	assert.NilError(t, err)
	assert.DeepEqual(t, volumes.Removed, []string{"scratch"})
	assert.Equal(t, volumes.Refused["jellyfin_data"], "not a leftover any more")
	assert.DeepEqual(t, daemon.volumes, []string{"scratch"})

	assert.Assert(t, !daemon.forced, "nothing is ever forced")
}

func TestWhatTheDaemonRefusesIsSaidAndNotCounted(t *testing.T) {
	daemon := aBox(t)
	daemon.refuse = map[string]error{"scratch": errors.New("volume is in use"), "jf:old": errors.New("conflict")}

	volumes, err := removeLeftoverVolumes(context.Background(), daemon, []string{"scratch"})
	assert.NilError(t, err)
	assert.Equal(t, volumes.Refused["scratch"], "volume is in use")
	assert.Equal(t, volumes.Size, int64(0))

	images, err := removeLeftoverImages(context.Background(), daemon, []string{"old"})
	assert.NilError(t, err)
	assert.Equal(t, images.Refused["old"], "conflict")
	assert.Equal(t, len(images.Removed), 0)
}

// While an operation runs, an image just pulled or a volume between two containers is
// held by nothing: nothing is offered and nothing is deleted.
func TestNothingIsOfferedOrDeletedWhileAnOperationRuns(t *testing.T) {
	daemon := aBox(t)
	end, err := Begin("jellyfin", "update")
	assert.NilError(t, err)
	defer end()

	report, err := dockerCleanup(context.Background(), daemon)
	assert.NilError(t, err)
	assert.Equal(t, report.Held, CleanupHeldBusy)
	assert.Equal(t, len(report.Images)+len(report.Volumes), 0)

	_, err = removeLeftoverVolumes(context.Background(), daemon, []string{"scratch"})
	assert.ErrorIs(t, err, ErrCleanupHeld{Reason: CleanupHeldBusy})
	_, err = removeLeftoverImages(context.Background(), daemon, []string{"old"})
	assert.ErrorIs(t, err, ErrCleanupHeld{Reason: CleanupHeldBusy})
	assert.Equal(t, len(daemon.images)+len(daemon.volumes), 0)
}

// A compose file that does not load claims nobody knows what.
func TestNothingIsOfferedOrDeletedWhenAnAppCannotBeRead(t *testing.T) {
	daemon := aBox(t)
	appsProtection = func(context.Context) (cleanupProtection, error) {
		return cleanupProtection{}, errors.New("compose file of `odd`: yaml")
	}

	report, err := dockerCleanup(context.Background(), daemon)
	assert.NilError(t, err)
	assert.Equal(t, report.Held, CleanupHeldUnreadable)
	assert.Equal(t, len(report.Images)+len(report.Volumes), 0)

	_, err = removeLeftoverVolumes(context.Background(), daemon, []string{"scratch"})
	assert.ErrorIs(t, err, ErrCleanupHeld{Reason: CleanupHeldUnreadable})
	assert.Equal(t, len(daemon.volumes), 0)
}

func TestTheBuildCachePruneTakesAllOfItAndSaysWhatWasFreed(t *testing.T) {
	daemon := &fakeCleanupDaemon{}

	freed, err := pruneBuildCache(context.Background(), daemon)
	assert.NilError(t, err)
	assert.Equal(t, freed, int64(42))
	assert.Assert(t, daemon.pruned.All)
}

// appFolders points the apps' folder at a fresh one and Docker Compose at no stack: the
// state of an app whose containers are all gone.
func appFolders(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	wasPath, wasStacks := config.AppInfo.AppsPath, ListComposeStacks
	config.AppInfo.AppsPath = root
	ListComposeStacks = func(context.Context) ([]api.Stack, error) { return nil, nil }
	t.Cleanup(func() { config.AppInfo.AppsPath, ListComposeStacks = wasPath, wasStacks })

	return root
}

func writeCompose(t *testing.T, root, app, text string) {
	t.Helper()
	assert.NilError(t, os.MkdirAll(filepath.Join(root, app), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(root, app, "docker-compose.yml"), []byte(text), 0o600))
}

// An app between the two halves of an update, or waiting for its storage, has no
// container: Docker Compose does not list it, and its folder is all that says it is there.
func TestAnAppWithNoContainerStillClaimsItsImagesAndVolumes(t *testing.T) {
	root := appFolders(t)
	writeCompose(t, root, "immich", `name: immich
services:
  web:
    image: ghcr.io/immich-app/immich-server:v1
    volumes:
      - pgdata:/data
volumes:
  pgdata:
`)
	// a folder with no compose file still keeps the volumes labelled with its name
	assert.NilError(t, os.MkdirAll(filepath.Join(root, "empty"), 0o755))

	protection, err := appsProtection(context.Background())
	assert.NilError(t, err)

	_, claimed := protection.Images["ghcr.io/immich-app/immich-server:v1"]
	assert.Assert(t, claimed, "%v", protection.Images)
	_, declared := protection.Volumes["immich_pgdata"]
	assert.Assert(t, declared, "%v", protection.Volumes)
	for _, project := range []string{"immich", "empty"} {
		_, owned := protection.Projects[project]
		assert.Assert(t, owned, project)
	}
}

func TestAComposeFileThatDoesNotLoadIsAnError(t *testing.T) {
	root := appFolders(t)
	writeCompose(t, root, "odd", "services: [this is not a compose file")

	_, err := appsProtection(context.Background())
	assert.ErrorContains(t, err, "odd")
}
