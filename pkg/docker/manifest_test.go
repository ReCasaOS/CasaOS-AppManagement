package docker_test

import (
	"testing"

	"github.com/inkly/CasaOS-AppManagement/pkg/docker"
	"gotest.tools/v3/assert"
)

// The tag this returns is what the update check compares versions with. Reading a
// registry's port as part of the tag makes every such tag unparseable as a version,
// which sends the comparison down its "cannot be ordered, so any difference counts"
// path -- and a catalogue sitting on an older version then reads as an update.
func TestExtractImageAndTag(t *testing.T) {
	for _, c := range []struct{ image, wantImage, wantTag string }{
		{"nginx", "nginx", "latest"},
		{"library/nginx:1.25", "library/nginx", "1.25"},
		{"ghcr.io/wg-easy/wg-easy:14", "ghcr.io/wg-easy/wg-easy", "14"},

		// a registry with a port: the colon that matters is the last one after the
		// last slash, not the first one in the string
		{"registry:5000/img:2.0", "registry:5000/img", "2.0"},
		{"192.168.1.10:5000/acme/app:3.1.4", "192.168.1.10:5000/acme/app", "3.1.4"},
		{"registry.local:5000/acme/app", "registry.local:5000/acme/app", "latest"},

		// pinned by digest: there is no tag to compare, and the reference must come
		// back whole so nothing downstream rebuilds it wrongly
		{
			"acme/app@sha256:0000000000000000000000000000000000000000000000000000000000000000",
			"acme/app@sha256:0000000000000000000000000000000000000000000000000000000000000000",
			"",
		},
		{
			"registry:5000/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			"registry:5000/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			"",
		},
	} {
		t.Run(c.image, func(t *testing.T) {
			image, tag := docker.ExtractImageAndTag(c.image)
			assert.Equal(t, image, c.wantImage)
			assert.Equal(t, tag, c.wantTag)
		})
	}
}
