package route

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/model"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/dockerstats"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/logstore"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/ReCasaOS/CasaOS-Common/utils/jwt"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/gorilla/websocket"
	"gotest.tools/v3/assert"
)

// Two projects, `jarvis` and `immich`, the second hidden from account 2 (a user). Account 1
// is the admin, account 3 another user the entry does not name.
const (
	shownApp  = "jarvis"
	hiddenApp = "immich"
	otherUser = 3
)

// readsServices is a box with two apps and no Docker daemon.
type readsServices struct {
	service.Services
	compose *service.ComposeService
	docker  service.DockerService
	store   *service.AppStoreManagement
}

func (s readsServices) Compose() *service.ComposeService                { return s.compose }
func (s readsServices) Docker() service.DockerService                   { return s.docker }
func (s readsServices) AppStoreManagement() *service.AppStoreManagement { return s.store }

type readsDocker struct {
	service.DockerService
	inspected map[string]*types.ContainerJSON
	loose     []model.MyAppList
}

// SampleContainerStats samples nothing: a host with a Docker daemon lists the app's containers (none), and the
// route then asks for their usage; without a daemon the route stops before.
func (d readsDocker) SampleContainerStats(context.Context, []string) map[string]*container.StatsResponse {
	return map[string]*container.StatsResponse{}
}

func (d readsDocker) GetContainerAppList(_, _, _ *string) (*[]model.MyAppList, *[]model.MyAppList) {
	v1, loose := []model.MyAppList{}, slices.Clone(d.loose)
	return &v1, &loose
}

func (d readsDocker) GetContainerByName(string) (*types.Container, error) {
	return &types.Container{}, nil
}

func (d readsDocker) CheckContainerHealth(id string) (bool, error) {
	if _, ok := d.inspected[id]; !ok {
		return false, noSuchContainer(id)
	}
	return true, nil
}

func (d readsDocker) VolumeUsage(context.Context) map[string]docker.VolumeSpace { return nil }

func (d readsDocker) DescribeContainer(_ context.Context, id string) (*types.ContainerJSON, error) {
	if inspected, ok := d.inspected[id]; ok {
		return inspected, nil
	}
	return nil, noSuchContainer(id)
}

// noSuchContainer is shaped like the docker client's own error for an unknown container.
func noSuchContainer(id string) error {
	return errdefs.NotFound(errors.New("Error response from daemon: No such container: " + id))
}

func (d readsDocker) GetNetworkList() []network.Summary {
	return []network.Summary{
		{Name: "bridge", Driver: "bridge", ID: "n1"},
		{Name: "jarvis_default", Driver: "bridge", ID: "n2", Labels: map[string]string{"com.docker.compose.project": shownApp}},
		{Name: "immich_default", Driver: "bridge", ID: "n3", Labels: map[string]string{"com.docker.compose.project": hiddenApp}},
	}
}

func (d readsDocker) GetContainerStats() []model.DockerStatsModel {
	return []model.DockerStatsModel{
		{Title: "jarvis-web-1", ComposeProject: shownApp},
		{Title: "immich-db-1", ComposeProject: hiddenApp},
		{Title: "plain"},
	}
}

func inspectedOf(id, project string, env ...string) *types.ContainerJSON {
	labels := map[string]string{}
	if project != "" {
		labels["com.docker.compose.project"] = project
	}
	return &types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{ID: id, Name: "/" + id, HostConfig: &container.HostConfig{}},
		Config:            &container.Config{Labels: labels, Env: env},
	}
}

type readsRig struct {
	*roleRig
	logs   *logstore.Store
	stats  *dockerstats.Sampler
	stacks []api.Stack
	root   string
}

// without answers as if app were not installed while f runs.
func (r *readsRig) without(app string, f func()) {
	all := r.stacks
	r.stacks = slices.DeleteFunc(slices.Clone(all), func(s api.Stack) bool { return s.ID == app })
	defer func() { r.stacks = all }()
	f()
}

