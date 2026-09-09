package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"gotest.tools/v3/assert"
)

func appWith(images map[string]string) *ComposeApp {
	services := types.Services{}
	for name, image := range images {
		services[name] = types.ServiceConfig{Name: name, Image: image}
	}

	return &ComposeApp{Services: services}
}

// fakeDaemon answers the two questions the check asks docker, so the branches below
// run on a machine where no daemon can start.
type fakeDaemon struct {
	containers []dockertypes.Container
	// what a reference -- an image ID or a tag -- resolves to. A missing key is an
	// image the daemon does not have.
	repoDigests map[string][]string
	listErr     error
}

func (d fakeDaemon) ContainerList(_ context.Context, _ container.ListOptions) ([]dockertypes.Container, error) {
	return d.containers, d.listErr
}

func (d fakeDaemon) ImageInspectWithRaw(_ context.Context, ref string) (dockertypes.ImageInspect, []byte, error) {
	digests, ok := d.repoDigests[ref]
	if !ok {
		return dockertypes.ImageInspect{}, nil, fmt.Errorf("no such image: %s", ref)
	}

	return dockertypes.ImageInspect{RepoDigests: digests}, nil, nil
}

// containerOf is one container of a service, created from an image ID.
func containerOf(service, imageID string) dockertypes.Container {
	return dockertypes.Container{
		ID:      service + "-" + imageID,
		ImageID: imageID,
		Labels: map[string]string{
			api.ProjectLabel: "app",
			api.ServiceLabel: service,
		},
	}
}

// The bug this check exists to answer: something pulled the tag -- a failed update,
// or another tool on the host -- so the copy on DISK is the published one while the
// container goes on running the image it was created from. Asking the disk answers
// `up to date` forever; asking the container is the real question.
func TestVerdictAsksTheContainerAndNotTheTagOnDisk(t *testing.T) {
	app := appWith(map[string]string{"main": "acme/main:latest"})
	cli := fakeDaemon{
		containers: []dockertypes.Container{containerOf("main", "sha256:old")},
		repoDigests: map[string][]string{
			"sha256:old":       {"acme/main@sha256:beforeitmoved"},
			"acme/main:latest": {"acme/main@sha256:published"},
		},
	}

	updatable, reason := verdict(context.Background(), cli, app,
		map[string][]dockertypes.Container{"main": cli.containers},
		map[string]registryDigest{"acme/main:latest": {digest: "sha256:published"}})

	assert.Equal(t, reason, "")
	assert.Assert(t, updatable)
}

func TestVerdictNeedsOnlyOneStaleImage(t *testing.T) {
	// every service of a compose app is recreated together, so one moved image is
	// enough to make the app updatable
	app := appWith(map[string]string{
		"main": "acme/main:1.0",
		"db":   "acme/db:1.0",
	})
	cli := fakeDaemon{repoDigests: map[string][]string{
		"sha256:main": {"acme/main@sha256:current"},
		"sha256:db":   {"acme/db@sha256:stale"},
	}}
	containers := map[string][]dockertypes.Container{
		"main": {containerOf("main", "sha256:main")},
		"db":   {containerOf("db", "sha256:db")},
	}

	updatable, reason := verdict(context.Background(), cli, app, containers, map[string]registryDigest{
		"acme/main:1.0": {digest: "sha256:current"},
		"acme/db:1.0":   {digest: "sha256:moved"},
	})

	assert.Equal(t, reason, "")
	assert.Assert(t, updatable)
}

// Replicas of one service can disagree, and a container left on the old image is
// still a container left on the old image.
func TestVerdictSpotsTheOneReplicaLeftBehind(t *testing.T) {
	app := appWith(map[string]string{"main": "acme/main:1.0"})
	cli := fakeDaemon{repoDigests: map[string][]string{
		"sha256:new": {"acme/main@sha256:current"},
		"sha256:old": {"acme/main@sha256:stale"},
	}}
	containers := map[string][]dockertypes.Container{"main": {
		containerOf("main", "sha256:new"),
		containerOf("main", "sha256:old"),
	}}

	updatable, reason := verdict(context.Background(), cli, app, containers,
		map[string]registryDigest{"acme/main:1.0": {digest: "sha256:current"}})

	assert.Equal(t, reason, "")
	assert.Assert(t, updatable)
}

