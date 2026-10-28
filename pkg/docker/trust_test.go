package docker_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
	cliconfig "github.com/docker/cli/cli/config"
	"gotest.tools/v3/assert"
)

// config writes a docker config with one login, as `docker login registry` leaves it.
func config(t *testing.T, dir, registry, user, password string) {
	t.Helper()
	token := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	text, err := json.Marshal(map[string]any{"auths": map[string]any{registry: map[string]string{"auth": token}}})
	assert.NilError(t, err)
	assert.NilError(t, os.MkdirAll(dir, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "config.json"), text, 0o600))
}

func decoded(t *testing.T, encoded string) map[string]string {
	t.Helper()
	raw, err := base64.URLEncoding.DecodeString(encoded)
	assert.NilError(t, err)
	out := map[string]string{}
	assert.NilError(t, json.Unmarshal(raw, &out))
	return out
}

// `docker login` writes ~/.docker/config.json; a pull used to look for the config at the root
// of the file system only, so a private registry's login was never seen and its image never pulled.
func TestAPullFindsTheLoginDockerLoginWrote(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", "")
	home := filepath.Join(t.TempDir(), ".docker")
	previous := cliconfig.Dir()
	cliconfig.SetDir(home)
	t.Cleanup(func() { cliconfig.SetDir(previous) })
	config(t, home, "registry.example.org", "ci", "s3cret")

	auth, err := docker.EncodedConfigAuth("registry.example.org/team/app-php:latest")
	assert.NilError(t, err)
	got := decoded(t, auth)
	assert.Equal(t, got["username"], "ci")
	assert.Equal(t, got["password"], "s3cret")

	// another registry has no login
	auth, err = docker.EncodedConfigAuth("registry.other.org/team/app:latest")
	assert.NilError(t, err)
	assert.Equal(t, auth, "")
}

func TestDockerConfigNamesTheDirectoryWhenSet(t *testing.T) {
	dir := t.TempDir()
	config(t, dir, "registry.example.org", "ci", "from-env-dir")
	t.Setenv("DOCKER_CONFIG", dir)

	auth, err := docker.EncodedConfigAuth("registry.example.org/team/app:latest")
	assert.NilError(t, err)
	assert.Equal(t, decoded(t, auth)["password"], "from-env-dir")
}
