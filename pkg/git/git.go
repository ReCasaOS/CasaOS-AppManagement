// Package git runs the host's git binary for apps deployed from their repository. It
// knows nothing about CasaOS apps: a folder, a URL, a branch and a way in.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// How a remote is reached.
const (
	AccessNone  = "none"
	AccessKey   = "key"
	AccessToken = "token"
)

const (
	lsRemoteTimeout = 30 * time.Second
	networkTimeout  = 10 * time.Minute
	// ponytail: one minute for every local command, which none of them comes near; a
	// budget per command when a repository of hundreds of thousands of files shows up
	localTimeout = time.Minute
)

// ErrNoSSHClient is the answer for a deploy key on a machine that cannot use one.
var ErrNoSSHClient = errors.New("the ssh client is not installed on this machine")

// Auth is how a remote is reached. It becomes environment, never arguments.
type Auth struct {
	Mode           string
	KeyPath        string
	KnownHostsPath string
	Token          string
	// AskpassDir is where a token's askpass helper is written, in a folder of its own that
	// goes after each command. Not /tmp, which may be mounted noexec.
	AskpassDir string
}

// Info is what a work tree says about itself.
type Info struct {
	Toplevel  string
	RemoteURL string
	// Branch is empty in detached HEAD.
	Branch string
	// Head is empty before the first commit.
	Head              string
	TrackedFilesClean bool
}

// Error is a git command that failed, with what it printed. A token never appears in it.
type Error struct {
	Command string
	Output  string
	Err     error
}

func (e *Error) Error() string {
	if e.Output == "" {
		return fmt.Sprintf("git %s: %v", e.Command, e.Err)
	}

	return fmt.Sprintf("git %s: %s", e.Command, e.Output)
}

func (e *Error) Unwrap() error { return e.Err }

// askpassScript answers git's two questions: a user name hosting services accept with a
// token, and the token. A user name written in the URL is used instead, and git then
// only asks for the password.
const askpassScript = `#!/bin/sh
case "$1" in
Username*) echo x-access-token ;;
*) printf '%s\n' "$CASAOS_GIT_TOKEN" ;;
esac
`

// environ is the environment a command reaches the remote with, and what removes the
// askpass helper afterwards.
func (a Auth) environ() ([]string, func(), error) {
	switch a.Mode {
	case AccessKey:
		if _, err := exec.LookPath("ssh"); err != nil {
			return nil, func() {}, ErrNoSSHClient
		}

		// accept-new records the server's key on the first connection; a changed key fails
		return []string{"GIT_SSH_COMMAND=ssh -i " + a.KeyPath + " -o IdentitiesOnly=yes -o UserKnownHostsFile=" + a.KnownHostsPath + " -o StrictHostKeyChecking=accept-new"}, func() {}, nil

	case AccessToken:
		if a.AskpassDir == "" {
			return nil, func() {}, errors.New("no folder to write the askpass helper in")
		}

		dir, err := os.MkdirTemp(a.AskpassDir, "askpass-") // 0700
		if err != nil {
			return nil, func() {}, err
		}
		cleanup := func() { _ = os.RemoveAll(dir) }

		helper := filepath.Join(dir, "askpass")
		if err := os.WriteFile(helper, []byte(askpassScript), 0o700); err != nil {
			cleanup()
			return nil, func() {}, err
		}

		return []string{"GIT_ASKPASS=" + helper, "CASAOS_GIT_TOKEN=" + a.Token}, cleanup, nil
	}

	return nil, func() {}, nil
}

func (a Auth) redact(s string) string {
	if a.Token == "" {
		return s
	}

	return strings.ReplaceAll(s, a.Token, "***")
}

