package route

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	v2Route "github.com/ReCasaOS/CasaOS-AppManagement/route/v2"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

const unknownApp = "nothing-here"

// What each path parameter is given: the value naming the hidden app (or its container, or
// its stats item) and the value naming nothing. A parameter that names no app takes the same
// value on both sides.
var parameterValues = map[string][2]string{
	"app":             {hiddenApp, unknownApp},
	"id":              {hiddenApp, unknownApp}, // a compose app, a store app; containers and stats below
	"containerId":     {"c-hidden", "c-nothing"},
	"name":            {"nas", "nas"},
	"stamp":           {"2026-11-01T10-00-00Z", "2026-11-01T10-00-00Z"},
	"serviceName":     {"web", "web"},
	"key":             {"OPENAI_API_KEY", "OPENAI_API_KEY"},
	"backup_id":       {"b1", "b1"},
	"verification_id": {"v1", "v1"},
}

// containerAndStats are the paths whose `id` is a container or a stats item, not an app name.
func idValues(path string) [][2]string {
	switch {
	case strings.Contains(path, "/container/:id"):
		return [][2]string{{"c-hidden", "c-nothing"}}
	case strings.Contains(path, "/stats/:id"):
		return [][2]string{
			{"app:" + hiddenApp + ":db:1", "app:" + unknownApp + ":db:1"},
			// a `docker compose run` container of the app
			{"container:" + hiddenApp + "-web-run-ab12", "container:" + unknownApp + "-web-run-ab12"},
		}
	case strings.HasSuffix(path, "/visibility/accounts/:id"):
		return [][2]string{{"2", "2"}}
	}
	return [][2]string{parameterValues["id"]}
}

// A variant of a read: a query string, an Accept header, or whitespace (escaped) around the
// app's name.
type variant struct{ query, accept, lead, trail string }

// The variants each read is also asked with, beside none.
var readVariants = map[string][]variant{
	"/v2/app_management/stats/:id":                                        {{query: "?range=bad"}, {query: "?range=1h"}, {query: "?range=7d"}, {query: "?range="}},
	"/v2/app_management/git/:app":                                         {{trail: "%20"}, {lead: "%20"}},
	"/v2/app_management/git/:app/tags":                                    {{trail: "%20"}},
	"/v2/app_management/backup/destinations/:name/runs/:app/:stamp/files": {{query: "?path=config"}, {query: "?path=../etc"}, {trail: "%20"}, {lead: "%20"}, {lead: "%09", trail: "%20%20"}},
	"/v2/app_management/compose/:id":                                      {{accept: common.MIMEApplicationYAML}},
	"/v2/app_management/compose/:id/logs":                                 {{query: "?service=web&lines=5"}, {query: "?service=nope"}},
}

// Reads whose answer to the owner is the same as to a user, with why: the comparison of the
// hidden name and the unknown one cannot fail for want of a difference there.
var sameForTheOwner = map[string]string{
	"/v2/app_management/apps/:id":                         "the catalogue, the same for everyone; it says nothing of what is installed",
	"/v2/app_management/apps/:id/compose":                 "the catalogue, as above",
	"/v2/app_management/apps/:id/stable":                  "the catalogue, as above",
	"/v2/app_management/apps/:id/stable/:serviceName":     "the catalogue, as above",
	"/v2/app_management/backup/destinations/:name/restic": "a destination's own state; it names no app",
	// shortcut: no rclone in the harness, so what a destination holds is never listed here; a fake
	// router in the rig would exercise the omission of hidden apps from it
	"/v2/app_management/backup/destinations/:name/runs":                   "no rclone in the harness: every destination fails alike, for the owner too",
	"/v2/app_management/backup/destinations/:name/runs/:app/:stamp/files": "no rclone in the harness: the hidden app is asked about under a probe name, and the owner's read fails as an unknown one does",
	"/v2/app_management/git/:app":                                         "no Docker daemon in the harness: the owner's read fails at it too",
	"/v2/app_management/git/:app/tags":                                    "no Docker daemon in the harness, as above",
}

