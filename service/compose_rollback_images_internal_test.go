package service

import (
	"context"
	"errors"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	"gotest.tools/v3/assert"
)

// fakeTagger is a daemon whose tags point at images, and that records the tags it moved.
type fakeTagger struct {
	tags   map[string]string
	tagged []client.ImageTagOptions
}

func (d *fakeTagger) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	id, ok := d.tags[ref]
	if !ok {
		return client.ImageInspectResult{}, errors.New("No such image: " + ref)
	}

	return client.ImageInspectResult{InspectResponse: image.InspectResponse{ID: id}}, nil
}

func (d *fakeTagger) ImageTag(_ context.Context, options client.ImageTagOptions) (client.ImageTagResult, error) {
	d.tagged = append(d.tagged, options)
	d.tags[options.Target] = options.Source

	return client.ImageTagResult{}, nil
}

// `nginx:latest` pulled again moves the tag to the new image. A failed update that only
// put the old compose file back started the app on the new image all the same.
func TestAFailedUpdatePointsAMovedTagBackAtTheImageTheAppRan(t *testing.T) {
	daemon := &fakeTagger{tags: map[string]string{"nginx:latest": "sha256:old", "redis:7": "sha256:redis"}}
	app := &ComposeApp{Services: types.Services{
		"web":    {Name: "web", Image: "nginx:latest"},
		"cache":  {Name: "cache", Image: "redis:7"},
		"pinned": {Name: "pinned", Image: "postgres@sha256:aaaa"},
		"gone":   {Name: "gone", Image: "nothing:here"},
		"built":  {Name: "built"},
	}}

	held := imagesRunBy(context.Background(), daemon, app)
	// a digest does not move, and an image the daemon does not hold has nothing to go back to
	assert.DeepEqual(t, held, map[string]string{"nginx:latest": "sha256:old", "redis:7": "sha256:redis"})

	// the update pulled a newer nginx under the same tag, and redis did not move
	daemon.tags["nginx:latest"] = "sha256:new"
	pointTagsBack(context.Background(), daemon, held)

	assert.DeepEqual(t, daemon.tagged, []client.ImageTagOptions{{Source: "sha256:old", Target: "nginx:latest"}})
	assert.Equal(t, daemon.tags["nginx:latest"], "sha256:old")
}