func newReadsRig(t *testing.T) *readsRig {
	t.Helper()
	r := &readsRig{roleRig: newRoleRig(t), root: t.TempDir()}

	// the box's data (git apps, backups, image-update answers) in a folder of its own; the
	// visibility file stays the one newRoleRig chose
	visibility := service.VisibilityPath
	service.ApplyDataPath(r.root)
	service.VisibilityPath = visibility
	t.Cleanup(func() { service.ApplyDataPath("") })

	// the two compose projects, each installed from the store, whose catalogue has a newer image
	// of both: an update is on offer for each
	stores := t.TempDir()
	wasStores := config.AppInfo.AppStorePath
	config.AppInfo.AppStorePath = stores
	t.Cleanup(func() { config.AppInfo.AppStorePath = wasStores })
	compose := func(name, tag string) []byte {
		return []byte("name: " + name + "\nservices:\n  web:\n    image: nginx:" + tag + "\n    environment:\n      API_TOKEN: hunter2\n" +
			// every place compose fills from .env, with a value a user must not read
			"    command: [\"serve\", \"--db=${DB_PASSWORD}\"]\n    entrypoint: [\"/run\", \"${DB_PASSWORD}\"]\n" +
			"    healthcheck:\n      test: [\"CMD\", \"check\", \"${DB_PASSWORD}\"]\n    labels:\n      pass: ${DB_PASSWORD}\n" +
			"    logging:\n      driver: splunk\n      options:\n        splunk-token: ${LOG_TOKEN}\n" +
			"    volumes:\n      - data:/data\n    networks:\n      - net\n" +
			"volumes:\n  data:\n    driver_opts:\n      o: pass=${DB_PASSWORD}\n" +
			"networks:\n  net:\n    driver_opts:\n      secret: ${DB_PASSWORD}\n" +
			"x-casaos:\n  main: web\n  store_app_id: " + name + "\n  title:\n    en_us: " + name + "\n")
	}
	var stacks []api.Stack
	for _, name := range []string{shownApp, hiddenApp} {
		dir := filepath.Join(t.TempDir(), name)
		assert.NilError(t, os.MkdirAll(dir, 0o755))
		file := filepath.Join(dir, "docker-compose.yml")
		assert.NilError(t, os.WriteFile(file, compose(name, "1.27"), 0o600))
		assert.NilError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_PASSWORD=s3cret\nLOG_TOKEN=l0gt0ken\n"), 0o600))
		stacks = append(stacks, api.Stack{ID: name, ConfigFiles: file})

		entry := filepath.Join(stores, "default", "Apps", name)
		assert.NilError(t, os.MkdirAll(entry, 0o755))
		assert.NilError(t, os.WriteFile(filepath.Join(entry, "docker-compose.yml"), compose(name, "1.28"), 0o600))
	}
	r.stacks = stacks
	was := service.ListComposeStacks
	service.ListComposeStacks = func(context.Context) ([]api.Stack, error) { return r.stacks, nil }
	t.Cleanup(func() { service.ListComposeStacks = was })

	// containers: one per project, a loose one, and one of a project whose file does not load
	fake := readsDocker{
		inspected: map[string]*types.ContainerJSON{
			"c-shown": inspectedOf("c-shown", shownApp, "DB_PASSWORD=s3cret"), "c-hidden": inspectedOf("c-hidden", hiddenApp), "c-loose": inspectedOf("c-loose", ""),
		},
		loose: []model.MyAppList{
			{ID: "c-loose", Name: "loose"},
			{ID: "c-broken", Name: "broken", ComposeProject: hiddenApp},
		},
	}
	fake.inspected["c-shown"].Config.Labels["show_env"] = "DB_PASSWORD"
	fake.inspected["c-shown"].Config.Cmd = []string{"serve", "--token=t0ps3cret"}
	previous := service.MyService
	service.MyService = readsServices{compose: service.NewComposeService(), docker: fake, store: service.NewAppStoreManagement()}
	t.Cleanup(func() { service.MyService = previous })

	assert.NilError(t, service.AppVisibility.Hide(hiddenApp, []int{userID}))

	r.openLogs(t)
	r.openStats(t)

	return r
}

func (r *readsRig) openLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, err := logstore.Open(ctx, t.TempDir())
	assert.NilError(t, err)
	service.SetLogs(store)
	t.Cleanup(func() { service.SetLogs(nil); _ = store.Close(); cancel() })
	r.logs = store
	r.writeLogs(logsDay, "from jarvis", "from immich", "from other")
}

// writeLogs writes one line per app, `_other` standing for what no app owns, and an event for
// the first two.
func (r *readsRig) writeLogs(at time.Time, shown, hidden, other string) {
	for i, line := range []struct{ app, text, container string }{{shownApp, shown, "aaaaaaaaaaaa"}, {hiddenApp, hidden, "bbbbbbbbbbbb"}, {"_other", other, "cccccccccccc"}} {
		w := r.logs.Writer(line.app, "web", line.container)
		assert.NilError(r.t, w.Write(at.Add(time.Duration(i)*time.Millisecond), "o", line.text))
		assert.NilError(r.t, w.Flush())
	}
	for _, app := range []string{shownApp, hiddenApp} {
		assert.NilError(r.t, r.logs.Record(logstore.Event{At: at, App: app, Service: "web", Container: "dddddddddddd", Kind: "started", By: "docker"}))
	}
}

