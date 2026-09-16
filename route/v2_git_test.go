package route

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"gotest.tools/v3/assert"
)

// The git routes go through the same router as every v2 route: its authentication and its
// validation of the request against the spec, which a route missing from the spec fails.
func TestTheGitRoutesAreServed(t *testing.T) {
	runtime := t.TempDir()
	previous := config.CommonInfo.RuntimePath
	config.CommonInfo.RuntimePath = runtime
	t.Cleanup(func() { config.CommonInfo.RuntimePath = previous })
	assert.NilError(t, os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret"), 0o600))

	router := InitV2Router()

	for _, call := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPost, "/v2/app_management/git", `{"name":"Not A Name","url":"https://example.invalid/r.git","access":"none"}`, http.StatusBadRequest},
		{http.MethodGet, "/v2/app_management/git/no-such-app", "", http.StatusNotFound},
		{http.MethodPut, "/v2/app_management/git/no-such-app", `{"auto_deploy":true}`, http.StatusNotFound},
		{http.MethodPost, "/v2/app_management/git/no-such-app/check", "", http.StatusNotFound},
		{http.MethodPost, "/v2/app_management/git/no-such-app/deploy", "", http.StatusNotFound},
		{http.MethodPost, "/v2/app_management/git/no-such-app/deploy", `{"commit":"4f5f60c"}`, http.StatusBadRequest},
		{http.MethodDelete, "/v2/app_management/git/no-such-app", "", http.StatusNotFound},
	} {
		request := httptest.NewRequest(call.method, call.path, strings.NewReader(call.body))
		request.RemoteAddr = "127.0.0.1:40000"
		request.Header.Set("Authorization", "Internal s3cret")
		if call.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)

		assert.Equal(t, recorder.Code, call.want, "%s %s: %s", call.method, call.path, recorder.Body.String())
	}
}
