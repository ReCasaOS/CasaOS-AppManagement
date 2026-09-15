package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

// compose-go v2.1.0 refused each of these keys ("Additional property ... is not allowed"), and a
// project it refuses disappears from CasaOS, while the Docker Compose the hosts run accepts them.
func TestComposeKeysNewerThanComposeGoV21Load(t *testing.T) {
	logger.LogInitConsoleOnly()
	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)
	const compose = `name: newer-keys
services:
  app:
    image: alpine:3.20
    post_start:
      - command: ["sh", "-c", "echo started"]
    label_file: ./app.labels
    gpus: all
    models:
      llm:
        endpoint_var: LLM_URL
models:
  llm:
    model: ai/smollm2
`
	assert.NilError(t, os.WriteFile(composeFile, []byte(compose), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "app.labels"), []byte("com.example.from-file=yes\n"), 0o600))

	app, err := service.LoadComposeAppFromConfigFile("newer-keys", composeFile)
	assert.NilError(t, err)

	svc := app.Services["app"]
	assert.Equal(t, len(svc.PostStart), 1)
	assert.Equal(t, strings.Join(svc.PostStart[0].Command, " "), "sh -c echo started")
	assert.Equal(t, svc.Labels["com.example.from-file"], "yes")
	assert.Equal(t, len(svc.Gpus), 1)
	assert.Equal(t, int64(svc.Gpus[0].Count), int64(-1), "gpus: all")
	assert.Assert(t, svc.Models["llm"] != nil)
	assert.Equal(t, svc.Models["llm"].EndpointVariable, "LLM_URL")
	assert.Equal(t, app.Models["llm"].Model, "ai/smollm2")
}
