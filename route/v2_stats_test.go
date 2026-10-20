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
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/dockerstats"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/docker/api/types/container"
	"gotest.tools/v3/assert"
)

// statsCtr is one scripted container of statsFake.
type statsCtr struct {
	ID, Name, Image string
	State           string // "running" when empty
	Labels          map[string]string
	User            uint64 // cumulative CPU ns
	Memory          int64  // declared memory limit, 0 for none
	NanoCPUs        int64  // declared CPU limit, 0 for none
}

// statsFake is a Docker that answers from a script.
type statsFake struct {
	mu      sync.Mutex
	ctrs    []*statsCtr
	listErr error
}

func (f *statsFake) List(context.Context) ([]container.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []container.Summary
	for _, c := range f.ctrs {
		state, status := c.State, "Exited (0) 1 hour ago"
		if state == "" {
			state, status = "running", "Up 1 hour"
		}
		out = append(out, container.Summary{ID: c.ID, Names: []string{"/" + c.Name}, Labels: c.Labels, State: container.ContainerState(state), Status: status})
	}
	return out, nil
}

func (f *statsFake) Inspect(_ context.Context, id string) (container.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.ctrs {
		if c.ID == id {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					ID: c.ID, State: &container.State{StartedAt: "2026-10-20T09:00:00Z"},
					HostConfig: &container.HostConfig{Resources: container.Resources{Memory: c.Memory, NanoCPUs: c.NanoCPUs}},
				},
				Config: &container.Config{Image: c.Image},
			}, nil
		}
	}
	return container.InspectResponse{}, errors.New("no such container")
}

func (f *statsFake) Stats(_ context.Context, ids []string) map[string]*container.StatsResponse {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]*container.StatsResponse{}
	for _, c := range f.ctrs {
		if !slices.Contains(ids, c.ID) {
			continue
		}
		c.User += uint64(time.Second) // a fifth of a core over 5 s
		s := &container.StatsResponse{}
		s.CPUStats.CPUUsage.TotalUsage = c.User
		s.CPUStats.CPUUsage.UsageInUsermode = c.User
		s.CPUStats.OnlineCPUs = 4
		s.MemoryStats.Usage = 100 << 20
		s.MemoryStats.Limit = 8 << 30
		if c.Memory > 0 {
			s.MemoryStats.Limit = uint64(c.Memory)
		}
		s.MemoryStats.Stats = map[string]uint64{"anon": 100 << 20}
		out[c.ID] = s
	}
	return out
}

type statsRig struct {
	t      *testing.T
	router http.Handler
	src    *statsFake
	clock  *stats.FakeClock
	s      *dockerstats.Sampler
	dir    string

	mu     sync.Mutex
	events []stats.Event
}

func newStatsRig(t *testing.T) *statsRig {
	t.Helper()
	logger.LogInitConsoleOnly()
	runtime := t.TempDir()
	previous := config.CommonInfo.RuntimePath
	config.CommonInfo.RuntimePath = runtime
	t.Cleanup(func() { config.CommonInfo.RuntimePath = previous })
	assert.NilError(t, os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret"), 0o600))

	src := &statsFake{ctrs: []*statsCtr{
		{ID: "d1", Name: "jarvis-web-1", Image: "nginx:1.27", Labels: map[string]string{
			"com.docker.compose.project": "jarvis", "com.docker.compose.service": "web", "com.docker.compose.container-number": "1",
		}, Memory: 256 << 20, NanoCPUs: 1.5e9},
		{ID: "d2", Name: "my.db_1", Image: "<img src=x onerror=1>"},
		{ID: "d3", Name: "<img src=x onerror=1>"},
	}}
	r := &statsRig{t: t, router: InitV2Router(), src: src, dir: filepath.Join(t.TempDir(), "stats"),
		clock: stats.NewFakeClock(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC))}
	r.open()
	t.Cleanup(func() { service.SetStats(nil); _ = r.s.Close() })
	return r
}

// open starts a sampler on the rig's directory, in place of the previous one.
func (r *statsRig) open() {
	r.t.Helper()
	s, err := dockerstats.New(dockerstats.Config{Dir: r.dir, Source: r.src, Clock: r.clock, Interval: 5 * time.Second,
		Emit: func(e stats.Event, _ dockerstats.Identity, _ int) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.events = append(r.events, e)
		}})
	assert.NilError(r.t, err)
	r.s = s
	service.SetStats(s)
}