func TestVerdictIsUpToDateWhenEveryContainerRunsThePublishedImage(t *testing.T) {
	app := appWith(map[string]string{"main": "acme/main:1.0"})
	cli := fakeDaemon{repoDigests: map[string][]string{"sha256:main": {"acme/main@sha256:current"}}}
	containers := map[string][]dockertypes.Container{"main": {containerOf("main", "sha256:main")}}

	updatable, reason := verdict(context.Background(), cli, app, containers,
		map[string]registryDigest{"acme/main:1.0": {digest: "sha256:current"}})

	assert.Equal(t, reason, "")
	assert.Assert(t, !updatable)
}

func TestVerdictAnswersFromTheImagesItCouldCheck(t *testing.T) {
	// a partial "yes" is still a yes: one image is known to have moved, and no
	// answer about the other one can take that back
	app := appWith(map[string]string{
		"main": "acme/main:1.0",
		"db":   "private.example/db:1.0",
	})
	cli := fakeDaemon{repoDigests: map[string][]string{"sha256:main": {"acme/main@sha256:stale"}}}
	containers := map[string][]dockertypes.Container{"main": {containerOf("main", "sha256:main")}}

	updatable, reason := verdict(context.Background(), cli, app, containers, map[string]registryDigest{
		"acme/main:1.0":          {digest: "sha256:published"},
		"private.example/db:1.0": {reason: "its registry could not be reached"},
	})

	assert.Equal(t, reason, "")
	assert.Assert(t, updatable)
}

// The other half of it: a partial "no" is not an answer. The reachable service
// matched, the private one could not be asked, and calling that up to date is how a
// host silently stops being told about the half nobody can see.
func TestVerdictWillNotCallAnAppUpToDateOnHalfOfIt(t *testing.T) {
	app := appWith(map[string]string{
		"main": "acme/main:1.0",
		"db":   "private.example/db:1.0",
	})
	cli := fakeDaemon{repoDigests: map[string][]string{"sha256:main": {"acme/main@sha256:current"}}}
	containers := map[string][]dockertypes.Container{"main": {containerOf("main", "sha256:main")}}

	updatable, reason := verdict(context.Background(), cli, app, containers, map[string]registryDigest{
		"acme/main:1.0":          {digest: "sha256:current"},
		"private.example/db:1.0": {reason: "its registry could not be reached"},
	})

	assert.Assert(t, !updatable)
	assert.Equal(t, reason, "private.example/db:1.0: its registry could not be reached")
}

func TestVerdictReportsUncheckedRatherThanGuessingUpToDate(t *testing.T) {
	// an unreachable registry is not evidence that nothing changed. Reporting it as
	// up to date is how a host silently stops being told about updates.
	app := appWith(map[string]string{"main": "private.example/main:1.0"})

	updatable, reason := verdict(context.Background(), fakeDaemon{}, app, nil,
		map[string]registryDigest{"private.example/main:1.0": {reason: "its registry could not be reached"}})

	assert.Assert(t, !updatable)
	assert.Equal(t, reason, "private.example/main:1.0: its registry could not be reached")
}

// An image built here has no published digest, so there is nothing a registry answer
// could be compared against -- and that is a reason, not an "up to date".
func TestVerdictSaysSoWhenTheImageWasBuiltLocally(t *testing.T) {
	app := appWith(map[string]string{"main": "local/main:1.0"})
	cli := fakeDaemon{repoDigests: map[string][]string{"sha256:built": {}}}
	containers := map[string][]dockertypes.Container{"main": {containerOf("main", "sha256:built")}}

	updatable, reason := verdict(context.Background(), cli, app, containers,
		map[string]registryDigest{"local/main:1.0": {digest: "sha256:published"}})

	assert.Assert(t, !updatable)
	assert.Equal(t, reason, "local/main:1.0: built locally, so there is no published digest to compare")
}

// No container to ask -- the app was never created -- so the tag on disk is all
// there is. It answers a weaker question, which is why it is a fallback and why the
// caller logs it rather than letting it pass for the normal path.
func TestRunningDigestsFallsBackToTheTagOnDisk(t *testing.T) {
	cli := fakeDaemon{repoDigests: map[string][]string{"acme/main:1.0": {"acme/main@sha256:ondisk"}}}

	digests, fromDisk := runningDigests(context.Background(), cli, nil, "acme/main:1.0")

	assert.Assert(t, fromDisk)
	assert.DeepEqual(t, digests, [][]string{{"acme/main@sha256:ondisk"}})
}

