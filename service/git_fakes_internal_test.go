package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"gotest.tools/v3/assert"
)

// fakeGitRuntime stands in for Docker in the git apps' tests: it writes down what it was
// asked, in order, and fails what a test tells it to.
type fakeGitRuntime struct {
	mu sync.Mutex

	projects  map[string]string
	buildErr  error
	startErrs []error
	retagErrs map[string]error
	missing   map[string]bool
	// gate, when set, is told a build starts, then holds it until the test sends a value.
	gate chan struct{}

	calls   []string
	removed []string
}

func withFakeGitDocker(t *testing.T) *fakeGitRuntime {
	t.Helper()

	fake := &fakeGitRuntime{projects: map[string]string{}, retagErrs: map[string]error{}, missing: map[string]bool{}}
	previous := gitDocker
	gitDocker = fake
	t.Cleanup(func() { gitDocker = previous })

	return fake
}

func (f *fakeGitRuntime) call(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeGitRuntime) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string{}, f.calls...)
}

func (f *fakeGitRuntime) Projects(context.Context) (map[string]string, error) {
	return f.projects, nil
}

func (f *fakeGitRuntime) Build(_ context.Context, app, root, _, commit string, out io.Writer) (map[string]string, error) {
	f.call("build %s", commit[:12])
	if f.gate != nil {
		f.gate <- struct{}{}
		<-f.gate
	}
	fmt.Fprintf(out, "building %s\n", commit[:12])
	if _, err := os.Stat(filepath.Join(root, "compose.yaml")); err != nil {
		return nil, fmt.Errorf("the build was not given the commit's files: %w", err)
	}
	if f.buildErr != nil {
		return nil, f.buildErr
	}

	return map[string]string{"web": app + "-web:git-" + commit[:12]}, nil
}

func (f *fakeGitRuntime) Retag(_ context.Context, _, _, commit string) error {
	f.call("retag %s", commit[:12])

	return f.retagErrs[commit]
}

func (f *fakeGitRuntime) TagRunning(_ context.Context, app, _, commit string) (map[string]string, error) {
	f.call("tag running %s", commit[:12])

	return map[string]string{"web": app + "-web:git-" + commit[:12]}, nil
}

// Start writes down the commit the folder is on when it is started.
func (f *fakeGitRuntime) Start(ctx context.Context, _, dir string) error {
	info, err := git.Describe(ctx, dir)
	if err != nil {
		return err
	}
	f.call("start %s", info.Head[:12])

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.startErrs) == 0 {
		return nil
	}
	err, f.startErrs = f.startErrs[0], f.startErrs[1:]

	return err
}

func (f *fakeGitRuntime) Stop(context.Context, string) error {
	f.call("stop")

	return nil
}

func (f *fakeGitRuntime) Remove(context.Context, string) error {
	f.call("remove")

	return nil
}

func (f *fakeGitRuntime) ImagesExist(_ context.Context, images map[string]string) bool {
	for _, image := range images {
		if f.missing[image] {
			return false
		}
	}

	return true
}

func (f *fakeGitRuntime) RemoveImages(_ context.Context, images []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, images...)
}

// waitForGitApp waits until nothing holds the app any more, a check and the deployment
// it started included, and returns the app's state.
func waitForGitApp(t *testing.T, name string) *gitApp {
	t.Helper()

	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if end, err := Begin(name, "test"); err == nil {
			end()
			st, err := loadGitApp(name)
			assert.NilError(t, err)

			return st
		}
	}
	t.Fatal("the app is still busy")

	return nil
}

// gitShell runs a test's setup the way a person would type it.
func gitShell(t *testing.T, dir, script string) string {
	t.Helper()

	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))

	return strings.TrimSpace(string(out))
}

// testComposeFile is the repository's app: one service built from the repository.
const testComposeFile = "services:\n  web:\n    build: .\n    ports:\n      - \"8080:8080\"\n"

// newTestRemote is a bare repository whose branch main holds a compose file, and a work
// tree that pushes to it.
func newTestRemote(t *testing.T) (url, work string) {
	t.Helper()

	root := t.TempDir()
	gitShell(t, root, "git init -q --bare --initial-branch=main remote.git && git clone -q remote.git work 2>/dev/null")
	work = filepath.Join(root, "work")
	gitShell(t, work, "git symbolic-ref HEAD refs/heads/main")
	pushTestCommit(t, work, "compose.yaml", testComposeFile)

	return "file://" + filepath.Join(root, "remote.git"), work
}

// pushTestCommit commits one file, pushes it, and returns the commit.
func pushTestCommit(t *testing.T, work, name, content string) string {
	t.Helper()

	assert.NilError(t, os.WriteFile(filepath.Join(work, name), []byte(content), 0o644))

	return gitShell(t, work, "git add -A && git commit -qm 'change "+name+"' && git push -q origin main && git rev-parse HEAD")
}