func (r *readsRig) openStats(t *testing.T) {
	src := &statsFake{ctrs: []*statsCtr{
		{ID: "d1", Name: "jarvis-web-1", Image: "nginx", Labels: map[string]string{"com.docker.compose.project": shownApp, "com.docker.compose.service": "web", "com.docker.compose.container-number": "1"}},
		{ID: "d2", Name: "immich-db-1", Image: "postgres", Labels: map[string]string{"com.docker.compose.project": hiddenApp, "com.docker.compose.service": "db", "com.docker.compose.container-number": "1"}},
		{ID: "d3", Name: "my.db_1", Image: "mariadb"},
		// `docker compose run` of the hidden app: a one-off, known by its name
		{ID: "d4", Name: "immich-web-run-ab12", Image: "nginx", Labels: map[string]string{"com.docker.compose.project": hiddenApp, "com.docker.compose.service": "web", "com.docker.compose.oneoff": "True"}},
	}}
	clock := stats.NewFakeClock(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC))
	s, err := dockerstats.New(dockerstats.Config{Dir: filepath.Join(t.TempDir(), "stats"), Source: src, Clock: clock, Interval: 5 * time.Second})
	assert.NilError(t, err)
	service.SetStats(s)
	t.Cleanup(func() { service.SetStats(nil); _ = s.Close() })
	for range 2 {
		s.Tick(context.Background())
		clock.Advance(5 * time.Second)
	}
	r.stats = s
}

// as sends a GET as the account: "admin", "user", "other" (user 3) or "internal".
func (r *readsRig) as(who, path string) *httptest.ResponseRecorder {
	token := map[string]string{"admin": r.admin, "user": r.user, "other": r.token(otherUser), "internal": "internal"}[who]
	return r.do(r.v2, http.MethodGet, "/v2/app_management"+path, token, "")
}

func (r *readsRig) token(id int) string {
	private, _ := testUserKey()
	token, err := jwt.GetAccessToken("someone", private, id)
	assert.NilError(r.t, err)
	return token
}

func bodyOf(r *httptest.ResponseRecorder) string { return r.Body.String() }

func TestAppGridOmitsHiddenApps(t *testing.T) {
	r := newReadsRig(t)

	for who, want := range map[string]bool{"admin": true, "internal": true, "other": true, "user": false} {
		got := r.as(who, "/web/appgrid")
		assert.Equal(t, got.Code, http.StatusOK, "%s: %s", who, bodyOf(got))
		assert.Assert(t, strings.Contains(bodyOf(got), `"`+shownApp+`"`), "%s sees the shown app: %s", who, bodyOf(got))
		assert.Equal(t, strings.Contains(bodyOf(got), hiddenApp), want, "%s: %s", who, bodyOf(got))
		// a container of the hidden project the compose list did not claim is hidden with it
		assert.Equal(t, strings.Contains(bodyOf(got), "c-broken"), want, "%s: %s", who, bodyOf(got))
		// a container no app owns is the owner's alone
		assert.Equal(t, strings.Contains(bodyOf(got), "c-loose"), who == "admin" || who == "internal", "%s: %s", who, bodyOf(got))
	}
}

func TestComposeListOmitsHiddenApps(t *testing.T) {
	r := newReadsRig(t)

	got := r.as("user", "/compose")
	assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
	var list struct{ Data map[string]json.RawMessage }
	assert.NilError(t, json.Unmarshal(got.Body.Bytes(), &list))
	assert.DeepEqual(t, sortedKeys(list.Data), []string{shownApp})

	got = r.as("admin", "/compose")
	assert.NilError(t, json.Unmarshal(got.Body.Bytes(), &list))
	assert.DeepEqual(t, sortedKeys(list.Data), []string{hiddenApp, shownApp})
}

