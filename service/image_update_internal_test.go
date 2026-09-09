package service

import (
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

	imageUpdates.byApp = map[string]bool{"checked": false, "unchecked": true}
	defer func() { imageUpdates.byApp = map[string]bool{} }()

	remember(map[string]bool{"checked": true}, installed)

	assert.Equal(t, *ImageUpdateAvailable("checked"), true)
	// no answer this pass, so it keeps the one it had rather than flipping to false
	assert.Equal(t, *ImageUpdateAvailable("unchecked"), true)
}

func TestRememberForgetsAnAppThatIsGone(t *testing.T) {
	imageUpdates.byApp = map[string]bool{"removed": true}
	defer func() { imageUpdates.byApp = map[string]bool{} }()

	remember(map[string]bool{}, map[string]*ComposeApp{})

	assert.Assert(t, ImageUpdateAvailable("removed") == nil)
}

func TestImageUpdateAvailableIsNilBeforeAnythingHasChecked(t *testing.T) {
	imageUpdates.byApp = map[string]bool{}

	// nil is not "up to date": the dashboard shows no badge for either, but only
	// one of them is a claim about the app
	assert.Assert(t, ImageUpdateAvailable("never-checked") == nil)
}