func (r *statsRig) ticks(n int) {
	for range n {
		r.s.Tick(context.Background())
		r.clock.Advance(5 * time.Second)
	}
}

func (r *statsRig) do(method, path string) *httptest.ResponseRecorder {
	r.t.Helper()
	return r.send(method, path, "")
}

func (r *statsRig) send(method, path, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	request := httptest.NewRequest(method, "/v2/app_management"+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "127.0.0.1:40000"
	request.Header.Set("Authorization", "Internal s3cret")
	recorder := httptest.NewRecorder()
	r.router.ServeHTTP(recorder, request)
	return recorder
}

func (r *statsRig) get(path string, status int) map[string]json.RawMessage {
	r.t.Helper()
	recorder := r.do(http.MethodGet, path)
	assert.Equal(r.t, recorder.Code, status, recorder.Body.String())
	var body map[string]json.RawMessage
	assert.NilError(r.t, json.Unmarshal(recorder.Body.Bytes(), &body), recorder.Body.String())
	return body
}

func sortedKeys(m map[string]json.RawMessage) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func listItems(t *testing.T, body map[string]json.RawMessage) map[string]map[string]json.RawMessage {
	t.Helper()
	var items []map[string]json.RawMessage
	assert.NilError(t, json.Unmarshal(body["items"], &items))
	out := map[string]map[string]json.RawMessage{}
	for _, it := range items {
		var id string
		assert.NilError(t, json.Unmarshal(it["id"], &id))
		out[id] = it
	}
	return out
}

func TestTheStatsListHasTheContractShape(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)

	body := r.get("/stats", http.StatusOK)
	assert.DeepEqual(t, sortedKeys(body), []string{"items", "notes"})
	assert.Equal(t, string(body["notes"]), "[]")
	items := listItems(t, body)

	web := items["app:jarvis:web:1"]
	assert.DeepEqual(t, sortedKeys(web), []string{"alerts", "app", "cpu_percent", "detail", "group", "id", "kind", "memory_limit", "memory_used", "name", "started_at", "state"})
	assert.Equal(t, string(web["app"]), `"jarvis"`)
	assert.Equal(t, string(web["cpu_percent"]), "null", "one sample gives no CPU rate")
	assert.Equal(t, string(web["memory_used"]), "104857600")
	assert.Equal(t, string(web["memory_limit"]), "268435456")
	assert.Equal(t, string(web["started_at"]), `"2026-10-20T09:00:00Z"`)
	assert.Equal(t, string(web["detail"]), `"nginx:1.27"`)

	db := items["container:my.db_1"]
	assert.DeepEqual(t, sortedKeys(db), []string{"alerts", "cpu_percent", "detail", "group", "id", "kind", "memory_limit", "memory_used", "name", "started_at", "state"})
	assert.Equal(t, string(db["memory_limit"]), "null", "no declared limit")

	r.ticks(1)
	web = listItems(t, r.get("/stats", http.StatusOK))["app:jarvis:web:1"]
	var cpu float64
	assert.NilError(t, json.Unmarshal(web["cpu_percent"], &cpu))
	assert.Assert(t, cpu > 0)
}

func TestTheStatsRoutesNeedTheToken(t *testing.T) {
	r := newStatsRig(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/stats"},
		{http.MethodGet, "/stats/container:my.db_1"},
		{http.MethodGet, "/stats/container:my.db_1/alerts"},
		{http.MethodPut, "/stats/container:my.db_1/alerts"},
	} {
		request := httptest.NewRequest(route.method, "/v2/app_management"+route.path, strings.NewReader(`{"rules":[]}`))
		request.RemoteAddr = "203.0.113.5:40000"
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		r.router.ServeHTTP(recorder, request)
		assert.Equal(t, recorder.Code, http.StatusUnauthorized, "%s %s", route.method, route.path)
	}
}

func TestTheIdArrivesDecodedWhetherOrNotItsColonsAreEncoded(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	for path, id := range map[string]string{
		"/stats/app:jarvis:web:1":       "app:jarvis:web:1",
		"/stats/app%3Ajarvis%3Aweb%3A1": "app:jarvis:web:1",
		"/stats/app%3ajarvis%3aweb%3a1": "app:jarvis:web:1",
		"/stats/container:my.db_1":      "container:my.db_1",
		"/stats/container%3Amy.db_1":    "container:my.db_1",
	} {
		assert.Equal(t, string(r.get(path, http.StatusOK)["id"]), `"`+id+`"`, path)
	}
}