func TestComposeRoutesOfAHiddenAppAre404(t *testing.T) {
	r := newReadsRig(t)

	for _, tail := range []string{"", "/containers", "/containers/stats", "/logs", "/env", "/healthcheck"} {
		got := r.as("user", "/compose/"+hiddenApp+tail)
		unknown := r.as("user", "/compose/nothing-here"+tail)
		assert.Equal(t, got.Code, http.StatusNotFound, "%s: %s", tail, bodyOf(got))
		assert.Equal(t, unknown.Code, http.StatusNotFound, "%s: %s", tail, bodyOf(unknown))
		// the same body, but for the name
		assert.Equal(t, strings.ReplaceAll(bodyOf(got), hiddenApp, "nothing-here"), bodyOf(unknown), tail)

		// the owner and an account the entry does not name get past the check
		for _, who := range []string{"admin", "internal", "other"} {
			assert.Assert(t, r.as(who, "/compose/"+hiddenApp+tail).Code != http.StatusNotFound, "%s %s", who, tail)
		}
		assert.Assert(t, r.as("user", "/compose/"+shownApp+tail).Code != http.StatusNotFound, "the shown app %s", tail)
	}
}

func TestContainerDetailOfAHiddenAppIs404(t *testing.T) {
	r := newReadsRig(t)

	got := r.as("user", "/container/c-hidden/detail")
	assert.Equal(t, got.Code, http.StatusNotFound, bodyOf(got))
	unknown := r.as("user", "/container/c-nothing/detail")
	assert.Equal(t, unknown.Code, http.StatusNotFound, bodyOf(unknown))
	assert.Equal(t, strings.ReplaceAll(bodyOf(got), "c-hidden", "c-nothing"), bodyOf(unknown))
	assert.Assert(t, !strings.Contains(bodyOf(got), hiddenApp), "the project is not named: %s", bodyOf(got))

	assert.Equal(t, r.as("user", "/container/c-shown/detail").Code, http.StatusOK)
	for _, who := range []string{"admin", "internal", "other"} {
		assert.Equal(t, r.as(who, "/container/c-hidden/detail").Code, http.StatusOK, who)
	}

	// a container no app owns is the owner's alone: any other account is answered as for one that does not exist
	for _, who := range []string{"user", "other"} {
		loose := r.as(who, "/container/c-loose/detail")
		assert.Equal(t, loose.Code, http.StatusNotFound, who)
		assert.Equal(t, strings.ReplaceAll(bodyOf(loose), "c-loose", "c-nothing"), bodyOf(unknown), who)
	}
	for _, who := range []string{"admin", "internal"} {
		assert.Equal(t, r.as(who, "/container/c-loose/detail").Code, http.StatusOK, who)
	}
}

func TestStatsOmitsAndRefusesHiddenAppsAndWhatNoAppOwns(t *testing.T) {
	r := newReadsRig(t)
	const hiddenID, shownID, plainID, oneOffID = "app:" + hiddenApp + ":db:1", "app:" + shownApp + ":web:1", "container:my.db_1", "container:immich-web-run-ab12"

	ids := func(who string) []string {
		got := r.as(who, "/stats")
		assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
		var body map[string]json.RawMessage
		assert.NilError(t, json.Unmarshal(got.Body.Bytes(), &body))
		var keys []string
		for id := range listItems(t, body) {
			keys = append(keys, id)
		}
		slices.Sort(keys)
		return keys
	}
	assert.DeepEqual(t, ids("user"), []string{shownID})
	assert.DeepEqual(t, ids("other"), []string{hiddenID, shownID, oneOffID})
	assert.DeepEqual(t, ids("admin"), []string{hiddenID, shownID, oneOffID, plainID})
	assert.DeepEqual(t, ids("internal"), []string{hiddenID, shownID, oneOffID, plainID})

	for _, tail := range []string{"", "/alerts"} {
		got := r.as("user", "/stats/"+hiddenID+tail)
		unknown := r.as("user", "/stats/app:nothing:db:1"+tail)
		assert.Equal(t, got.Code, http.StatusNotFound, "%s: %s", tail, bodyOf(got))
		assert.Equal(t, bodyOf(got), bodyOf(unknown), tail)
		// a container no app owns: the owner's alone
		for _, id := range []string{plainID, oneOffID} {
			loose := r.as("user", "/stats/"+id+tail)
			assert.Equal(t, loose.Code, http.StatusNotFound, id+tail)
			assert.Equal(t, bodyOf(loose), bodyOf(unknown), id+tail)
			assert.Equal(t, r.as("admin", "/stats/"+id+tail).Code, http.StatusOK, id+tail)
		}
		// an encoded colon is the same id
		assert.Equal(t, r.as("user", "/stats/"+strings.ReplaceAll(hiddenID, ":", "%3A")+tail).Code, http.StatusNotFound, tail)

		assert.Equal(t, r.as("admin", "/stats/"+hiddenID+tail).Code, http.StatusOK, tail)
		assert.Equal(t, r.as("user", "/stats/"+shownID+tail).Code, http.StatusOK, tail)
	}
}

