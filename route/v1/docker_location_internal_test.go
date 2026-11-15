package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestHiddenContainersCountsTheContainersOfTheAppsGiven(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) (string, error) {
		switch strings.Join(append([]string{name}, args...), " ") {
		case "docker ps -aq --filter label=com.docker.compose.project=immich":
			return "aaa\nbbb\n", nil
		case "docker ps -aq --filter label=com.docker.compose.project=paperless":
			return "ccc\n", nil
		}
		return "", nil
	}
	if got := hiddenContainers(context.Background(), run, []string{"immich", "paperless"}); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
	if got := hiddenContainers(context.Background(), run, nil); got != 0 {
		t.Fatalf("nothing hidden counts nothing: %d", got)
	}
}

// A caller no account can be told from cannot have the hidden apps left out of the count: refused.
func TestCheckDockerLocationRefusesACallerWithoutAnAccount(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/docker/location/check?path="+t.TempDir(), nil)
	request.RemoteAddr = "203.0.113.5:40000"
	rec := httptest.NewRecorder()
	if err := CheckDockerLocation(echo.New().NewContext(request, rec)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403: %s", rec.Code, rec.Body.String())
	}
}
