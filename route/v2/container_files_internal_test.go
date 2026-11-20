//go:build linux

package v2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/containerfs"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

// a container: /config on a volume, /app in its own layer, a binary and a big file
func fakeContainer(t *testing.T, running bool) {
	t.Helper()
	dir := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "config", "sub"), 0o755))
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "app"), 0o755))
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "etc"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "etc", "passwd"), []byte("root:x:0:0::/root:/bin/sh\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "config", "app.yml"), []byte("port: 8080\n"), 0o640))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "app", "bin"), []byte{0x7f, 'E', 'L', 'F', 0}, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "app", "big.log"), []byte(strings.Repeat("a", containerTextMax+1)), 0o644))

	was := openContainerRoot
	t.Cleanup(func() { openContainerRoot = was })
	openContainerRoot = func(context.Context, string) (*containerfs.Root, containerOf, error) {
		if !running {
			return nil, containerOf{}, errContainerStopped
		}
		root, err := containerfs.Open(dir)
		if err == containerfs.ErrUnsupported {
			t.Skip(err)
		}
		return root, containerOf{mounts: []string{"/config"}, project: "files-test-app"}, err
	}
}

func filesCall(t *testing.T, owner bool, route, p string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/?path="+p, nil)
	if owner {
		request = internalRequest(t, request)
	} else {
		request.Header.Set("user_id", "2")
	}
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	a := &AppManagement{}
	switch route {
	case "list":
		assert.NilError(t, a.ContainerFiles(ctx, "c1", codegen.ContainerFilesParams{Path: p}))
	case "content":
		assert.NilError(t, a.ContainerFileContent(ctx, "c1", codegen.ContainerFileContentParams{Path: p}))
	case "download":
		assert.NilError(t, a.ContainerFileDownload(ctx, "c1", codegen.ContainerFileDownloadParams{Path: p}))
	}

	return recorder
}

func TestTheOwnerListsAFolderAndSeesWhatAVolumeKeeps(t *testing.T) {
	fakeContainer(t, true)

	res := filesCall(t, true, "list", "/")
	assert.Equal(t, res.Code, http.StatusOK)
	var body codegen.ContainerFilesOK
	assert.NilError(t, json.Unmarshal(res.Body.Bytes(), &body))
	assert.Assert(t, body.Data.InVolume == nil)
	byName := map[string]codegen.ContainerFileEntry{}
	for _, e := range body.Data.Entries {
		byName[e.Name] = e
	}
	assert.Assert(t, byName["config"].Volume != nil && *byName["config"].Volume)
	assert.Assert(t, byName["app"].Volume == nil)

	res = filesCall(t, true, "list", "/config")
	assert.NilError(t, json.Unmarshal(res.Body.Bytes(), &body))
	assert.Equal(t, *body.Data.InVolume, "/config")
	assert.Equal(t, body.Data.Entries[0].Name, "sub") // folders first
	file := body.Data.Entries[1]
	assert.Equal(t, file.Type, codegen.File)
	assert.Equal(t, file.Mode, "rw-r-----")
	assert.Equal(t, *file.Octal, "640")
}

func TestATextIsReadAndABinaryOrABigFileIsNot(t *testing.T) {
	fakeContainer(t, true)

	res := filesCall(t, true, "content", "/config/app.yml")
	assert.Equal(t, res.Code, http.StatusOK)
	assert.Assert(t, strings.Contains(res.Body.String(), "port: 8080"))

	assert.Equal(t, filesCall(t, true, "content", "/app/bin").Code, http.StatusUnsupportedMediaType)
	assert.Equal(t, filesCall(t, true, "content", "/app/big.log").Code, http.StatusRequestEntityTooLarge)
	assert.Equal(t, filesCall(t, true, "content", "/config").Code, http.StatusBadRequest)
}

func TestAFileIsDownloadedAsItIs(t *testing.T) {
	fakeContainer(t, true)

	res := filesCall(t, true, "download", "/config/app.yml")
	assert.Equal(t, res.Code, http.StatusOK)
	assert.Equal(t, res.Body.String(), "port: 8080\n")
	assert.Assert(t, strings.Contains(res.Header().Get(echo.HeaderContentDisposition), "app.yml"))
}

func TestOnlyTheOwnerReadsAContainersFiles(t *testing.T) {
	fakeContainer(t, true)

	assert.Equal(t, filesCall(t, false, "list", "/").Code, http.StatusForbidden)
	assert.Equal(t, filesCall(t, false, "content", "/config/app.yml").Code, http.StatusForbidden)
	assert.Equal(t, filesCall(t, false, "download", "/config/app.yml").Code, http.StatusForbidden)
}

func TestTheAnswersSayWhatIsWrong(t *testing.T) {
	fakeContainer(t, true)
	assert.Equal(t, filesCall(t, true, "list", "config").Code, http.StatusBadRequest)
	assert.Equal(t, filesCall(t, true, "list", "/nothing").Code, http.StatusNotFound)
	assert.Equal(t, filesCall(t, true, "list", "/config/app.yml").Code, http.StatusBadRequest)

	fakeContainer(t, false)
	assert.Equal(t, filesCall(t, true, "list", "/").Code, http.StatusConflict)
}