func TestLogsAppsLogsStreamEventsExportOmitHiddenApps(t *testing.T) {
	r := newReadsRig(t)

	appsOf := func(who string) []string {
		got := r.as(who, "/logs/apps")
		assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
		t.Logf("%s: %s", who, bodyOf(got))
		var body struct{ Data []struct{ App string } }
		assert.NilError(t, json.Unmarshal(got.Body.Bytes(), &body))
		var apps []string
		for _, a := range body.Data {
			apps = append(apps, a.App)
		}
		slices.Sort(apps)
		return apps
	}
	// what no app owns is the owner's alone
	assert.DeepEqual(t, appsOf("user"), []string{shownApp})
	assert.DeepEqual(t, appsOf("other"), []string{hiddenApp, shownApp})
	assert.DeepEqual(t, appsOf("admin"), []string{"_other", hiddenApp, shownApp})

	// history, also when the hidden app is asked for by name, and the events of its window
	for _, query := range []string{"/logs", "/logs?app=" + hiddenApp, "/logs?app=" + hiddenApp + "&app=" + shownApp, "/logs?app=_other"} {
		got := r.as("user", query)
		assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
		assert.Assert(t, !strings.Contains(bodyOf(got), "from immich") && !strings.Contains(bodyOf(got), `"`+hiddenApp+`"`), "%s: %s", query, bodyOf(got))
		assert.Assert(t, !strings.Contains(bodyOf(got), "from other"), "%s: %s", query, bodyOf(got))
	}
	got := r.as("user", "/logs")
	assert.Assert(t, strings.Contains(bodyOf(got), "from jarvis"), bodyOf(got))
	assert.Assert(t, strings.Contains(bodyOf(r.as("admin", "/logs")), "from other"))
	assert.Assert(t, strings.Contains(bodyOf(r.as("admin", "/logs")), "from immich"))
	assert.Assert(t, strings.Contains(bodyOf(r.as("other", "/logs")), "from immich"))

	got = r.as("user", "/logs/events")
	assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
	assert.Assert(t, !strings.Contains(bodyOf(got), hiddenApp) && strings.Contains(bodyOf(got), shownApp), bodyOf(got))
	got = r.as("user", "/logs/events?app="+hiddenApp)
	assert.Assert(t, !strings.Contains(bodyOf(got), hiddenApp), bodyOf(got))
	assert.Assert(t, strings.Contains(bodyOf(r.as("admin", "/logs/events")), hiddenApp))

	got = r.as("user", "/logs/export")
	assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
	assert.Assert(t, !strings.Contains(bodyOf(got), "from immich") && !strings.Contains(bodyOf(got), "from other") && strings.Contains(bodyOf(got), "from jarvis"), bodyOf(got))
	got = r.as("user", "/logs/export?app="+hiddenApp)
	assert.Assert(t, !strings.Contains(bodyOf(got), "from immich"), bodyOf(got))
	assert.Assert(t, strings.Contains(bodyOf(r.as("admin", "/logs/export")), "from immich"))
}

// The settings of the logs are read by every account; where the logs are kept is the owner's alone.
func TestTheLogsSettingsGiveTheirFolderToTheOwnerAlone(t *testing.T) {
	r := newReadsRig(t)

	pathOf := func(who string) any {
		got := r.as(who, "/logs/settings")
		assert.Equal(t, got.Code, http.StatusOK, "%s: %s", who, bodyOf(got))
		var body struct{ Data map[string]any }
		assert.NilError(t, json.Unmarshal(got.Body.Bytes(), &body))
		_, kept := body.Data["retention_days"]
		assert.Assert(t, kept, "%s: %s", who, bodyOf(got))

		return body.Data["path"]
	}
	owner := pathOf("admin")
	assert.Assert(t, owner != "", "the owner reads the folder")
	assert.Equal(t, pathOf("internal"), owner)
	for _, who := range []string{"user", "other"} {
		assert.Equal(t, pathOf(who), "", who)
	}
}

