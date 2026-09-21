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
	"testing"

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

func TestDownloadUnpacksZipOverHTTP(t *testing.T) {
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	w, err := zw.Create("store/Apps/demo/docker-compose.yml")
	assert.NilError(t, err)
	_, err = w.Write([]byte("name: demo\n"))
	assert.NilError(t, err)
	assert.NilError(t, zw.Close())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipped.Bytes())
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
		_ = Download(srv.URL+"/store/", dst)
		assertNoSecret(t, dst)
	})
}