func TestTheModeIsWrittenAsLsWritesIt(t *testing.T) {
	assert.Equal(t, modeText(0o777|os.ModeSticky), "rwxrwxrwt")
	assert.Equal(t, modeOctal(0o777|os.ModeSticky), "1777")
	assert.Equal(t, modeText(0o4755|os.ModeSetuid), "rwsr-xr-x")
	assert.Equal(t, modeText(0o644|os.ModeSetgid), "rw-r-Sr--")
	assert.Equal(t, modeOctal(0o755|os.ModeSetuid), "4755")
}

func writeCall(t *testing.T, owner bool, method, target, contentType, body string, handle func(*AppManagement, echo.Context) error) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set(echo.HeaderContentType, contentType)
	}
	if owner {
		request = internalRequest(t, request)
	} else {
		request.Header.Set("user_id", "2")
	}
	recorder := httptest.NewRecorder()
	assert.NilError(t, handle(&AppManagement{}, echo.New().NewContext(request, recorder)))

	return recorder
}

func mkdirCall(t *testing.T, owner bool, p string) int {
	return writeCall(t, owner, http.MethodPost, "/", echo.MIMEApplicationJSON, `{"path":"`+p+`"}`, func(a *AppManagement, c echo.Context) error {
		return a.MakeContainerFolder(c, "c1")
	}).Code
}

func writeTextCall(t *testing.T, p, content string, replace bool) int {
	body, _ := json.Marshal(map[string]any{"path": p, "content": content, "replace": replace})
	return writeCall(t, true, http.MethodPut, "/", echo.MIMEApplicationJSON, string(body), func(a *AppManagement, c echo.Context) error {
		return a.WriteContainerFileContent(c, "c1")
	}).Code
}

func deleteCall(t *testing.T, owner bool, p string) int {
	return writeCall(t, owner, http.MethodDelete, "/", "", "", func(a *AppManagement, c echo.Context) error {
		return a.DeleteContainerFile(c, "c1", codegen.DeleteContainerFileParams{Path: p})
	}).Code
}

func TestTheOwnerMakesWritesAndDeletes(t *testing.T) {
	fakeContainer(t, true)

	assert.Equal(t, mkdirCall(t, true, "/config/backups"), http.StatusOK)
	assert.Equal(t, mkdirCall(t, true, "/config/backups"), http.StatusConflict)
	assert.Equal(t, writeTextCall(t, "/config/backups/a.txt", "one", false), http.StatusOK)
	assert.Equal(t, writeTextCall(t, "/config/backups/a.txt", "two", false), http.StatusConflict)
	assert.Equal(t, writeTextCall(t, "/config/backups/a.txt", "two", true), http.StatusOK)
	res := filesCall(t, true, "content", "/config/backups/a.txt")
	assert.Assert(t, strings.Contains(res.Body.String(), `"content":"two"`), res.Body.String())

	assert.Equal(t, deleteCall(t, true, "/config/backups"), http.StatusOK)
	assert.Equal(t, filesCall(t, true, "list", "/config/backups").Code, http.StatusNotFound)
	assert.Equal(t, deleteCall(t, true, "/"), http.StatusBadRequest)
}

func TestAFileIsUploadedStreamed(t *testing.T) {
	fakeContainer(t, true)
	body := "--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"x.bin\"\r\nContent-Type: application/octet-stream\r\n\r\nbinary\x00data\r\n--b--\r\n"
	upload := func(replace bool) int {
		return writeCall(t, true, http.MethodPost, "/", "multipart/form-data; boundary=b", body, func(a *AppManagement, c echo.Context) error {
			return a.UploadContainerFile(c, "c1", codegen.UploadContainerFileParams{Path: "/app/x.bin", Replace: &replace})
		}).Code
	}

	assert.Equal(t, upload(false), http.StatusOK)
	assert.Equal(t, upload(false), http.StatusConflict)
	res := filesCall(t, true, "download", "/app/x.bin")
	assert.Equal(t, res.Body.String(), "binary\x00data")
}

func TestNothingIsWrittenByAnotherAccountOrWhileTheAppIsBusy(t *testing.T) {
	fakeContainer(t, true)

	assert.Equal(t, mkdirCall(t, false, "/config/x"), http.StatusForbidden)
	assert.Equal(t, deleteCall(t, false, "/config/app.yml"), http.StatusForbidden)

	end, err := service.Begin("files-test-app", "update")
	assert.NilError(t, err)
	assert.Equal(t, mkdirCall(t, true, "/config/x"), http.StatusConflict)
	assert.Equal(t, deleteCall(t, true, "/config/app.yml"), http.StatusConflict)
	end()
	assert.Equal(t, deleteCall(t, true, "/config/app.yml"), http.StatusOK)
}