// run runs one git command. No stored credential helper answers, no hook of the
// repository runs, and a folder owned by someone else is trusted for this command only.
func run(ctx context.Context, timeout time.Duration, dir string, auth Auth, args ...string) (string, error) {
	env, cleanup, err := auth.environ()
	if err != nil {
		return "", err
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	full := []string{"-c", "credential.helper=", "-c", "core.hooksPath=/dev/null"}
	if dir != "" {
		full = append(full, "-c", "safe.directory="+dir, "-C", dir)
	}

	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	// the timeout kills git, but an ssh or a git-remote-https it started can keep the output
	// pipes open, and Run would wait for them
	cmd.WaitDelay = 5 * time.Second
	// GIT_OPTIONAL_LOCKS=0: a status asked for while a deployment moves the same work tree
	// must not hold the index lock the fast-forward needs
	cmd.Env = append(append(os.Environ(), env...), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err = cmd.Run()
	out := strings.TrimSpace(auth.redact(stdout.String()))
	if err == nil {
		return out, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("timed out after %s: %w", timeout, err)
	}

	return out, &Error{Command: args[0], Output: strings.TrimSpace(auth.redact(stderr.String())), Err: err}
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}

	return -1
}

// ValidRefName is a name git reads as a branch or a tag name: not an option, not a revision
// expression (`..`, `@`, `@{`, `~`, `^`), and no character a ref name forbids. Empty passes: a
// caller that needs a name checks that itself.
func ValidRefName(name string) bool {
	return !strings.HasPrefix(name, "-") && name != "@" && !strings.Contains(name, "..") && !strings.Contains(name, "@{") &&
		!strings.ContainsAny(name, " ~^:?*[\\") && !strings.ContainsFunc(name, unicode.IsControl)
}

// LsRemote is the commit the branch points at on the remote.
func LsRemote(ctx context.Context, url, branch string, auth Auth) (string, error) {
	out, err := run(ctx, lsRemoteTimeout, "", auth, "ls-remote", "--", url, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}

	commit, _, _ := strings.Cut(out, "\t")
	if commit == "" {
		return "", fmt.Errorf("the branch %s does not exist on the remote", branch)
	}

	return commit, nil
}

// DefaultBranch is the branch the remote's HEAD points to.
func DefaultBranch(ctx context.Context, url string, auth Auth) (string, error) {
	out, err := run(ctx, lsRemoteTimeout, "", auth, "ls-remote", "--symref", "--", url, "HEAD")
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(out, "\n") {
		if ref, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			branch, _, _ := strings.Cut(ref, "\t")
			return branch, nil
		}
	}

	return "", errors.New("the remote does not say which branch its HEAD points to")
}

// LsRemoteTags is every tag of the remote and the commit it names. An annotated tag comes
// twice: its plain line names the tag object, its peeled line `<name>^{}` the commit, which
// wins. A name that is no valid ref name is left out, so that it never reaches git. So is a
// line whose hash is no commit hash: a ref name with a newline in it, which a hostile remote
// can advertise, makes ls-remote print a line of its own that starts with anything.
func LsRemoteTags(ctx context.Context, url string, auth Auth) (map[string]string, error) {
	out, err := run(ctx, lsRemoteTimeout, "", auth, "ls-remote", "--tags", "--", url)
	if err != nil {
		return nil, err
	}

	tags := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		hash, ref, _ := strings.Cut(line, "\t")
		if len(hash) != 40 || strings.Trim(hash, "0123456789abcdef") != "" {
			continue
		}
		name, ok := strings.CutPrefix(ref, "refs/tags/")
		if !ok {
			continue
		}
		name, peeled := strings.CutSuffix(name, "^{}")
		if name == "" || !ValidRefName(name) {
			continue
		}
		if peeled || tags[name] == "" {
			tags[name] = hash
		}
	}

	return tags, nil
}

// Clone clones one branch of url into dir, or one tag, which leaves it in detached HEAD.
func Clone(ctx context.Context, url, branch, dir string, auth Auth) error {
	_, err := run(ctx, networkTimeout, "", auth, "clone", "--branch", branch, "--single-branch", "--", url, dir)

	return err
}

// Fetch fetches the branch from url into dir and returns the commit it points at.
func Fetch(ctx context.Context, dir, url, branch string, auth Auth) (string, error) {
	defer giveBack(dir, nil)

	if _, err := run(ctx, networkTimeout, dir, auth, "fetch", "--no-tags", "--", url, "refs/heads/"+branch); err != nil {
		return "", err
	}

	return run(ctx, localTimeout, dir, Auth{}, "rev-parse", "FETCH_HEAD")
}

