package route

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/jwt"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

const (
	adminID = 1
	userID  = 2
	goneID  = 9 // user-service has no such account
)

type roleRig struct {
	t           *testing.T
	v1, v2      http.Handler
	admin, user string // access tokens
}

// newRoleRig is a user-service that hands out the key and says account 1 is the admin and
// anyone else a user, in front of both routers.
func newRoleRig(t *testing.T) *roleRig {
	t.Helper()
	logger.LogInitConsoleOnly()
	runtime := t.TempDir()
	previous := config.CommonInfo.RuntimePath
	config.CommonInfo.RuntimePath = runtime
	t.Cleanup(func() { config.CommonInfo.RuntimePath = previous })
	assert.NilError(t, os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret"), 0o600))

	private, public := testUserKey()
	jwks, err := jwt.GenerateJwksJSON(public)
	assert.NilError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/"+jwt.JWKSPath, jwt.JWKSHandler(jwks))
	mux.HandleFunc("/v1/users/role/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, fmt.Sprintf("/%d", goneID)) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"success":10006,"message":"User does not exist"}`)
			return
		}
		role := "user"
		if strings.HasSuffix(r.URL.Path, fmt.Sprintf("/%d", adminID)) {
			role = "admin"
		}
		fmt.Fprintf(w, `{"success":200,"message":"ok","data":%q}`, role)
	})
	users := httptest.NewServer(mux)
	t.Cleanup(users.Close)
	assert.NilError(t, os.WriteFile(filepath.Join(runtime, external.UserServiceAddressFilename), []byte(users.URL), 0o600))

	r := &roleRig{t: t, v1: InitV1Router(), v2: InitV2Router()}
	r.admin, err = jwt.GetAccessToken("admin", private, adminID)
	assert.NilError(t, err)
	r.user, err = jwt.GetAccessToken("someone", private, userID)
	assert.NilError(t, err)

	was := service.VisibilityPath
	service.VisibilityPath = filepath.Join(t.TempDir(), "visibility.json")
	t.Cleanup(func() { service.VisibilityPath = was })

	return r
}

