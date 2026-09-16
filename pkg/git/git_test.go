package git

import (
	"context"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"gotest.tools/v3/assert"
)

// sh runs a test's setup the way a person would type it.
func sh(t *testing.T, dir, script string) string {
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

// newRemote is a bare repository whose branch main holds one commit, and a work tree
// that pushes to it.
func newRemote(t *testing.T) (url, work string) {
	t.Helper()

	root := t.TempDir()
	sh(t, root, "git init -q --bare --initial-branch=main remote.git && git clone -q remote.git work 2>/dev/null")
	work = filepath.Join(root, "work")
	sh(t, work, "git symbolic-ref HEAD refs/heads/main && echo one > file && git add file && git commit -qm one && git push -q origin main")

	return "file://" + filepath.Join(root, "remote.git"), work
}

// push commits a file in the work tree, pushes it, and returns the commit.
func push(t *testing.T, work, name, content string) string {
	t.Helper()

	assert.NilError(t, os.MkdirAll(filepath.Dir(filepath.Join(work, name)), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(work, name), []byte(content), 0o644))

	return sh(t, work, "git add -A && git commit -qm "+filepath.Base(name)+" && git push -q origin main && git rev-parse HEAD")
}

func TestCloneDescribesTheDefaultBranch(t *testing.T) {
	ctx := context.Background()
	url, work := newRemote(t)
	head := sh(t, work, "git rev-parse HEAD")

	branch, err := DefaultBranch(ctx, url, Auth{})
	assert.NilError(t, err)
	assert.Equal(t, branch, "main")

	remote, err := LsRemote(ctx, url, "main", Auth{})
	assert.NilError(t, err)
	assert.Equal(t, remote, head)

	_, err = LsRemote(ctx, url, "nope", Auth{})
	assert.ErrorContains(t, err, "does not exist")

	dir := filepath.Join(t.TempDir(), "app")
	assert.NilError(t, Clone(ctx, url, "main", dir, Auth{}))

	info, err := Describe(ctx, dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, info, Info{Toplevel: dir, RemoteURL: url, Branch: "main", Head: head, TrackedFilesClean: true})

	subject, err := Subject(ctx, dir, head)
	assert.NilError(t, err)
	assert.Equal(t, subject, "one")
	assert.Assert(t, HasCommit(ctx, dir, head))
	assert.Assert(t, !HasCommit(ctx, dir, strings.Repeat("0", 40)))
}

func TestFetchThenAncestry(t *testing.T) {
	ctx := context.Background()
	url, work := newRemote(t)
	first := sh(t, work, "git rev-parse HEAD")
	dir := filepath.Join(t.TempDir(), "app")
	assert.NilError(t, Clone(ctx, url, "main", dir, Auth{}))

	second := push(t, work, "file", "two")

	fetched, err := Fetch(ctx, dir, url, "main", Auth{})
	assert.NilError(t, err)
	assert.Equal(t, fetched, second)

	forward, err := IsAncestor(ctx, dir, first, second)
	assert.NilError(t, err)
	assert.Assert(t, forward)

	backward, err := IsAncestor(ctx, dir, second, first)
	assert.NilError(t, err)
	assert.Assert(t, !backward)

	assert.NilError(t, FastForward(ctx, dir, second))
	content, err := os.ReadFile(filepath.Join(dir, "file"))
	assert.NilError(t, err)
	assert.Equal(t, string(content), "two")

	assert.NilError(t, ResetKeep(ctx, dir, first))
	info, err := Describe(ctx, dir)
	assert.NilError(t, err)
	assert.Equal(t, info.Head, first)
}

func TestFastForwardRefusesADivergedOrModifiedTree(t *testing.T) {
	ctx := context.Background()
	url, work := newRemote(t)

	modified := filepath.Join(t.TempDir(), "modified")
	assert.NilError(t, Clone(ctx, url, "main", modified, Auth{}))
	diverged := filepath.Join(t.TempDir(), "diverged")
	assert.NilError(t, Clone(ctx, url, "main", diverged, Auth{}))

	second := push(t, work, "file", "two")

	assert.NilError(t, os.WriteFile(filepath.Join(modified, "file"), []byte("edited here"), 0o644))
	_, err := Fetch(ctx, modified, url, "main", Auth{})
	assert.NilError(t, err)
	info, err := Describe(ctx, modified)
	assert.NilError(t, err)
	assert.Assert(t, !info.TrackedFilesClean)
	assert.Assert(t, FastForward(ctx, modified, second) != nil, "a modified tracked file is never overwritten")

	sh(t, diverged, "echo local > other && git add other && git commit -qm local")
	_, err = Fetch(ctx, diverged, url, "main", Auth{})
	assert.NilError(t, err)
	assert.Assert(t, FastForward(ctx, diverged, second) != nil, "a local commit is never merged")
}

func TestIsTrackedTellsATrackedEnvFromAnUntrackedOne(t *testing.T) {
	ctx := context.Background()
	url, work := newRemote(t)
	push(t, work, ".env", "KEY=value\n")

	dir := filepath.Join(t.TempDir(), "app")
	assert.NilError(t, Clone(ctx, url, "main", dir, Auth{}))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".env.local"), []byte("X=1\n"), 0o644))

	tracked, err := IsTracked(ctx, dir, ".env")
	assert.NilError(t, err)
	assert.Assert(t, tracked)

	tracked, err = IsTracked(ctx, dir, ".env.local")
	assert.NilError(t, err)
	assert.Assert(t, !tracked)
}

