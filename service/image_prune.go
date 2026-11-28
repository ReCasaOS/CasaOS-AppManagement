package service

import (
	"context"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"go.uber.org/zap"
)

// Old app versions: the images no tag points at any more, which is what an update leaves
// behind when the tag it pulled moved to a newer image.
//
// Two things keep this from the image of an installed app. The list is asked of the
// daemon with dangling=true: without it, or with dangling=false, which is what "prune
// everything unused" means to Docker, the image of every stopped app would be in it. And
// an image an app pins by its digest has no tag either, on a daemon that keeps images the
// classic way: it is dangling to Docker and the app's all the same, so what an installed
// app names by digest is left out. Each image is then removed by its id and never forced,
// so one a container still holds stays; such an image is not counted either, or Free up
// would keep showing what it cannot free (CasaOS#167).
func danglingOnly() filters.Args {
	return filters.NewArgs(filters.Arg("dangling", "true"))
}

// oldVersions is the dangling images no installed app names by digest and no container uses.
func oldVersions(ctx context.Context, cli cleanupDaemon) ([]image.Summary, error) {
	list, err := cli.ImageList(ctx, image.ListOptions{Filters: danglingOnly(), SharedSize: true, ContainerCount: true})
	if err != nil || len(list) == 0 {
		// nothing dangling is the usual answer, and it needs no app read
		return nil, err
	}
	protection, err := appsProtection(ctx)
	if err != nil {
		return nil, ErrCleanupHeld{Reason: CleanupHeldUnreadable, Detail: err.Error()}
	}

	old := []image.Summary{}
	for _, img := range list {
		// a container uses it (Containers > 0); -1 is a daemon that did not count, left to the removal
		if img.Containers <= 0 && !imageClaimed(img.RepoDigests, protection) {
			old = append(old, img)
		}
	}

	return old, nil
}

// DanglingImages reports what PruneDanglingImages would free, without freeing it. While
// what the apps claim cannot be told, that is nothing.
func DanglingImages(ctx context.Context) (*codegen.DanglingImages, error) {
	cli, err := newCleanupDaemon()
	if err != nil {
		return nil, err
	}
	defer cli.Close()

	return danglingImages(ctx, cli)
}

func danglingImages(ctx context.Context, cli cleanupDaemon) (*codegen.DanglingImages, error) {
	if cleanupHeld() != "" {
		return &codegen.DanglingImages{}, nil
	}
	old, err := oldVersions(ctx, cli)
	if _, held := err.(ErrCleanupHeld); held { //nolint:errorlint // the type oldVersions returns, never wrapped
		return &codegen.DanglingImages{}, nil
	}
	if err != nil {
		return nil, err
	}

	return &codegen.DanglingImages{Count: len(old), Size: reclaimableSize(old)}, nil
}

// PruneDanglingImages deletes them and reports how many went and what they held alone.
func PruneDanglingImages(ctx context.Context) (*codegen.DanglingImages, error) {
	cli, err := newCleanupDaemon()
	if err != nil {
		return nil, err
	}
	defer cli.Close()

	return pruneDanglingImages(ctx, cli)
}

func pruneDanglingImages(ctx context.Context, cli cleanupDaemon) (*codegen.DanglingImages, error) {
	end, err := beginCleanup()
	if err != nil {
		return nil, err
	}
	defer end()

	old, err := oldVersions(ctx, cli)
	if err != nil {
		return nil, err
	}

	freed := []image.Summary{}
	for _, img := range old {
		// by id and not forced: an image a container holds, or one two repositories
		// name, is refused by the daemon and stays
		if _, err := cli.ImageRemove(ctx, img.ID, image.RemoveOptions{}); err != nil {
			logger.Info("cleanup: an old app version stays", zap.String("image", img.ID), zap.Error(err))
		} else {
			freed = append(freed, img)
		}
	}

	return &codegen.DanglingImages{Count: len(freed), Size: reclaimableSize(freed)}, nil
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
