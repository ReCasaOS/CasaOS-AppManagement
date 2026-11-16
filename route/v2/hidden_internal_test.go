package v2

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/backupprogress"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

func ownerCaller(t *testing.T) echo.Context {
	return echo.New().NewContext(internalRequest(t, httptest.NewRequest(http.MethodGet, "/", nil)), httptest.NewRecorder())
}

// A pass of hidden apps alone is, for a user, no pass; a path naming a hidden app's folder is
// not shown, whatever backup copies it.
func TestEmptyListsDoNotRevealHiddenWork(t *testing.T) {
	visible := func(app string) bool { return app != "immich" }
	at := time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)
	lives := []service.LiveBackup{
		{ID: "b1", App: service.SystemBackupName, Source: "/DATA/AppData/immich", Stats: &backupprogress.Stats{Current: "/DATA/AppData/immich/db/pg.conf"}, StartedAt: at},
		{ID: "b2", App: "jarvis", Source: "/DATA/AppData/jarvis", Stats: &backupprogress.Stats{Current: "/DATA/AppData/jarvis/config"}, StartedAt: at},
		{ID: "b3", App: "immich", Source: "/DATA/AppData/immich", StartedAt: at},
	}
	passes := []service.BackupPass{
		{Origin: "schedule", StartedAt: at, Items: []service.BackupPassItem{{App: "immich", Destination: "nas"}}},
		{Origin: "schedule", StartedAt: at, Items: []service.BackupPassItem{{App: "immich", Destination: "nas"}, {App: "jarvis", Destination: "nas"}}},
		{Origin: "now", StartedAt: at, Items: []service.BackupPassItem{}},
	}

	got := runningOut(visible, lives, passes)
	assert.Equal(t, len(got.Backups), 2)
	assert.Equal(t, got.Backups[0].Source, "")
	assert.Assert(t, got.Backups[0].Stats.Current == nil)
	assert.Equal(t, got.Backups[1].Source, "/DATA/AppData/jarvis")
	assert.Equal(t, *got.Backups[1].Stats.Current, "/DATA/AppData/jarvis/config")
	// the pass of immich alone is gone; the mixed one keeps jarvis; an empty pass was empty already
	assert.Equal(t, len(got.Passes), 2)
	assert.Equal(t, len(got.Passes[0].Items), 1)
	assert.Equal(t, got.Passes[0].Items[0].App, "jarvis")
	assert.Equal(t, len(got.Passes[1].Items), 0)

	all := runningOut(func(string) bool { return true }, lives, passes)
	assert.Equal(t, len(all.Backups), 3)
	assert.Equal(t, len(all.Passes), 3)
	assert.Equal(t, all.Backups[0].Source, "/DATA/AppData/immich")
}

// The folders and files a backup reads are the owner's to see: a user gets the backup, its
// figures and its pass, with no path, whatever app it is.
func TestTheRunningBackupsKeepTheirPathsForTheOwner(t *testing.T) {
	at := time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)
	lives := []service.LiveBackup{
		{ID: "b1", App: "jarvis", Source: "/DATA/AppData/jarvis", Stats: &backupprogress.Stats{Current: "/DATA/AppData/jarvis/config", Bytes: 10}, StartedAt: at},
		{ID: "b2", App: service.SystemBackupName, Source: "/var/lib/casaos", Stats: &backupprogress.Stats{Current: "/var/lib/casaos/db/user.db"}, StartedAt: at},
	}

	owner := runningFor(ownerCaller(t), lives, nil)
	assert.Equal(t, owner.Backups[0].Source, "/DATA/AppData/jarvis")
	assert.Equal(t, *owner.Backups[0].Stats.Current, "/DATA/AppData/jarvis/config")
	assert.Equal(t, owner.Backups[1].Source, "/var/lib/casaos")

	user := runningFor(visibilityCaller(t, "2"), lives, nil)
	assert.Equal(t, len(user.Backups), 2)
	for _, backup := range user.Backups {
		assert.Equal(t, backup.Source, "", backup.Id)
		assert.Assert(t, backup.Stats.Current == nil, backup.Id)
	}
	assert.Equal(t, user.Backups[0].Stats.Bytes, int64(10))
}