// A container whose image ID no longer resolves -- re-tagged or pruned since it
// started -- cannot say what it runs, so it does not get a vote.
func TestRunningDigestsIgnoresAContainerWhoseImageIsGone(t *testing.T) {
	cli := fakeDaemon{repoDigests: map[string][]string{
		"sha256:kept":   {"acme/main@sha256:kept"},
		"acme/main:1.0": {"acme/main@sha256:ondisk"},
	}}
	containers := []dockertypes.Container{
		containerOf("main", "sha256:pruned"),
		containerOf("main", "sha256:kept"),
	}

	digests, fromDisk := runningDigests(context.Background(), cli, containers, "acme/main:1.0")

	assert.Assert(t, !fromDisk)
	assert.DeepEqual(t, digests, [][]string{{"acme/main@sha256:kept"}})

	// and when NO container resolves, the disk is all that is left
	digests, fromDisk = runningDigests(context.Background(), cli, containers[:1], "acme/main:1.0")
	assert.Assert(t, fromDisk)
	assert.DeepEqual(t, digests, [][]string{{"acme/main@sha256:ondisk"}})
}

// Replicas made from the same image are one answer, not three round trips.
func TestRunningDigestsAsksTheDaemonOncePerDistinctImage(t *testing.T) {
	cli := fakeDaemon{repoDigests: map[string][]string{"sha256:same": {"acme/main@sha256:same"}}}
	containers := []dockertypes.Container{
		containerOf("main", "sha256:same"),
		containerOf("main", "sha256:same"),
	}

	digests, fromDisk := runningDigests(context.Background(), cli, containers, "acme/main:1.0")

	assert.Assert(t, !fromDisk)
	assert.Equal(t, len(digests), 1)
}

func TestContainersByServiceGroupsByAppAndService(t *testing.T) {
	oneoff := containerOf("main", "sha256:oneoff")
	oneoff.Labels[api.OneoffLabel] = "True"

	other := containerOf("main", "sha256:other")
	other.Labels[api.ProjectLabel] = "another"

	cli := fakeDaemon{containers: []dockertypes.Container{
		containerOf("main", "sha256:main"),
		containerOf("main", "sha256:replica"),
		containerOf("db", "sha256:db"),
		oneoff,
		other,
		{ID: "not-compose"},
	}}

	byApp, err := containersByService(context.Background(), cli, "")
	assert.NilError(t, err)

	assert.Equal(t, len(byApp["app"]["main"]), 2)
	assert.Equal(t, len(byApp["app"]["db"]), 1)
	assert.Equal(t, len(byApp["another"]["main"]), 1)
	// a `compose run` leftover is not what the app runs, and a container with no
	// compose labels belongs to no app at all
	assert.Equal(t, len(byApp), 2)
}

func TestDistinctImagesAsksEachRegistryOnce(t *testing.T) {
	apps := map[string]*ComposeApp{
		"a": appWith(map[string]string{"main": "acme/shared:1.0", "side": "acme/a:1.0"}),
		"b": appWith(map[string]string{"main": "acme/shared:1.0"}),
		"c": appWith(map[string]string{"main": ""}),
	}

	assert.DeepEqual(t, distinctImages(apps), []string{"acme/a:1.0", "acme/shared:1.0"})
}

func TestRememberKeepsTheOldAnswerForAnAppItCouldNotCheck(t *testing.T) {
	installed := map[string]*ComposeApp{
		"checked":   appWith(map[string]string{"main": "acme/a:1.0"}),
		"unchecked": appWith(map[string]string{"main": "acme/b:1.0"}),
	}

	imageUpdates.registry = map[string]bool{"checked": false, "unchecked": true}
	defer func() { imageUpdates.registry = map[string]bool{} }()

	remember(map[string]bool{"checked": true}, installed)

	assert.Equal(t, imageUpdatable("checked"), true)
	// no answer this pass, so it keeps the one it had rather than flipping to false
	assert.Equal(t, imageUpdatable("unchecked"), true)
}

// An app that is gone leaves nothing behind in EITHER map. An `offered` entry that
// outlives its app badges the next install under the same name, and the button --
// asking the catalogue afresh -- then refuses to act on it. A restart used to clear
// that; persisting the cache made it permanent.
func TestRememberForgetsAnAppThatIsGone(t *testing.T) {
	imageUpdates.registry = map[string]bool{"removed": true, "kept": true}
	imageUpdates.offered = map[string]bool{"removed": true, "kept": true}
	defer func() {
		imageUpdates.registry = map[string]bool{}
		imageUpdates.offered = map[string]bool{}
	}()

	remember(map[string]bool{}, map[string]*ComposeApp{
		"kept": appWith(map[string]string{"main": "acme/a:1.0"}),
	})

	assert.Equal(t, imageUpdatable("removed"), false)
	assert.Assert(t, ImageUpdateAvailable("removed") == nil)

	// and an app still installed keeps both of its answers
	assert.Equal(t, imageUpdatable("kept"), true)
	assert.Equal(t, *ImageUpdateAvailable("kept"), true)
}