func TestTheSeriesAnswerHasTheContractShape(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(40) // 10:00:00 to 10:03:15

	body := r.get("/stats/app:jarvis:web:1?range=15m", http.StatusOK)
	assert.DeepEqual(t, sortedKeys(body), []string{"from", "id", "notes", "range", "scale", "series", "step_seconds", "to"})
	assert.Equal(t, string(body["id"]), `"app:jarvis:web:1"`)
	assert.Equal(t, string(body["range"]), `"15m"`)
	assert.Equal(t, string(body["step_seconds"]), "60")
	var from, to int64
	assert.NilError(t, json.Unmarshal(body["from"], &from))
	assert.NilError(t, json.Unmarshal(body["to"], &to))
	assert.Assert(t, from > 0 && to > from)

	var scale map[string]json.RawMessage
	assert.NilError(t, json.Unmarshal(body["scale"], &scale))
	assert.DeepEqual(t, sortedKeys(scale), []string{"cpu", "cpu_allocation", "memory_limit"})
	assert.Equal(t, string(scale["cpu"]), `"percent_of_one_core"`)
	assert.Equal(t, string(scale["cpu_allocation"]), "1.5")
	assert.Equal(t, string(scale["memory_limit"]), "268435456")
	assert.Equal(t, string(body["notes"]), "[]")

	// without declared limits: the machine's cores, no memory limit, both notes
	db := r.get("/stats/container:my.db_1?range=15m", http.StatusOK)
	assert.Equal(t, string(db["scale"]), `{"cpu":"percent_of_one_core","cpu_allocation":4,"memory_limit":null}`)
	var notes []string
	assert.NilError(t, json.Unmarshal(db["notes"], &notes))
	assert.Assert(t, slices.Contains(notes, "no_cpu_limit") && slices.Contains(notes, "no_memory_limit"), "%v", notes)

	var series []map[string]json.RawMessage
	assert.NilError(t, json.Unmarshal(body["series"], &series))
	assert.Assert(t, len(series) > 0)
	gap, full := false, false
	for _, sd := range series {
		assert.DeepEqual(t, sortedKeys(sd), []string{"chart", "name", "points", "unit"})
		if string(sd["chart"]) != `"cpu"` || string(sd["name"]) != `"user"` {
			continue
		}
		var points [][]*float64
		assert.NilError(t, json.Unmarshal(sd["points"], &points))
		for _, p := range points {
			assert.Equal(t, len(p), 4)
			assert.Assert(t, p[0] != nil)
			gap = gap || (p[1] == nil && p[2] == nil && p[3] == nil)
			full = full || (p[1] != nil && p[2] != nil && p[3] != nil)
		}
	}
	assert.Assert(t, gap && full, "cpu/user wants a gap and a full point")
}

func TestAlertSeriesAreNotInTheAnswer(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(40)
	for _, rng := range []string{"live", "15m"} {
		var series []struct{ Chart string }
		assert.NilError(t, json.Unmarshal(r.get("/stats/app:jarvis:web:1?range="+rng, http.StatusOK)["series"], &series))
		assert.Assert(t, len(series) > 0)
		for _, sd := range series {
			assert.Assert(t, sd.Chart != "alert")
		}
	}
}

func TestRangeDefaultsToLiveAndABadOneIsRefused(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	assert.Equal(t, string(r.get("/stats/container:my.db_1", http.StatusOK)["range"]), `"live"`)
	for _, q := range []string{"?range=2h", "?range=", "?range=%00%3Cjunk%3E", "?range=LIVE", "?range=" + strings.Repeat("x", 10000)} {
		recorder := r.do(http.MethodGet, "/stats/container:my.db_1"+q)
		assert.Equal(t, recorder.Code, http.StatusBadRequest, q)
		assert.Equal(t, strings.TrimSpace(recorder.Body.String()), `{"message":"bad_range"}`, q)
	}
	// a range is checked before the item is looked up
	assert.Equal(t, r.do(http.MethodGet, "/stats/container:nope?range=2h").Code, http.StatusBadRequest)
}

