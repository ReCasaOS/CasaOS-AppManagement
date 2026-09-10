package docker_test

import (
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
	"gotest.tools/v3/assert"
)

// A registry answers this header, so this parser reads whatever a registry chooses to
// send. It ran on the request goroutine before, where net/http recovers a panic into a
// dropped request; it now also runs inside an errgroup, which has no recover, so
// anything that panics here takes the whole daemon down and systemd restarts it in the
// middle of whatever else was installing.
func TestGetAuthURLSurvivesAnythingARegistryCanSend(t *testing.T) {
	for _, challenge := range []string{
		"",
		"Bearer",
		"Bearer ",
		// a valueless directive anywhere in the list: a reverse proxy or a captive
		// portal in front of a registry is enough to produce one
		`Bearer realm="https://auth.example/token",service="registry",error`,
		`Bearer error,realm="https://auth.example/token",service="registry"`,
		"Basic realm=\"registry\"",
		// a realm that is not a URL at all
		`Bearer realm="://",service="registry"`,
		`Bearer realm="%zz",service="registry"`,
		// = inside the value, which is legal in a URL query
		`Bearer realm="https://auth.example/token?a=b",service="registry"`,
	} {
		t.Run(challenge, func(t *testing.T) {
			// the contract is an error, never a panic
			_, _ = docker.GetAuthURL(challenge, "acme/app")
		})
	}
}

func TestGetAuthURLReadsAWellFormedChallenge(t *testing.T) {
	authURL, err := docker.GetAuthURL(
		`Bearer realm="https://auth.docker.io/token",service="registry.docker.io"`, "library/nginx")
	assert.NilError(t, err)
	assert.Equal(t, authURL.Scheme, "https")
	assert.Equal(t, authURL.Host, "auth.docker.io")
	assert.Equal(t, authURL.Path, "/token")
	assert.Equal(t, authURL.Query().Get("service"), "registry.docker.io")
	assert.Equal(t, authURL.Query().Get("scope"), "repository:library/nginx:pull")
}

// A value that itself contains `=` must survive, or every realm carrying a query
// string is truncated and the token request goes to the wrong URL.
func TestGetAuthURLKeepsAnEqualsSignInsideAValue(t *testing.T) {
	authURL, err := docker.GetAuthURL(
		`Bearer realm="https://auth.example/token?flavour=v2",service="registry"`, "acme/app")
	assert.NilError(t, err)
	assert.Equal(t, authURL.Query().Get("flavour"), "v2")
}
