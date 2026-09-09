package service

import (
	"context"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/inkly/CasaOS-AppManagement/codegen"
)

// The entire safety story of this file. Without it -- or with dangling=false, which is
// what "prune everything unused" means to Docker -- a prune deletes the image of every
// stopped app. On a home server a stopped app is a normal state, not garbage, and its
// owner would find it unable to start again without a re-pull.
func danglingOnly() filters.Args {
	return filters.NewArgs(filters.Arg("dangling", "true"))
}

// DanglingImages reports what PruneDanglingImages would free, without freeing it.
func DanglingImages(ctx context.Context) (*codegen.DanglingImages, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	defer cli.Close()

	list, err := cli.ImageList(ctx, image.ListOptions{Filters: danglingOnly(), SharedSize: true})
	if err != nil {
		return nil, err
	}

	return &codegen.DanglingImages{Count: len(list), Size: reclaimableSize(list)}, nil
}

// PruneDanglingImages deletes them and reports what the daemon says it actually freed,
// which can be less than DanglingImages estimated if something claimed a layer since.
func PruneDanglingImages(ctx context.Context) (*codegen.DanglingImages, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	defer cli.Close()

	report, err := cli.ImagesPrune(ctx, danglingOnly())
	if err != nil {
		return nil, err
	}

	deleted := 0
	for _, entry := range report.ImagesDeleted {
		// The report also carries Untagged entries for names that merely stopped
		// pointing at an image; those freed nothing and are not images removed.
		if entry.Deleted != "" {
			deleted++
		}
	}

	return &codegen.DanglingImages{Count: deleted, Size: int64(report.SpaceReclaimed)}, nil
}

// A dangling image is usually the previous build of one still in use and shares most of
// its layers with it; deleting it does not give those bytes back. Summing Size would
// promise the user several GB and free a few hundred MB.
func reclaimableSize(images []image.Summary) int64 {
	var total int64

	for _, img := range images {
		// Docker reports -1 for a shared size it was not asked to compute. Better to
		// over-promise than to subtract a sentinel.
		if img.SharedSize > 0 {
			total += img.Size - img.SharedSize
			continue
		}

		total += img.Size
	}

	return total
}