// A worktree carries the submodules, and neither it nor a fast-forward runs a hook of
// the repository: an adopted repository's hooks would run as root.
func TestWorktreeWithASubmoduleRunsNoHook(t *testing.T) {
	ctx := context.Background()
	// git refuses submodules over file:// unless told otherwise
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")

	subURL, _ := newRemote(t)
	url, work := newRemote(t)
	sh(t, work, "git submodule add -q "+subURL+" sub && git commit -qm sub && git push -q origin main")
	head := sh(t, work, "git rev-parse HEAD")

	dir := filepath.Join(t.TempDir(), "app")
	assert.NilError(t, Clone(ctx, url, "main", dir, Auth{}))
	marker := filepath.Join(t.TempDir(), "hook-ran")
	for _, hook := range []string{"post-checkout", "post-merge"} {
		assert.NilError(t, os.WriteFile(filepath.Join(dir, ".git", "hooks", hook), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755))
	}

	path := filepath.Join(t.TempDir(), "work", "app-"+head[:12])
	assert.NilError(t, AddWorktree(ctx, dir, head, path, Auth{}))
	content, err := os.ReadFile(filepath.Join(path, "sub", "file"))
	assert.NilError(t, err)
	assert.Equal(t, string(content), "one\n")

	second := push(t, work, "file", "two")
	_, err = Fetch(ctx, dir, url, "main", Auth{})
	assert.NilError(t, err)
	assert.NilError(t, FastForward(ctx, dir, second))

	_, err = os.Stat(marker)
	assert.Assert(t, os.IsNotExist(err), "no hook may run")

	assert.NilError(t, RemoveWorktree(ctx, dir, path))
	_, err = os.Stat(path)
	assert.Assert(t, os.IsNotExist(err))
	assert.Equal(t, sh(t, dir, "git worktree list | wc -l"), "1")
}