func TestTheLogStreamOmitsHiddenApps(t *testing.T) {
	r := newReadsRig(t)
	server := httptest.NewServer(r.v2)
	t.Cleanup(server.Close)

	dial := func(token string) *websocket.Conn {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v2/app_management/logs/stream?token="+token, nil)
		assert.NilError(t, err)
		t.Cleanup(func() { conn.Close() })
		assert.NilError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		return conn
	}
	user, admin := dial(r.user), dial(r.admin)

	// the hidden app writes first: if the stream let it through, it would be read first
	r.writeLogs(logsDay.Add(time.Hour), "second jarvis", "second immich", "second other")

	read := func(conn *websocket.Conn, n int) []string {
		var got []string
		for len(got) < n {
			var m logstore.Message
			assert.NilError(t, conn.ReadJSON(&m))
			if m.Line != nil {
				got = append(got, m.Line.Text)
			}
		}
		slices.Sort(got)
		return got
	}
	assert.DeepEqual(t, read(user, 1), []string{"second jarvis"})
	assert.DeepEqual(t, read(admin, 3), []string{"second immich", "second jarvis", "second other"})
}

func TestOperationsOmitHiddenApps(t *testing.T) {
	r := newReadsRig(t)
	for _, app := range []string{shownApp, hiddenApp} {
		end, err := service.Begin(app, "deploy")
		assert.NilError(t, err)
		t.Cleanup(end)
	}

	got := r.as("user", "/operations")
	assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
	assert.Assert(t, strings.Contains(bodyOf(got), shownApp) && !strings.Contains(bodyOf(got), hiddenApp), bodyOf(got))
	for _, who := range []string{"admin", "internal", "other"} {
		assert.Assert(t, strings.Contains(bodyOf(r.as(who, "/operations")), hiddenApp), who)
	}
}

// Readers beyond the brief's list that name an app or read a container by its id.
func TestBackupListsOmitHiddenApps(t *testing.T) {
	r := newReadsRig(t)
	dir := t.TempDir()
	was, wasLog := service.BackupSchedulePath, service.BackupLogPath
	service.BackupSchedulePath, service.BackupLogPath = filepath.Join(dir, "schedules.json"), filepath.Join(dir, "runs.json")
	t.Cleanup(func() { service.BackupSchedulePath, service.BackupLogPath = was, wasLog })
	var schedules, runs []string
	for _, app := range []string{shownApp, hiddenApp} {
		schedules = append(schedules, `{"app":"`+app+`","destination":"offsite","every":"daily","at":"03:00","enabled":true}`)
		runs = append(runs, `{"app":"`+app+`","destination":"offsite","stamp":"20261101T100000Z","started_at":"2026-11-01T10:00:00Z","finished_at":"2026-11-01T10:01:00Z"}`)
	}
	assert.NilError(t, os.WriteFile(service.BackupSchedulePath, []byte("["+strings.Join(schedules, ",")+"]"), 0o600))
	assert.NilError(t, os.WriteFile(service.BackupLogPath, []byte("["+strings.Join(runs, ",")+"]"), 0o600))

	for _, path := range []string{"/backup/schedules", "/backup/runs"} {
		got := r.as("user", path)
		assert.Equal(t, got.Code, http.StatusOK, "%s: %s", path, bodyOf(got))
		assert.Assert(t, strings.Contains(bodyOf(got), shownApp) && !strings.Contains(bodyOf(got), hiddenApp), "%s: %s", path, bodyOf(got))
		for _, who := range []string{"admin", "internal", "other"} {
			assert.Assert(t, strings.Contains(bodyOf(r.as(who, path)), hiddenApp), "%s %s", who, path)
		}
	}
}

func TestAContainerOfAHiddenAppIsNotReadByItsID(t *testing.T) {
	r := newReadsRig(t)

	assert.Equal(t, r.as("user", "/container/c-shown/healthcheck").Code, http.StatusOK)

	// every read answers, status and body, as for a container that does not exist
	for _, c := range []struct {
		handler http.Handler
		path    string
	}{
		{r.v2, "/v2/app_management/container/%s/healthcheck"},
		{r.v2, "/v2/app_management/container/%s/detail"},
		{r.v1, "/v1/container/%s"},
		{r.v1, "/v1/container/%s/compose"},
	} {
		hidden := r.do(c.handler, http.MethodGet, strings.Replace(c.path, "%s", "c-hidden", 1), r.user, "")
		unknown := r.do(c.handler, http.MethodGet, strings.Replace(c.path, "%s", "c-nothing", 1), r.user, "")
		assert.Equal(t, hidden.Code, unknown.Code, c.path)
		assert.Equal(t, strings.ReplaceAll(bodyOf(hidden), "c-hidden", "c-nothing"), bodyOf(unknown), c.path)
		assert.Assert(t, !strings.Contains(bodyOf(hidden), hiddenApp), "%s: %s", c.path, bodyOf(hidden))

		for _, token := range []string{r.admin, "internal", r.token(otherUser)} {
			got := r.do(c.handler, http.MethodGet, strings.Replace(c.path, "%s", "c-hidden", 1), token, "")
			assert.Assert(t, !strings.Contains(bodyOf(got), "No such container"), "%.8s %s: %s", token, c.path, bodyOf(got))
		}
	}
}

