package v2

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
	"github.com/samber/lo"
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

// A forge learns how its delivery went, and a caller without a valid signature only which
// refusal it got.
func TestAGitWebhookIsAnsweredWithItsStatus(t *testing.T) {
	for _, c := range []struct {
		answer string
		err    error
		status int
		body   string
	}{
		{service.GitWebhookPong, nil, http.StatusOK, "pong"},
		{service.GitWebhookChecking, nil, http.StatusAccepted, "checking"},
		{service.GitWebhookCoalesced, nil, http.StatusAccepted, "coalesced"},
		{service.GitWebhookQueued, nil, http.StatusAccepted, "queued"},
		{service.GitWebhookIgnored, nil, http.StatusAccepted, "ignored"},
		{"", service.ErrGitWebhookSignature, http.StatusUnauthorized, "invalid signature"},
		{"", service.ErrGitWebhookNotFound, http.StatusNotFound, "not found"},
		{"", service.ErrGitWebhookTooLarge, http.StatusRequestEntityTooLarge, "the body is over 5 MiB"},
		{"", errors.New("open /var/lib/casaos/git_apps/jarvis.webhook: permission denied"), http.StatusInternalServerError, "internal error"},
	} {
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), recorder)
		assert.NilError(t, gitWebhookAnswer(ctx, c.answer, c.err))

		assert.Equal(t, recorder.Code, c.status, c.body)
		assert.Equal(t, recorder.Body.String(), fmt.Sprintf("{\"message\":%q}\n", c.body))
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

// The service builds the view itself: what it answers is what the spec declares.
func TestAGitAppsWebhookIsAnsweredAsTheSpecDeclaresIt(t *testing.T) {
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	view := &service.GitAppView{App: "jarvis", History: []service.GitHistoryView{}, Webhook: service.GitWebhookView{
		Enabled: true, Path: "/v2/app_management/git/jarvis/webhook", Secret: strings.Repeat("5e", 32),
		LastDelivery: &service.GitWebhookDelivery{At: at, Forge: "github", Event: "push", Result: "checked"},
	}}
	raw, err := json.Marshal(view)
	assert.NilError(t, err)

	var answered codegen.GitApp
	assert.NilError(t, json.Unmarshal(raw, &answered))
	assert.Equal(t, answered.Webhook.Enabled, true)
	assert.Equal(t, answered.Webhook.Path, "/v2/app_management/git/jarvis/webhook")
	assert.Equal(t, lo.FromPtr(answered.Webhook.Secret), strings.Repeat("5e", 32))
	assert.Assert(t, answered.Webhook.LastDelivery.At.Equal(at))
	assert.Equal(t, answered.Webhook.LastDelivery.Forge, "github")
	assert.Equal(t, answered.Webhook.LastDelivery.Event, "push")
	assert.Equal(t, answered.Webhook.LastDelivery.Result, "checked")
}

func TestAPutPassesTheWebhookFieldsOn(t *testing.T) {
	var body codegen.GitAppUpdateRequest
	assert.NilError(t, json.Unmarshal([]byte(`{"auto_deploy":true,"webhook_enabled":false,"regenerate_webhook_secret":true}`), &body))

	assert.DeepEqual(t, gitAppChanges(body), service.GitAppChanges{
		AutoDeploy: lo.ToPtr(true), WebhookEnabled: lo.ToPtr(false), RegenerateWebhookSecret: lo.ToPtr(true),
	})
}

// The service builds the view itself: the tag fields it answers are those the spec declares.
func TestAGitAppsTagFieldsAreAnsweredAsTheSpecDeclaresThem(t *testing.T) {
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	view := &service.GitAppView{
		App: "jarvis", Follow: "tags", TagPattern: "v1.*", Prereleases: true,
		Deployed: &service.GitDeployedView{Commit: "abc", Subject: "fix", At: at, Tag: "v1.4.1"},
		Check:    &service.GitCheckView{At: at, RemoteCommit: "def", RemoteTag: "v1.4.1", TagMoved: true},
		History:  []service.GitHistoryView{{Commit: "abc", At: at, Outcome: "deployed", Tag: "v1.4.1"}},
	}
	// the operation's type is the service's own: it comes the way a state file holds it
	assert.NilError(t, json.Unmarshal([]byte(`{"kind":"build","commit":"def","started_at":"2026-09-23T10:00:00Z","tag":"v1.4.2"}`), &view.Operation))
	raw, err := json.Marshal(view)
	assert.NilError(t, err)

	var answered codegen.GitApp
	assert.NilError(t, json.Unmarshal(raw, &answered))
	assert.Equal(t, answered.Follow, "tags")
	assert.Equal(t, answered.TagPattern, "v1.*")
	assert.Equal(t, answered.Prereleases, true)
	assert.Equal(t, answered.Deployed.Tag, "v1.4.1")
	assert.Equal(t, answered.Check.RemoteTag, "v1.4.1")
	assert.Equal(t, answered.Check.TagMoved, true)
	assert.Equal(t, answered.History[0].Tag, "v1.4.1")
	assert.Equal(t, answered.Operation.Tag, "v1.4.2")
}

func TestAPostAndAPutPassTheTagFieldsOn(t *testing.T) {
	var create codegen.GitAppCreateRequest
	assert.NilError(t, json.Unmarshal([]byte(`{"name":"jarvis","url":"https://example.invalid/r.git","access":"none","follow":"tags","tag_pattern":"v2.*","prereleases":true}`), &create))
	assert.DeepEqual(t, gitAppRegistration(create), service.GitAppRegistration{
		Name: "jarvis", URL: "https://example.invalid/r.git", Access: "none", Follow: "tags", TagPattern: "v2.*", Prereleases: true,
	})

	var update codegen.GitAppUpdateRequest
	assert.NilError(t, json.Unmarshal([]byte(`{"follow":"branch","branch":"main","tag_pattern":"","prereleases":false}`), &update))
	assert.DeepEqual(t, gitAppChanges(update), service.GitAppChanges{
		Follow: lo.ToPtr("branch"), Branch: lo.ToPtr("main"), TagPattern: lo.ToPtr(""), Prereleases: lo.ToPtr(false),
	})
}

func TestTheTagsOfAGitAppAreAnsweredUnderData(t *testing.T) {
	answer := func(tags []service.GitTag, err error) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), recorder)
		assert.NilError(t, gitAppTagsAnswer(ctx, tags, err))

		return recorder
	}
	const commit = "4f5f60c16eba0123456789abcdef0123456789ab"

	recorder := answer([]service.GitTag{{Name: "v1.4.2", Commit: commit}}, nil)
	assert.Equal(t, recorder.Code, http.StatusOK)
	var answered codegen.GitAppTagsOK
	assert.NilError(t, json.Unmarshal(recorder.Body.Bytes(), &answered))
	assert.DeepEqual(t, *answered.Data, []codegen.GitTag{{Name: "v1.4.2", Commit: commit}})

	recorder = answer(nil, service.GitRequestError("jarvis follows a branch, not tags"))
	assert.Equal(t, recorder.Code, http.StatusBadRequest)
	assert.Equal(t, recorder.Body.String(), "{\"message\":\"jarvis follows a branch, not tags\"}\n")
}