// The token reaches git through the askpass helper only: a server that wants it gets
// it, and no argument git was started with carries it. The helper is written where it is
// told, never under /tmp, and goes with its command.
func TestATokenReachesGitThroughAskpassOnly(t *testing.T) {
	ctx := context.Background()
	const token = "ghp_s3cr3t-token-for-the-test"

	_, work := newRemote(t)
	root := filepath.Dir(work)
	backend := &cgi.Handler{
		Path: "/usr/lib/git-core/git-http-backend",
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, password, ok := r.BasicAuth(); !ok || password != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()

	// every git started from here writes down its askpass helper and its arguments
	realGit, err := exec.LookPath("git")
	assert.NilError(t, err)
	bin := t.TempDir()
	argsLog := filepath.Join(t.TempDir(), "args")
	wrapper := "#!/bin/sh\nprintf '%s %s\\n' \"$GIT_ASKPASS\" \"$*\" >> " + argsLog + "\nexec " + realGit + " \"$@\"\n"
	assert.NilError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o755))
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	url := server.URL + "/remote.git"
	helpers := t.TempDir()
	auth := Auth{Mode: AccessToken, Token: token, AskpassDir: helpers}

	commit, err := LsRemote(ctx, url, "main", auth)
	assert.NilError(t, err)
	assert.Equal(t, commit, sh(t, work, "git rev-parse HEAD"))

	dir := filepath.Join(t.TempDir(), "app")
	assert.NilError(t, Clone(ctx, url, "main", dir, auth))

	_, err = LsRemote(ctx, url, "main", Auth{Mode: AccessToken, Token: "wrong", AskpassDir: helpers})
	assert.Assert(t, err != nil, "a wrong token is refused")

	args, err := os.ReadFile(argsLog)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(args), "ls-remote"))
	assert.Assert(t, !strings.Contains(string(args), token), "the token is in no argument")
	for _, line := range strings.Split(strings.TrimSpace(string(args)), "\n") {
		// the commands of this package, not the test's own
		if strings.Contains(line, "credential.helper=") {
			assert.Assert(t, strings.HasPrefix(line, filepath.Join(helpers, "askpass-")), "the helper is where it was told to be: %s", line)
		}
	}
	left, err := os.ReadDir(helpers)
	assert.NilError(t, err)
	assert.Equal(t, len(left), 0, "each helper goes with its command")

	config, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(config), token), "nor in .git/config")
}

// Whatever git prints is given back with the token blanked out.
func TestCapturedOutputNeverCarriesTheToken(t *testing.T) {
	const token = "ghp_printed-by-a-broken-git"

	bin := t.TempDir()
	loud := "#!/bin/sh\necho \"fatal: could not read from $CASAOS_GIT_TOKEN\" >&2\nexit 128\n"
	assert.NilError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(loud), 0o755))
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	_, err := LsRemote(context.Background(), "https://example.invalid/repo.git", "main", Auth{Mode: AccessToken, Token: token, AskpassDir: t.TempDir()})
	assert.ErrorContains(t, err, "could not read from ***")
	assert.Assert(t, !strings.Contains(err.Error(), token))
}

func TestADeployKeyNeedsAnSSHClient(t *testing.T) {
	realGit, err := exec.LookPath("git")
	assert.NilError(t, err)
	bin := t.TempDir()
	assert.NilError(t, os.Symlink(realGit, filepath.Join(bin, "git")))
	t.Setenv("PATH", bin)

	_, err = LsRemote(context.Background(), "git@github.com:owner/repo.git", "main", Auth{Mode: AccessKey, KeyPath: "/nonexistent", KnownHostsPath: "/nonexistent"})
	assert.Assert(t, errors.Is(err, ErrNoSSHClient), err)
	assert.Equal(t, err.Error(), "the ssh client is not installed on this machine")
}

// AppManagement runs as root, and a stack started by hand lives in its owner's folder:
// what a fetch and a fast-forward write there goes back to that owner.
func TestWhatRootWritesInSomebodysFolderGoesBackToThem(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root, as AppManagement runs")
	}
	ctx := context.Background()
	url, work := newRemote(t)
	dir := filepath.Join(t.TempDir(), "app")
	assert.NilError(t, Clone(ctx, url, "main", dir, Auth{}))
	sh(t, dir, "chown -R 1000:1000 .")

	second := push(t, work, "deep/new/file", "two")

	// the folder belongs to someone else: without safe.directory both would refuse
	_, err := Fetch(ctx, dir, url, "main", Auth{})
	assert.NilError(t, err)
	assert.NilError(t, FastForward(ctx, dir, second))

	for _, path := range []string{"deep", "deep/new", "deep/new/file", ".git/index", ".git/FETCH_HEAD", ".git/refs/heads/main"} {
		info, err := os.Lstat(filepath.Join(dir, path))
		assert.NilError(t, err, path)
		assert.Equal(t, info.Sys().(*syscall.Stat_t).Uid, uint32(1000), path)
	}
	assert.Equal(t, sh(t, dir, "find .git ! -uid 1000 | wc -l"), "0")
}
