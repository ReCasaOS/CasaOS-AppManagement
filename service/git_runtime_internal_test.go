package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/compose/v5/pkg/api"
	"gotest.tools/v3/assert"
)

func TestGitComposeFilesReadsWhatComposeUpReads(t *testing.T) {
	dir := t.TempDir()
	_, err := gitComposeFiles(dir)
	assert.Assert(t, errors.Is(err, ErrGitNoComposeFile))

	for _, name := range []string{"docker-compose.yml", "compose.yml", "compose.override.yml"} {
		assert.NilError(t, os.WriteFile(filepath.Join(dir, name), []byte("services: {}\n"), 0o644))
	}

	files, err := gitComposeFiles(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, files, []string{filepath.Join(dir, "compose.yml"), filepath.Join(dir, "compose.override.yml")})
}

func TestGitImageTagKeepsTheRepositoryOfTheName(t *testing.T) {
	const commit = "4f5f60c16eba0123456789abcdef0123456789ab"

	for image, want := range map[string]string{
		"jarvis-web":                        "jarvis-web:git-4f5f60c16eba",
		"ghcr.io/owner/jarvis:1.0":          "ghcr.io/owner/jarvis:git-4f5f60c16eba",
		"registry.local:5000/jarvis:latest": "registry.local:5000/jarvis:git-4f5f60c16eba",
	} {
		tag, err := gitImageTag(image, commit)
		assert.NilError(t, err)
		assert.Equal(t, tag, want)
	}
}

// A worktree holds tracked files only: the app's .env interpolates the build, and an
// env_file that is not there does not stop the load.
func TestLoadGitProjectInAWorktreeWithoutItsEnvFile(t *testing.T) {
	logger.LogInitConsoleOnly()
	root := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(`services:
  web:
    build:
      context: .
      args:
        VERSION: ${VERSION}
    env_file: .env
  cache:
    image: redis:${REDIS_TAG}
`), 0o644))
	envFile := filepath.Join(t.TempDir(), ".env")
	assert.NilError(t, os.WriteFile(envFile, []byte("VERSION=2\nREDIS_TAG=7\n"), 0o600))

	project, err := loadGitProject(context.Background(), "jarvis", root, envFile)
	assert.NilError(t, err)
	assert.Equal(t, project.Name, "jarvis")
	assert.Equal(t, *project.Services["web"].Build.Args["VERSION"], "2")
	assert.Equal(t, project.Services["cache"].Image, "redis:7")
	assert.DeepEqual(t, builtServiceNames(project.Services), []string{"web"})
}

func TestFailedBuiltContainerJudgesOnlyWhatWasBuilt(t *testing.T) {
	containers := map[string][]api.ContainerSummary{
		"web":   {{Name: "jarvis-web-1", State: "running"}},
		"cache": {{Name: "jarvis-cache-1", State: "exited", ExitCode: 1}},
	}
	assert.NilError(t, failedBuiltContainer(containers, []string{"web"}), "a pulled service is not the deployment's")

	for _, broken := range []api.ContainerSummary{
		{Name: "jarvis-web-1", State: "exited", ExitCode: 3},
		{Name: "jarvis-web-1", State: "restarting"},
		{Name: "jarvis-web-1", State: "running", Health: "unhealthy"},
	} {
		containers["web"] = []api.ContainerSummary{broken}
		assert.Assert(t, failedBuiltContainer(containers, []string{"web"}) != nil, "%+v", broken)
	}

	containers["web"] = []api.ContainerSummary{{Name: "jarvis-web-1", State: "exited", ExitCode: 0}}
	assert.NilError(t, failedBuiltContainer(containers, []string{"web"}), "a one-shot that finished is fine")
}