func TestAdminAndInternalSeeEverything(t *testing.T) {
	r := newReadsRig(t)

	for _, who := range []string{"admin", "internal"} {
		for _, path := range []string{"/web/appgrid", "/compose", "/compose/" + hiddenApp, "/container/c-hidden/detail", "/stats/app:" + hiddenApp + ":db:1", "/logs/events"} {
			got := r.as(who, path)
			assert.Assert(t, got.Code != http.StatusNotFound && got.Code != http.StatusForbidden, "%s %s: %d", who, path, got.Code)
		}
	}
}

func TestAnIdBelowOneIsABadRequest(t *testing.T) {
	r := newRoleRig(t)

	for _, body := range []string{`{"hidden_for":[0]}`, `{"hidden_for":[2,-1]}`} {
		got := r.do(r.v2, http.MethodPut, "/v2/app_management/visibility/jellyfin", r.admin, body)
		assert.Equal(t, got.Code, http.StatusBadRequest, "%s: %s", body, got.Body.String())
	}
	assert.Assert(t, !service.AppVisibility.HiddenFor("jellyfin", 2), "a refused list changed nothing")
}

func TestTheUpdateRunNamesNoHiddenApp(t *testing.T) {
	r := newReadsRig(t)
	_, err := service.StartUpdateAll([]string{shownApp, hiddenApp}, func(context.Context, string) (codegen.ComposeAppUpdateRunAppState, string) {
		return codegen.ComposeAppUpdateRunAppStateUpdated, "done"
	})
	assert.NilError(t, err)
	awaitUpdateRun(t)

	got := r.as("user", "/compose/updates/run")
	assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
	assert.Assert(t, strings.Contains(bodyOf(got), shownApp) && !strings.Contains(bodyOf(got), hiddenApp), bodyOf(got))
	for _, who := range []string{"admin", "internal", "other"} {
		assert.Assert(t, strings.Contains(bodyOf(r.as(who, "/compose/updates/run")), hiddenApp), who)
	}
}

func TestContainerUsageOmitsContainersOfHiddenApps(t *testing.T) {
	r := newReadsRig(t)
	get := func(token string) string {
		got := r.do(r.v1, http.MethodGet, "/v1/container/usage", token, "")
		assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
		return bodyOf(got)
	}

	user := get(r.user)
	// a container no app owns is the owner's alone
	assert.Assert(t, strings.Contains(user, "jarvis-web-1") && !strings.Contains(user, "plain") && !strings.Contains(user, "immich"), user)
	for _, token := range []string{r.admin, "internal", r.token(otherUser)} {
		assert.Assert(t, strings.Contains(get(token), "immich-db-1"), "%.8s", token)
	}
	for _, token := range []string{r.admin, "internal"} {
		assert.Assert(t, strings.Contains(get(token), "plain"), "%.8s", token)
	}
}

