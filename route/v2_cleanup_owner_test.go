package route

import (
	"net/http"
	"testing"

	"gotest.tools/v3/assert"
)

// The cleanup lists the images and the volumes of every app and deletes: a `user`
// account is refused before anything is read.
func TestTheCleanupIsTheOwnersOnly(t *testing.T) {
	r := newReadsRig(t)

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/cleanup", ""},
		{http.MethodDelete, "/cleanup/build-cache", ""},
		{http.MethodPost, "/cleanup/images", `{"names":["sha256:x"]}`},
		{http.MethodPost, "/cleanup/volumes", `{"names":["data"]}`},
	} {
		got := r.do(r.v2, c.method, "/v2/app_management"+c.path, r.user, c.body)
		assert.Equal(t, got.Code, http.StatusForbidden, "%s %s: %s", c.method, c.path, bodyOf(got))
	}
}
