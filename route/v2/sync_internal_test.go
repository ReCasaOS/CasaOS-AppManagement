package v2

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

// ownerCall is a request of the box's own services, with a body and a recorder to read.
func ownerCall(t *testing.T, method, target, body string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := internalRequest(t, httptest.NewRequest(method, target, strings.NewReader(body)))
	request.Header.Set("Content-Type", "application/json")

	return echo.New().NewContext(request, recorder), recorder
}

func syncTasksFile(t *testing.T, tasks ...service.SyncTask) {
	t.Helper()
	was := service.SyncTasksPath
	service.SyncTasksPath = filepath.Join(t.TempDir(), "sync.json")
	t.Cleanup(func() { service.SyncTasksPath = was })
	encoded, err := json.Marshal(tasks)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(service.SyncTasksPath, encoded, 0o600))
}

func TestSyncRoutesAreTheOwnersOnly(t *testing.T) {
	a := &AppManagement{}
	routes := map[string]func(echo.Context) error{
		"list":            a.SyncTasks,
		"save":            a.SetSyncTasks,
		"run":             func(c echo.Context) error { return a.RunSyncTask(c, "s1") },
		"first pass":      func(c echo.Context) error { return a.FirstPassSyncTask(c, "s1") },
		"confirm deletes": func(c echo.Context) error { return a.ConfirmSyncTaskDeletes(c, "s1") },
		"folders": func(c echo.Context) error {
			return a.BackupDestinationFolders(c, "nas", codegen.BackupDestinationFoldersParams{})
		},
	}
	for name, route := range routes {
		ctx := visibilityCaller(t, "2")
		assert.NilError(t, route(ctx), name)
		assert.Equal(t, ctx.Response().Status, http.StatusForbidden, name)
	}
}

func TestSavingTasksReadsTheBodyStrictly(t *testing.T) {
	syncTasksFile(t)
	a := &AppManagement{}

	ctx, rec := ownerCall(t, http.MethodPut, "/", `{"tasks":[],"extra":1}`)
	assert.NilError(t, a.SetSyncTasks(ctx))
	assert.Equal(t, rec.Code, http.StatusBadRequest)

	ctx, rec = ownerCall(t, http.MethodPut, "/", `{"tasks":[]} {"tasks":[]}`)
	assert.NilError(t, a.SetSyncTasks(ctx))
	assert.Equal(t, rec.Code, http.StatusBadRequest)

	ctx, rec = ownerCall(t, http.MethodPut, "/", `{}`)
	assert.NilError(t, a.SetSyncTasks(ctx))
	assert.Equal(t, rec.Code, http.StatusBadRequest)

	// a refusal of the box is a bad request that says why
	ctx, rec = ownerCall(t, http.MethodPut, "/", `{"tasks":[{"name":""}]}`)
	assert.NilError(t, a.SetSyncTasks(ctx))
	assert.Equal(t, rec.Code, http.StatusBadRequest)
	assert.Assert(t, strings.Contains(rec.Body.String(), "a sync task needs a name"), rec.Body.String())

	ctx, rec = ownerCall(t, http.MethodPut, "/", `{"tasks":[]}`)
	assert.NilError(t, a.SetSyncTasks(ctx))
	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Assert(t, strings.Contains(rec.Body.String(), `"data":[]`), rec.Body.String())
}

func TestRunNowAnswers(t *testing.T) {
	a := &AppManagement{}
	wasRun, wasFirst, wasConfirm := runSyncNow, runSyncFirstPass, confirmSyncDeletes
	t.Cleanup(func() { runSyncNow, runSyncFirstPass, confirmSyncDeletes = wasRun, wasFirst, wasConfirm })

	var err error
	runSyncNow = func(string) error { return err }
	runSyncFirstPass = func(string) error { return err }
	confirmSyncDeletes = func(string) error { return err }

	routes := map[string]func(echo.Context) error{
		"run":             func(c echo.Context) error { return a.RunSyncTask(c, "s1") },
		"first pass":      func(c echo.Context) error { return a.FirstPassSyncTask(c, "s1") },
		"confirm deletes": func(c echo.Context) error { return a.ConfirmSyncTaskDeletes(c, "s1") },
	}
	for name, route := range routes {
		for _, c := range []struct {
			err  error
			code int
		}{
			{nil, http.StatusAccepted},
			{service.ErrSyncNotFound, http.StatusNotFound},
			{service.ErrSyncRunning, http.StatusConflict},
			{service.ErrSyncUpgrade, http.StatusConflict},
			{service.ErrSyncWrongState, http.StatusConflict},
			{errors.New("disk on fire"), http.StatusInternalServerError},
		} {
			err = c.err
			ctx, rec := ownerCall(t, http.MethodPost, "/", "")
			assert.NilError(t, route(ctx), name)
			assert.Equal(t, rec.Code, c.code, "%s: %v", name, c.err)
			if c.code == http.StatusConflict {
				assert.Assert(t, strings.Contains(rec.Body.String(), c.err.Error()), rec.Body.String())
			}
		}
	}
}