// FetchTag fetches the tag from url into dir and returns the commit it names, an annotated
// tag peeled. No local ref is written.
func FetchTag(ctx context.Context, dir, url, tag string, auth Auth) (string, error) {
	defer giveBack(dir, nil)

	if _, err := run(ctx, networkTimeout, dir, auth, "fetch", "--no-tags", "--", url, "refs/tags/"+tag); err != nil {
		return "", err
	}

	return run(ctx, localTimeout, dir, Auth{}, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
}

// FetchCommit fetches commit itself from url into dir. Not every forge serves a commit by its
// hash: one that does not refuses it.
func FetchCommit(ctx context.Context, dir, url, commit string, auth Auth) error {
	defer giveBack(dir, nil)

	_, err := run(ctx, networkTimeout, dir, auth, "fetch", "--", url, commit)

	return err
}

// Describe says what the work tree at dir is on.
func Describe(ctx context.Context, dir string) (Info, error) {
	top, err := run(ctx, localTimeout, dir, Auth{}, "rev-parse", "--show-toplevel")
	if err != nil {
		return Info{}, err
	}
	info := Info{Toplevel: top}

	// both fail for a state that is an answer: detached HEAD, no commit yet
	info.Branch, _ = run(ctx, localTimeout, dir, Auth{}, "symbolic-ref", "--quiet", "--short", "HEAD")
	info.Head, _ = run(ctx, localTimeout, dir, Auth{}, "rev-parse", "--verify", "--quiet", "HEAD")

	remote := "origin"
	if info.Branch != "" {
		if configured, err := run(ctx, localTimeout, dir, Auth{}, "config", "--get", "branch."+info.Branch+".remote"); err == nil && configured != "" {
			remote = configured
		}
	}
	info.RemoteURL, _ = run(ctx, localTimeout, dir, Auth{}, "remote", "get-url", remote)

	status, err := run(ctx, localTimeout, dir, Auth{}, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return Info{}, err
	}
	info.TrackedFilesClean = status == ""

	return info, nil
}

// Subject is the first line of a commit's message.
func Subject(ctx context.Context, dir, commit string) (string, error) {
	return run(ctx, localTimeout, dir, Auth{}, "log", "-1", "--format=%s", commit, "--")
}

// HasCommit reports whether the repository at dir holds commit.
func HasCommit(ctx context.Context, dir, commit string) bool {
	_, err := run(ctx, localTimeout, dir, Auth{}, "cat-file", "-e", commit+"^{commit}")

	return err == nil
}

// IsTracked reports whether path is a tracked file of the work tree at dir.
func IsTracked(ctx context.Context, dir, path string) (bool, error) {
	_, err := run(ctx, localTimeout, dir, Auth{}, "ls-files", "--error-unmatch", "--", path)
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}

	return false, err
}

// IsAncestor reports whether ancestor is descendant or one of its ancestors.
func IsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
	_, err := run(ctx, localTimeout, dir, Auth{}, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}

	return false, err
}

// AddWorktree checks commit out, detached, at path, submodules included.
func AddWorktree(ctx context.Context, dir, commit, path string, auth Auth) error {
	defer giveBack(dir, nil)

	if _, err := run(ctx, localTimeout, dir, Auth{}, "worktree", "add", "--detach", "--", path, commit); err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(path, ".gitmodules")); err != nil {
		return nil
	}

	_, err := run(ctx, networkTimeout, path, auth, "submodule", "update", "--init", "--recursive")

	return err
}

// RemoveWorktree removes a worktree AddWorktree made, and what git knows of it.
func RemoveWorktree(ctx context.Context, dir, path string) error {
	defer giveBack(dir, nil)

	if _, err := run(ctx, localTimeout, dir, Auth{}, "worktree", "remove", "--force", "--", path); err == nil {
		return nil
	}

	// a folder git no longer knows, or one half made: removed by hand, then forgotten
	if err := os.RemoveAll(path); err != nil {
		return err
	}

	return Prune(ctx, dir)
}