// writes are refused to a user before any handler reads the name; the owner is not asked
// (a write would change the fixture)
func isRead(method, path string) bool {
	return method == http.MethodGet && !strings.HasSuffix(path, "/terminal")
}

type routeCall struct {
	method, path, accept string
	hidden, unknown      string // the path with the parameters filled in
	replace              [][2]string
	// the store's catalogue knows apps that are not installed: the hidden app is compared with
	// itself on a box where it is not installed
	catalogue bool
}

func appRouteCalls(t *testing.T, routes []*echo.Route) []routeCall {
	var calls []routeCall
	for _, route := range routes {
		if !strings.Contains(route.Path, "/:") || !slices.Contains([]string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete}, route.Method) {
			continue
		}
		segments := strings.Split(route.Path, "/")
		var params []int
		for i, s := range segments {
			if strings.HasPrefix(s, ":") {
				params = append(params, i)
			}
		}
		// every combination of the `id` values; the other parameters have one pair each
		for _, idPair := range idValues(route.Path) {
			variants := []variant{{}}
			if isRead(route.Method, route.Path) {
				variants = append(variants, readVariants[route.Path]...)
			}
			for _, v := range variants {
				hidden, unknown := slices.Clone(segments), slices.Clone(segments)
				var replace [][2]string
				for _, i := range params {
					name := segments[i][1:]
					pair, known := parameterValues[name]
					if name == "id" {
						pair = idPair
					} else if !known {
						t.Errorf("%s %s: no value for the parameter %q; add it to parameterValues", route.Method, route.Path, name)
						continue
					}
					hidden[i], unknown[i] = pair[0], pair[1]
					if name == "app" {
						hidden[i], unknown[i] = v.lead+pair[0]+v.trail, v.lead+pair[1]+v.trail
					}
					if pair[0] != pair[1] {
						replace = append(replace, pair)
					}
				}
				catalogue := strings.HasPrefix(route.Path, "/v2/app_management/apps/:id")
				if catalogue {
					unknown, replace = hidden, nil
				}
				calls = append(calls, routeCall{
					method: route.Method, path: route.Path, accept: v.accept, catalogue: catalogue,
					hidden: strings.Join(hidden, "/") + v.query, unknown: strings.Join(unknown, "/") + v.query, replace: replace,
				})
			}
		}
	}
	return calls
}

