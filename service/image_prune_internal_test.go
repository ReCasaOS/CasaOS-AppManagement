package service

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/image"
	"gotest.tools/v3/assert"
)

// The figure the dashboard shows before asking "reclaim 4.2 GB?". A dangling image is
// usually the previous build of a tagged one and shares nearly all its layers, so its
// full Size is not what pruning gives back. Docker also uses -1 for a shared size it
// was not asked to compute, which subtracted blindly inflates the promise by a byte.
func TestReclaimableSizeCountsOnlyUniqueBytes(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []image.Summary
		want int64
	}{
		{"nothing dangling", nil, 0},
		{"shares layers with an image still in use", []image.Summary{{Size: 900, SharedSize: 850}}, 50},
		{"shares nothing", []image.Summary{{Size: 900, SharedSize: 0}}, 900},
		{"shared size not computed", []image.Summary{{Size: 900, SharedSize: -1}}, 900},
		{"summed", []image.Summary{{Size: 900, SharedSize: 850}, {Size: 300, SharedSize: -1}}, 350},
	} {
		if got := reclaimableSize(c.in); got != c.want {
			t.Errorf("%s: reclaimableSize = %d, want %d", c.name, got, c.want)
		}
	}
}

// aBoxWithOldVersions is aBox whose daemon lists two dangling images: what an update of
// Jellyfin left, and the image Jellyfin pins by its digest, which has no tag either.
func aBoxWithOldVersions(t *testing.T) *fakeCleanupDaemon {
	t.Helper()
	daemon := aBox(t)
	daemon.dangling = []image.Summary{
		{ID: "left", RepoDigests: []string{"linuxserver/jellyfin@sha256:" + otherSha}, Size: 300},
		{ID: "pinned", RepoDigests: []string{"postgres@sha256:" + sha}, Size: 200},
	}

	return daemon
}

const otherSha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// Deleting the image of every stopped app is the one thing these two must never do, and
// "prune everything unused" is spelled dangling=false, or an empty filter, which is the
// same thing with nothing to notice. What reaches the daemon is what matters, so this
// reads the filter back off the call.
func TestOldVersionsAreAskedOfTheDaemonAsDanglingOnly(t *testing.T) {
	daemon := aBoxWithOldVersions(t)

	_, err := danglingImages(context.Background(), daemon)
	assert.NilError(t, err)
	assert.DeepEqual(t, daemon.listed.Get("dangling"), []string{"true"})

	daemon.listed = danglingOnly()
	_, err = pruneDanglingImages(context.Background(), daemon)
	assert.NilError(t, err)
	assert.DeepEqual(t, daemon.listed.Get("dangling"), []string{"true"})
}

// An image an app pins by digest has no tag on a daemon that keeps images the classic
// way: Docker calls it dangling, and it is the app's.
func TestAnImageAnAppPinsByDigestIsNotAnOldVersion(t *testing.T) {
	daemon := aBoxWithOldVersions(t)

	report, err := danglingImages(context.Background(), daemon)
	assert.NilError(t, err)
	assert.Equal(t, report.Count, 1)
	assert.Equal(t, report.Size, int64(300))

	freed, err := pruneDanglingImages(context.Background(), daemon)
	assert.NilError(t, err)
	assert.Equal(t, freed.Count, 1)
	// by id, the pinned one never asked for, nothing forced
	assert.DeepEqual(t, daemon.images, []string{"left"})
	assert.Assert(t, !daemon.forced)
}

func TestAnOldVersionTheDaemonRefusesIsNotCountedAsFreed(t *testing.T) {
	daemon := aBoxWithOldVersions(t)
	daemon.refuse = map[string]error{"left": errors.New("conflict: image is being used")}

	freed, err := pruneDanglingImages(context.Background(), daemon)
	assert.NilError(t, err)
	assert.Equal(t, freed.Count, 0)
	assert.Equal(t, freed.Size, int64(0))
}

// CasaOS#167: an old version a container still uses (an app whose container was not
// recreated after its update, a stopped leftover) is refused by the daemon, so Free up
// left it, and the count kept saying it was there to free.
func TestAnOldVersionAContainerUsesIsNeitherCountedNorAsked(t *testing.T) {
	daemon := aBoxWithOldVersions(t)
	daemon.dangling = append(daemon.dangling,
		image.Summary{ID: "held", RepoDigests: []string{"firefox@sha256:" + otherSha}, Size: 275, Containers: 1},
		image.Summary{ID: "unknown", Size: 40, Containers: -1})

	report, err := danglingImages(context.Background(), daemon)
	assert.NilError(t, err)
	assert.Assert(t, daemon.counted, "the daemon must be asked to count the containers")
	assert.Equal(t, report.Count, 2)
	assert.Equal(t, report.Size, int64(340))

	freed, err := pruneDanglingImages(context.Background(), daemon)
	assert.NilError(t, err)
	assert.Equal(t, freed.Count, 2)
	assert.DeepEqual(t, daemon.images, []string{"left", "unknown"})
}

// While an operation runs, or when an app cannot be read, the old versions say nothing
// to free and free nothing.
func TestOldVersionsAreHeldLikeTheRest(t *testing.T) {
	daemon := aBoxWithOldVersions(t)
	end, err := Begin("jellyfin", "update")
	assert.NilError(t, err)

	report, err := danglingImages(context.Background(), daemon)
	assert.NilError(t, err)
	assert.Equal(t, report.Count, 0)
	_, err = pruneDanglingImages(context.Background(), daemon)
	assert.ErrorIs(t, err, ErrCleanupHeld{Reason: CleanupHeldBusy})
	end()

	appsProtection = func(context.Context) (cleanupProtection, error) {
		return cleanupProtection{}, errors.New("compose file of `odd`: yaml")
	}
	report, err = danglingImages(context.Background(), daemon)
	assert.NilError(t, err)
	assert.Equal(t, report.Count, 0)
	_, err = pruneDanglingImages(context.Background(), daemon)
	var held ErrCleanupHeld
	assert.Assert(t, errors.As(err, &held))
	assert.Equal(t, held.Reason, CleanupHeldUnreadable)
	assert.Equal(t, len(daemon.images), 0)
}
