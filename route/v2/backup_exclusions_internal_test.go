package v2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
)

func exclusionsCall(t *testing.T, method, body string) (int, []string) {
	t.Helper()
	request := httptest.NewRequest(method, "/", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	a := &AppManagement{}
	var err error
	if method == http.MethodGet {
		err = a.BackupExclusions(ctx)
	} else {
		err = a.SetBackupExclusions(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	var out codegen.BackupExclusionsOK
	_ = json.Unmarshal(recorder.Body.Bytes(), &out)
	if out.Data == nil {
		return recorder.Code, nil
	}

	return recorder.Code, out.Data.Paths
}

func TestTheBackupExclusionsRoutes(t *testing.T) {
	was := service.BackupExclusionsPath
	service.BackupExclusionsPath = filepath.Join(t.TempDir(), "exclusions.json")
	t.Cleanup(func() { service.BackupExclusionsPath = was })

	if code, paths := exclusionsCall(t, http.MethodGet, ""); code != http.StatusOK || paths == nil || len(paths) != 0 {
		t.Fatalf("nothing left out yet, as an empty list: %d %v", code, paths)
	}

	for _, body := range []string{
		`{"paths":["mnt/nas"]}`,
		`{"paths":["/"]}`,
		`{}`,
		`{"path":["/mnt/nas"]}`,
		`{"paths":["/mnt/nas"],"extra":1}`,
		`not json`,
		`{"paths":["/mnt/nas"]}{}`,
	} {
		if code, _ := exclusionsCall(t, http.MethodPut, body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", body, code)
		}
	}

	want := []string{"/DATA/Media", "/mnt/nas"}
	if code, paths := exclusionsCall(t, http.MethodPut, `{"paths":["/mnt/nas/","/DATA/Media"]}`); code != http.StatusOK || !slices.Equal(paths, want) {
		t.Fatalf("saved and returned clean: %d %v", code, paths)
	}
	if code, paths := exclusionsCall(t, http.MethodGet, ""); code != http.StatusOK || !slices.Equal(paths, want) {
		t.Fatalf("GET returns what PUT stored: %d %v", code, paths)
	}

	if code, paths := exclusionsCall(t, http.MethodPut, `{"paths":[]}`); code != http.StatusOK || len(paths) != 0 {
		t.Fatalf("an empty list empties it: %d %v", code, paths)
	}
}