func TestAnUnknownItemIs404WithACode(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	recorder := r.do(http.MethodGet, "/stats/container:nope")
	assert.Equal(t, recorder.Code, http.StatusNotFound)
	assert.Equal(t, strings.TrimSpace(recorder.Body.String()), `{"message":"unknown_item"}`)
}

func TestWithoutASamplerEveryStatsRouteIs503(t *testing.T) {
	r := newStatsRig(t)
	service.SetStats(nil)
	for _, path := range []string{"/stats", "/stats/container:my.db_1", "/stats/container:my.db_1?range=2h", "/stats/container:my.db_1/alerts"} {
		recorder := r.do(http.MethodGet, path)
		assert.Equal(t, recorder.Code, http.StatusServiceUnavailable, path)
		assert.Equal(t, strings.TrimSpace(recorder.Body.String()), `{"message":"stats_unavailable"}`, path)
	}
	recorder := r.send(http.MethodPut, "/stats/container:my.db_1/alerts", `{"rules":[]}`)
	assert.Equal(t, recorder.Code, http.StatusServiceUnavailable)
	assert.Equal(t, strings.TrimSpace(recorder.Body.String()), `{"message":"stats_unavailable"}`)
}

func TestWhenDockerIsDownTheListIs503ButHistoryIsStillServed(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(3)
	r.src.mu.Lock()
	r.src.listErr = errors.New("dial unix /var/run/docker.sock: connect: connection refused")
	r.src.mu.Unlock()
	r.ticks(1)

	recorder := r.do(http.MethodGet, "/stats")
	assert.Equal(t, recorder.Code, http.StatusServiceUnavailable)
	assert.Equal(t, strings.TrimSpace(recorder.Body.String()), `{"message":"stats_unavailable"}`)
	assert.Equal(t, string(r.get("/stats/app:jarvis:web:1", http.StatusOK)["id"]), `"app:jarvis:web:1"`)
}

func TestNoDockerTextLeaksIntoAnError(t *testing.T) {
	r := newStatsRig(t)
	r.src.listErr = errors.New("dial unix /var/run/docker.sock: secret-name-xyz")
	r.ticks(1)
	recorder := r.do(http.MethodGet, "/stats")
	assert.Equal(t, recorder.Code, http.StatusServiceUnavailable)
	assert.Assert(t, !strings.Contains(recorder.Body.String(), "secret-name-xyz"))
	assert.Assert(t, !strings.Contains(recorder.Body.String(), "docker.sock"))
}

