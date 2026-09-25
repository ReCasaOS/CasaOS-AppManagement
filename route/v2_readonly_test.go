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

// A body sent back as it was read, a readOnly field included, passes the validator:
// the handler ignores that field and keeps its own (ReCasaOS/CasaOS#7).
func TestAReadOnlyFieldSentBackIsNotRefused(t *testing.T) {
	runtime := t.TempDir()
	previous := config.CommonInfo.RuntimePath
	config.CommonInfo.RuntimePath = runtime
	t.Cleanup(func() { config.CommonInfo.RuntimePath = previous })
	assert.NilError(t, os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret"), 0o600))

	router := InitV2Router()
	for _, body := range []string{
		`[{"app":"nextcloud","destination":"offsite","every":"weekly","at":"03:00","weekday":1,"keep":1,"enabled":true,"last_run":"2026-09-24T03:00:00Z"}]`,
		// and a real mistake is still one
		`[{"app":"nextcloud","destination":"offsite","every":"hourly","at":"03:00"}]`,
	} {
		request := httptest.NewRequest(http.MethodPut, "/v2/app_management/backup/schedules", strings.NewReader(body))
		request.RemoteAddr = "127.0.0.1:40000"
		request.Header.Set("Authorization", "Internal s3cret")
		request.Header.Set("Content-Type", "application/json")

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)

		refusedAsReadOnly := recorder.Code == http.StatusBadRequest && strings.Contains(recorder.Body.String(), "readOnly")
		if strings.Contains(body, "hourly") {
			assert.Equal(t, recorder.Code, http.StatusBadRequest, "an unknown `every` still fails validation: %s", recorder.Body.String())
			continue
		}
		assert.Assert(t, !refusedAsReadOnly, "last_run sent back was refused: %s", recorder.Body.String())
	}
}
