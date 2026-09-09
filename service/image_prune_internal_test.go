package service

import (
	"testing"

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

// Deleting the image of every stopped app is the one thing this endpoint must never do,
// and "prune everything unused" is spelled dangling=false, one character away.
func TestPruneFilterIsDanglingTrue(t *testing.T) {
	if got := danglingOnly().Get("dangling"); len(got) != 1 || got[0] != "true" {
		t.Fatalf("prune filter is %v, want [true]", got)
	}
}
