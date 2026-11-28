package service

import (
	"context"
	"errors"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	"gotest.tools/v3/assert"
)

// fakeTagger is a daemon whose tags point at images and whose containers run images,
// and that records the tags it moved and the images it was asked to remove. Any other
// call of the client panics.
type fakeTagger struct {
	client.APIClient
	tags       map[string]string
	containers []container.Summary
	tagged     []client.ImageTagOptions
	digests    map[string][]string // RepoDigests an inspect gives back, by reference
	removed    []client.ImageRemoveOptions
	calls      []string // "tag <target> <source>" and "remove <id>", in order
}

func (d *fakeTagger) ContainerList(_ context.Context, options client.ContainerListOptions) (client.ContainerListResult, error) {
	project := ""
	for term, values := range options.Filters {
		for value := range values {
			if term == "label" {
				_, project, _ = cutLabel(value)
			}
		}
	}
	items := []container.Summary{}
	for _, c := range d.containers {
		if project == "" || c.Labels[api.ProjectLabel] == project {
			items = append(items, c)
		}
	}

	return client.ContainerListResult{Items: items}, nil
}

func cutLabel(value string) (string, string, bool) {
	for i := range value {
		if value[i] == '=' {
			return value[:i], value[i+1:], true
		}
	}

	return value, "", false
}

func (d *fakeTagger) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	id, ok := d.tags[ref]
	if !ok {
		return client.ImageInspectResult{}, errors.New("No such image: " + ref)
	}

	return client.ImageInspectResult{InspectResponse: image.InspectResponse{ID: id, RepoDigests: d.digests[ref]}}, nil
}

func (d *fakeTagger) ImageTag(_ context.Context, options client.ImageTagOptions) (client.ImageTagResult, error) {
	d.tagged = append(d.tagged, options)
	d.calls = append(d.calls, "tag "+options.Target+" "+options.Source)
	d.tags[options.Target] = options.Source

	return client.ImageTagResult{}, nil
}

func (d *fakeTagger) ImageRemove(_ context.Context, id string, options client.ImageRemoveOptions) (client.ImageRemoveResult, error) {
	d.removed = append(d.removed, options)
	d.calls = append(d.calls, "remove "+id)

	return client.ImageRemoveResult{}, errors.New("image is being used by a stopped container") // the outcome is ignored
}

func (d *fakeTagger) Close() error { return nil }

func aContainer(project, service, imageID string) container.Summary {
	return container.Summary{ImageID: imageID, Labels: map[string]string{api.ProjectLabel: project, api.ServiceLabel: service}}
}

// `nginx:latest` pulled again moves the tag to the new image. A failed update that only
// put the old compose file back started the app on the new image all the same.
func TestAFailedUpdatePointsAMovedTagBackAtTheImageTheAppRan(t *testing.T) {
	daemon := &fakeTagger{
		tags:       map[string]string{"nginx:latest": "sha256:old", "redis:7": "sha256:redis"},
		containers: []container.Summary{aContainer("web", "web", "sha256:old"), aContainer("web", "cache", "sha256:redis")},
	}
	app := &ComposeApp{Name: "web", Services: types.Services{
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
	pointTagsBack(context.Background(), daemon, "web", held)

	assert.DeepEqual(t, daemon.tagged, []client.ImageTagOptions{{Source: "sha256:old", Target: "nginx:latest"}})
}

// Two apps on `redis:7`: B was updated last week, so the tag points at the new image, but
// A still runs the old one. What A goes back to is what its container ran, not the tag.
func TestWhatIsNotedIsTheImageTheContainersRunNotTheTag(t *testing.T) {
	daemon := &fakeTagger{
		tags:       map[string]string{"redis:7": "sha256:y"},
		containers: []container.Summary{aContainer("a", "cache", "sha256:x"), aContainer("b", "cache", "sha256:y")},
	}
	app := &ComposeApp{Name: "a", Services: types.Services{"cache": {Name: "cache", Image: "redis:7"}}}

	assert.DeepEqual(t, imagesRunBy(context.Background(), daemon, app), map[string]string{"redis:7": "sha256:x"})
}

// Pointing a tag back that another app runs would take that app back a version at its
// next start: the tag is left where it is.
func TestATagAnotherAppRunsIsLeftWhereItIs(t *testing.T) {
	daemon := &fakeTagger{
		tags:       map[string]string{"redis:7": "sha256:y"},
		containers: []container.Summary{aContainer("a", "cache", "sha256:y"), aContainer("b", "cache", "sha256:y")},
	}

	pointTagsBack(context.Background(), daemon, "a", map[string]string{"redis:7": "sha256:x"})

	assert.Equal(t, len(daemon.tagged), 0)
	assert.Equal(t, daemon.tags["redis:7"], "sha256:y")
}