// Docker refuses such a container name; the identity refuses it too, so it is
// never listed. An image name can hold anything, and stays a JSON string.
func TestAContainerNameIsOnlyEverJSONText(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	recorder := r.do(http.MethodGet, "/stats")
	assert.Equal(t, recorder.Code, http.StatusOK)
	assert.Assert(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json"))
	var list struct {
		Items []struct {
			ID, Name string
			Detail   *string
		}
	}
	assert.NilError(t, json.Unmarshal(recorder.Body.Bytes(), &list))
	var ids []string
	for _, it := range list.Items {
		ids = append(ids, it.ID)
		if it.ID == "container:my.db_1" {
			assert.Equal(t, *it.Detail, "<img src=x onerror=1>")
		}
	}
	slices.Sort(ids)
	assert.DeepEqual(t, ids, []string{"app:jarvis:web:1", "container:my.db_1"})
	assert.Assert(t, !strings.Contains(recorder.Body.String(), "<img"), "encoding/json escapes < and >")
}

// Hostile ids never reach a 500, never echo themselves back.
func TestAHostileIdIsRefusedQuietly(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	for _, path := range []string{
		"/stats/..",
		"/stats/%2e%2e",
		"/stats/container:..%2F..%2Fetc%2Fpasswd-MARK",
		"/stats/container%3Amy.db_1%2FMARK",
		"/stats/container:a%00MARK",
		"/stats/container:" + strings.Repeat("M", 10000) + "MARK",
		"/stats/app:jarvis:web:1:MARK",
		"/stats/%25MARK",
	} {
		recorder := r.do(http.MethodGet, path)
		assert.Assert(t, recorder.Code == http.StatusNotFound || recorder.Code == http.StatusBadRequest, "%s: %d %s", path[:min(len(path), 60)], recorder.Code, recorder.Body.String())
		assert.Assert(t, !strings.Contains(recorder.Body.String(), "MARK"), "%s: %s", path[:min(len(path), 60)], recorder.Body.String())
	}
	// a repeated range is refused, whichever the values
	recorder := r.do(http.MethodGet, "/stats/container:my.db_1?range=15m&range=1h")
	assert.Equal(t, recorder.Code, http.StatusBadRequest, recorder.Body.String())
}

func TestAGoneContainerIsListedStoppedWithoutNumbers(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(3)
	r.src.mu.Lock()
	r.src.ctrs = r.src.ctrs[:1]
	r.src.mu.Unlock()
	r.ticks(3)

	db := listItems(t, r.get("/stats", http.StatusOK))["container:my.db_1"]
	assert.Equal(t, string(db["state"]), `"stopped"`)
	for _, k := range []string{"started_at", "detail", "cpu_percent", "memory_used", "memory_limit"} {
		assert.Equal(t, string(db[k]), "null", k)
	}
	body := r.get("/stats/container:my.db_1", http.StatusOK)
	assert.Equal(t, string(body["notes"]), `["container_gone"]`)
	assert.Equal(t, string(body["scale"]), `{"cpu":"percent_of_one_core","cpu_allocation":null,"memory_limit":null}`)
}

const defaultRules = `[{"key":"cpu_high","enabled":true,"threshold":90,"minutes":10},{"key":"mem_high","enabled":true,"threshold":90,"minutes":5}]`

func TestEveryStatsAnswerIsNotCached(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	for _, path := range []string{"/stats", "/stats/container:my.db_1", "/stats/container:nope", "/stats/container:my.db_1/alerts", "/stats/container:nope/alerts"} {
		assert.Equal(t, r.do(http.MethodGet, path).Header().Get("Cache-Control"), "no-store", path)
	}
	for _, body := range []string{`{"rules":[]}`, `junk`} {
		assert.Equal(t, r.send(http.MethodPut, "/stats/container:my.db_1/alerts", body).Header().Get("Cache-Control"), "no-store", body)
	}
}

func TestGetAlertsHasTheDefaultRules(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	body := r.get("/stats/container:my.db_1/alerts", http.StatusOK)
	assert.DeepEqual(t, sortedKeys(body), []string{"active", "rules"})
	assert.Equal(t, string(body["rules"]), defaultRules)
	assert.Equal(t, string(body["active"]), "[]")
}

func TestPutAlertsStoresAndReturnsTheRules(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	recorder := r.send(http.MethodPut, "/stats/app%3Ajarvis%3Aweb%3A1/alerts", `{"rules":[{"key":"cpu_high","enabled":true,"threshold":80,"minutes":3}]}`)
	assert.Equal(t, recorder.Code, http.StatusOK, recorder.Body.String())
	want := `{"rules":[{"key":"cpu_high","enabled":true,"threshold":80,"minutes":3},{"key":"mem_high","enabled":true,"threshold":90,"minutes":5}],"active":[]}`
	assert.Equal(t, strings.TrimSpace(recorder.Body.String()), want)
	recorder = r.do(http.MethodGet, "/stats/app:jarvis:web:1/alerts")
	assert.Equal(t, strings.TrimSpace(recorder.Body.String()), want)
	// another item keeps the defaults
	assert.Equal(t, string(r.get("/stats/container:my.db_1/alerts", http.StatusOK)["rules"]), defaultRules)
}

func TestPutAlertsRefusesWhatEvaluatorRefuses(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	rule := func(key, enabled, threshold, minutes string) string {
		return `{"key":` + key + `,"enabled":` + enabled + `,"threshold":` + threshold + `,"minutes":` + minutes + `}`
	}
	ok := rule(`"mem_high"`, "true", "50", "5")
	for body, field := range map[string]string{
		`{"rules":[` + rule(`"cpu_high"`, "true", "0", "3") + `]}`:        "rules[0].threshold",
		`{"rules":[` + rule(`"cpu_high"`, "true", "101", "3") + `]}`:      "rules[0].threshold",
		`{"rules":[` + rule(`"cpu_high"`, "true", "100.5", "3") + `]}`:    "rules[0].threshold",
		`{"rules":[` + rule(`"cpu_high"`, "true", "80", "0") + `]}`:       "rules[0].minutes",
		`{"rules":[` + rule(`"cpu_high"`, "true", "80", "121") + `]}`:     "rules[0].minutes",
		`{"rules":[` + rule(`"foo"`, "true", "80", "3") + `]}`:            "rules[0].key",
		`{"rules":[` + ok + `,` + rule(`"foo"`, "true", "80", "3") + `]}`: "rules[1].key",
		`{"rules":[` + ok + `,` + ok + `]}`:                               "rules[1].key",
		`{"rules":[` + rule(`"cpu_high"`, "true", `"80"`, "3") + `]}`:     "rules[0].threshold",
		`{"rules":[` + rule(`"cpu_high"`, "true", "null", "3") + `]}`:     "rules[0].threshold",
		`{"rules":[` + rule(`"cpu_high"`, "true", "80", `"3"`) + `]}`:     "rules[0].minutes",
		`{"rules":[` + rule(`"cpu_high"`, "true", "80", "3.5") + `]}`:     "rules[0].minutes",
		`{"rules":[` + rule(`"cpu_high"`, "true", "80", "1e30") + `]}`:    "rules[0].minutes",
		`{"rules":[` + rule(`5`, "true", "80", "3") + `]}`:                "rules[0].key",
		`{"rules":[` + rule(`"cpu_high"`, `"yes"`, "80", "3") + `]}`:      "rules[0].enabled",
		`{"rules":[{"key":"cpu_high","threshold":80,"minutes":3}]}`:       "rules[0].enabled",
		`{"rules":[` + rule(`"cpu_high"`, "null", "80", "3") + `]}`:       "rules[0].enabled",
		`{"rules":[{"key":"cpu_high","enabled":true,"minutes":3}]}`:       "rules[0].threshold",
		`{"rules":[{"key":"cpu_high","enabled":true,"threshold":80}]}`:    "rules[0].minutes",
		`{"rules":[{"enabled":true,"threshold":80,"minutes":3}]}`:         "rules[0].key",
		`{"rules":[null]}`: "rules[0].key",
		`{"rules":[[]]}`:   "rules",
		`{"rules":[` + rule(`"cpu_high"`, "true", "NaN", "3") + `]}`:      "rules",
		`{"rules":[` + rule(`"cpu_high"`, "true", "Infinity", "3") + `]}`: "rules",
		`{"rules":{}}`:   "rules",
		`{"rules":null}`: "rules",
		`{}`:             "rules",
		`[]`:             "rules",
		`"rules"`:        "rules",
		`null`:           "rules",
		``:               "rules",
		`{"rules":[]} x`: "rules",
		`{"rules":[`:     "rules",
	} {
		recorder := r.send(http.MethodPut, "/stats/container:my.db_1/alerts", body)
		assert.Equal(t, recorder.Code, http.StatusBadRequest, body)
		assert.Equal(t, strings.TrimSpace(recorder.Body.String()), `{"message":"bad_rules: `+field+`"}`, body)
	}
	assert.Equal(t, string(r.get("/stats/container:my.db_1/alerts", http.StatusOK)["rules"]), defaultRules, "a refusal changes nothing")
}

func TestPutAlertsToleratesUnknownPropertiesAndTakesTheLastOfADuplicate(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	recorder := r.send(http.MethodPut, "/stats/container:my.db_1/alerts",
		`{"extra":1,"rules":[{"key":"cpu_high","enabled":false,"threshold":5,"threshold":70,"minutes":3,"note":"x"}]}`)
	assert.Equal(t, recorder.Code, http.StatusOK, recorder.Body.String())
	var body struct{ Rules []stats.Rule }
	assert.NilError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.DeepEqual(t, body.Rules[0], stats.Rule{Key: "cpu_high", Enabled: false, Threshold: 70, Minutes: 3})
}

func TestPutAlertsBoundsTheBody(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	pad := func(n int) string {
		head, tail := `{"rules":[{"key":"cpu_high","enabled":true,"threshold":60,"minutes":3}],"pad":"`, `"}`
		return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
	}
	recorder := r.send(http.MethodPut, "/stats/container:my.db_1/alerts", pad(64<<10))
	assert.Equal(t, recorder.Code, http.StatusOK, "64 KiB is accepted")
	recorder = r.send(http.MethodPut, "/stats/container:my.db_1/alerts", pad(64<<10+1))
	assert.Equal(t, recorder.Code, http.StatusBadRequest)
	assert.Equal(t, strings.TrimSpace(recorder.Body.String()), `{"message":"bad_rules: rules"}`)
	recorder = r.send(http.MethodPut, "/stats/container:my.db_1/alerts", pad(8<<20))
	assert.Equal(t, recorder.Code, http.StatusBadRequest)
	// cut at the bound, valid JSON followed by blanks would still parse
	recorder = r.send(http.MethodPut, "/stats/container:my.db_1/alerts", `{"rules":[]}`+strings.Repeat(" ", 64<<10))
	assert.Equal(t, recorder.Code, http.StatusBadRequest)
}

func TestPutAlertsOnAnUnknownItemIs404(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	recorder := r.send(http.MethodPut, "/stats/container:nope/alerts", `{"rules":[{"key":"cpu_high","enabled":true,"threshold":80,"minutes":3}]}`)
	assert.Equal(t, recorder.Code, http.StatusNotFound)
	assert.Equal(t, strings.TrimSpace(recorder.Body.String()), `{"message":"unknown_item"}`)
	recorder = r.do(http.MethodGet, "/stats/container:nope/alerts")
	assert.Equal(t, recorder.Code, http.StatusNotFound)
	assert.Equal(t, strings.TrimSpace(recorder.Body.String()), `{"message":"unknown_item"}`)
}

func TestAnActiveAlertIsListedAndDisablingItResolvesIt(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	// a fifth of a core of the machine's 4 is 5 %: over 2 % for one minute fires
	recorder := r.send(http.MethodPut, "/stats/container:my.db_1/alerts", `{"rules":[{"key":"cpu_high","enabled":true,"threshold":2,"minutes":1}]}`)
	assert.Equal(t, recorder.Code, http.StatusOK, recorder.Body.String())
	r.ticks(40)

	var body struct {
		Active []map[string]json.RawMessage
	}
	assert.NilError(t, json.Unmarshal(r.do(http.MethodGet, "/stats/container:my.db_1/alerts").Body.Bytes(), &body))
	assert.Equal(t, len(body.Active), 1)
	a := body.Active[0]
	assert.DeepEqual(t, sortedKeys(a), []string{"key", "since", "value"})
	assert.Equal(t, string(a["key"]), `"cpu_high"`)
	var since string
	assert.NilError(t, json.Unmarshal(a["since"], &since))
	at, err := time.Parse(time.RFC3339, since)
	assert.NilError(t, err, since)
	assert.Assert(t, !at.Before(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)) && at.Before(r.clock.Now()), since)
	var value float64
	assert.NilError(t, json.Unmarshal(a["value"], &value), string(a["value"]))
	assert.Assert(t, value > 2, value)
	assert.Equal(t, string(listItems(t, r.get("/stats", http.StatusOK))["container:my.db_1"]["alerts"]), "1")

	recorder = r.send(http.MethodPut, "/stats/container:my.db_1/alerts", `{"rules":[{"key":"cpu_high","enabled":false,"threshold":2,"minutes":1}]}`)
	assert.Equal(t, recorder.Code, http.StatusOK, recorder.Body.String())
	assert.Equal(t, string(r.get("/stats/container:my.db_1/alerts", http.StatusOK)["active"]), "[]")
	r.mu.Lock()
	defer r.mu.Unlock()
	var states []string
	for _, e := range r.events {
		assert.Equal(t, e.Item, "container:my.db_1")
		states = append(states, e.State)
	}
	assert.DeepEqual(t, states, []string{"firing", "resolved"})
}