// Prune forgets the worktrees whose folder is gone.
func Prune(ctx context.Context, dir string) error {
	defer giveBack(dir, nil)

	_, err := run(ctx, localTimeout, dir, Auth{}, "worktree", "prune")

	return err
}

// FastForward moves the checked-out branch forward to commit, and refuses anything else.
func FastForward(ctx context.Context, dir, commit string) error {
	return moveTo(ctx, dir, "merge", "--ff-only", commit)
}

// ResetKeep moves the checked-out branch, or a detached HEAD, to commit, keeping local
// changes git can keep. A rollback, a revert, a redeployment and every move of an app that
// follows tags use it: tags do not form a line.
func ResetKeep(ctx context.Context, dir, commit string) error {
	return moveTo(ctx, dir, "reset", "--keep", commit)
}

// Detach leaves the work tree at dir in detached HEAD on the commit it is on: no file and no
// branch moves.
func Detach(ctx context.Context, dir string) error {
	defer giveBack(dir, nil)

	_, err := run(ctx, localTimeout, dir, Auth{}, "checkout", "--quiet", "--detach")

	return err
}

// Attach puts the work tree at dir on branch, made or moved to the commit it is on: no file
// moves.
func Attach(ctx context.Context, dir, branch string) error {
	defer giveBack(dir, nil)

	_, err := run(ctx, localTimeout, dir, Auth{}, "checkout", "--quiet", "-B", branch)

	return err
}

// keptRefs is where a folder keeps one ref per commit a revert may go back to, so that git
// never collects one whose tag or branch moved on.
const keptRefs = "refs/recasaos/kept/"

// KeepCommit makes dir keep commit, whatever becomes of the refs that brought it.
func KeepCommit(ctx context.Context, dir, commit string) error {
	defer giveBack(dir, nil)

	_, err := run(ctx, localTimeout, dir, Auth{}, "update-ref", keptRefs+commit, commit)

	return err
}

// DropKeptCommit removes the ref KeepCommit made for commit.
func DropKeptCommit(ctx context.Context, dir, commit string) error {
	defer giveBack(dir, nil)

	_, err := run(ctx, localTimeout, dir, Auth{}, "update-ref", "-d", keptRefs+commit)

	return err
}

// KeptCommits is every commit dir keeps a ref for.
func KeptCommits(ctx context.Context, dir string) ([]string, error) {
	out, err := run(ctx, localTimeout, dir, Auth{}, "for-each-ref", "--format=%(refname:lstrip=3)", keptRefs)
	if err != nil || out == "" {
		return []string{}, err
	}

	return strings.Split(out, "\n"), nil
}

func moveTo(ctx context.Context, dir string, args ...string) error {
	before, err := run(ctx, localTimeout, dir, Auth{}, "rev-parse", "HEAD")
	if err != nil {
		return err
	}

	_, err = run(ctx, localTimeout, dir, Auth{}, args...)

	changed, _ := run(ctx, localTimeout, dir, Auth{}, "diff", "--name-only", before, "HEAD")
	giveBack(dir, strings.Split(changed, "\n"))

	return err
}

// giveBack hands what a command run as root created or rewrote in a folder back to the
// folder's owner: everything under .git, and the paths given with the folders above
// them. Without it, a stack somebody started by hand in their own folder is left with
// files, an index and objects only root can write, and their next `git pull` fails.
func giveBack(dir string, paths []string) {
	if os.Geteuid() != 0 {
		return
	}

	info, err := os.Stat(dir)
	if err != nil {
		return
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid == 0 {
		return
	}

	chown := func(path string) {
		if info, err := os.Lstat(path); err == nil {
			if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid == 0 {
				_ = os.Lchown(path, int(owner.Uid), int(owner.Gid))
			}
		}
	}

	_ = filepath.WalkDir(filepath.Join(dir, ".git"), func(path string, _ fs.DirEntry, err error) error {
		if err == nil {
			chown(path)
		}

		return nil
	})

	for _, p := range paths {
		if p == "" {
			continue
		}
		for path := filepath.Join(dir, p); strings.HasPrefix(path, dir+string(filepath.Separator)); path = filepath.Dir(path) {
			chown(path)
		}
	}
}
