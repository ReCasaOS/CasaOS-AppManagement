package v2

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

// Updating past the scan module's verdict is the owner's call: another account is refused
// before anything is looked up or started.
func TestSkipScanIsOwnerOnly(t *testing.T) {
	was := config.CommonInfo.RuntimePath
	config.CommonInfo.RuntimePath = t.TempDir() // no user-service answers here, so every account is a user
	t.Cleanup(func() { config.CommonInfo.RuntimePath = was })

	request := httptest.NewRequest(http.MethodPatch, "/v2/app_management/compose/immich?skip_scan=true", nil)
	request.RemoteAddr = "203.0.113.5:40000"
	request.Header.Set("user_id", "2")
	recorder := httptest.NewRecorder()

	skip := true
	assert.NilError(t, (&AppManagement{}).UpdateComposeApp(echo.New().NewContext(request, recorder), "immich", codegen.UpdateComposeAppParams{SkipScan: &skip}))
	assert.Equal(t, recorder.Code, http.StatusForbidden)
}