func TestAlertsAreAvailableWhileDockerIsDown(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(3)
	r.src.mu.Lock()
	r.src.listErr = errors.New("connection refused")
	r.src.mu.Unlock()
	r.ticks(1)
	assert.Equal(t, r.do(http.MethodGet, "/stats").Code, http.StatusServiceUnavailable)
	r.get("/stats/app:jarvis:web:1/alerts", http.StatusOK)
	recorder := r.send(http.MethodPut, "/stats/app:jarvis:web:1/alerts", `{"rules":[{"key":"mem_high","enabled":true,"threshold":70,"minutes":2}]}`)
	assert.Equal(t, recorder.Code, http.StatusOK, recorder.Body.String())

	// after a restart with Docker still down, the item is known from disk only
	assert.NilError(t, r.s.Close())
	r.open()
	r.ticks(1)
	assert.Assert(t, strings.Contains(string(r.get("/stats/app:jarvis:web:1/alerts", http.StatusOK)["rules"]), `"threshold":70`))
	assert.Equal(t, r.send(http.MethodPut, "/stats/app:jarvis:web:1/alerts", `{"rules":[]}`).Code, http.StatusOK)
}

// A listed container that never ran has no history: the hourly Prune would drop
// its rules, so they are refused rather than lost after a 200.
func TestRulesOfAnItemWithoutHistoryAreRefused(t *testing.T) {
	r := newStatsRig(t)
	r.src.ctrs[1].State = "exited"
	r.ticks(1)
	recorder := r.send(http.MethodPut, "/stats/container:my.db_1/alerts", `{"rules":[{"key":"mem_high","enabled":true,"threshold":70,"minutes":2}]}`)
	assert.Equal(t, recorder.Code, http.StatusNotFound, recorder.Body.String())
	assert.Equal(t, strings.TrimSpace(recorder.Body.String()), `{"message":"unknown_item"}`)
	assert.Equal(t, string(r.get("/stats/container:my.db_1/alerts", http.StatusOK)["rules"]), defaultRules, "still listed, with the defaults")
}