func TestFoldersOfADestination(t *testing.T) {
	a := &AppManagement{}
	was := listFolders
	t.Cleanup(func() { listFolders = was })

	var gotFs, gotRemote string
	var fail error
	listFolders = func(fs, remote string) ([]string, error) {
		gotFs, gotRemote = fs, remote

		return []string{"a", "b"}, fail
	}

	path := "/Photos/2026/"
	ctx, rec := ownerCall(t, http.MethodGet, "/", "")
	assert.NilError(t, a.BackupDestinationFolders(ctx, "nas", codegen.BackupDestinationFoldersParams{Path: &path}))
	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, gotRemote, "Photos/2026")
	assert.Assert(t, strings.HasSuffix(gotFs, "nas:"), gotFs)
	assert.Assert(t, strings.Contains(rec.Body.String(), `"data":{"folders":["a","b"]}`), rec.Body.String())

	// the destination's root when no path is given
	ctx, rec = ownerCall(t, http.MethodGet, "/", "")
	assert.NilError(t, a.BackupDestinationFolders(ctx, "nas", codegen.BackupDestinationFoldersParams{}))
	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, gotRemote, "")

	gotRemote = "untouched"
	up := "../etc"
	ctx, rec = ownerCall(t, http.MethodGet, "/", "")
	assert.NilError(t, a.BackupDestinationFolders(ctx, "nas", codegen.BackupDestinationFoldersParams{Path: &up}))
	assert.Equal(t, rec.Code, http.StatusBadRequest)
	assert.Equal(t, gotRemote, "untouched")

	fail = errors.New("couldn't connect")
	ctx, rec = ownerCall(t, http.MethodGet, "/", "")
	assert.NilError(t, a.BackupDestinationFolders(ctx, "nas", codegen.BackupDestinationFoldersParams{}))
	assert.Equal(t, rec.Code, http.StatusBadRequest)
	assert.Assert(t, strings.Contains(rec.Body.String(), "couldn't connect"), rec.Body.String())
}

func TestADestinationInUseIsNotForgotten(t *testing.T) {
	a := &AppManagement{}

	syncTasksFile(t, service.SyncTask{ID: "s1", Name: "Photos", Destination: "nas"})
	ctx, rec := ownerCall(t, http.MethodDelete, "/", "")
	assert.NilError(t, a.DeleteBackupDestination(ctx, "nas"))
	assert.Equal(t, rec.Code, http.StatusBadRequest)
	assert.Assert(t, strings.Contains(rec.Body.String(), "used by the sync task Photos"), rec.Body.String())

	// a tasks file nobody can read: the destination is not deleted blindly
	assert.NilError(t, os.WriteFile(service.SyncTasksPath, []byte("not json"), 0o600))
	ctx, rec = ownerCall(t, http.MethodDelete, "/", "")
	assert.NilError(t, a.DeleteBackupDestination(ctx, "nas"))
	assert.Equal(t, rec.Code, http.StatusBadRequest)
}

func syncLive() service.LiveBackup {
	return service.LiveBackup{ID: "b9", App: "Photos", Destination: "nas", Kind: "sync", StartedAt: time.Now(), Source: "/DATA/Photos"}
}

func TestARunningSyncIsShownToTheOwnerOnly(t *testing.T) {
	lives := []service.LiveBackup{syncLive()}

	user := runningFor(visibilityCaller(t, "2"), lives, nil)
	assert.Equal(t, len(user.Backups), 0)

	owner := runningFor(ownerCaller(t), lives, nil)
	assert.Equal(t, len(owner.Backups), 1)
	assert.Equal(t, owner.Backups[0].Kind, "sync")
	assert.Equal(t, owner.Backups[0].App, "Photos")
	assert.Equal(t, owner.Backups[0].Source, "/DATA/Photos")

	// whatever the per-app filter says about a task that bears an app's name
	hidden := runningOut(func(string) bool { return false }, lives, nil)
	assert.Equal(t, len(hidden.Backups), 1)
	assert.Equal(t, hidden.Backups[0].Source, "/DATA/Photos")
}

func TestABackupStillSaysBackup(t *testing.T) {
	out := runningOut(func(string) bool { return true }, []service.LiveBackup{{ID: "b1", App: "jarvis", StartedAt: time.Now()}}, nil)
	assert.Equal(t, len(out.Backups), 1)
	assert.Equal(t, out.Backups[0].Kind, "backup")
}
