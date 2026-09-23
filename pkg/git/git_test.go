package git

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// ls-remote prints an annotated tag twice: the tag object, then the commit on its peeled
// line, which is the one kept. A name git would read as an option never comes back.
func TestLsRemoteTagsGivesEachTagTheCommitItNames(t *testing.T) {
	ctx := context.Background()
	url, work := newRemote(t)

	tags, err := LsRemoteTags(ctx, url, Auth{})
	assert.NilError(t, err)
	assert.DeepEqual(t, tags, map[string]string{})

	first := sh(t, work, "git rev-parse HEAD")
	sh(t, work, "git tag -a -m one v1.0.0 && git tag light && git push -q origin --tags")
	second := push(t, work, "file", "two")
	// a tag of a tag peels down to the commit
	sh(t, work, "git tag -a -m two v1.1.0 && git tag -a -m nested nested v1.1.0 && git push -q origin --tags")
	// written in the remote by hand: `git tag` refuses a leading dash, a ref does not
	sh(t, strings.TrimPrefix(url, "file://"), "git update-ref refs/tags/-evil "+first)

	tags, err = LsRemoteTags(ctx, url, Auth{})
	assert.NilError(t, err)
	assert.DeepEqual(t, tags, map[string]string{"v1.0.0": first, "light": first, "v1.1.0": second, "nested": second})
}

// A hostile remote advertises a tag whose name holds a newline: ls-remote prints the rest of
// the name as a line of its own, where a strict semver tag names what the remote wrote in
// place of a commit hash. That line is left out.
func TestATagListingLineWithoutACommitHashIsLeftOut(t *testing.T) {
	// the remote is a fake upload-pack reached through ext::, which git refuses unless told
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.ext.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")

	oid := strings.Repeat("1", 40)
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	dir := t.TempDir()
	advert := filepath.Join(dir, "advert")
	assert.NilError(t, os.WriteFile(advert, []byte(pkt(oid+" HEAD\x00multi_ack\n")+
		pkt(oid+" refs/tags/v1.0.0\n--output=pwned\trefs/tags/v99.0.0\n")+"0000"), 0o644))
	server := filepath.Join(dir, "upload-pack.sh")
	assert.NilError(t, os.WriteFile(server, []byte("cat '"+advert+"'\ncat >/dev/null\n"), 0o755))

	tags, err := LsRemoteTags(context.Background(), "ext::sh "+server, Auth{})
	assert.NilError(t, err)
	assert.DeepEqual(t, tags, map[string]string{"v1.0.0": oid})
}

// A clone at a tag is in detached HEAD, a tag fetched is its commit, and a commit no ref of
// the clone names is fetched by its hash when the remote has it.
func TestAFetchedTagIsItsCommitAndACommitIsFetchedByItsHash(t *testing.T) {
	ctx := context.Background()
	url, work := newRemote(t)
	first := sh(t, work, "git rev-parse HEAD")
	sh(t, work, "git tag -a -m one v1.0.0 && git push -q origin v1.0.0")

	dir := filepath.Join(t.TempDir(), "app")
	assert.NilError(t, Clone(ctx, url, "v1.0.0", dir, Auth{}))
	info, err := Describe(ctx, dir)
	assert.NilError(t, err)
	assert.Equal(t, info.Branch, "", "a clone at a tag is in detached HEAD")
	assert.Equal(t, info.Head, first)

	second := push(t, work, "file", "two")
	sh(t, work, "git tag -a -m two v1.1.0 && git push -q origin v1.1.0")
	fetched, err := FetchTag(ctx, dir, url, "v1.1.0", Auth{})
	assert.NilError(t, err)
	assert.Equal(t, fetched, second, "the commit, never the tag object")
	_, err = FetchTag(ctx, dir, url, "v9.9.9", Auth{})
	assert.Assert(t, err != nil, "a tag the remote does not have")

	// a detached HEAD moves to any commit
	assert.NilError(t, ResetKeep(ctx, dir, second))
	info, err = Describe(ctx, dir)
	assert.NilError(t, err)
	assert.Equal(t, info.Head, second)
	assert.Equal(t, info.Branch, "")

	third := push(t, work, "file", "three")
	assert.Assert(t, !HasCommit(ctx, dir, third))
	assert.NilError(t, FetchCommit(ctx, dir, url, third, Auth{}))
	assert.Assert(t, HasCommit(ctx, dir, third))
	assert.Assert(t, FetchCommit(ctx, dir, url, strings.Repeat("a", 40), Auth{}) != nil, "a commit the remote does not have")
}

func TestDetachAndAttachMoveNoFile(t *testing.T) {
	ctx := context.Background()
	url, work := newRemote(t)
	head := sh(t, work, "git rev-parse HEAD")
	dir := filepath.Join(t.TempDir(), "app")
	assert.NilError(t, Clone(ctx, url, "main", dir, Auth{}))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "file"), []byte("edited here"), 0o644))

	assert.NilError(t, Detach(ctx, dir))
	info, err := Describe(ctx, dir)
	assert.NilError(t, err)
	assert.Equal(t, info.Branch, "")
	assert.Equal(t, info.Head, head)

	assert.NilError(t, Attach(ctx, dir, "release"))
	info, err = Describe(ctx, dir)
	assert.NilError(t, err)
	assert.Equal(t, info.Branch, "release")
	assert.Equal(t, info.Head, head)

	content, err := os.ReadFile(filepath.Join(dir, "file"))
	assert.NilError(t, err)
	assert.Equal(t, string(content), "edited here")
}

// A kept commit survives what git collects once no other ref names it, and goes once dropped.
func TestAKeptCommitOutlivesTheRefsThatBroughtIt(t *testing.T) {
	ctx := context.Background()
	url, work := newRemote(t)
	first := sh(t, work, "git rev-parse HEAD")
	dir := filepath.Join(t.TempDir(), "app")
	assert.NilError(t, Clone(ctx, url, "main", dir, Auth{}))
	// fetched, and named by no ref of the clone
	second := push(t, work, "file", "two")
	_, err := Fetch(ctx, dir, url, "main", Auth{})
	assert.NilError(t, err)
	collect := func() {
		t.Helper()
		sh(t, dir, "rm -f .git/FETCH_HEAD && git reflog expire --expire=now --all && git gc -q --prune=now")
	}

	assert.NilError(t, KeepCommit(ctx, dir, first))
	assert.NilError(t, KeepCommit(ctx, dir, second))
	kept, err := KeptCommits(ctx, dir)
	assert.NilError(t, err)
	sort.Strings(kept)
	want := []string{first, second}
	sort.Strings(want)
	assert.DeepEqual(t, kept, want)

	collect()
	assert.Assert(t, HasCommit(ctx, dir, second), "a kept commit stays")

	assert.NilError(t, DropKeptCommit(ctx, dir, second))
	kept, err = KeptCommits(ctx, dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, kept, []string{first})
	collect()
	assert.Assert(t, !HasCommit(ctx, dir, second), "and goes once dropped")
}