// Uninstalling an app, and updating one, both make the cached answers answers about
// something that is not there any more. Unknown, not false: nobody has looked since.
func TestForgettingAnAppDropsBothAnswersOnDiskToo(t *testing.T) {
	imageUpdateStatePath = filepath.Join(t.TempDir(), "image_updates.json")

	imageUpdates.registry = map[string]bool{"gone": true, "other": true}
	imageUpdates.offered = map[string]bool{"gone": true, "other": true}
	defer func() {
		imageUpdates.registry = map[string]bool{}
		imageUpdates.offered = map[string]bool{}
	}()

	forgetImageUpdates("gone")

	assert.Assert(t, ImageUpdateAvailable("gone") == nil)
	assert.Equal(t, imageUpdatable("gone"), false)
	assert.Equal(t, *ImageUpdateAvailable("other"), true)

	// the restart, which is where forgetting only in memory brings the badge back
	imageUpdates.registry = map[string]bool{}
	imageUpdates.offered = map[string]bool{}
	LoadImageUpdates()

	assert.Assert(t, ImageUpdateAvailable("gone") == nil)
	assert.Equal(t, imageUpdatable("gone"), false)
	assert.Equal(t, *ImageUpdateAvailable("other"), true)
}

func TestImageUpdateAvailableIsNilBeforeAnythingHasChecked(t *testing.T) {
	imageUpdates.offered = map[string]bool{}

	// nil is not "up to date": the dashboard shows no badge for either, but only
	// one of them is a claim about the app
	assert.Assert(t, ImageUpdateAvailable("never-checked") == nil)
}

// The dashboard badges an app from what the update button will DO, not from what the
// registry said. A moved image the catalogue will not follow is not an update anyone
// can take, and badging it put a badge saying an update was available next to a
// button answering `is up to date`, which is what a user reported seeing.
func TestTheBadgeFollowsTheButtonAndNotTheRegistry(t *testing.T) {
	imageUpdates.registry = map[string]bool{"app": true}
	imageUpdates.offered = map[string]bool{}
	defer func() {
		imageUpdates.registry = map[string]bool{}
		imageUpdates.offered = map[string]bool{}
	}()

	// the registry has moved, but nothing has decided what to do about it yet
	assert.Equal(t, imageUpdatable("app"), true)
	assert.Assert(t, ImageUpdateAvailable("app") == nil)

	// the decision was: nothing to offer
	rememberOffered(map[string]bool{"app": false})
	assert.Equal(t, *ImageUpdateAvailable("app"), false)
	assert.Equal(t, imageUpdatable("app"), true)

	// and when there is
	rememberOffered(map[string]bool{"app": true})
	assert.Equal(t, *ImageUpdateAvailable("app"), true)
}

// rememberOffered answers for the apps it was asked about and leaves the rest alone,
// so a single-app check cannot wipe what a sweep found for everything else.
func TestRememberOfferedLeavesOtherAppsAlone(t *testing.T) {
	imageUpdates.offered = map[string]bool{"a": true, "b": false}
	defer func() { imageUpdates.offered = map[string]bool{} }()

	rememberOffered(map[string]bool{"b": true})

	assert.Equal(t, *ImageUpdateAvailable("a"), true)
	assert.Equal(t, *ImageUpdateAvailable("b"), true)
}

// The two maps mean different things, and a restart must not blur them: a badge
// restored from what the registries said, rather than from what the button would do,
// is a badge on an app the button then refuses to act on.
func TestARestartRestoresBothAnswersWithoutMixingThem(t *testing.T) {
	imageUpdateStatePath = filepath.Join(t.TempDir(), "image_updates.json")

	imageUpdates.registry = map[string]bool{"moved": true}
	imageUpdates.offered = map[string]bool{"moved": false}
	defer func() {
		imageUpdates.registry = map[string]bool{}
		imageUpdates.offered = map[string]bool{}
	}()

	saveImageUpdates()

	// the restart
	imageUpdates.registry = map[string]bool{}
	imageUpdates.offered = map[string]bool{}
	LoadImageUpdates()

	assert.Equal(t, imageUpdatable("moved"), true)
	assert.Equal(t, *ImageUpdateAvailable("moved"), false)
}

