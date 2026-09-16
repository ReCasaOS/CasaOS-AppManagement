package v2

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

// Each refusal of the service reaches the dashboard as the status the API promises, with
// the service's own words.
func TestAGitAppRefusalIsAnsweredWithItsStatus(t *testing.T) {
	answer := func(err error) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), recorder)
		assert.NilError(t, gitAppError(ctx, err))

		return recorder
	}

	for err, status := range map[error]int{
		service.GitRequestError("tracked files were modified in /DATA/AppData/jarvis"): http.StatusBadRequest,
		fmt.Errorf("jarvis: %w", service.ErrGitAppNotFound):                            http.StatusNotFound,
		service.ErrGitAppNameTaken:                                                     http.StatusConflict,
		service.ErrGitAppDeployed:                                                      http.StatusConflict,
		service.ErrAppBusy{App: "jarvis", Running: "deploy"}:                           http.StatusConflict,
		errors.New("disk full"):                                                        http.StatusInternalServerError,
	} {
		recorder := answer(err)
		assert.Equal(t, recorder.Code, status, err.Error())
		assert.Equal(t, recorder.Body.String(), fmt.Sprintf("{\"message\":%q}\n", err.Error()))
	}
}

func TestAGitAppIsAnsweredUnderData(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), recorder)

	assert.NilError(t, gitAppAnswer(ctx, http.StatusAccepted, &service.GitAppView{App: "jarvis", State: "building"}, nil))

	assert.Equal(t, recorder.Code, http.StatusAccepted)
	assert.Assert(t, len(recorder.Body.String()) > 0)
	assert.Equal(t, recorder.Body.String()[:36], `{"data":{"app":"jarvis","origin":"",`)
}
