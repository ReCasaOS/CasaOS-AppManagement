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

func autoHealCall(t *testing.T, ctx echo.Context, put bool) (int, string) {
	var err error
	if put {
		err = (&AppManagement{}).SetAutoHeal(ctx)
	} else {
		err = (&AppManagement{}).AutoHeal(ctx)
	}
	assert.NilError(t, err)
	rec := ctx.Response().Writer.(*httptest.ResponseRecorder)

	return rec.Code, rec.Body.String()
}

func putCaller(t *testing.T, body string) echo.Context {
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	return echo.New().NewContext(internalRequest(t, req), httptest.NewRecorder())
}

func TestAutoHealRoutesOwnerOnly(t *testing.T) {
	was := service.AutoHealPath
	service.AutoHealPath = filepath.Join(t.TempDir(), "auto-heal.json")
	t.Cleanup(func() { service.AutoHealPath = was })

	code, body := autoHealCall(t, ownerCaller(t), false)
	assert.Equal(t, code, http.StatusOK)
	assert.Assert(t, strings.Contains(body, `"enabled":true`), body)

	code, body = autoHealCall(t, putCaller(t, `{"enabled":false}`), true)
	assert.Equal(t, code, http.StatusOK)
	assert.Assert(t, strings.Contains(body, `"enabled":false`), body)
	assert.Assert(t, !service.AutoHealEnabled())

	user := visibilityCaller(t, "2")
	code, _ = autoHealCall(t, user, false)
	assert.Equal(t, code, http.StatusForbidden)

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("user_id", "2")
	userPut := echo.New().NewContext(req, httptest.NewRecorder())
	code, _ = autoHealCall(t, userPut, true)
	assert.Equal(t, code, http.StatusForbidden)
	assert.Assert(t, !service.AutoHealEnabled(), "a refused call changes nothing")
}

func TestPutAutoHealNeedsEnabled(t *testing.T) {
	was := service.AutoHealPath
	service.AutoHealPath = filepath.Join(t.TempDir(), "auto-heal.json")
	t.Cleanup(func() { service.AutoHealPath = was })

	for _, body := range []string{`{}`, `not json`, `{"enabled":"yes"}`} {
		code, _ := autoHealCall(t, putCaller(t, body), true)
		assert.Equal(t, code, http.StatusBadRequest, body)
	}
}
