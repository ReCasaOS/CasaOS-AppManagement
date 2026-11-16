package route

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"gotest.tools/v3/assert"
)

func internalRouter(t *testing.T) http.Handler {
	t.Helper()
	runtime := t.TempDir()
	previous, previousTasks := config.CommonInfo.RuntimePath, service.SyncTasksPath
	config.CommonInfo.RuntimePath = runtime
	service.SyncTasksPath = filepath.Join(runtime, "sync.json")
	t.Cleanup(func() { config.CommonInfo.RuntimePath, service.SyncTasksPath = previous, previousTasks })
	assert.NilError(t, os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret"), 0o600))

	return InitV2Router()
}

func serve(router http.Handler, method, url, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, url, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:40000"
	request.Header.Set("Authorization", "Internal s3cret")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	return recorder
}

// The dialog sends only what the owner sets (and the id when editing); the toggle and the
// delete send back whole what they read. None of them may be stopped by the validator.
func TestTheDialogsBodyPassesTheRequestValidator(t *testing.T) {
	router := internalRouter(t)

	owners := `"name":"Photos","destination":"e2e","remote_path":"Photos","local_path":"/DATA/Gallery/Photos","direction":"download","mirror":false,"archive":true,"archive_days":30,"every":"daily","at":"02:00","weekday":0,"enabled":true`
	for what, body := range map[string]string{
		"new task":    `{"tasks":[{` + owners + `}]}`,
		"edited task": `{"tasks":[{"id":"s0123456789abcdef",` + owners + `}]}`,
		"whole task":  `{"tasks":[{"id":"s0123456789abcdef",` + owners + `,"last_run":"2026-11-16T03:00:00Z","last_result":"ok","last_error":"","last_files":3,"last_bytes":42,"first_pass_done":false,"needs_first_pass":false,"needs_delete_confirm":false}]}`,
	} {
		recorder := serve(router, http.MethodPut, "/v2/app_management/sync/tasks", body)
		assert.Assert(t, !strings.Contains(recorder.Body.String(), "request body has an error"),
			"%s stopped by the validator: %d %s", what, recorder.Code, recorder.Body.String())
	}

	// and a real mistake is still one
	recorder := serve(router, http.MethodPut, "/v2/app_management/sync/tasks", `{"tasks":[{"destination":"e2e"}]}`)
	assert.Equal(t, recorder.Code, http.StatusBadRequest, "a task without a name: %s", recorder.Body.String())
}

// What a route this AppManagement does not have answers, under the same prefix: an older
// AppManagement answers the sync routes the same way.
func TestAnUnknownRouteAnswer(t *testing.T) {
	recorder := serve(internalRouter(t), http.MethodGet, "/v2/app_management/does-not-exist", "")
	t.Logf("GET /v2/app_management/does-not-exist: %d %s", recorder.Code, recorder.Body.String())
	assert.Equal(t, recorder.Code, http.StatusBadRequest)
	assert.Assert(t, strings.Contains(recorder.Body.String(), "no matching operation was found"), recorder.Body.String())
}
