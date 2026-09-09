package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"gotest.tools/v3/assert"
)

func appWith(images map[string]string) *ComposeApp {
	services := types.Services{}
	for name, image := range images {
		services[name] = types.ServiceConfig{Name: name, Image: image}
	}

	return &ComposeApp{Services: services}
}

func TestVerdictNeedsOnlyOneStaleImage(t *testing.T) {
	// every service of a compose app is recreated together, so one moved image is
	// enough to make the app updatable
	app := appWith(map[string]string{
		"main": "acme/main:1.0",
		"db":   "acme/db:1.0",
	})
	digests := map[string]imageVerdict{
		"acme/main:1.0": {updatable: false},
		"acme/db:1.0":   {updatable: true},
	}

	updatable, reason := verdict(app, digests)
	assert.Equal(t, reason, "")
	assert.Assert(t, updatable)
}

func TestVerdictIsUpToDateWhenEveryImageMatches(t *testing.T) {
	app := appWith(map[string]string{"main": "acme/main:1.0"})
	digests := map[string]imageVerdict{"acme/main:1.0": {updatable: false}}

	updatable, reason := verdict(app, digests)
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
	digests := map[string]imageVerdict{
		"acme/main:1.0":          {updatable: true},
		"private.example/db:1.0": {reason: "its registry could not be reached"},
	}

	updatable, reason := verdict(app, digests)
	assert.Equal(t, reason, "")
	assert.Assert(t, updatable)
}

func TestVerdictReportsUncheckedRatherThanGuessingUpToDate(t *testing.T) {
	// an unreachable registry is not evidence that nothing changed. Reporting it as
	// up to date is how a host silently stops being told about updates.
	app := appWith(map[string]string{"main": "private.example/main:1.0"})
	digests := map[string]imageVerdict{
		"private.example/main:1.0": {reason: "its registry could not be reached"},
	}

	updatable, reason := verdict(app, digests)
	assert.Assert(t, !updatable)
	assert.Equal(t, reason, "private.example/main:1.0: its registry could not be reached")
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

func TestRememberForgetsAnAppThatIsGone(t *testing.T) {
	imageUpdates.registry = map[string]bool{"removed": true}
	defer func() { imageUpdates.registry = map[string]bool{} }()

	remember(map[string]bool{}, map[string]*ComposeApp{})

	assert.Equal(t, imageUpdatable("removed"), false)
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

func TestNothingRememberedYetLeavesTheCacheEmpty(t *testing.T) {
	imageUpdateStatePath = filepath.Join(t.TempDir(), "image_updates.json")

	imageUpdates.offered = map[string]bool{}
	LoadImageUpdates()

	assert.Assert(t, ImageUpdateAvailable("anything") == nil)
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

// The answers arrive by rename, so the file on disk is never half of anything and no
// temporary file is left lying beside it.
func TestSavingReplacesTheFileWholeAndLeavesNoTempBehind(t *testing.T) {
	dir := t.TempDir()
	imageUpdateStatePath = filepath.Join(dir, "image_updates.json")

	imageUpdates.offered = map[string]bool{"app": true}
	defer func() { imageUpdates.offered = map[string]bool{} }()

	saveImageUpdates()
	imageUpdates.offered = map[string]bool{"app": false}
	saveImageUpdates()

	entries, err := os.ReadDir(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 1)
	assert.Equal(t, entries[0].Name(), "image_updates.json")

	imageUpdates.offered = map[string]bool{}
	LoadImageUpdates()
	assert.Equal(t, *ImageUpdateAvailable("app"), false)
}