// The engine's own error text can name the box's folders: a user sees that an item failed, not why.
func TestTheRunningPassesKeepTheirErrorsForTheOwner(t *testing.T) {
	at := time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)
	passes := []service.BackupPass{{Origin: "schedule", StartedAt: at, Items: []service.BackupPassItem{
		{App: "jarvis", Destination: "nas", State: "failed", Error: "open /DATA/AppData/jarvis/db: permission denied"},
	}}}

	owner := runningFor(ownerCaller(t), nil, passes)
	assert.Equal(t, *owner.Passes[0].Items[0].Error, "open /DATA/AppData/jarvis/db: permission denied")

	user := runningFor(visibilityCaller(t, "2"), nil, passes)
	assert.Assert(t, user.Passes[0].Items[0].Error == nil)
	assert.Equal(t, user.Passes[0].Items[0].State, "failed")
}

// The same for the finished runs: a user sees that a run failed, not the engine's words.
func TestTheRunsKeepTheirErrorsForTheOwner(t *testing.T) {
	records := []service.BackupRunRecord{
		{App: "jarvis", Destination: "nas", Error: "open /DATA/AppData/jarvis/db: permission denied"},
		{App: "jarvis", Destination: "nas"},
		{App: "jarvis", Destination: "nas", Error: "stopped from the interface: copying /DATA/AppData/jarvis/db: context canceled"},
	}

	owner := runsFor(ownerCaller(t), records)
	assert.Equal(t, *owner[0].Error, "open /DATA/AppData/jarvis/db: permission denied")

	user := runsFor(visibilityCaller(t, "2"), records)
	assert.Equal(t, len(user), 3)
	assert.Equal(t, *user[0].Error, "failed")
	assert.Equal(t, *user[1].Error, "", "a run that went well stays without error")
	assert.Equal(t, *user[2].Error, "stopped from the interface", "a stop keeps its word")
}

func TestGitWebhookSecretStaysWithTheOwner(t *testing.T) {
	answer := func(ctx echo.Context) string {
		view := &service.GitAppView{App: "jarvis", Webhook: service.GitWebhookView{Enabled: true, Secret: strings.Repeat("5e", 32)}}
		assert.NilError(t, gitAppAnswer(ctx, http.StatusOK, view, nil))
		return ctx.Response().Writer.(*httptest.ResponseRecorder).Body.String()
	}

	assert.Assert(t, strings.Contains(answer(ownerCaller(t)), strings.Repeat("5e", 32)))
	user := answer(visibilityCaller(t, "2"))
	assert.Assert(t, !strings.Contains(user, "5e5e") && strings.Contains(user, `"enabled":true`), user)
}

func TestRedactEnvKeepsTheNamesOnly(t *testing.T) {
	assert.Assert(t, !isOwner(visibilityCaller(t, "2")) && isOwner(ownerCaller(t)))
	user, owner := false, true
	secret := "hunter2"
	env := map[string]*string{"API_TOKEN": &secret, "EMPTY": nil}

	got := redactEnv(user, env)
	assert.Equal(t, len(got), 2)
	assert.Equal(t, *got["API_TOKEN"], "")
	assert.Equal(t, *got["EMPTY"], "")
	assert.Equal(t, *env["API_TOKEN"], "hunter2", "the map given is not changed")
	assert.Equal(t, *redactEnv(owner, env)["API_TOKEN"], "hunter2")

	assert.DeepEqual(t, RedactEnvList(user, []string{"A=1", "B=x=y", "C"}), []string{"A=", "B=", "C="})
	assert.DeepEqual(t, RedactEnvList(owner, []string{"A=1"}), []string{"A=1"})

	text := []byte("# the database\nDB_PASSWORD=s3cret\n\nexport TOKEN=abc\nKEY=\"-----BEGIN KEY-----\nAAAA==\nPART=two\n-----END KEY-----\"\n")
	assert.Equal(t, string(redactEnvText(user, text)), "DB_PASSWORD=\nKEY=\nTOKEN=\n")
	assert.Equal(t, string(redactEnvText(owner, text)), string(text))
	assert.Equal(t, string(redactEnvText(user, []byte("KEY=\"never closed\n"))), "", "text that does not parse gives nothing")
}

