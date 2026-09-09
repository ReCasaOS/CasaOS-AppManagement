package service

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
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

// fakeImageDaemon records the filter it was handed, which is the only thing standing
// between a prune and the image of every stopped app on the host.
type fakeImageDaemon struct {
	listed filters.Args
	pruned filters.Args
}

func (d *fakeImageDaemon) ImageList(_ context.Context, options image.ListOptions) ([]image.Summary, error) {
	d.listed = options.Filters

	return nil, nil
}

func (d *fakeImageDaemon) ImagesPrune(_ context.Context, pruneFilter filters.Args) (types.ImagesPruneReport, error) {
	d.pruned = pruneFilter

	return types.ImagesPruneReport{}, nil
}

// Deleting the image of every stopped app is the one thing these two must never do, and
// "prune everything unused" is spelled dangling=false -- or an empty filter, which is
// the same thing with nothing to notice. What reaches the daemon is what matters, so
// this reads the filter back off the call rather than off the helper that builds it.
func TestPruneFilterIsDanglingTrue(t *testing.T) {
	for _, c := range []struct {
		name string
		call func(*fakeImageDaemon) filters.Args
	}{
		{"estimating", func(d *fakeImageDaemon) filters.Args {
			_, _ = danglingImages(context.Background(), d)

			return d.listed
		}},
		{"pruning", func(d *fakeImageDaemon) filters.Args {
			_, _ = pruneDanglingImages(context.Background(), d)

			return d.pruned
		}},
	} {
		got := c.call(&fakeImageDaemon{}).Get("dangling")
		if len(got) != 1 || got[0] != "true" {
			t.Errorf("%s: filter given to the daemon is %v, want [true]", c.name, got)
		}
	}
}
