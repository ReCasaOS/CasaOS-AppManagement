package v2

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
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

// A running sync is listed to everybody, as the core counts it, but a user account does not read its task's name.
func TestARunningSyncNamesItsTaskToTheOwnerOnly(t *testing.T) {
	was := runningOperations
	t.Cleanup(func() { runningOperations = was })
	runningOperations = func() []service.AppOperation {
		return []service.AppOperation{{App: "sync:photos", Kind: "sync"}}
	}
	a := &AppManagement{}

	read := func(ctx echo.Context) []codegen.AppOperation {
		assert.NilError(t, a.AppOperations(ctx))
		var out codegen.AppOperationsOK
		assert.NilError(t, json.Unmarshal(ctx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &out))

		return out.Operations
	}
	owner := read(ownerCaller(t))
	user := read(visibilityCaller(t, "2"))
	assert.Equal(t, len(owner), 1)
	assert.Equal(t, len(user), 1)
	assert.Equal(t, owner[0].App, "sync:photos")
	assert.Equal(t, user[0].App, "sync")
	assert.Equal(t, user[0].Kind, "sync")
}
