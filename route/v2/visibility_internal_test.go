package v2

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

func TestAppOfStatsID(t *testing.T) {
	for id, want := range map[string]string{
		"app:immich:db:1":    "immich",
		"app:immich:db:12":   "immich",
		"app:my-app_1.x:a:1": "my-app_1.x",
	} {
		app, ok := appOfStatsID(id)
		assert.Assert(t, ok, id)
		assert.Equal(t, app, want, id)
	}
	// the host, a virtual machine, a container no app owns and nothing are not apps
	for _, id := range []string{"host", "vm:x", "container:my.db_1", "container:app", "", "app", "app:"} {
		app, ok := appOfStatsID(id)
		assert.Assert(t, !ok, "%q is no app id, got %q", id, app)
	}
}

// internalRequest makes the request one of the box's own services, which sees every app.
func internalRequest(t *testing.T, request *http.Request) *http.Request {
	t.Helper()
	runtime := t.TempDir()
	was := config.CommonInfo.RuntimePath
	config.CommonInfo.RuntimePath = runtime
	t.Cleanup(func() { config.CommonInfo.RuntimePath = was })
	assert.NilError(t, os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret"), 0o600))
	request.RemoteAddr = "127.0.0.1:40000"
	request.Header.Set("Authorization", "Internal s3cret")

	return request
}

func visibilityCaller(t *testing.T, userID string) echo.Context {
	t.Helper()
	was := service.VisibilityPath
	service.VisibilityPath = filepath.Join(t.TempDir(), "visibility.json")
	t.Cleanup(func() { service.VisibilityPath = was })
	assert.NilError(t, service.AppVisibility.Hide("immich", []int{2}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "203.0.113.5:40000"
	request.Header.Set("user_id", userID)

	return echo.New().NewContext(request, httptest.NewRecorder())
}

func TestVisibleAppsKeepsWhatTheCallerMaySee(t *testing.T) {
	apps := map[string]*service.ComposeApp{"jarvis": nil, "immich": nil}

	// no user-service answers here, so every account is a user
	got := visibleApps(visibilityCaller(t, "2"), apps)
	assert.DeepEqual(t, mapKeys(got), []string{"jarvis"})
	assert.Equal(t, len(apps), 2, "the caller's map is not touched")

	got = visibleApps(visibilityCaller(t, "3"), apps)
	assert.DeepEqual(t, mapKeys(got), []string{"immich", "jarvis"})

	// a caller no account can be told from sees no app
	got = visibleApps(visibilityCaller(t, "nobody"), apps)
	assert.Equal(t, len(got), 0)
}

func TestHiddenAppsListsWhatIsHiddenFromTheCaller(t *testing.T) {
	apps, ok := hiddenApps(visibilityCaller(t, "2"))
	assert.Assert(t, ok)
	assert.DeepEqual(t, apps, []string{"immich"})

	apps, ok = hiddenApps(visibilityCaller(t, "3"))
	assert.Assert(t, ok && len(apps) == 0)

	_, ok = hiddenApps(visibilityCaller(t, "nobody"))
	assert.Assert(t, !ok, "nothing can be left out for a caller nobody can tell")
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	return keys
}

func TestTheRunningRouteLeavesOutBackupsOfHiddenApps(t *testing.T) {
	done, id := runningDemo(t, true)
	t.Cleanup(func() { _ = service.StopLiveBackup(id); <-done })

	ask := func(userID string) string {
		ctx := visibilityCaller(t, userID)
		assert.NilError(t, service.AppVisibility.Hide("demo", []int{2}))
		recorder := httptest.NewRecorder()
		ctx.SetResponse(echo.NewResponse(recorder, ctx.Echo()))
		assert.NilError(t, (&AppManagement{}).BackupRunning(ctx))

		return recorder.Body.String()
	}
	assert.Assert(t, !strings.Contains(ask("2"), `"app":"demo"`))
	assert.Assert(t, strings.Contains(ask("3"), `"app":"demo"`))
}