// A first start has no file, and no file must not become an answer: every app stays
// unknown rather than being claimed up to date. A file holding only half the state --
// written by a version that kept one map, or a pass that only ever asked registries --
// restores that half and leaves the other one unknown for the same reason.
func TestNothingRememberedYetLeavesTheCacheEmpty(t *testing.T) {
	imageUpdateStatePath = filepath.Join(t.TempDir(), "image_updates.json")

	imageUpdates.registry = map[string]bool{}
	imageUpdates.offered = map[string]bool{}
	defer func() {
		imageUpdates.registry = map[string]bool{}
		imageUpdates.offered = map[string]bool{}
	}()

	LoadImageUpdates()

	assert.Assert(t, ImageUpdateAvailable("anything") == nil)
	assert.Equal(t, imageUpdatable("anything"), false)

	assert.NilError(t, os.WriteFile(imageUpdateStatePath, []byte(`{"registry":{"moved":true}}`), 0o644))
	LoadImageUpdates()

	assert.Equal(t, imageUpdatable("moved"), true)
	// nothing has decided what the button would do, so there is still no badge
	assert.Assert(t, ImageUpdateAvailable("moved") == nil)
}

// A file that cannot be read is not a reason to answer with a wrong one.
func TestAnUnreadableFileIsIgnoredRatherThanBelieved(t *testing.T) {
	imageUpdateStatePath = filepath.Join(t.TempDir(), "image_updates.json")
	assert.NilError(t, os.WriteFile(imageUpdateStatePath, []byte(`{"offered": tru`), 0o644))

	imageUpdates.offered = map[string]bool{"app": true}
	defer func() { imageUpdates.offered = map[string]bool{} }()

	LoadImageUpdates()

	assert.Equal(t, *ImageUpdateAvailable("app"), true)
}

// The answers arrive by rename onto the live file -- never written in place, where a
// start reading it catches half of it -- and every save uses a temporary name of its
// own, because the six-hourly sweep and someone pressing `Check then update` can
// finish at once. One fixed temp name has both writing the same file: one renames it
// while the other is mid-write, the next start cannot parse what landed, and every
// badge disappears.
//
// The occupied name below stands in for the other save in flight. A directory,
// because nothing can write over one -- a save that insists on that single name is
// stopped dead by it, and one that picks its own is not.
func TestSavingReplacesTheFileWholeAndLeavesNoTempBehind(t *testing.T) {
	dir := t.TempDir()
	imageUpdateStatePath = filepath.Join(dir, "image_updates.json")

	imageUpdates.offered = map[string]bool{"app": true}
	defer func() { imageUpdates.offered = map[string]bool{} }()

	// through the open file, because os.Stat alone identifies the file at the path
	// when the two are compared -- which is after the save, and so always the same one
	identify := func() os.FileInfo {
		f, err := os.Open(imageUpdateStatePath)
		assert.NilError(t, err)
		defer f.Close()

		info, err := f.Stat()
		assert.NilError(t, err)

		return info
	}

	saveImageUpdates()
	before := identify()

	occupied := imageUpdateStatePath + ".tmp"
	assert.NilError(t, os.Mkdir(occupied, 0o755))

	imageUpdates.offered = map[string]bool{"app": false}
	saveImageUpdates()

	// a different file took the name. The same one would mean it had been truncated
	// and rewritten where the next start can read it half-written.
	assert.Assert(t, !os.SameFile(before, identify()))

	assert.NilError(t, os.Remove(occupied))
	entries, err := os.ReadDir(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 1)
	assert.Equal(t, entries[0].Name(), "image_updates.json")

	imageUpdates.offered = map[string]bool{}
	LoadImageUpdates()
	assert.Equal(t, *ImageUpdateAvailable("app"), false)
}

// A save that cannot land takes its temporary file with it. The disk being full is
// exactly when this runs, and leaking a file per attempt is the worst moment to.
func TestASaveThatCannotLandLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	imageUpdateStatePath = filepath.Join(dir, "image_updates.json")

	// a directory where the file goes: the rename has nowhere to land
	assert.NilError(t, os.Mkdir(imageUpdateStatePath, 0o755))

	imageUpdates.offered = map[string]bool{"app": true}
	defer func() { imageUpdates.offered = map[string]bool{} }()

	saveImageUpdates()

	entries, err := os.ReadDir(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 1)
	assert.Equal(t, entries[0].Name(), "image_updates.json")
}