func (r *readsRig) call(method, path, accept, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	request.RemoteAddr = "203.0.113.5:40000"
	if token == "internal" {
		request.RemoteAddr = "127.0.0.1:40000"
		token = "Internal s3cret"
	}
	request.Header.Set("Authorization", token)
	handler := r.v2
	if strings.HasPrefix(path, "/v1/") {
		handler = r.v1
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// Every route that takes an app, a container, a stats item or a destination answers a user
// the same for a hidden app as for an app that does not exist: status and body, the name aside.
func TestHiddenEqualsUnknownOnEveryAppRoute(t *testing.T) {
	r := newReadsRig(t)
	config.SetGlobal("OPENAI_API_KEY", "sk-secret")
	t.Cleanup(func() { config.DeleteGlobal("OPENAI_API_KEY") })

	routes := append(r.v2.(*echo.Echo).Routes(), r.v1.(*echo.Echo).Routes()...)
	calls := appRouteCalls(t, routes)
	assert.Assert(t, len(calls) > 60, "the routes were read: %d calls", len(calls))

	differs := map[string]bool{}
	for _, c := range calls {
		hidden := r.call(c.method, c.hidden, c.accept, r.user)
		var unknown *httptest.ResponseRecorder
		if c.catalogue {
			r.without(hiddenApp, func() { unknown = r.call(c.method, c.unknown, c.accept, r.user) })
		} else {
			unknown = r.call(c.method, c.unknown, c.accept, r.user)
		}
		body := bodyOf(hidden)
		for _, pair := range c.replace {
			body = strings.ReplaceAll(body, pair[0], pair[1])
		}
		assert.Check(t, hidden.Code == unknown.Code, "%s %s: hidden %d, unknown %d: %s", c.method, c.hidden, hidden.Code, unknown.Code, bodyOf(hidden))
		assert.Check(t, body == bodyOf(unknown), "%s %s %s\nhidden:  %s\nunknown: %s", c.method, c.hidden, c.accept, bodyOf(hidden), bodyOf(unknown))

		if isRead(c.method, c.path) {
			owner := r.call(c.method, c.hidden, c.accept, r.admin)
			if owner.Code != hidden.Code || bodyOf(owner) != bodyOf(hidden) {
				differs[c.path] = true
			}
		}
	}

	// each read either shows the owner something the user does not get, or says why it cannot
	for _, c := range calls {
		if !isRead(c.method, c.path) {
			continue
		}
		_, listed := sameForTheOwner[c.path]
		assert.Check(t, differs[c.path] != listed, "%s: owner and user answers differ: %v; listed in sameForTheOwner: %v", c.path, differs[c.path], listed)
	}
}

func TestStatsRangeCheckedBeforeHidden(t *testing.T) {
	r := newReadsRig(t)
	for _, id := range [][2]string{{"app:" + hiddenApp + ":db:1", "app:" + unknownApp + ":db:1"}, {"container:immich-web-run-ab12", "container:nothing-here-web-run-ab12"}} {
		for _, query := range []string{"", "?range=bad", "?range=", "?range=24h"} {
			for _, tail := range []string{"", "/alerts"} {
				hidden := r.as("user", "/stats/"+id[0]+tail+query)
				unknown := r.as("user", "/stats/"+id[1]+tail+query)
				assert.Equal(t, hidden.Code, unknown.Code, "%s%s%s: %s", id[0], tail, query, bodyOf(hidden))
				assert.Equal(t, bodyOf(hidden), bodyOf(unknown), "%s%s%s", id[0], tail, query)
			}
		}
		assert.Equal(t, r.as("admin", "/stats/"+id[0]+"?range=1h").Code, http.StatusOK, id[0])
	}
}

func TestBackupBrowseSameForHiddenAndUnknown(t *testing.T) {
	r := newReadsRig(t)
	for _, destination := range []string{"nas", "nothing"} {
		for _, query := range []string{"", "?path=config", "?path=../x"} {
			path := "/backup/destinations/" + destination + "/runs/%s/2026-11-01T10-00-00Z/files" + query
			hidden := r.as("user", strings.Replace(path, "%s", hiddenApp, 1))
			unknown := r.as("user", strings.Replace(path, "%s", unknownApp, 1))
			assert.Equal(t, hidden.Code, unknown.Code, "%s: %s", path, bodyOf(hidden))
			assert.Equal(t, strings.ReplaceAll(bodyOf(hidden), hiddenApp, unknownApp), bodyOf(unknown), path)
			assert.Assert(t, !strings.Contains(bodyOf(hidden), "probe-"), bodyOf(hidden))
		}
	}
}

func TestNetworksOmitHiddenProjects(t *testing.T) {
	r := newReadsRig(t)
	names := func(token string) []string {
		got := r.do(r.v1, http.MethodGet, "/v1/container/networks", token, "")
		assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
		var body struct{ Data []struct{ Name string } }
		assert.NilError(t, json.Unmarshal(got.Body.Bytes(), &body))
		var out []string
		for _, n := range body.Data {
			out = append(out, n.Name)
		}
		slices.Sort(out)
		return out
	}
	assert.DeepEqual(t, names(r.user), []string{"bridge", "jarvis_default"})
	assert.DeepEqual(t, names(r.admin), []string{"bridge", "immich_default", "jarvis_default"})
	assert.DeepEqual(t, names(r.token(otherUser)), []string{"bridge", "immich_default", "jarvis_default"})
}

func TestUpdatesCheckOnlyForOwner(t *testing.T) {
	r := newReadsRig(t)
	checks := 0
	was := v2Route.CheckImageUpdates
	v2Route.CheckImageUpdates = func(context.Context) (*codegen.ImageUpdateCheckResult, error) {
		checks++
		return &codegen.ImageUpdateCheckResult{}, nil
	}
	t.Cleanup(func() { v2Route.CheckImageUpdates = was })

	plain := r.as("user", "/compose/updates")
	checked := r.as("user", "/compose/updates?check=true")
	assert.Equal(t, checks, 0, "a user's check=true starts nothing")
	assert.Equal(t, checked.Code, plain.Code)
	assert.Equal(t, bodyOf(checked), bodyOf(plain))

	assert.Equal(t, r.as("admin", "/compose/updates?check=true").Code, http.StatusOK)
	assert.Equal(t, checks, 1, "the owner's check runs")
}

func TestUserGetsNoSecrets(t *testing.T) {
	r := newReadsRig(t)
	config.SetGlobal("OPENAI_API_KEY", "sk-secret")
	t.Cleanup(func() { config.DeleteGlobal("OPENAI_API_KEY") })

	for _, c := range []struct {
		path, accept, secret, name string
	}{
		{"/v2/app_management/compose/" + shownApp, "", "hunter2", "API_TOKEN"},
		{"/v2/app_management/compose/" + shownApp, common.MIMEApplicationYAML, "hunter2", "API_TOKEN"},
		{"/v2/app_management/compose", "", "hunter2", "API_TOKEN"},
		{"/v2/app_management/compose", "", "s3cret", "API_TOKEN"},
		{"/v2/app_management/compose", "", "l0gt0ken", "splunk-token"},
		{"/v2/app_management/compose/" + shownApp, "", "l0gt0ken", "splunk-token"},
		{"/v2/app_management/compose/" + shownApp, "", "s3cret", "API_TOKEN"},
		{"/v2/app_management/compose/" + shownApp + "/env", "", "s3cret", "DB_PASSWORD="},
		{"/v2/app_management/container/c-shown/detail", "", "s3cret", "DB_PASSWORD="},
		{"/v1/container/c-shown", "", "s3cret", "DB_PASSWORD"},
		{"/v1/container/c-shown/compose", "", "s3cret", "DB_PASSWORD"},
		{"/v2/app_management/container/c-shown/detail", "", "t0ps3cret", "DB_PASSWORD="},
		{"/v1/container/c-shown", "", "t0ps3cret", "DB_PASSWORD"},
		{"/v2/app_management/global", "", "sk-secret", "OPENAI_API_KEY"},
		{"/v2/app_management/global/OPENAI_API_KEY", "", "sk-secret", "OPENAI_API_KEY"},
	} {
		user := r.call(http.MethodGet, c.path, c.accept, r.user)
		assert.Equal(t, user.Code, http.StatusOK, "%s %s: %s", c.path, c.accept, bodyOf(user))
		assert.Assert(t, !strings.Contains(bodyOf(user), c.secret), "%s %s: %s", c.path, c.accept, bodyOf(user))
		assert.Assert(t, strings.Contains(bodyOf(user), c.name), "the name is kept: %s %s: %s", c.path, c.accept, bodyOf(user))
		for _, token := range []string{r.admin, "internal"} {
			owner := r.call(http.MethodGet, c.path, c.accept, token)
			assert.Assert(t, strings.Contains(bodyOf(owner), c.secret), "%.8s %s %s: %s", token, c.path, c.accept, bodyOf(owner))
		}
	}

	// the restic password is the owner's alone; the git webhook secret: TestGitWebhookSecretStaysWithTheOwner
	got := r.as("user", "/backup/destinations/nas/restic/password")
	assert.Equal(t, got.Code, http.StatusForbidden, bodyOf(got))
	assert.Assert(t, r.as("admin", "/backup/destinations/nas/restic/password").Code != http.StatusForbidden)
}

func TestUninstallKeepsVisibilityEntry(t *testing.T) {
	r := newReadsRig(t)
	orphans := func(token string) (int, []string) {
		got := r.do(r.v2, http.MethodGet, "/v2/app_management/visibility-orphans", token, "")
		var body struct{ Data []string }
		_ = json.Unmarshal(got.Body.Bytes(), &body)
		return got.Code, body.Data
	}

	code, list := orphans(r.admin)
	assert.Equal(t, code, http.StatusOK)
	assert.Equal(t, len(list), 0, "an installed app's entry is no orphan: %v", list)

	r.without(hiddenApp, func() {
		assert.Assert(t, service.AppVisibility.HiddenFor(hiddenApp, userID), "the entry outlives the app")
		// what is left of the app stays hidden
		assert.Assert(t, !strings.Contains(bodyOf(r.as("user", "/logs")), "from immich"))

		code, list = orphans(r.admin)
		assert.Equal(t, code, http.StatusOK)
		assert.DeepEqual(t, list, []string{hiddenApp})
		code, _ = orphans(r.user)
		assert.Equal(t, code, http.StatusForbidden)

		got := r.do(r.v2, http.MethodDelete, "/v2/app_management/visibility/"+hiddenApp, r.user, "")
		assert.Equal(t, got.Code, http.StatusForbidden, bodyOf(got))
		assert.Assert(t, service.AppVisibility.HiddenFor(hiddenApp, userID))

		got = r.do(r.v2, http.MethodDelete, "/v2/app_management/visibility/"+hiddenApp, r.admin, "")
		assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
		assert.Assert(t, !service.AppVisibility.HiddenFor(hiddenApp, userID))
		code, list = orphans(r.admin)
		assert.Equal(t, code, http.StatusOK)
		assert.Equal(t, len(list), 0, "%v", list)

		// an app with no entry is answered the same way
		got = r.do(r.v2, http.MethodDelete, "/v2/app_management/visibility/"+hiddenApp, "internal", "")
		assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
	})

	// the listing has a path of its own: an app of any name keeps its entry routes
	got := r.do(r.v2, http.MethodPut, "/v2/app_management/visibility/orphans", r.admin, `{"hidden_for":[3]}`)
	assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
	got = r.do(r.v2, http.MethodGet, "/v2/app_management/visibility/orphans", r.admin, "")
	assert.Equal(t, got.Code, http.StatusOK, bodyOf(got))
	assert.Assert(t, strings.Contains(bodyOf(got), `"hidden_for":[3]`), bodyOf(got))
}

func TestVisibilityNameIsValidated(t *testing.T) {
	r := newRoleRig(t)
	for _, name := range []string{"Immich", "-immich", "_immich", "im.mich", "im%20mich", strings.Repeat("a", 65), "probe-" + strings.Repeat("0", 64)} {
		got := r.do(r.v2, http.MethodPut, "/v2/app_management/visibility/"+name, r.admin, `{"hidden_for":[2]}`)
		assert.Equal(t, got.Code, http.StatusBadRequest, "%q: %s", name, got.Body.String())
	}
	// no name at all reaches no visibility entry
	got := r.do(r.v2, http.MethodPut, "/v2/app_management/visibility/", r.admin, `{"hidden_for":[2]}`)
	assert.Assert(t, got.Code != http.StatusOK, "%d %s", got.Code, got.Body.String())
	assert.Equal(t, len(service.AppVisibility.Snapshot()), 0)

	for _, name := range []string{"immich", "0ad", "my-app_2", strings.Repeat("a", 64)} {
		got := r.do(r.v2, http.MethodPut, "/v2/app_management/visibility/"+name, r.admin, `{"hidden_for":[2]}`)
		assert.Equal(t, got.Code, http.StatusOK, "%q: %s", name, got.Body.String())
	}
}

// An update run of hidden apps alone answers a user as no run at all.
func TestAnUpdateRunOfHiddenAppsAloneIsNoRun(t *testing.T) {
	r := newReadsRig(t)
	_, err := service.StartUpdateAll([]string{hiddenApp}, func(context.Context, string) (codegen.ComposeAppUpdateRunAppState, string) {
		return codegen.ComposeAppUpdateRunAppStateUpdated, "done"
	})
	assert.NilError(t, err)
	awaitUpdateRun(t)

	got := r.as("user", "/compose/updates/run")
	assert.Equal(t, got.Code, http.StatusNotFound, bodyOf(got))
	assert.Equal(t, strings.TrimSpace(bodyOf(got)), `{"message":"no update of every app has run since this service started"}`)
	assert.Assert(t, strings.Contains(bodyOf(r.as("admin", "/compose/updates/run")), hiddenApp))
}
