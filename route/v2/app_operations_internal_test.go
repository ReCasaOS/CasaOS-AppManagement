package v2

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

// An update, a save or an install refused because another operation holds the app is a
// conflict the dashboard can show, not a server error.
func TestAnOperationRefusedForABusyAppIsAConflict(t *testing.T) {
	answer := func(err error) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPut, "/", nil), recorder)
		assert.NilError(t, conflictOrServerError(ctx, err))

		return recorder
	}

	busy := answer(service.ErrAppBusy{App: "jarvis", Running: "deploy"})
	assert.Equal(t, busy.Code, http.StatusConflict)
	assert.Equal(t, busy.Body.String(), "{\"message\":\"`jarvis` is busy: deploy in progress\"}\n")

	assert.Equal(t, answer(errors.New("disk full")).Code, http.StatusInternalServerError)
}