// More than an hour of ticks goes through the hourly Sweep and Prune.
func TestRulesOfAnItemWithHistoryOutliveTheHourlyPrune(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(2)
	recorder := r.send(http.MethodPut, "/stats/container:my.db_1/alerts", `{"rules":[{"key":"mem_high","enabled":true,"threshold":70,"minutes":2}]}`)
	assert.Equal(t, recorder.Code, http.StatusOK, recorder.Body.String())
	r.ticks(721)
	assert.Assert(t, strings.Contains(string(r.get("/stats/container:my.db_1/alerts", http.StatusOK)["rules"]), `"threshold":70`))
}

// Rules applied but not written to alerts.json are not durable: 503, and the
// next good write makes them so.
func TestAFailedSaveOfTheRulesIs503(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(1)
	tmp := filepath.Join(r.dir, "alerts.json.tmp")
	assert.NilError(t, os.Mkdir(tmp, 0o755)) // the atomic write cannot open it, even as root
	put := func(threshold string) *httptest.ResponseRecorder {
		return r.send(http.MethodPut, "/stats/container:my.db_1/alerts", `{"rules":[{"key":"mem_high","enabled":true,"threshold":`+threshold+`,"minutes":2}]}`)
	}
	recorder := put("70")
	assert.Equal(t, recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	assert.Equal(t, strings.TrimSpace(recorder.Body.String()), `{"message":"stats_unavailable"}`)
	assert.Assert(t, !strings.Contains(recorder.Body.String(), "alerts.json"))

	assert.NilError(t, os.Remove(tmp))
	assert.Equal(t, put("60").Code, http.StatusOK)
	b, err := os.ReadFile(filepath.Join(r.dir, "alerts.json"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(b), `"threshold":60`), string(b))
}

// The history of a container that is gone could not be read at start: its
// alerts are unavailable, not unknown.
func TestAlertsOfAnUnreadableHistoryAre503(t *testing.T) {
	r := newStatsRig(t)
	r.ticks(3)
	assert.NilError(t, r.s.Close())
	rings, err := filepath.Glob(filepath.Join(r.dir, "*.ring"))
	assert.NilError(t, err)
	assert.Assert(t, len(rings) > 0)
	for _, ring := range rings {
		assert.NilError(t, os.Remove(ring))
		if err := os.Symlink(filepath.Join(r.dir, "nowhere"), ring); err != nil {
			t.Skipf("no symlinks here: %v", err)
		}
	}
	r.src.mu.Lock()
	r.src.ctrs = nil
	r.src.mu.Unlock()
	r.open()
	r.ticks(1)
	for _, recorder := range []*httptest.ResponseRecorder{
		r.do(http.MethodGet, "/stats/container:my.db_1/alerts"),
		r.send(http.MethodPut, "/stats/container:my.db_1/alerts", `{"rules":[]}`),
	} {
		assert.Equal(t, recorder.Code, http.StatusServiceUnavailable)
		assert.Equal(t, strings.TrimSpace(recorder.Body.String()), `{"message":"stats_unavailable"}`)
	}
	assert.Equal(t, r.do(http.MethodGet, "/stats/container:nope/alerts").Code, http.StatusNotFound)
}
