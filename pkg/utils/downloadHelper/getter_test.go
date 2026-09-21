package downloadHelper

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/go-getter"
	"go.uber.org/goleak"
	"gotest.tools/v3/assert"
)

func TestDownload(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start")) // https://github.com/census-instrumentation/opencensus-go/issues/1191

	src := "https://github.com/ReCasaOS/CasaOS-Install/archive/refs/heads/main.zip"

	dst := t.TempDir()

	err := Download(src, dst)
	assert.NilError(t, err)
}

// a zip holding one app store entry
func storeZip(t *testing.T) []byte {
	t.Helper()
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	w, err := zw.Create("store/Apps/demo/docker-compose.yml")
	assert.NilError(t, err)
	_, err = w.Write([]byte("name: demo\n"))
	assert.NilError(t, err)
	assert.NilError(t, zw.Close())
	return zipped.Bytes()
}

func TestDownloadUnpacksZipOverHTTP(t *testing.T) {
	zipped := storeZip(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipped)
	}))
	defer srv.Close()

	dst := t.TempDir()
	assert.NilError(t, Download(srv.URL+"/store/main.zip", dst))

	got, err := os.ReadFile(filepath.Join(dst, "store", "Apps", "demo", "docker-compose.yml"))
	assert.NilError(t, err)
	assert.Equal(t, string(got), "name: demo\n")
}

// Neither the URL nor the server's answer may make Download copy or link a
// local path into the app store directory.
func TestDownloadReadsNothingLocal(t *testing.T) {
	secret := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(secret, "secret.txt"), []byte("TOP SECRET"), 0o600))

	assertNoSecret := func(t *testing.T, dst string) {
		t.Helper()
		_, err := os.Stat(filepath.Join(dst, "secret.txt"))
		assert.Assert(t, errors.Is(err, fs.ErrNotExist), "secret reached %s: %v", dst, err)
	}

	t.Run("forced file getter", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "store")
		assert.ErrorContains(t, Download("file::"+secret, dst), "not supported")
		assertNoSecret(t, dst)
	})

	t.Run("X-Terraform-Get redirect", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Terraform-Get", "file::"+secret)
		}))
		defer srv.Close()

		dst := filepath.Join(t.TempDir(), "store")
		// the header is ignored, so this fails like any directory URL; were it
		// followed, the error would name the X-Terraform-Get source instead
		assert.ErrorContains(t, Download(srv.URL+"/store/", dst), "nothing was downloaded")
		assertNoSecret(t, dst)
	})
}

// go-getter reads an http(s) URL whose path ends in "/" as a directory, and
// with X-Terraform-Get disabled its directory download fetches nothing and
// reports success. UpdateCatalog would then swap the live catalogue for an
// empty directory.
func TestDownloadRefusesDirectoryURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>an index page</html>"))
	}))
	defer srv.Close()

	assert.ErrorContains(t, Download(srv.URL+"/store/", filepath.Join(t.TempDir(), "store")), "nothing was downloaded")
}

// Pins what a later edit must keep: X-Terraform-Get and terraform-get meta
// redirects stay off, so the server cannot pick the next URL fetched (an
// internal address, say), .netrc stays unread, and only http and https exist.
func TestDownloadClientSettings(t *testing.T) {
	c := newClient("https://example.com/store/main.zip", t.TempDir())
	assert.NilError(t, c.Configure(c.Options...))
	assert.Assert(t, c.DisableSymlinks)
	assert.Equal(t, len(c.Getters), 2)
	for _, scheme := range []string{"http", "https"} {
		g, ok := c.Getters[scheme].(*getter.HttpGetter)
		assert.Assert(t, ok, "%s getter is %T", scheme, c.Getters[scheme])
		assert.Assert(t, g.XTerraformGetDisabled, scheme)
		assert.Assert(t, !g.Netrc, scheme)
	}
}

// The credentials in .netrc must not go to whatever host an app store URL names.
func TestDownloadSendsNoNetrcCredentials(t *testing.T) {
	zipped := storeZip(t)
	var withAuth atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			withAuth.Add(1)
		}
		_, _ = w.Write(zipped)
	}))
	defer srv.Close()

	netrc := filepath.Join(t.TempDir(), "netrc")
	assert.NilError(t, os.WriteFile(netrc, []byte("machine "+srv.Listener.Addr().String()+" login casa password s3cret\n"), 0o600))
	t.Setenv("NETRC", netrc)

	// go-getter's default client reads .netrc: this shows the file is found
	assert.NilError(t, getter.GetFile(filepath.Join(t.TempDir(), "main.zip"), srv.URL+"/store/main.zip"))
	assert.Assert(t, withAuth.Load() > 0, "the netrc fixture was not used")

	withAuth.Store(0)
	assert.NilError(t, Download(srv.URL+"/store/main.zip", t.TempDir()))
	assert.Equal(t, withAuth.Load(), int32(0))
}