func TestRedactProjectEmptiesBuildArgumentsConfigsAndSecrets(t *testing.T) {
	secret := "hunter2"
	project := &types.Project{
		Services: types.Services{"web": {Name: "web", Build: &types.BuildConfig{Args: types.MappingWithEquals{"NPM_TOKEN": &secret}}}},
		Configs:  types.Configs{"app.conf": {Content: "password=hunter2"}},
		Secrets:  types.Secrets{"db": {Content: "hunter2", Environment: "DB_PASSWORD"}},
	}

	got := redactProject(false, project)
	assert.Equal(t, *got.Services["web"].Build.Args["NPM_TOKEN"], "")
	assert.Equal(t, got.Configs["app.conf"].Content, "")
	assert.Equal(t, got.Secrets["db"].Content, "")
	assert.Equal(t, got.Secrets["db"].Environment, "DB_PASSWORD", "the name of the variable is kept")

	// the project given is not changed, and the owner reads it whole
	assert.Equal(t, *project.Services["web"].Build.Args["NPM_TOKEN"], "hunter2")
	assert.Equal(t, project.Configs["app.conf"].Content, "password=hunter2")
	assert.Equal(t, project.Secrets["db"].Content, "hunter2")
	assert.Equal(t, redactProject(true, project), project)
}

// What no app owns, a one-off container known only from disk included, is the owner's alone.
func TestStatsHiddenKeepsWhatNoAppOwnsForTheOwner(t *testing.T) {
	visible := func(app string) bool { return app != "immich" }
	for id, want := range map[string]bool{
		"app:immich:db:1":               true,
		"app:jarvis:db:1":               false,
		"container:jarvis-web-run-ab12": true,
		"container:my.db_1":             true,
		"host":                          true,
	} {
		assert.Equal(t, statsHidden(false, visible, id, ""), want, id)
	}
	everything := func(string) bool { return true }
	for _, id := range []string{"app:immich:db:1", "container:my.db_1", "container:jarvis-web-run-ab12"} {
		assert.Assert(t, !statsHidden(true, everything, id, ""), id)
	}
	// the sampler's own app decides for a container it knows the app of
	assert.Assert(t, statsHidden(false, visible, "container:custom-name", "immich"))
	assert.Assert(t, !statsHidden(false, visible, "container:custom-name", "jarvis"))
}

// A name the services trim or read without case is hidden as the name itself is.
func TestAHiddenAppIsAskedAboutUnderTheProbeWhateverItsSpelling(t *testing.T) {
	user := visibilityCaller(t, "2")
	for name, want := range map[string]string{
		"immich": probeApp, "immich ": probeApp + " ", " immich": " " + probeApp, "\timmich": "\t" + probeApp,
		"Immich": probeApp, "IMMICH\n": probeApp + "\n",
	} {
		assert.Equal(t, askAs(user, name), want, "%q: the caller's whitespace stays around the probe", name)
	}
	for _, name := range []string{"jarvis", "Jarvis ", "immich-2"} {
		assert.Equal(t, askAs(user, name), name, "%q", name)
	}
	assert.Equal(t, askAs(ownerCaller(t), "immich"), "immich")
}

// nothingHeld stands in for rclone: a destination that holds nothing, answering as rclone does
// with the path it was asked for.
type nothingHeld struct{ service.BackupBrowser }

func (nothingHeld) Fetch(_ context.Context, _, remotePath string) ([]byte, error) {
	return nil, fmt.Errorf("object not found: %s", remotePath)
}

// The browse of a hidden app reaches the manifest read under the probe, and its answer, with
// whatever whitespace the name came with, is the one for an unknown name.
func TestBrowseOfAHiddenAppReadsAsAnUnknownOne(t *testing.T) {
	was := backupBrowser
	backupBrowser = func() service.BackupBrowser { return nothingHeld{} }
	t.Cleanup(func() { backupBrowser = was })

	browse := func(app string) (int, string) {
		ctx := visibilityCaller(t, "2")
		assert.NilError(t, (&AppManagement{}).BrowseBackup(ctx, "nas", app, "2026-11-01T10-00-00Z", codegen.BrowseBackupParams{}))
		recorder := ctx.Response().Writer.(*httptest.ResponseRecorder)
		return recorder.Code, recorder.Body.String()
	}
	for _, around := range [][2]string{{"", ""}, {"", " "}, {" ", ""}, {"\t", "  "}} {
		hiddenCode, hidden := browse(around[0] + "immich" + around[1])
		unknownCode, unknown := browse(around[0] + "nothing" + around[1])
		assert.Assert(t, strings.Contains(unknown, "manifest"), "the manifest is read: %s", unknown)
		assert.Equal(t, hiddenCode, unknownCode, "%q", around)
		assert.Equal(t, strings.ReplaceAll(hidden, "immich", "nothing"), unknown, "%q", around)
		assert.Assert(t, !strings.Contains(hidden, "probe-"), hidden)
	}
}
