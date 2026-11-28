package docker

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestTheTagListIsBesideTheManifests(t *testing.T) {
	got, err := tagsListURL("https://index.docker.io/v2/linuxserver/jellyfin/manifests/10.9.11")
	assert.NilError(t, err)
	assert.Equal(t, got, "https://index.docker.io/v2/linuxserver/jellyfin/tags/list?n=1000")

	got, err = tagsListURL("https://registry.local:5000/v2/team/app/manifests/sha256:abc")
	assert.NilError(t, err)
	assert.Equal(t, got, "https://registry.local:5000/v2/team/app/tags/list?n=1000")

	_, err = tagsListURL("https://example.com/v2/x/blobs/y")
	assert.Assert(t, err != nil)
}

func TestTheNextPageIsReadFromTheLinkHeaderOnTheSameHostOnly(t *testing.T) {
	current := "https://ghcr.io/v2/org/app/tags/list?n=1000"

	next, err := nextTagsPage(current, `</v2/org/app/tags/list?last=1.4.2&n=1000>; rel="next"`)
	assert.NilError(t, err)
	assert.Equal(t, next, "https://ghcr.io/v2/org/app/tags/list?last=1.4.2&n=1000")

	next, err = nextTagsPage(current, "")
	assert.NilError(t, err)
	assert.Equal(t, next, "")

	_, err = nextTagsPage(current, `<https://elsewhere.example/v2/x/tags/list?last=a>; rel="next"`)
	assert.ErrorContains(t, err, "another host")
}
