package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

// A stack somebody started by hand with `docker compose up`: a service built from a
// Dockerfile, a relative env_file, and an image from a registry beside it.
const handWrittenStack = `services:
  jarvis:
    build: .
    env_file: .env
    ports:
      - "8080:8080"
  redis:
    image: redis:7
`

const handWrittenOverride = "services:\n  jarvis:\n    environment:\n      DEBUG: \"1\"\n"

func writeStack(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "jarvis")
	assert.NilError(t, os.MkdirAll(dir, 0o755))
	for name, body := range files {
		assert.NilError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}

	return dir
}

// compose records every file a stack was started from on its containers, comma-separated:
// an override beside the main file is loaded without being named, `-f a -f b` names both.
// Read as one directory name, that list made the stack vanish from the compose list.
func TestLoadComposeAppFromConfigFileReadsEveryFileComposeRecorded(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"docker-compose.yml":          handWrittenStack,
		"docker-compose.override.yml": handWrittenOverride,
		".env":                        "GREETING=hello\n",
		"Dockerfile":                  "FROM busybox\n",
	})
	main := filepath.Join(dir, "docker-compose.yml")
	override := filepath.Join(dir, "docker-compose.override.yml")

	app, err := LoadComposeAppFromConfigFile("jarvis", main+","+override)
	assert.NilError(t, err)

	assert.DeepEqual(t, app.ComposeFiles, []string{main, override})
	assert.Equal(t, app.WorkingDir, dir)

	debug := app.Services["jarvis"].Environment["DEBUG"]
	assert.Assert(t, debug != nil && *debug == "1", "the override is merged in")

	greeting := app.Services["jarvis"].Environment["GREETING"]
	assert.Assert(t, greeting != nil && *greeting == "hello", "the env_file is read in the stack's own folder")
}

// Left to find a default name in the folder, compose refused a stack started with
// `-f jarvis.yml`.
func TestLoadComposeAppFromConfigFileTakesAnyFileName(t *testing.T) {
	dir := writeStack(t, map[string]string{"jarvis.yml": handWrittenStack, ".env": "GREETING=hello\n"})

	app, err := LoadComposeAppFromConfigFile("jarvis", filepath.Join(dir, "jarvis.yml"))
	assert.NilError(t, err)
	assert.Equal(t, len(app.Services), 2)
}

// No file named at all is an error, not a search of whatever folder the service runs in.
func TestLoadComposeAppFromConfigFileWithoutAFile(t *testing.T) {
	_, err := LoadComposeAppFromConfigFile("jarvis", " , ")
	assert.Assert(t, errors.Is(err, ErrComposeFileNotFound), err)
}

// ComposeApp.apply parses the text it is about to write before writing it, for a settings
// save and for a .env save. In a temporary folder the relative env_file was looked for
// there, and every save of an app that has one failed.
func TestApplyParsesTheFileInTheAppFolder(t *testing.T) {
	logger.LogInitConsoleOnly()

	dir := writeStack(t, map[string]string{"docker-compose.yml": handWrittenStack, ".env": "GREETING=hello\n"})

	_, err := newComposeAppFromYAML([]byte(handWrittenStack), true, true, nil, dir)
	assert.NilError(t, err)
}

// The .env save re-applies the app's own compose file. A stack started by hand rarely
// declares `name:`, and its file was refused as belonging to another app.
func TestComposeYAMLNamesAnotherApp(t *testing.T) {
	assert.Assert(t, !composeYAMLNamesAnotherApp([]byte(handWrittenStack), "jarvis"))
	assert.Assert(t, !composeYAMLNamesAnotherApp([]byte("name: jarvis\n"+handWrittenStack), "jarvis"))
	assert.Assert(t, composeYAMLNamesAnotherApp([]byte("name: other\n"+handWrittenStack), "jarvis"))
}

// CasaOS never builds: a pull of a service built on this box would fetch nothing, or worse,
// replace the build with a registry image of the same name.
func TestPulledServiceNamesLeavesOutWhatIsBuiltHere(t *testing.T) {
	dir := writeStack(t, map[string]string{"docker-compose.yml": handWrittenStack, ".env": "GREETING=hello\n"})

	app, err := LoadComposeAppFromConfigFile("jarvis", filepath.Join(dir, "docker-compose.yml"))
	assert.NilError(t, err)

	assert.DeepEqual(t, pulledServiceNames(app.Services), []string{"redis"})
}

// A backup of a stack started from several files keeps the first and says it leaves the
// others out, instead of dropping them in silence.
func TestBackupInventoryNamesTheComposeFilesItLeavesOut(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"docker-compose.yml":          handWrittenStack,
		"docker-compose.override.yml": handWrittenOverride,
		".env":                        "GREETING=hello\n",
	})
	main := filepath.Join(dir, "docker-compose.yml")
	override := filepath.Join(dir, "docker-compose.override.yml")

	app, err := LoadComposeAppFromConfigFile("jarvis", main+","+override)
	assert.NilError(t, err)

	composeEntries := []BackupEntry{}
	for _, entry := range app.BackupInventory() {
		if entry.Kind == BackupKindCompose {
			composeEntries = append(composeEntries, entry)
		}
	}

	assert.Equal(t, len(composeEntries), 2)
	assert.Equal(t, composeEntries[0].Path, filepath.ToSlash(main))
	assert.Equal(t, composeEntries[0].Skip, "")
	assert.Equal(t, composeEntries[1].Path, filepath.ToSlash(override))
	assert.Assert(t, composeEntries[1].Skip != "")
}