func (r *roleRig) do(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.5:40000"
	if token == "internal" {
		request.RemoteAddr = "127.0.0.1:40000"
		request.Header.Set("Authorization", "Internal s3cret")
	} else if token != "" {
		request.Header.Set("Authorization", token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

func TestUserWriteIs403OnV1AndV2(t *testing.T) {
	r := newRoleRig(t)

	for _, tc := range []struct {
		handler http.Handler
		method  string
		path    string
	}{
		{r.v1, http.MethodPut, "/v1/container/archive/abc"},
		{r.v1, http.MethodDelete, "/v1/container/abc"},
		{r.v1, http.MethodGet, "/v1/container/abc/terminal"}, // a shell is a write
		{r.v2, http.MethodDelete, "/v2/app_management/compose/jarvis"},
		{r.v2, http.MethodPut, "/v2/app_management/backup/exclusions"},
		{r.v2, http.MethodPost, "/v2/app_management/compose"},
	} {
		got := r.do(tc.handler, tc.method, tc.path, r.user, `{}`)
		assert.Equal(t, got.Code, http.StatusForbidden, "%s %s: %s", tc.method, tc.path, got.Body.String())
	}
}

func TestUserGetIsAllowed(t *testing.T) {
	r := newRoleRig(t)

	got := r.do(r.v2, http.MethodGet, "/v2/app_management/backup/exclusions", r.user, "")
	assert.Equal(t, got.Code, http.StatusOK, got.Body.String())
	got = r.do(r.v1, http.MethodGet, "/v1/container/docker-location", r.user, "")
	assert.Assert(t, got.Code != http.StatusForbidden && got.Code != http.StatusUnauthorized, "v1 GET: %d", got.Code)
}

func TestInternalAndAdminWritesPass(t *testing.T) {
	r := newRoleRig(t)

	for _, token := range []string{r.admin, "internal"} {
		got := r.do(r.v2, http.MethodPut, "/v2/app_management/backup/exclusions", token, `{"paths":["/mnt/nas"]}`)
		assert.Assert(t, got.Code != http.StatusForbidden && got.Code != http.StatusUnauthorized, "v2 %.8s: %d %s", token, got.Code, got.Body.String())
		got = r.do(r.v1, http.MethodPut, "/v1/container/archive/abc", token, `{}`)
		assert.Assert(t, got.Code != http.StatusForbidden && got.Code != http.StatusUnauthorized, "v1 %.8s: %d %s", token, got.Code, got.Body.String())
	}
}

func TestVisibilityRoutesAreAdminOnly(t *testing.T) {
	r := newRoleRig(t)
	const path = "/v2/app_management/visibility/jellyfin"

	// the Gateway refuses a user's write first; this is the service's own check, so a
	// user is refused the reads as well
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		got := r.do(r.v2, method, path, r.user, `{"hidden_for":[2]}`)
		assert.Equal(t, got.Code, http.StatusForbidden, "user %s: %s", method, got.Body.String())
	}
	assert.Assert(t, !service.AppVisibility.HiddenFor("jellyfin", userID), "a refused write changed nothing")

	got := r.do(r.v2, http.MethodPut, path, r.admin, `{"hidden_for":[2,3]}`)
	assert.Equal(t, got.Code, http.StatusOK, got.Body.String())
	assert.Assert(t, service.AppVisibility.HiddenFor("jellyfin", 3))

	got = r.do(r.v2, http.MethodGet, path, "internal", "")
	assert.Equal(t, got.Code, http.StatusOK, got.Body.String())
	assert.Assert(t, strings.Contains(got.Body.String(), `"hidden_for":[2,3]`), got.Body.String())

	got = r.do(r.v2, http.MethodPut, path, r.admin, `{"hidden":[2]}`)
	assert.Equal(t, got.Code, http.StatusBadRequest, "a misspelt body is not an empty list: %s", got.Body.String())
	assert.Assert(t, service.AppVisibility.HiddenFor("jellyfin", 3))
}

func TestAccountRemovalRouteIsInternalOnly(t *testing.T) {
	r := newRoleRig(t)
	assert.NilError(t, service.AppVisibility.Hide("jellyfin", []int{2, 3}))
	const path = "/v2/app_management/visibility/accounts/3"

	for _, token := range []string{r.user, r.admin} {
		got := r.do(r.v2, http.MethodDelete, path, token, "")
		assert.Equal(t, got.Code, http.StatusForbidden, "%.8s: %s", token, got.Body.String())
		assert.Assert(t, service.AppVisibility.HiddenFor("jellyfin", 3))
	}

	got := r.do(r.v2, http.MethodDelete, path, "internal", "")
	assert.Equal(t, got.Code, http.StatusOK, got.Body.String())
	assert.Assert(t, !service.AppVisibility.HiddenFor("jellyfin", 3) && service.AppVisibility.HiddenFor("jellyfin", 2))

	// idempotent: an id nothing mentions, and a second call
	for _, p := range []string{path, "/v2/app_management/visibility/accounts/99"} {
		got = r.do(r.v2, http.MethodDelete, p, "internal", "")
		assert.Equal(t, got.Code, http.StatusOK, got.Body.String())
	}
}

// A token of an account that is gone is refused on both routers, reads included.
func TestTokenOfAGoneAccountIs401(t *testing.T) {
	r := newRoleRig(t)
	private, _ := testUserKey()
	gone, err := jwt.GetAccessToken("someone", private, goneID)
	assert.NilError(t, err)

	for _, tc := range []struct {
		handler      http.Handler
		method, path string
	}{
		{r.v2, http.MethodGet, "/v2/app_management/backup/exclusions"},
		{r.v2, http.MethodPut, "/v2/app_management/backup/exclusions"},
		{r.v1, http.MethodGet, "/v1/container/networks"},
		{r.v1, http.MethodPut, "/v1/container/archive/abc"},
	} {
		got := r.do(tc.handler, tc.method, tc.path, gone, `{}`)
		assert.Equal(t, got.Code, http.StatusUnauthorized, "%s %s: %s", tc.method, tc.path, got.Body.String())
	}
	// a live user still reads
	got := r.do(r.v2, http.MethodGet, "/v2/app_management/backup/exclusions", r.user, "")
	assert.Equal(t, got.Code, http.StatusOK, got.Body.String())
}
