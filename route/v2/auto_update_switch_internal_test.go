package v2

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

func putAutoUpdate(t *testing.T, caller echo.Context, body string) int {
	t.Helper()
	request := caller.Request()
	request.Body = io.NopCloser(strings.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header.Set("Content-Type", "application/json")
	assert.NilError(t, (&AppManagement{}).SetAutoUpdate(caller))

	return caller.Response().Writer.(*httptest.ResponseRecorder).Code
}

func withAutoUpdateFile(t *testing.T) {
	t.Helper()
	logger.LogInitConsoleOnly()
	was := service.AutoUpdatePath
	service.AutoUpdatePath = filepath.Join(t.TempDir(), "auto-update.json")
	t.Cleanup(func() { service.AutoUpdatePath = was })
}

func TestPutAutoUpdateTakesEitherField(t *testing.T) {
	withAutoUpdateFile(t)

	assert.Equal(t, putAutoUpdate(t, ownerCaller(t), `{"scan_before_update":false}`), http.StatusOK)
	assert.Assert(t, !service.ScanBeforeUpdate())
	assert.Equal(t, putAutoUpdate(t, ownerCaller(t), `{"min_days":5}`), http.StatusOK)
	view, err := service.AutoUpdate(service.BoxNow())
	assert.NilError(t, err)
	assert.Equal(t, view.MinDays, 5)
	assert.Assert(t, !view.ScanBeforeUpdate, "a body with one field leaves the other alone")

	assert.Equal(t, putAutoUpdate(t, ownerCaller(t), `{"min_days":1,"scan_before_update":true}`), http.StatusOK)
	view, err = service.AutoUpdate(service.BoxNow())
	assert.NilError(t, err)
	assert.Assert(t, view.MinDays == 1 && view.ScanBeforeUpdate)
}

func TestPutAutoUpdateRefusesEmptyBody(t *testing.T) {
	withAutoUpdateFile(t)
	assert.Equal(t, putAutoUpdate(t, ownerCaller(t), `{}`), http.StatusBadRequest)
	// a wait out of bounds applies nothing, not even the other field
	assert.Equal(t, putAutoUpdate(t, ownerCaller(t), `{"min_days":99,"scan_before_update":false}`), http.StatusBadRequest)
	assert.Assert(t, service.ScanBeforeUpdate())
}

func TestPutAutoUpdateIsTheOwnersOnly(t *testing.T) {
	withAutoUpdateFile(t)
	assert.Equal(t, putAutoUpdate(t, visibilityCaller(t, "2"), `{"scan_before_update":false}`), http.StatusForbidden)
	assert.Assert(t, service.ScanBeforeUpdate())
}