// What a user asks for by app name, or that names an app, leaves a hidden one out.
func TestOtherAppNamedReadsOmitHiddenApps(t *testing.T) {
	r := newReadsRig(t)
	dir := t.TempDir()
	was := service.BackupVerificationLogPath
	service.BackupVerificationLogPath = filepath.Join(dir, "verifications.json")
	t.Cleanup(func() { service.BackupVerificationLogPath = was })
	var records []string
	for _, app := range []string{shownApp, hiddenApp} {
		records = append(records, `{"id":"v-`+app+`","destination":"offsite","app":"`+app+`","stamp":"s","status":"done","started_at":"2026-11-01T10:00:00Z"}`)
	}
	assert.NilError(t, os.WriteFile(service.BackupVerificationLogPath, []byte("["+strings.Join(records, ",")+"]"), 0o600))

	// both apps have an update on offer: the user's lists are the lists of a box without the
	// hidden app
	for _, path := range []string{"/apps/upgradable", "/compose/updates"} {
		assert.Assert(t, strings.Contains(bodyOf(r.as("admin", path)), hiddenApp), "the owner is offered the update: %s", path)
		got := r.as("user", path)
		assert.Equal(t, got.Code, http.StatusOK, "%s: %s", path, bodyOf(got))
		assert.Assert(t, strings.Contains(bodyOf(got), shownApp), "%s: %s", path, bodyOf(got))
		r.without(hiddenApp, func() {
			gone := r.as("user", path)
			assert.Equal(t, gone.Code, got.Code, path)
			assert.Equal(t, bodyOf(gone), bodyOf(got), path)
		})
	}

	got := r.as("user", "/backup/verifications")
	assert.Assert(t, strings.Contains(bodyOf(got), shownApp) && !strings.Contains(bodyOf(got), hiddenApp), bodyOf(got))
	assert.Assert(t, strings.Contains(bodyOf(r.as("admin", "/backup/verifications")), hiddenApp))

	// a hidden app's backup and git view answer as an app's that does not exist
	for _, path := range []string{"/backup/destinations/nas/runs/%s/2026-11-01T10-00-00Z/files", "/git/%s", "/git/%s/tags"} {
		hidden := r.as("user", strings.Replace(path, "%s", hiddenApp, 1))
		unknown := r.as("user", strings.Replace(path, "%s", "nothing-here", 1))
		assert.Equal(t, hidden.Code, unknown.Code, path)
		assert.Equal(t, strings.ReplaceAll(bodyOf(hidden), hiddenApp, "nothing-here"), bodyOf(unknown), path)
	}
}

// flakyDocker fails the next inspect with an error that is not "no such container", as a
// daemon that is busy or restarting does, then answers as before.
type flakyDocker struct {
	readsDocker
	failNext *bool
}

func (d flakyDocker) DescribeContainer(ctx context.Context, id string) (*types.ContainerJSON, error) {
	if *d.failNext {
		*d.failNext = false
		return nil, errors.New("Cannot connect to the Docker daemon: connection reset")
	}
	return d.readsDocker.DescribeContainer(ctx, id)
}

// When the daemon fails the check of whose container it is, a caller that is not the owner is
// answered as for a container that does not exist.
func TestAContainerIsNotReadWhenItsAppCannotBeChecked(t *testing.T) {
	r := newReadsRig(t)
	failNext := false
	services := service.MyService.(readsServices)
	services.docker = flakyDocker{readsDocker: services.docker.(readsDocker), failNext: &failNext}
	service.MyService = services

	for _, c := range []struct {
		handler http.Handler
		path    string
	}{
		{r.v2, "/v2/app_management/container/%s/detail"},
		{r.v1, "/v1/container/%s"},
		{r.v1, "/v1/container/%s/compose"},
	} {
		failNext = true
		hidden := r.do(c.handler, http.MethodGet, strings.Replace(c.path, "%s", "c-hidden", 1), r.user, "")
		unknown := r.do(c.handler, http.MethodGet, strings.Replace(c.path, "%s", "c-nothing", 1), r.user, "")
		assert.Equal(t, hidden.Code, unknown.Code, c.path)
		assert.Assert(t, !strings.Contains(bodyOf(hidden), hiddenApp), "%s: %s", c.path, bodyOf(hidden))

		failNext = true
		got := r.do(c.handler, http.MethodGet, strings.Replace(c.path, "%s", "c-hidden", 1), r.admin, "")
		assert.Assert(t, !strings.Contains(bodyOf(got), "No such container"), "the owner is not asked about: %s %s", c.path, bodyOf(got))
		// with the daemon answering, the owner reads the container the user was refused
		failNext = false
		got = r.do(c.handler, http.MethodGet, strings.Replace(c.path, "%s", "c-hidden", 1), r.admin, "")
		assert.Assert(t, got.Code != hidden.Code, "%s: owner %d, user %d", c.path, got.Code, hidden.Code)
	}
}

// awaitUpdateRun waits, a few seconds at most, for the update run to finish, and forgets it
// when the test ends so that the next test starts with no run.
func awaitUpdateRun(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for service.CurrentUpdateAllRun().FinishedAt == nil {
		if time.Now().After(deadline) {
			t.Fatal("the update run did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { assert.Assert(t, service.ForgetFinishedUpdateAllRun()) })
}
