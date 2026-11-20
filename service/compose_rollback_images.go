package service

import (
	"context"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"
	"go.uber.org/zap"
)

// What a failed update puts back besides the files.
//
// An update that keeps a tag, `nginx:latest` pulled again for a newer build, moves the
// tag to the new image. Putting the old compose file back then starts the app on the new
// image all the same: the file names the tag, and the tag has moved. So the image each
// service of the app runs is noted before the pull, and a failed update points the tags
// back at them.

// imageTagger is what this needs of the daemon. An interface, so a test can read back
// what was tagged.
type imageTagger interface {
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	ImageInspect(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImageTag(ctx context.Context, options client.ImageTagOptions) (client.ImageTagResult, error)
}

// imagesRunBy is, for each tag the app names, the image its containers run: not where the
// tag points, which another app's update may have moved already. A service with no
// container falls back on where the tag points. A reference pinned by digest is left
// out: a digest does not move. So is one the daemon does not hold.
func imagesRunBy(ctx context.Context, cli imageTagger, app *ComposeApp) map[string]string {
	running := map[string]string{}
	if listed, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", api.ProjectLabel+"="+app.Name),
	}); err == nil {
		for _, c := range listed.Items {
			if service := c.Labels[api.ServiceLabel]; service != "" && c.ImageID != "" {
				running[service] = c.ImageID
			}
		}
	}

	held := map[string]string{}
	for name, service := range app.Services {
		ref := service.Image
		if ref == "" || strings.Contains(ref, "@") {
			continue
		}
		if id := running[name]; id != "" {
			held[ref] = id
			continue
		}
		if inspected, err := cli.ImageInspect(ctx, ref); err == nil && inspected.ID != "" {
			held[ref] = inspected.ID
		}
	}

	return held
}

// pointTagsBack makes each tag point at the image the app ran before the update. A tag
// that did not move is left alone, and so is one whose image another app's container
// runs now: pointing it back would take that app back a version at its next start. A
// failure is logged: the rest of the rollback goes on.
func pointTagsBack(ctx context.Context, cli imageTagger, app string, held map[string]string) {
	for ref, id := range held {
		now, err := cli.ImageInspect(ctx, ref)
		if err == nil && now.ID == id {
			continue
		}
		if err == nil && usedByAnotherApp(ctx, cli, app, now.ID) {
			logger.Info("a tag is left on the new image: another app runs it", zap.String("tag", ref), zap.String("app", app))
			continue
		}
		if _, err := cli.ImageTag(ctx, client.ImageTagOptions{Source: id, Target: ref}); err != nil {
			logger.Error("failed to point a tag back at the image the app ran", zap.Error(err), zap.String("tag", ref), zap.String("image", id))
		}
	}
}

// usedByAnotherApp: a container of another compose project, or of none, runs the image.
// A daemon that cannot list its containers counts as yes: the tag is then left alone.
func usedByAnotherApp(ctx context.Context, cli imageTagger, app, image string) bool {
	listed, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return true
	}
	for _, c := range listed.Items {
		if c.ImageID == image && c.Labels[api.ProjectLabel] != app {
			return true
		}
	}

	return false
}
