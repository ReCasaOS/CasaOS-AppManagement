package service

import (
	"context"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/moby/moby/client"
	"go.uber.org/zap"
)

// What a failed update puts back besides the files.
//
// An update that keeps a tag, `nginx:latest` pulled again for a newer build, moves the
// tag to the new image. Putting the old compose file back then starts the app on the new
// image all the same: the file names the tag, and the tag has moved. So the images the
// app ran are noted before the pull, and a failed update points their tags back at them.

// imageTagger is what this needs of the daemon. An interface, so a test can read back
// what was tagged.
type imageTagger interface {
	ImageInspect(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImageTag(ctx context.Context, options client.ImageTagOptions) (client.ImageTagResult, error)
}

// imagesRunBy is the image each tag of the app points at right now. A reference pinned
// by digest is left out: a digest does not move. So is one the daemon does not hold.
func imagesRunBy(ctx context.Context, cli imageTagger, app *ComposeApp) map[string]string {
	held := map[string]string{}
	for _, service := range app.Services {
		ref := service.Image
		if ref == "" || strings.Contains(ref, "@") {
			continue
		}
		if inspected, err := cli.ImageInspect(ctx, ref); err == nil && inspected.ID != "" {
			held[ref] = inspected.ID
		}
	}

	return held
}

// pointTagsBack makes each tag point at the image it pointed at before the update. A tag
// that did not move is left alone. A failure is logged: the rest of the rollback goes on.
func pointTagsBack(ctx context.Context, cli imageTagger, held map[string]string) {
	for ref, id := range held {
		if now, err := cli.ImageInspect(ctx, ref); err == nil && now.ID == id {
			continue
		}
		if _, err := cli.ImageTag(ctx, client.ImageTagOptions{Source: id, Target: ref}); err != nil {
			logger.Error("failed to point a tag back at the image the app ran", zap.Error(err), zap.String("tag", ref), zap.String("image", id))
		}
	}
}
