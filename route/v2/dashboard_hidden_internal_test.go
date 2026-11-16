package v2

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

func putDashboardHidden(t *testing.T, owner bool, app, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
	if owner {
		request = internalRequest(t, request)
	} else {
		request.RemoteAddr = "203.0.113.5:40000"
		request.Header.Set("user_id", "2")
	}
	recorder := httptest.NewRecorder()
	assert.NilError(t, (&AppManagement{}).SetDashboardHidden(echo.New().NewContext(request, recorder), app))

	return recorder
}

func TestOnlyTheOwnerHidesAnAppFromTheDashboard(t *testing.T) {
	was := service.DashboardHiddenPath
	service.DashboardHiddenPath = filepath.Join(t.TempDir(), "dashboard-hidden.json")
	t.Cleanup(func() { service.DashboardHiddenPath = was })

	assert.Equal(t, putDashboardHidden(t, false, "cloudflared", `{"hidden":true}`).Code, http.StatusForbidden)
	assert.DeepEqual(t, service.DashboardHidden.List(), []string{})

	// neither a missing field nor a name that is no app's is taken for "show"
	assert.Equal(t, putDashboardHidden(t, true, "cloudflared", `{}`).Code, http.StatusBadRequest)
	assert.Equal(t, putDashboardHidden(t, true, "../etc", `{"hidden":true}`).Code, http.StatusBadRequest)

	answer := putDashboardHidden(t, true, "cloudflared", `{"hidden":true}`)
	assert.Equal(t, answer.Code, http.StatusOK)
	assert.Assert(t, strings.Contains(answer.Body.String(), `"hidden":true`), answer.Body.String())
	assert.DeepEqual(t, service.DashboardHidden.List(), []string{"cloudflared"})

	assert.Equal(t, putDashboardHidden(t, true, "cloudflared", `{"hidden":false}`).Code, http.StatusOK)
	assert.DeepEqual(t, service.DashboardHidden.List(), []string{})
}
