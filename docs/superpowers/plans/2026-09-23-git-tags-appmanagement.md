# Git Apps That Follow Tags (AppManagement) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A git app can follow the highest semver tag of its repository instead of the head of a branch: it deploys a strictly higher tag automatically, never goes back by itself, and deploys any eligible tag by hand.

**Architecture:** The commit stays the identity of a version everywhere; a `tag` is recorded beside it on `deployed`, the history, the operation and the check. `pkg/git` gains the tag listing (peeled lines win, invalid names dropped), a tag fetch, a fetch by commit, detach/attach, and kept refs `refs/recasaos/kept/<commit>`. Pure choice functions live in `service/git_tags.go` (strict semver after one leading `v`, `path.Match` pattern, pre-releases, ties by name). The check, the deployment, the automatic gate, the backup manifest and the restore branch on `st.followsTags()`; the branch mode keeps its code path. The API gains the mode fields, `GET /git/{app}/tags` and `deploy {tag}`.

**Tech Stack:** Go 1.26.8, module `github.com/ReCasaOS/CasaOS-AppManagement`, `github.com/Masterminds/semver/v3` v3.2.1 (direct dependency already), echo v4, oapi-codegen v1.12.4 (deepmap), gotest.tools/v3, the host's git (protocol v2, git 2.29 or later).

**Spec:** D:/clients/casaos/CasaOS-AppManagement/docs/superpowers/specs/2026-09-23-git-tags-design.md

## Global Constraints

- Repository: `D:/clients/casaos/CasaOS-AppManagement`, branch `feat/git-tags` (checked out, holds the spec). Work on it; never switch branch.
- Mode strings: `follow` is `"branch"` or `"tags"`, a plain string validated in code (`checkGitFollow`), never an OpenAPI enum. An empty `follow` in a state file or a manifest means branch.
- `tag_pattern`: a `path.Match` glob on the full tag name; empty means every version tag; at most 100 characters, no whitespace or control character, `path.Match(pattern, "")` returns no error (`checkGitTagPattern`).
- `prereleases`: bool, default false.
- Eligible tag: name matches `tag_pattern` when there is one; `semver.StrictNewVersion(strings.TrimPrefix(name, "v"))` succeeds (one leading `v` only); a pre-release only with `prereleases`. Highest version first; equal versions ordered by name ascending (`strings.Compare`).
- A tag name from the remote that `git.ValidRefName` refuses (the rules `validGitBranch` applies today) is dropped by `git.LsRemoteTags` and never passed to git. A tag named in an API request is used only once found among the eligible tags of the remote.
- No eligible tag: the message is exactly `no tag matches (<filter>, <pre-releases>)`, `<filter>` being ``pattern `v2.*` `` or `no pattern`, `<pre-releases>` being `pre-releases excluded` or `pre-releases included` (`gitNoTagMessage`, which starts with the constant `gitNoTag` = `no tag matches`). The check puts it in `check.error` and keeps the previous `remote_commit` and `remote_tag`; the app's `state` stays `idle`, not `unreachable` (the repository was reached); a deployment answers it as a 400.
- In tag mode `branch` is `""`, and the check never calls `git.DefaultBranch`.
- Automatic gate (`gitMayDeployAutomatically`): today's conditions (auto on, not paused, not blocked, deployed, check ok, `remote_commit` non-empty, differs from `deployed.commit`, not in `attempted`), plus in tag mode `deployed.tag != ""` and `remote_tag` strictly higher in semver than `deployed.tag`, and in branch mode `deployed.tag == ""`.
- Pause: a deployment by hand (or a restore) that is not a redeployment of the running commit sets `auto_paused` to: a revert, except the first deployment of an app that follows tags (empty `deployed.tag` before it); or a tag lower in semver than the previous `deployed.tag`.
- Tag mode moves the folder with `reset --keep` (`git.ResetKeep`), never a fast-forward; the branch-only rules (target on the branch, descends from the deployed commit, folder contained) do not apply in tag mode.
- Back on a branch: the first deployment after tags (`deployed.tag` non-empty before it) skips "descends from the deployed commit" and moves with `reset --keep`, since the tag that ran may come from another line. On a branch, a commit given by hand runs under the tag it ran as (the running version's `deployed.tag`, or its history entry's `tag`); only the branch's head (no commit given, or the automatic deployment) records an empty tag.
- In tag mode, a deployment by hand of the running commit (by its tag, as the highest tag, or by its hash) is no revert: it is a repair when the app is blocked, the folder moved or the tag differs, and `<12> is already deployed: there is nothing new to build` otherwise. A commit given by hand in tag mode is the running one or a history version that ran.
- Before a build in tag mode: `git fetch --no-tags -- <url> refs/tags/<tag>`, peel `FETCH_HEAD^{commit}`; a different commit than the target fails the deployment with `the tag <tag> moved since the check, from <12> to <12>: check again`. A restore skips this (it builds the backed-up commit, wherever its tag went), and so does a repair (the target is the running commit, which the folder holds).
- Kept refs: `refs/recasaos/kept/<commit>` for `deployed.commit` and every history entry whose outcome is `deployed` or `adopted`, in both modes; reconciled by `keepGitCommits` at the end of every deployment and at startup recovery. `revertable` also requires `git.HasCommit`.
- JSON contract (other plans read these keys): the app's view gains top-level `follow` (string), `tag_pattern` (string), `prereleases` (bool); `check` becomes `{at, remote_commit, remote_tag, tag_moved, error}`; `deployed` becomes `{commit, subject, at, tag}`; every `history[]` entry gains `tag`; `operation` becomes `{kind, commit, started_at, tag}`. `tag_moved` = tag mode and `remote_tag != ""` and `remote_tag == deployed.tag` and `remote_commit != deployed.commit`, computed with the view. `new_commits` in tag mode: `remote_commit` differs from `deployed.commit` and (`deployed.tag == ""` or `remote_tag` strictly higher than `deployed.tag`).
- Routes: `POST /v2/app_management/git` and `PUT /v2/app_management/git/{app}` accept `follow`, `tag_pattern`, `prereleases`; `GET /v2/app_management/git/{app}/tags` answers `{"data":[{"name","commit"}...]}`, highest first, at most 50, 400 in branch mode; `POST /v2/app_management/git/{app}/deploy` accepts `tag` (`tag` with `commit`: 400 `give a tag or a commit, not both`; neither in tag mode: the highest eligible tag at request time).
- Backups: `BackupGit` gains `follow`, `tag`, `tag_pattern`, `prereleases`, all `omitempty`, written for a tag-mode app only, so a branch-mode manifest is byte-for-byte what it is today. A tag-mode manifest may have no `tag` (a backup taken before the first deployment in tag mode); it restores all the same, with `deployed.tag` empty.
- Restore of a tag-mode manifest on a box without the app: clone at the backup's tag while the remote has it, else at the highest eligible tag, else at the remote's default branch; the folder is then detached; the backed-up commit is fetched by its hash when the clone does not hold it, and a forge that refuses fails the restore; nothing else is ever deployed in its place.
- OpenAPI: no `enum` added (oapi-codegen v1.12.4 renames existing enum constants when one is added); the enum-constant count stays **34**.
- `codegen/` is gitignored and generated (CI runs `go generate`): regenerate locally, never commit it.
- Codegen command (Git Bash, repository root): `go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,server,spec -package codegen api/app_management/openapi.yaml > codegen/app_management_api.go`.
- Count command: `grep -cE '^[[:blank:]][A-Z][[:alnum:]_]* +[[:alnum:]_]+ = "' codegen/app_management_api.go` prints `34`.
- The packages only build for Linux (`pkg/git` uses `syscall.Stat_t`). Locally (Windows, Git Bash): `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./...` (it type-checks the tests too) and `go build` the same way. The Go tests run on Linux (CI `codecov.yml`: `go generate ./...` then `go test -race ./...`); each task names the `-run` filter to use there, always after `go generate ./...` (`service` imports the gitignored `codegen`).
- This host has no Linux runner (no WSL distro, Docker often down). When none is available, the executor runs every local gate of the step, writes `Linux tests not run, vet passed` in its task report with the `-run` filter of the step, and goes on; CI runs the tests. It never marks a Linux test step as passed without running it.
- Format check (works on CRLF files, where a plain `gofmt -l` always lists the file): `for f in <files>; do tr -d '\r' < "$f" | gofmt -l | sed "s|<standard input>|$f|"; done` prints nothing. When it prints a file, look at `tr -d '\r' < <file> | gofmt -d` and fix the layout by hand with the editor; never `gofmt -w`.
- Line endings: these files have CRLF in the working tree and must keep them: `service/git_apps.go`, `service/git_app_state.go`, `service/git_app_view.go`, `service/git_app_view_internal_test.go`, `service/git_restore.go`, `api/app_management/openapi.yaml`. Every other file touched here is LF. After a task, `git -C D:/clients/casaos/CasaOS-AppManagement ls-files --eol <files> | grep mixed` prints nothing. New files: write them with the Write tool (LF); inline Bash unescapes backslashes.
- Line numbers in **Files:** are those of the file before this plan's first task; each step names a text anchor, which wins when they disagree.
- Test helpers already there: `gitAppsIn`, `withFakeGitDocker` (`fakeGitRuntime`: `Build` needs `compose.yaml` at the worktree root and returns `{"web": "<app>-web:git-<12>"}`; `Start` records the folder's HEAD), `newTestRemote`, `pushTestCommit`, `gitShell`, `waitForGitApp`, `checkTestApp`, `assertBadRequest`, `clonedTestApp`, `deployedTestApp`, `deployTestApp`, `folderHead`, `holding`, `assertBusy`.
- Commits: `git -C D:/clients/casaos/CasaOS-AppManagement add <files>` then `git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "..."`; no `Co-Authored-By` trailer, no AI attribution anywhere.
- Forbidden: `git push`, `git tag`, `git merge`, `git pull`, `git rebase`, `git reset --hard`, `git checkout -- <path>`, `git clean`, `git stash`, `rm -rf` on directories, any linter or formatter run with `--fix` or `-w`. (The `git tag`, `git push` and `git checkout` inside test code run in test repositories under `t.TempDir()`, never on this repository.)
- Release: not part of this plan; AppManagement ships with the dashboard, Core and the installer in one distribution release, driven by the controller.
- The spec is the owner's and is never edited by an executor. The decisions below are this plan's, not the spec's.

## Decisions the spec leaves open (for the owner)

The controller puts this list to the owner before Task 1 starts. The tasks implement each decision as written here; when the owner rejects one, the controller changes the plan as the entry says before handing out the task named.

1. **The folder follows the mode** (Task 3). A `PUT` that changes `follow` on a cloned app detaches its folder at its commit (`git checkout --detach`) to follow tags, or puts it on the branch (`git checkout -B <branch>`) to follow one; no file moves. Without a `branch` in the request, the remote's default is read at that moment, and a remote that cannot be read is a 400 asking for the branch. The spec says the branch is left empty for the remote's default; an empty branch on a cloned, detached folder would make every branch deployment fail "not on the branch". If rejected: the lookup moves to the next check, which must then attach the folder; Task 3 changes.
2. **No branch in tag mode.** A `POST` or `PUT` that leaves an app following tags with a non-empty `branch` is a 400 (Task 3).
3. **The first deployment in a new mode may be the running commit** (Tasks 5, 6). Deploying by hand the commit that runs, under another tag (in tag mode) or as the branch's head (back on a branch), starts it again from its images and records the tag, so that automatic deployment can resume even when the newest version is what already runs.
4. **An older tag deployed by hand pauses automatic deployment**, as a revert does; otherwise the next check would upgrade the app again. The first deployment in tag mode pauses nothing, even when its commit ran before (Task 5). If rejected: drop `gitTagHigher(previousTag, tag)` from the pause in `runGitDeploy`.
5. **A commit given by hand to an app that follows tags** must be the running version (a repair, under its tag) or a version of the history that ran (a revert, under the tag it ran as); any other commit is a 400 (Task 5).
6. **Back on a branch after tags** (Task 5). A commit given by hand runs under the tag it ran as; only the branch's head clears `deployed.tag`. The first deployment on the branch after tags skips "descends from the deployed commit" and moves with `reset --keep`, since the tag that ran may come from another line.
7. **Kept refs in both modes.** `refs/recasaos/kept/<commit>` is kept for the running version and every history entry that ran, whatever the mode, adopted apps' folders included: a revert on a branch leaves a commit only the reflog holds too (Task 6). If rejected: `keepGitCommits` wants only `deployed.commit` when `st.followsTags()` or `deployed.tag != ""`, and the history entries whose `tag` is non-empty.
8. **`tag_moved` is computed with the view**, from the last check and `deployed`, so that it is never stale after a deployment (Task 2).
9. **Restore of a tag-mode backup without a usable tag** (Task 7). A manifest with `follow: tags` and no `tag` (taken before the first deployment in tag mode) restores in tag mode with `deployed.tag` empty. When the backup's tag is gone and no tag is eligible either, the repository is cloned at the remote's default branch, detached, and the commit fetched by its hash when needed.
10. **`GET …/tags` on a repository that cannot be reached** is a 400 carrying git's message, as `deploy` answers today (Task 4).
11. **A check that found no eligible tag leaves the state `idle`**, not `unreachable`: the repository was reached, and `check.error` says why nothing deploys (Task 4). The dashboard plan reads the message from `check.error`.

---

## File structure

| File | Change | Responsibility |
|---|---|---|
| `pkg/git/git.go` | modify | `ValidRefName`, `LsRemoteTags`, `FetchTag`, `FetchCommit`, `Detach`, `Attach`, `KeepCommit`, `DropKeptCommit`, `KeptCommits`. |
| `pkg/git/git_test.go` | modify | Tag listing, tag fetch, fetch by commit, detach/attach, kept refs. |
| `service/git_tags.go` | create | Mode constants, `GitTag`, `follow()`/`followsTags()`, validation, the choice of tags, `gitTagHigher`, `gitTagMoved`; later the remote listing and `GitAppTags`. |
| `service/git_tags_internal_test.go` | create | Pure tests of the choice, validation, `new_commits`, the view's tag fields. |
| `service/git_app_state.go` | modify | `validGitBranch` delegates to `git.ValidRefName`; state gains `Follow`, `TagPattern`, `Prereleases`, and `Tag` on deployment, check (`RemoteTag`), operation, history; `gitRan`. |
| `service/git_app_view.go` | modify | View fields, `GitCheckView`, `new_commits` in tag mode, `idle` for a check that found no eligible tag, `revertable` needs the commit. |
| `service/git_app_view_internal_test.go` | modify | The view's contract test lists the new keys. |
| `service/git_apps.go` | modify | Registration and `PUT` with the mode, the folder following the mode (`followGitFolder`), the check in tag mode, the clone at a ref. |
| `service/git_tags_mode_internal_test.go` | create | Registration and mode switching. |
| `service/git_tags_check_internal_test.go` | create | The check, the clone at the tag, `GitAppTags`; helpers `pushTestTag`, `taggedTestApp`. |
| `service/git_deploy.go` | modify | Deploy by tag, target resolution in both modes, preconditions, tag fetch and refusal, `reset --keep`, the first branch deployment after tags, tags recorded, older tag pauses, automatic gate, kept refs, restore trigger. |
| `service/git_tags_deploy_internal_test.go` | create | Manual deployments of tags, refusals, a tag moved since the check, interrupted tag deployment; helper `deployedTaggedTestApp`. |
| `service/git_tags_auto_internal_test.go` | create | The gate, upgrades only, the first deployment after a mode change, kept commits and reverts after a tag is gone. |
| `service/git_lifecycle.go` | modify | An interrupted operation keeps its tag; recovery reconciles the kept refs. |
| `service/backup_plan.go` | modify | `BackupGit` tag fields. |
| `service/git_restore.go` | modify | Manifest tag fields, restore at the tag, then by commit, then the clear error. |
| `service/git_tags_restore_internal_test.go` | create | Backup manifests (tag mode, branch mode byte-identical) and restores. |
| `api/app_management/openapi.yaml` | modify | `GitFollow`, `GitTag`, `GitAppTagsOK`, the new fields, `/git/{app}/tags`, deploy `tag`. |
| `codegen/app_management_api.go` | regenerate (gitignored) | Generated types and `ServerInterface.GetGitAppTags`. Never committed. |
| `route/v2/git.go` | modify | `gitAppRegistration`, `gitAppChanges` fields, deploy by tag, `GetGitAppTags`, `gitAppTagsAnswer`. |
| `route/v2/git_internal_test.go` | modify | The view's tag fields match the generated `GitApp`; POST/PUT copies; the tags answer. |
| `route/v2_git_test.go` | modify | Router rows for the new fields, `GET …/tags`, `deploy {tag}` and `tag`+`commit`. |

---

### Task 1: Git plumbing for tags

**Files:**
- Modify: `pkg/git/git.go:5-17` (imports), `:169-176` (after `exitCode`: `ValidRefName`), `:193-208` (after `DefaultBranch`: `LsRemoteTags`), `:210-215` (`Clone` comment), `:217-226` (after `Fetch`: `FetchTag`, `FetchCommit`), `:342-346` (`ResetKeep` comment; after it `Detach`, `Attach`, the kept refs)
- Modify: `service/git_app_state.go:3-23` (imports: drop `"unicode"`), `:176-181` (`validGitBranch`) — CRLF
- Test: `pkg/git/git_test.go:3-17` (imports: add `"sort"`) and appended tests

**Interfaces:**
- Consumes: `run`, `giveBack`, `lsRemoteTimeout`, `networkTimeout`, `localTimeout`, `Auth` (all in `pkg/git/git.go`); test helpers `sh`, `newRemote`, `push` in `pkg/git/git_test.go`.
- Produces (package `git`):
  - `func ValidRefName(name string) bool` (empty passes)
  - `func LsRemoteTags(ctx context.Context, url string, auth Auth) (map[string]string, error)`: tag name → commit; never nil on success.
  - `func FetchTag(ctx context.Context, dir, url, tag string, auth Auth) (string, error)`: the commit.
  - `func FetchCommit(ctx context.Context, dir, url, commit string, auth Auth) error`
  - `func Detach(ctx context.Context, dir string) error`, `func Attach(ctx context.Context, dir, branch string) error`
  - `const keptRefs = "refs/recasaos/kept/"`, `func KeepCommit(ctx context.Context, dir, commit string) error`, `func DropKeptCommit(ctx context.Context, dir, commit string) error`, `func KeptCommits(ctx context.Context, dir string) ([]string, error)` (never nil).
  - `service.validGitBranch(branch string) bool` now returns `git.ValidRefName(branch)`.

- [ ] **Step 1: Write the failing tests**

In `pkg/git/git_test.go`, replace the import block (lines 3-17) with:

```go
import (
	"context"
	"errors"
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
```

and append at the end of the file, after a blank line:

```go
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
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/git/`

Expected: FAIL, compile errors in `git_test.go`: `undefined: LsRemoteTags`, `undefined: FetchTag`, `undefined: FetchCommit`, `undefined: Detach`, `undefined: Attach`, `undefined: KeepCommit`, `undefined: KeptCommits`, `undefined: DropKeptCommit`.

- [ ] **Step 3: Implement**

In `pkg/git/git.go`, replace the import block (lines 5-17) with:

```go
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
```

After the closing brace of `exitCode` (anchor: `return -1\n}` of `func exitCode`), insert:

```go

// ValidRefName is a name git reads as a branch or a tag name: not an option, not a revision
// expression (`..`, `@`, `@{`, `~`, `^`), and no character a ref name forbids. Empty passes: a
// caller that needs a name checks that itself.
func ValidRefName(name string) bool {
	return !strings.HasPrefix(name, "-") && name != "@" && !strings.Contains(name, "..") && !strings.Contains(name, "@{") &&
		!strings.ContainsAny(name, " ~^:?*[\\") && !strings.ContainsFunc(name, unicode.IsControl)
}
```

After the closing brace of `DefaultBranch` (anchor: `return "", errors.New("the remote does not say which branch its HEAD points to")\n}`), insert:

```go

// LsRemoteTags is every tag of the remote and the commit it names. An annotated tag comes
// twice: its plain line names the tag object, its peeled line `<name>^{}` the commit, which
// wins. A name that is no valid ref name is left out, so that it never reaches git.
func LsRemoteTags(ctx context.Context, url string, auth Auth) (map[string]string, error) {
	out, err := run(ctx, lsRemoteTimeout, "", auth, "ls-remote", "--tags", "--", url)
	if err != nil {
		return nil, err
	}

	tags := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		hash, ref, _ := strings.Cut(line, "\t")
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
```

Replace the comment of `Clone`:

```go
// Clone clones one branch of url into dir.
```

with:

```go
// Clone clones one branch of url into dir, or one tag, which leaves it in detached HEAD.
```

After the closing brace of `Fetch` (anchor: `return run(ctx, localTimeout, dir, Auth{}, "rev-parse", "FETCH_HEAD")\n}`), insert:

```go

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
```

Replace `ResetKeep` and its comment:

```go
// ResetKeep moves the checked-out branch to commit, keeping local changes git can keep.
// Only a rollback or a revert uses it.
func ResetKeep(ctx context.Context, dir, commit string) error {
	return moveTo(ctx, dir, "reset", "--keep", commit)
}
```

with:

```go
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
```

In `service/git_app_state.go` (CRLF, keep it), delete the line `	"unicode"` from the import block, and replace `validGitBranch`:

```go
// validGitBranch is a name git reads as a branch name: not an option, not a revision
// expression (`..`, `@`, `@{`, `~`, `^`), and no character a ref name forbids.
func validGitBranch(branch string) bool {
	return !strings.HasPrefix(branch, "-") && branch != "@" && !strings.Contains(branch, "..") && !strings.Contains(branch, "@{") &&
		!strings.ContainsAny(branch, " ~^:?*[\\") && !strings.ContainsFunc(branch, unicode.IsControl)
}
```

with:

```go
// validGitBranch is a name git reads as a branch name: see git.ValidRefName, which tags from
// a remote pass too.
func validGitBranch(branch string) bool {
	return git.ValidRefName(branch)
}
```

- [ ] **Step 4: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && for f in pkg/git/git.go pkg/git/git_test.go service/git_app_state.go; do tr -d '\r' < "$f" | gofmt -l | sed "s|<standard input>|$f|"; done; git ls-files --eol pkg/git/git.go pkg/git/git_test.go service/git_app_state.go | grep mixed; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/git/ ./service/ ./route/...`

Expected: no output.

Run (Linux, after `go generate ./...`): `go test ./pkg/git/ -run 'TestLsRemoteTagsGivesEachTagTheCommitItNames|TestAFetchedTagIsItsCommitAndACommitIsFetchedByItsHash|TestDetachAndAttachMoveNoFile|TestAKeptCommitOutlivesTheRefsThatBroughtIt|TestCloneDescribesTheDefaultBranch|TestFetchThenAncestry' -count=1 -v && go test ./service/ -run 'TestABranchIsANameGitCannotReadAsSomethingElse' -count=1 -v`

Expected: `--- PASS` for each test named, `ok  	github.com/ReCasaOS/CasaOS-AppManagement/pkg/git` and `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`.

- [ ] **Step 5: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add pkg/git/git.go pkg/git/git_test.go service/git_app_state.go
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): list tags, fetch a tag or a commit, detach, and keep commits"
```

---

### Task 2: The tag mode in the state, the choice of tags, and the view

**Files:**
- Create: `service/git_tags.go`
- Modify: `service/git_app_state.go:68-118` (`gitApp`, `gitDeployment`, `gitCheck`, `gitOperation`, `gitHistoryEntry`) — CRLF
- Modify: `service/git_app_view.go:21-70` (`GitAppView`, `GitDeployedView`, `GitHistoryView`, new `GitCheckView`), `:126-130` (`gitNewCommits`), `:176-217` (`newGitAppView`) — CRLF
- Test: `service/git_tags_internal_test.go` (create); `service/git_app_view_internal_test.go:127-144` (`TestTheViewOfAGitAppIsTheContract`) — CRLF

**Interfaces:**
- Consumes: `GitRequestError`, `gitApp`, `newGitAppView`, `gitAppsIn`, `withFakeGitDocker`, `assertBadRequest`; `semver.StrictNewVersion`, `path.Match`.
- Produces:
  - constants `gitFollowBranch = "branch"`, `gitFollowTags = "tags"`, `gitTagPatternCap = 100`; `const errGitTagsHaveNoBranch = GitRequestError("an app that follows tags has no branch: set follow to branch to give one")`.
  - `type GitTag struct { Name string `json:"name"`; Commit string `json:"commit"` }`.
  - `func (st *gitApp) follow() string`, `func (st *gitApp) followsTags() bool`.
  - `func checkGitFollow(follow string) error` (message `follow is branch or tags, not `x``), `func checkGitTagPattern(pattern string) error` (message contains `tag pattern`).
  - `const gitNoTag = "no tag matches"`, `func gitNoTagMessage(pattern string, prereleases bool) string` (starts with `gitNoTag`).
  - `func gitTagVersion(name string) (*semver.Version, bool)`, `func gitTagHigher(a, b string) bool`, `func eligibleGitTags(tags map[string]string, pattern string, prereleases bool) []GitTag` (never nil), `func gitTagMoved(st *gitApp) bool`.
  - State fields: `gitApp.Follow string `json:"follow"``, `.TagPattern string `json:"tag_pattern"``, `.Prereleases bool `json:"prereleases"``; `gitDeployment.Tag`, `gitCheck.RemoteTag` (`json:"remote_tag"`), `gitOperation.Tag`, `gitHistoryEntry.Tag` (all `json:"tag"` except `remote_tag`).
  - View: `GitAppView.Follow/TagPattern/Prereleases` (`follow`, `tag_pattern`, `prereleases`), `GitAppView.Check *GitCheckView`, `type GitCheckView struct { At time.Time; RemoteCommit, RemoteTag string; TagMoved bool; Error string }` (`at`, `remote_commit`, `remote_tag`, `tag_moved`, `error`), `GitDeployedView.Tag`, `GitHistoryView.Tag`.

- [ ] **Step 1: Write the failing test**

Create `service/git_tags_internal_test.go`:

```go
package service

import (
	"context"
	stdjson "encoding/json"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func tagNames(tags []GitTag) []string {
	names := []string{}
	for _, tag := range tags {
		names = append(names, tag.Name)
	}

	return names
}

func TestAnEligibleTagIsStrictSemverAfterOneLeadingV(t *testing.T) {
	remote := map[string]string{
		"v1.0.0": "c1", "1.0.1": "c2", "v1.1.0-rc.1": "c3", "v1.1.0-rc.10": "c4", "v1.1.0-rc.2": "c5",
		"v2.0.0": "c6", "v2.1.0+build.7": "c7",
		// no version: floating tags, a date, partial or loose versions, a prefix
		"latest": "x", "stable": "x", "2024-09": "x", "v2": "x", "v1.2": "x", "vv3.0.0": "x", "V3.0.0": "x",
		"v01.2.3": "x", "1.2.3.4": "x", "release-4.0.0": "x",
	}

	assert.DeepEqual(t, tagNames(eligibleGitTags(remote, "", false)), []string{"v2.1.0+build.7", "v2.0.0", "1.0.1", "v1.0.0"})
	assert.DeepEqual(t, tagNames(eligibleGitTags(remote, "", true)),
		[]string{"v2.1.0+build.7", "v2.0.0", "v1.1.0-rc.10", "v1.1.0-rc.2", "v1.1.0-rc.1", "1.0.1", "v1.0.0"})
	assert.DeepEqual(t, tagNames(eligibleGitTags(remote, "v1.*", true)), []string{"v1.1.0-rc.10", "v1.1.0-rc.2", "v1.1.0-rc.1", "v1.0.0"})
	assert.DeepEqual(t, tagNames(eligibleGitTags(remote, "v[0-9].0.?", false)), []string{"v2.0.0", "v1.0.0"})
	assert.DeepEqual(t, eligibleGitTags(remote, "v3.*", true), []GitTag{})
	assert.DeepEqual(t, eligibleGitTags(map[string]string{}, "", false), []GitTag{})
	assert.DeepEqual(t, eligibleGitTags(remote, "", false)[0], GitTag{Name: "v2.1.0+build.7", Commit: "c7"})
}

// Equal versions are ordered by name, so the same tag is chosen every time.
func TestEqualVersionsAreOrderedByName(t *testing.T) {
	remote := map[string]string{"v1.2.3": "a", "1.2.3": "b", "v1.2.3+b1": "c", "1.2.3+b2": "d"}

	for range 20 {
		assert.DeepEqual(t, tagNames(eligibleGitTags(remote, "", false)), []string{"1.2.3", "1.2.3+b2", "v1.2.3", "v1.2.3+b1"})
	}
}

func TestATagIsHigherOnlyByItsVersion(t *testing.T) {
	for _, c := range []struct {
		a, b   string
		higher bool
	}{
		{"v1.10.0", "v1.9.0", true},
		{"v2.0.0-rc.1", "v1.9.9", true},
		{"v1.0.0", "v1.0.0-rc.1", true},
		{"v1.0.0-rc.10", "v1.0.0-rc.2", true},
		{"v1.0.0", "v1.0.0", false},
		{"1.2.3", "v1.2.3", false},
		{"v1.2.3+b2", "v1.2.3+b1", false},
		{"v1.0.0", "v1.1.0", false},
		{"latest", "v1.0.0", false},
		{"v1.0.0", "", false},
		{"", "v1.0.0", false},
	} {
		assert.Equal(t, gitTagHigher(c.a, c.b), c.higher, "%s over %s", c.a, c.b)
	}
}

func TestAModeAndATagPatternAreChecked(t *testing.T) {
	for _, follow := range []string{"branch", "tags"} {
		assert.NilError(t, checkGitFollow(follow))
	}
	for _, follow := range []string{"", "tag", "Tags", "branches"} {
		assertBadRequest(t, checkGitFollow(follow), "follow is branch or tags")
	}

	for _, pattern := range []string{"", "v2.*", "v[0-9].*", "release-?.*", strings.Repeat("v", 100)} {
		assert.NilError(t, checkGitTagPattern(pattern), pattern)
	}
	for _, pattern := range []string{"[", "v2.[", "v2\\", "v 2.*", "v2.*\t", "v2\x00", strings.Repeat("v", 101)} {
		assertBadRequest(t, checkGitTagPattern(pattern), "tag pattern")
	}
}

func TestNoEligibleTagSaysWhichFilterIsInForce(t *testing.T) {
	assert.Equal(t, gitNoTagMessage("v2.*", false), "no tag matches (pattern `v2.*`, pre-releases excluded)")
	assert.Equal(t, gitNoTagMessage("", true), "no tag matches (no pattern, pre-releases included)")
}

// Only a higher tag is new to an app that follows tags: a lower or a moved one never lights
// the badge. Before its first deployment in that mode, any other commit is new.
func TestNewCommitsOfAnAppThatFollowsTagsIsAHigherTag(t *testing.T) {
	deployed := &gitDeployment{Commit: "a", Tag: "v1.1.0"}
	tagged := func(d *gitDeployment, commit, tag string) *gitApp {
		return &gitApp{Follow: gitFollowTags, Deployed: d, Check: &gitCheck{RemoteCommit: commit, RemoteTag: tag}}
	}

	assert.Assert(t, gitNewCommits(tagged(deployed, "b", "v1.2.0")))
	assert.Assert(t, !gitNewCommits(tagged(deployed, "b", "v1.0.0")), "a lower tag")
	assert.Assert(t, !gitNewCommits(tagged(deployed, "b", "v1.1.0")), "the deployed tag, moved")
	assert.Assert(t, !gitNewCommits(tagged(deployed, "a", "v1.2.0")), "a higher tag on what runs")
	assert.Assert(t, gitNewCommits(tagged(&gitDeployment{Commit: "a"}, "b", "v1.0.0")), "no tag deployed yet in this mode")
	assert.Assert(t, !gitNewCommits(tagged(&gitDeployment{Commit: "a"}, "b", "")), "a branch check left from before the switch")
	assert.Assert(t, gitNewCommits(&gitApp{Deployed: deployed, Check: &gitCheck{RemoteCommit: "b"}}), "a branch: any other commit")
}

// The view shows the mode and its filter, the tags beside the commits, and a deployed tag the
// remote moved.
func TestTheViewOfAnAppThatFollowsTagsSaysWhenItsTagMoved(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	st := &gitApp{
		App: "jarvis", Follow: gitFollowTags, TagPattern: "v1.*", Prereleases: true,
		Deployed:  &gitDeployment{Commit: "a", Tag: "v1.1.0"},
		Check:     &gitCheck{RemoteCommit: "b", RemoteTag: "v1.1.0"},
		Operation: &gitOperation{Kind: gitOperationBuild, Commit: "c", Tag: "v1.2.0"},
		History:   []gitHistoryEntry{{Commit: "a", Tag: "v1.1.0", Outcome: gitOutcomeDeployed}},
	}

	view := newGitAppView(ctx, st)
	assert.Equal(t, view.Follow, gitFollowTags)
	assert.Equal(t, view.TagPattern, "v1.*")
	assert.Equal(t, view.Prereleases, true)
	assert.Equal(t, view.Deployed.Tag, "v1.1.0")
	assert.Equal(t, view.History[0].Tag, "v1.1.0")
	assert.Equal(t, view.Operation.Tag, "v1.2.0")
	assert.DeepEqual(t, view.Check, &GitCheckView{RemoteCommit: "b", RemoteTag: "v1.1.0", TagMoved: true})
	// the operation's key, as the API answers it
	encoded, err := stdjson.Marshal(view)
	assert.NilError(t, err)
	var answered map[string]any
	assert.NilError(t, stdjson.Unmarshal(encoded, &answered))
	assert.Equal(t, answered["operation"].(map[string]any)["tag"], "v1.2.0")

	st.Check.RemoteCommit = "a"
	assert.Equal(t, newGitAppView(ctx, st).Check.TagMoved, false, "the tag on what runs")
	st.Check.RemoteTag, st.Check.RemoteCommit = "v1.2.0", "b"
	assert.Equal(t, newGitAppView(ctx, st).Check.TagMoved, false, "another tag")
	st.Follow, st.Check.RemoteTag = "", "v1.1.0"
	view = newGitAppView(ctx, st)
	assert.Equal(t, view.Follow, gitFollowBranch, "an older state follows a branch")
	assert.Equal(t, view.Check.TagMoved, false)
}
```

In `service/git_app_view_internal_test.go` (CRLF, keep it), in `TestTheViewOfAGitAppIsTheContract`, replace:

```go
	assert.DeepEqual(t, keys, []string{
		"access", "app", "auto_deploy", "auto_paused", "blocked", "branch", "build_log", "check", "cloned", "compose",
		"compose_example", "deployed", "dir", "env_template", "env_tracked", "head", "history", "new_commits", "operation",
		"origin", "remote", "state", "token_set", "webhook",
	})
```

with:

```go
	assert.DeepEqual(t, keys, []string{
		"access", "app", "auto_deploy", "auto_paused", "blocked", "branch", "build_log", "check", "cloned", "compose",
		"compose_example", "deployed", "dir", "env_template", "env_tracked", "follow", "head", "history", "new_commits",
		"operation", "origin", "prereleases", "remote", "state", "tag_pattern", "token_set", "webhook",
	})
	// an older state, without the fields, follows a branch
	assert.Equal(t, view["follow"], "branch")
	assert.Equal(t, view["tag_pattern"], "")
	assert.Equal(t, view["prereleases"], false)
```

and replace:

```go
	assert.DeepEqual(t, view["deployed"], map[string]any{"commit": second, "subject": "change .env.example", "at": "2026-09-16T10:00:00Z"})
	assert.DeepEqual(t, view["check"], map[string]any{"at": "2026-09-16T10:00:00Z", "remote_commit": second, "error": ""})

	history := view["history"].([]any)
```

with:

```go
	assert.DeepEqual(t, view["deployed"], map[string]any{"commit": second, "subject": "change .env.example", "at": "2026-09-16T10:00:00Z", "tag": ""})
	assert.DeepEqual(t, view["check"], map[string]any{"at": "2026-09-16T10:00:00Z", "remote_commit": second, "remote_tag": "", "tag_moved": false, "error": ""})

	history := view["history"].([]any)
	assert.Equal(t, history[0].(map[string]any)["tag"], "")
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/`

Expected: FAIL, compile errors in `git_tags_internal_test.go`: `undefined: GitTag`, `undefined: eligibleGitTags`, `undefined: gitTagHigher`, `undefined: checkGitFollow`, `undefined: gitFollowTags`, `unknown field Tag in struct literal of type gitDeployment`, `undefined: GitCheckView`.

- [ ] **Step 3: Implement**

Create `service/git_tags.go`:

```go
package service

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/Masterminds/semver/v3"
)

// Git apps that follow the tags of their repository instead of a branch: which tags they may
// deploy, and in which order. The commit stays the identity of a version everywhere; the tag
// is written down beside it.

// What a git app follows.
const (
	gitFollowBranch = "branch"
	gitFollowTags   = "tags"
)

// gitTagPatternCap is the longest tag pattern accepted.
const gitTagPatternCap = 100

// errGitTagsHaveNoBranch refuses a branch given to an app that follows tags.
const errGitTagsHaveNoBranch = GitRequestError("an app that follows tags has no branch: set follow to branch to give one")

// GitTag is a tag of the remote and the commit it names.
type GitTag struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

// follow is what the app follows: gitFollowTags, or gitFollowBranch for anything else, an
// older state's empty value included.
func (st *gitApp) follow() string {
	if st.followsTags() {
		return gitFollowTags
	}

	return gitFollowBranch
}

func (st *gitApp) followsTags() bool {
	return st.Follow == gitFollowTags
}

// checkGitFollow refuses a mode that is neither branch nor tags.
func checkGitFollow(follow string) error {
	if follow != gitFollowBranch && follow != gitFollowTags {
		return GitRequestError(fmt.Sprintf("follow is branch or tags, not `%s`", follow))
	}

	return nil
}

// checkGitTagPattern refuses a tag pattern path.Match cannot read, one too long, or one holding
// a space or a control character. Empty is every version tag.
func checkGitTagPattern(pattern string) error {
	_, err := path.Match(pattern, "")
	if err != nil || len(pattern) > gitTagPatternCap || strings.ContainsFunc(pattern, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return GitRequestError(fmt.Sprintf("the tag pattern is a glob such as v2.* (`*`, `?`, `[...]`), at most %d characters, with no space", gitTagPatternCap))
	}

	return nil
}

// gitNoTag opens the message of a check that reached the remote and found no eligible tag.
const gitNoTag = "no tag matches"

// gitNoTagMessage says that no tag is eligible, and under which filter.
func gitNoTagMessage(pattern string, prereleases bool) string {
	filter := "no pattern"
	if pattern != "" {
		filter = "pattern `" + pattern + "`"
	}
	releases := "pre-releases excluded"
	if prereleases {
		releases = "pre-releases included"
	}

	return fmt.Sprintf("%s (%s, %s)", gitNoTag, filter, releases)
}

// gitTagVersion is the version a tag names: strict semver once one leading `v` is dropped, so
// that `v2`, `latest` or `2024-09` name none.
func gitTagVersion(name string) (*semver.Version, bool) {
	version, err := semver.StrictNewVersion(strings.TrimPrefix(name, "v"))

	return version, err == nil
}

// gitTagHigher reports whether tag a names a strictly higher version than tag b; false when
// either names none.
func gitTagHigher(a, b string) bool {
	va, okA := gitTagVersion(a)
	vb, okB := gitTagVersion(b)

	return okA && okB && va.GreaterThan(vb)
}

// eligibleGitTags is the tags an app may deploy among tags, highest first: the name matches
// pattern when there is one, names a version, and names a pre-release only with prereleases.
// Equal versions (`v1.2.3` and `1.2.3`, or build metadata only) come by name, so that the
// choice is always the same.
func eligibleGitTags(tags map[string]string, pattern string, prereleases bool) []GitTag {
	type versioned struct {
		tag     GitTag
		version *semver.Version
	}

	eligible := []versioned{}
	for name, commit := range tags {
		version, ok := gitTagVersion(name)
		matched, _ := path.Match(pattern, name)
		if !ok || (pattern != "" && !matched) || (version.Prerelease() != "" && !prereleases) {
			continue
		}
		eligible = append(eligible, versioned{GitTag{Name: name, Commit: commit}, version})
	}

	slices.SortFunc(eligible, func(a, b versioned) int {
		if order := b.version.Compare(a.version); order != 0 {
			return order
		}

		return strings.Compare(a.tag.Name, b.tag.Name)
	})

	sorted := make([]GitTag, 0, len(eligible))
	for _, e := range eligible {
		sorted = append(sorted, e.tag)
	}

	return sorted
}

// gitTagMoved reports whether the last check found the deployed tag on another commit than the
// one deployed: an app that follows tags never deploys it again by itself.
func gitTagMoved(st *gitApp) bool {
	return st.followsTags() && st.Deployed != nil && st.Check != nil && st.Check.RemoteTag != "" &&
		st.Check.RemoteTag == st.Deployed.Tag && st.Check.RemoteCommit != st.Deployed.Commit
}
```

In `service/git_app_state.go` (CRLF, keep it), replace:

```go
	// Attempted is what the automatic rebuild does not try again, the last 20.
	Attempted []string `json:"attempted"`
}

type gitDeployment struct {
	Commit  string    `json:"commit"`
	Subject string    `json:"subject"`
	At      time.Time `json:"at"`
	// Images are the git- tags of the running version, per service.
	Images map[string]string `json:"images"`
}

type gitCheck struct {
	At           time.Time `json:"at"`
	RemoteCommit string    `json:"remote_commit"`
	Error        string    `json:"error"`
}

type gitOperation struct {
	Kind      string    `json:"kind"`
	Commit    string    `json:"commit"`
	StartedAt time.Time `json:"started_at"`
}

type gitHistoryEntry struct {
	Commit  string            `json:"commit"`
	Subject string            `json:"subject"`
	At      time.Time         `json:"at"`
	Outcome string            `json:"outcome"`
	Reason  string            `json:"reason"`
	Images  map[string]string `json:"images"`
}
```

with:

```go
	// Attempted is what the automatic rebuild does not try again, the last 20.
	Attempted []string `json:"attempted"`
	// Follow is gitFollowTags for an app that follows the tags of its repository; anything
	// else, an older state's empty value included, follows a branch. TagPattern and
	// Prereleases choose the tags it may deploy, and are kept while it follows a branch.
	Follow      string `json:"follow"`
	TagPattern  string `json:"tag_pattern"`
	Prereleases bool   `json:"prereleases"`
}

type gitDeployment struct {
	Commit  string    `json:"commit"`
	Subject string    `json:"subject"`
	At      time.Time `json:"at"`
	// Images are the git- tags of the running version, per service.
	Images map[string]string `json:"images"`
	// Tag is the tag the commit was deployed as, empty for a branch's commit.
	Tag string `json:"tag"`
}

type gitCheck struct {
	At           time.Time `json:"at"`
	RemoteCommit string    `json:"remote_commit"`
	Error        string    `json:"error"`
	// RemoteTag is the highest eligible tag, which names RemoteCommit; empty on a branch.
	RemoteTag string `json:"remote_tag"`
}

type gitOperation struct {
	Kind      string    `json:"kind"`
	Commit    string    `json:"commit"`
	StartedAt time.Time `json:"started_at"`
	// Tag is the tag Commit is deployed as, empty for a branch's commit.
	Tag string `json:"tag"`
}

type gitHistoryEntry struct {
	Commit  string            `json:"commit"`
	Subject string            `json:"subject"`
	At      time.Time         `json:"at"`
	Outcome string            `json:"outcome"`
	Reason  string            `json:"reason"`
	Images  map[string]string `json:"images"`
	// Tag is the tag Commit was deployed as, empty for a branch's commit.
	Tag string `json:"tag"`
}
```

In `service/git_app_view.go` (CRLF, keep it), replace the line:

```go
	Check      *gitCheck        `json:"check"`
```

with:

```go
	Check      *GitCheckView    `json:"check"`
```

replace the end of `GitAppView`:

```go
	BuildLog       string         `json:"build_log"`
	Webhook        GitWebhookView `json:"webhook"`
}
```

with:

```go
	BuildLog       string         `json:"build_log"`
	Webhook        GitWebhookView `json:"webhook"`
	// Follow is `branch` or `tags`. TagPattern and Prereleases choose the tags an app that
	// follows them may deploy, and are kept while it follows a branch.
	Follow      string `json:"follow"`
	TagPattern  string `json:"tag_pattern"`
	Prereleases bool   `json:"prereleases"`
}
```

replace `GitDeployedView` and `GitHistoryView`:

```go
type GitDeployedView struct {
	Commit  string    `json:"commit"`
	Subject string    `json:"subject"`
	At      time.Time `json:"at"`
}

type GitHistoryView struct {
	Commit     string    `json:"commit"`
	Subject    string    `json:"subject"`
	At         time.Time `json:"at"`
	Outcome    string    `json:"outcome"`
	Reason     string    `json:"reason"`
	Revertable bool      `json:"revertable"`
}
```

with:

```go
type GitDeployedView struct {
	Commit  string    `json:"commit"`
	Subject string    `json:"subject"`
	At      time.Time `json:"at"`
	Tag     string    `json:"tag"`
}

// GitCheckView is the `check` of the API: the last check, and whether it found the deployed
// tag on another commit.
type GitCheckView struct {
	At           time.Time `json:"at"`
	RemoteCommit string    `json:"remote_commit"`
	RemoteTag    string    `json:"remote_tag"`
	TagMoved     bool      `json:"tag_moved"`
	Error        string    `json:"error"`
}

type GitHistoryView struct {
	Commit     string    `json:"commit"`
	Subject    string    `json:"subject"`
	At         time.Time `json:"at"`
	Outcome    string    `json:"outcome"`
	Reason     string    `json:"reason"`
	Revertable bool      `json:"revertable"`
	Tag        string    `json:"tag"`
}
```

replace `gitNewCommits`:

```go
// gitNewCommits reports whether the last check saw the branch somewhere else than what runs.
// Nothing runs before the first deployment, so nothing is new either.
func gitNewCommits(st *gitApp) bool {
	return st.Deployed != nil && st.Check != nil && st.Check.RemoteCommit != "" && st.Check.RemoteCommit != st.Deployed.Commit
}
```

with:

```go
// gitNewCommits reports whether the last check saw a version to deploy: the branch somewhere
// else than what runs, or for an app that follows tags a tag higher than the one deployed (any
// other tag, before its first deployment in that mode). A check that found no tag, such as the
// branch check left from before a switch to tags, sees nothing to deploy. Nothing runs before
// the first deployment, so nothing is new either.
func gitNewCommits(st *gitApp) bool {
	if st.Deployed == nil || st.Check == nil || st.Check.RemoteCommit == "" || st.Check.RemoteCommit == st.Deployed.Commit {
		return false
	}
	if !st.followsTags() {
		return true
	}

	return st.Check.RemoteTag != "" && (st.Deployed.Tag == "" || gitTagHigher(st.Check.RemoteTag, st.Deployed.Tag))
}
```

In `newGitAppView`, replace:

```go
		History:  []GitHistoryView{},
		BuildLog: readGitBuildLogTail(st.App),
		Webhook:  gitWebhookView(st.App),
	}

	// copies: a deployment goes on changing st once its view is taken
	if st.Check != nil {
		check := *st.Check
		view.Check = &check
	}
```

with:

```go
		History:     []GitHistoryView{},
		BuildLog:    readGitBuildLogTail(st.App),
		Webhook:     gitWebhookView(st.App),
		Follow:      st.follow(),
		TagPattern:  st.TagPattern,
		Prereleases: st.Prereleases,
	}

	// copies: a deployment goes on changing st once its view is taken
	if st.Check != nil {
		view.Check = &GitCheckView{
			At: st.Check.At, RemoteCommit: st.Check.RemoteCommit, RemoteTag: st.Check.RemoteTag, TagMoved: gitTagMoved(st),
			Error: st.Check.Error,
		}
	}
```

replace:

```go
		view.Deployed = &GitDeployedView{Commit: st.Deployed.Commit, Subject: st.Deployed.Subject, At: st.Deployed.At}
```

with:

```go
		view.Deployed = &GitDeployedView{Commit: st.Deployed.Commit, Subject: st.Deployed.Subject, At: st.Deployed.At, Tag: st.Deployed.Tag}
```

and replace:

```go
			Commit: entry.Commit, Subject: entry.Subject, At: entry.At, Outcome: entry.Outcome, Reason: entry.Reason,
```

with:

```go
			Commit: entry.Commit, Tag: entry.Tag, Subject: entry.Subject, At: entry.At, Outcome: entry.Outcome, Reason: entry.Reason,
```

- [ ] **Step 4: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && for f in service/git_tags.go service/git_tags_internal_test.go service/git_app_state.go service/git_app_view.go service/git_app_view_internal_test.go; do tr -d '\r' < "$f" | gofmt -l | sed "s|<standard input>|$f|"; done; git ls-files --eol service/git_app_state.go service/git_app_view.go service/git_app_view_internal_test.go | grep mixed; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/ ./route/...`

Expected: no output.

Run (Linux, after `go generate ./...`): `go test ./service/ -run 'TestAnEligibleTagIsStrictSemverAfterOneLeadingV|TestEqualVersionsAreOrderedByName|TestATagIsHigherOnlyByItsVersion|TestAModeAndATagPatternAreChecked|TestNoEligibleTagSaysWhichFilterIsInForce|TestNewCommitsOfAnAppThatFollowsTagsIsAHigherTag|TestTheViewOfAnAppThatFollowsTagsSaysWhenItsTagMoved|TestTheViewOfAGitAppIsTheContract|TestNewCommitsIsTheRemoteMovingAwayFromWhatRuns|TestTheGridCarriesGitOnlyForGitApps|TestAGitAppIsKeptInAFileOnlyRootReads' -count=1 -v`

Expected: `--- PASS` for each test named, `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`.

- [ ] **Step 5: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add service/git_tags.go service/git_tags_internal_test.go service/git_app_state.go service/git_app_view.go service/git_app_view_internal_test.go
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): the tag mode of a git app, its eligible tags and its view"
```

---

### Task 3: Registering an app that follows tags, and changing the mode

**Files:**
- Modify: `service/git_apps.go:34-41` (`GitAppRegistration`), `:43-54` (`GitAppChanges`), `:101-142` (`CreateGitApp`), `:220-295` (`UpdateGitApp`; new `followGitFolder` after it) — CRLF
- Test: `service/git_tags_mode_internal_test.go` (create)

**Interfaces:**
- Consumes: Task 1 (`git.Detach`, `git.Attach`, `git.DefaultBranch`), Task 2 (`checkGitFollow`, `checkGitTagPattern`, `errGitTagsHaveNoBranch`, `gitFollowBranch`, `gitFollowTags`, `st.follow()`, `st.followsTags()`, view fields), `gitAuth`, `validGitBranch`, `adoptGitApp`; test helpers `clonedTestApp`, `folderHead`, `assertBadRequest`.
- Produces:
  - `GitAppRegistration.Follow string`, `.TagPattern string`, `.Prereleases bool` (empty `Follow` means branch).
  - `GitAppChanges.Follow *string`, `.TagPattern *string`, `.Prereleases *bool`.
  - `func followGitFolder(ctx context.Context, st *gitApp, follow, branch string) (string, error)`: returns the branch followed (`""` for tags).
  - Rules: `follow` checked; tag mode with a non-empty `branch` is 400 `errGitTagsHaveNoBranch`; a cloned app's branch changes only with a change of mode; a change of mode on a cloned app detaches the folder (tags) or runs `git checkout -B <branch>` (branch; the remote's default when none is given, 400 when it cannot be read); `tag_pattern` and `prereleases` are kept across switches.

- [ ] **Step 1: Write the failing test**

Create `service/git_tags_mode_internal_test.go`:

```go
package service

import (
	"context"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"gotest.tools/v3/assert"
)

func TestAnAppThatFollowsTagsIsRegisteredWithoutABranch(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	register := func(r GitAppRegistration) error {
		_, err := CreateGitApp(ctx, r)
		return err
	}
	url := "https://github.com/owner/jarvis.git"

	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: "tag"}), "follow is branch or tags")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: gitFollowTags, Branch: "main"}), "has no branch")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: gitFollowTags, TagPattern: "v2.["}), "tag pattern")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: url, Access: "none", TagPattern: strings.Repeat("v", 101)}), "tag pattern")

	view, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: gitFollowTags, TagPattern: "v2.*", Prereleases: true})
	assert.NilError(t, err)
	assert.Equal(t, view.Follow, gitFollowTags)
	assert.Equal(t, view.Branch, "")
	assert.Equal(t, view.TagPattern, "v2.*")
	assert.Equal(t, view.Prereleases, true)

	// not cloned yet: the mode and the branch change freely
	view, err = CreateGitApp(ctx, GitAppRegistration{Name: "other", URL: url, Access: "none", Branch: "develop"})
	assert.NilError(t, err)
	assert.Equal(t, view.Follow, gitFollowBranch)
	tags, branch, main := gitFollowTags, gitFollowBranch, "main"
	view, err = UpdateGitApp(ctx, "other", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	assert.Equal(t, view.Branch, "")
	view, err = UpdateGitApp(ctx, "other", GitAppChanges{Follow: &branch, Branch: &main})
	assert.NilError(t, err)
	assert.Equal(t, view.Follow, gitFollowBranch)
	assert.Equal(t, view.Branch, "main")
}

// A cloned app's folder takes the shape its mode deploys from, and none of its files moves:
// detached to follow tags, on the branch to follow one.
func TestTheModeOfAClonedAppChangesAndItsFolderFollows(t *testing.T) {
	clonedTestApp(t)
	ctx := context.Background()
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	head := folderHead(t, st.Dir)
	describe := func() git.Info {
		t.Helper()
		info, err := git.Describe(ctx, st.Dir)
		assert.NilError(t, err)
		return info
	}
	tags, branch, main, develop, pattern, on := gitFollowTags, gitFollowBranch, "main", "develop", "v2.*", true

	view, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	assert.Equal(t, view.Follow, gitFollowTags)
	assert.Equal(t, view.Branch, "")
	info := describe()
	assert.Equal(t, info.Branch, "", "detached")
	assert.Equal(t, info.Head, head, "on the commit it was on")

	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern, Prereleases: &on})
	assert.NilError(t, err)
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Branch: &main})
	assertBadRequest(t, err, "has no branch")

	// back on a branch, the remote's default, with the tag filter kept
	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch})
	assert.NilError(t, err)
	assert.Equal(t, view.Follow, gitFollowBranch)
	assert.Equal(t, view.Branch, "main")
	assert.Equal(t, view.TagPattern, "v2.*")
	assert.Equal(t, view.Prereleases, true)
	info = describe()
	assert.Equal(t, info.Branch, "main")
	assert.Equal(t, info.Head, head)

	// the branch of a cloned app changes with its mode only
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Branch: &develop})
	assertBadRequest(t, err, "check another branch out there instead")
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch, Branch: &develop})
	assert.NilError(t, err)
	assert.Equal(t, view.Branch, "develop")
	assert.Equal(t, describe().Branch, "develop")

	bad, badPattern := "tag", "["
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &bad})
	assertBadRequest(t, err, "follow is branch or tags")
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &badPattern})
	assertBadRequest(t, err, "tag pattern")
}
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/`

Expected: FAIL, compile errors in `git_tags_mode_internal_test.go`: `unknown field Follow in struct literal of type GitAppRegistration`, `unknown field TagPattern in struct literal of type GitAppRegistration`, `unknown field Follow in struct literal of type GitAppChanges`.

- [ ] **Step 3: Implement**

In `service/git_apps.go` (CRLF, keep it), replace `GitAppRegistration`:

```go
// GitAppRegistration is an app to create from a repository.
type GitAppRegistration struct {
	Name   string
	URL    string
	Branch string
	Access string
	Token  string
}
```

with:

```go
// GitAppRegistration is an app to create from a repository.
type GitAppRegistration struct {
	Name   string
	URL    string
	Branch string
	Access string
	Token  string
	// Follow is gitFollowBranch, empty for the same, or gitFollowTags; TagPattern and
	// Prereleases choose the tags an app that follows them may deploy.
	Follow      string
	TagPattern  string
	Prereleases bool
}
```

In `GitAppChanges`, replace:

```go
	// RegenerateWebhookSecret replaces the secret at once; refused while the webhook is off.
	RegenerateWebhookSecret *bool
}
```

with:

```go
	// RegenerateWebhookSecret replaces the secret at once; refused while the webhook is off.
	RegenerateWebhookSecret *bool
	// Follow changes the mode: the folder of a cloned app is put on the branch or detached.
	// TagPattern and Prereleases change the tags an app that follows them may deploy.
	Follow      *string
	TagPattern  *string
	Prereleases *bool
}
```

In `CreateGitApp`, replace:

```go
	if !validGitBranch(registration.Branch) {
		return nil, GitRequestError(fmt.Sprintf("`%s` is not a branch name", registration.Branch))
	}
	if err := checkGitAccess(registration.URL, registration.Access, registration.Token != ""); err != nil {
```

with:

```go
	if !validGitBranch(registration.Branch) {
		return nil, GitRequestError(fmt.Sprintf("`%s` is not a branch name", registration.Branch))
	}
	follow := registration.Follow
	if follow == "" {
		follow = gitFollowBranch
	}
	if err := checkGitFollow(follow); err != nil {
		return nil, err
	}
	if follow == gitFollowTags && registration.Branch != "" {
		return nil, errGitTagsHaveNoBranch
	}
	if err := checkGitTagPattern(registration.TagPattern); err != nil {
		return nil, err
	}
	if err := checkGitAccess(registration.URL, registration.Access, registration.Token != ""); err != nil {
```

and replace:

```go
		Remote: registration.URL, Branch: registration.Branch,
		History: []gitHistoryEntry{}, Attempted: []string{},
	}
```

with:

```go
		Remote: registration.URL, Branch: registration.Branch,
		Follow: follow, TagPattern: registration.TagPattern, Prereleases: registration.Prereleases,
		History: []gitHistoryEntry{}, Attempted: []string{},
	}
```

Replace `UpdateGitApp` whole (from `// UpdateGitApp changes the branch, the automatic rebuild or the access of a git app, and` down to its closing brace, the line before `// DeleteGitApp removes a registered app`) with:

```go
// UpdateGitApp changes the branch, the automatic rebuild, the access, the webhook or the mode
// of a git app, and adopts an adoptable one.
func UpdateGitApp(ctx context.Context, name string, changes GitAppChanges) (*GitAppView, error) {
	end, err := Begin(name, "settings change")
	if err != nil {
		return nil, err
	}
	defer end()

	st, err := findGitApp(ctx, name)
	if err != nil {
		return nil, err
	}

	access, token := st.Access, ""
	if changes.Access != nil {
		access = *changes.Access
	}
	if changes.Token != nil {
		if *changes.Token == "" {
			return nil, GitRequestError("a token cannot be empty: switch access to none or key to forget it")
		}
		token = *changes.Token
	}
	accessChanged := changes.Access != nil || changes.Token != nil
	if accessChanged {
		if err := checkGitAccess(st.Remote, access, token != "" || pathExists(gitAppFile(name, ".token"))); err != nil {
			return nil, err
		}
	}

	webhookOn := pathExists(gitAppFile(name, ".webhook"))
	if changes.WebhookEnabled != nil {
		webhookOn = *changes.WebhookEnabled
	}
	if lo.FromPtr(changes.RegenerateWebhookSecret) && !webhookOn {
		return nil, GitRequestError("the webhook is off: turn it on to get a secret")
	}

	follow := st.follow()
	if changes.Follow != nil {
		if err := checkGitFollow(*changes.Follow); err != nil {
			return nil, err
		}
		follow = *changes.Follow
	}
	switching := follow != st.follow()
	if changes.TagPattern != nil {
		if err := checkGitTagPattern(*changes.TagPattern); err != nil {
			return nil, err
		}
	}

	branch := st.Branch
	switch {
	case follow == gitFollowTags && lo.FromPtr(changes.Branch) != "":
		return nil, errGitTagsHaveNoBranch
	case follow == gitFollowTags:
		branch = ""
	case changes.Branch != nil && *changes.Branch != st.Branch:
		// the folder is on its branch: another comes with a change of mode only
		if st.Cloned && !switching {
			return nil, GitRequestError(fmt.Sprintf("the branch of %s is the one its folder is on: check another branch out there instead", name))
		}
		if !validGitBranch(*changes.Branch) {
			return nil, GitRequestError(fmt.Sprintf("`%s` is not a branch name", *changes.Branch))
		}
		branch = *changes.Branch
	}

	if st.Origin == gitOriginAdoptable {
		if err := adoptGitApp(ctx, st); err != nil {
			return nil, err
		}
	}

	if changes.AutoDeploy != nil {
		st.AutoDeploy = *changes.AutoDeploy
		if st.AutoDeploy {
			st.AutoPaused = false
		}
	}
	if accessChanged {
		if err := setGitAccess(st, access, token); err != nil {
			return nil, err
		}
	}
	if switching && st.Cloned {
		// after the access is set: the remote may be asked for its default branch
		if branch, err = followGitFolder(ctx, st, follow, branch); err != nil {
			return nil, err
		}
	}
	st.Follow, st.Branch = follow, branch
	if changes.TagPattern != nil {
		st.TagPattern = *changes.TagPattern
	}
	if changes.Prereleases != nil {
		st.Prereleases = *changes.Prereleases
	}
	if err := setGitWebhook(name, changes.WebhookEnabled, lo.FromPtr(changes.RegenerateWebhookSecret)); err != nil {
		return nil, err
	}

	if err := saveGitApp(st); err != nil {
		return nil, err
	}

	return newGitAppView(ctx, st), nil
}

// followGitFolder puts a cloned app's folder in the shape its new mode deploys from, and moves
// none of its files: detached at its commit to follow tags, on the branch to follow one, the
// remote's default when none is given. It returns the branch the app follows.
func followGitFolder(ctx context.Context, st *gitApp, follow, branch string) (string, error) {
	if follow == gitFollowTags {
		return "", git.Detach(ctx, st.Dir)
	}

	if branch == "" {
		auth, err := gitAuth(st)
		if err == nil {
			branch, err = git.DefaultBranch(ctx, st.Remote, auth)
		}
		if err == nil && (branch == "" || !validGitBranch(branch)) {
			err = fmt.Errorf("`%s` is not a branch name", branch)
		}
		if err != nil {
			return "", GitRequestError(fmt.Sprintf("the remote's default branch cannot be read, give the branch to follow: %v", err))
		}
	}

	return branch, git.Attach(ctx, st.Dir, branch)
}
```

- [ ] **Step 4: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && for f in service/git_apps.go service/git_tags_mode_internal_test.go; do tr -d '\r' < "$f" | gofmt -l | sed "s|<standard input>|$f|"; done; git ls-files --eol service/git_apps.go | grep mixed; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/ ./route/...`

Expected: no output.

Run (Linux, after `go generate ./...`): `go test ./service/ -run 'TestAnAppThatFollowsTagsIsRegisteredWithoutABranch|TestTheModeOfAClonedAppChangesAndItsFolderFollows|TestRegisteringAGitAppChecksWhatItIsGiven|TestTheAccessOfAGitAppChangesAndItsTokenIsNeverShown|TestAStackStartedByHandInAGitFolderIsAdoptedByTheFirstAction|TestAFolderInDetachedHeadOrWithoutARemoteIsShownButNotAdopted|TestAWebhookSecretIsMadeReplacedAndForgotten' -count=1 -v`

Expected: `--- PASS` for each test named, `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`.

- [ ] **Step 5: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add service/git_apps.go service/git_tags_mode_internal_test.go
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): register an app that follows tags, and switch its mode"
```

---

### Task 4: The check in tag mode, the clone at the tag, and the tag list

**Files:**
- Modify: `service/git_tags.go` (imports; append `gitTagsListed`, `remoteGitTags`, `eligibleGitTag`, `GitAppTags`)
- Modify: `service/git_apps.go:415-454` (`checkGitApp`), `:456-470` (`cloneGitApp` signature and its `git.Clone` call) — CRLF
- Modify: `service/git_restore.go:78` (`cloneGitApp` call) — CRLF
- Modify: `service/git_app_view.go:119-120` (`gitAppStateOf`: its `unreachable` case) — CRLF
- Test: `service/git_tags_check_internal_test.go` (create)

**Interfaces:**
- Consumes: Task 1 (`git.LsRemoteTags`), Task 2 (`eligibleGitTags`, `gitNoTag`, `gitNoTagMessage`, `GitTag`, `st.followsTags()`, `gitCheck.RemoteTag`), Task 3 (`GitAppRegistration.Follow`, `GitAppChanges.Prereleases`, `GitAppChanges.TagPattern`), `findGitApp`, `gitAuth`; test helpers `newTestRemote`, `pushTestCommit`, `gitShell`, `checkTestApp`, `gitAppsIn`, `withFakeGitDocker`, `assertBadRequest`.
- Produces:
  - `const gitTagsListed = 50`.
  - `func remoteGitTags(ctx context.Context, st *gitApp, auth git.Auth) ([]GitTag, error)`: eligible tags of the remote, highest first.
  - `func eligibleGitTag(ctx context.Context, st *gitApp, auth git.Auth, tag string) (GitTag, error)`: `tag` among them, the highest when empty; `GitRequestError` `` `x` is not one of the eligible tags of the remote `` or `gitNoTagMessage(...)`; a network error as is.
  - `func GitAppTags(ctx context.Context, name string) ([]GitTag, error)`: at most 50, never nil; 400 `<name> follows a branch, not tags`; 400 `the repository cannot be reached: <git>`; `ErrGitAppNotFound`.
  - `func cloneGitApp(ctx context.Context, st *gitApp, ref string, auth git.Auth) error` (was without `ref`).
  - `gitAppStateOf` answers `idle`, not `unreachable`, for a `check.error` that starts with `gitNoTag`.
  - test helpers `func pushTestTag(t *testing.T, work, tag string) string` (annotated tag on the work tree's HEAD, pushed; returns the commit), `func taggedTestApp(t *testing.T) (fake *fakeGitRuntime, work, first string)` (created app `jarvis` following tags, checked and cloned at `v1.0.0` on the remote's first commit `first`, not deployed).

- [ ] **Step 1: Write the failing test**

Create `service/git_tags_check_internal_test.go`:

```go
package service

import (
	"context"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

// pushTestTag tags the work tree's HEAD with an annotated tag, pushes the tag, and returns the
// commit.
func pushTestTag(t *testing.T, work, tag string) string {
	t.Helper()

	return gitShell(t, work, "git tag -a -m "+tag+" "+tag+" && git push -q origin "+tag+" && git rev-parse HEAD")
}

// taggedTestApp is a created app that follows tags, checked and cloned at v1.0.0, the remote's
// first commit, and not deployed yet.
func taggedTestApp(t *testing.T) (fake *fakeGitRuntime, work, first string) {
	t.Helper()

	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake = withFakeGitDocker(t)
	url, work := newTestRemote(t)
	first = pushTestTag(t, work, "v1.0.0")

	_, err := CreateGitApp(context.Background(), GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: gitFollowTags})
	assert.NilError(t, err)
	st := checkTestApp(t, "jarvis")
	assert.Equal(t, st.Cloned, true, st.Check.Error)
	assert.Equal(t, st.Check.RemoteTag, "v1.0.0")

	return fake, work, first
}

func TestAnAppThatFollowsTagsIsClonedAtItsHighestTag(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	first := pushTestTag(t, work, "v1.0.0")
	second := pushTestCommit(t, work, "index.html", "v2")
	gitShell(t, work, "git tag v1.1.0 && git push -q origin v1.1.0")
	third := pushTestCommit(t, work, "index.html", "v3")
	gitShell(t, work, "git tag -a -m rc v1.2.0-rc.1 && git tag latest && git push -q origin --tags")

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: gitFollowTags})
	assert.NilError(t, err)
	st := checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.Error, "")
	assert.Equal(t, st.Branch, "", "the remote's default branch is never asked for")
	assert.Equal(t, st.Check.RemoteTag, "v1.1.0")
	assert.Equal(t, st.Check.RemoteCommit, second)
	assert.Equal(t, st.Cloned, true)
	info, err := git.Describe(ctx, st.Dir)
	assert.NilError(t, err)
	assert.Equal(t, info.Branch, "", "detached at the tag")
	assert.Equal(t, info.Head, second)

	// pre-releases, once included
	on := true
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Prereleases: &on})
	assert.NilError(t, err)
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.2.0-rc.1")
	assert.Equal(t, st.Check.RemoteCommit, third)

	// no tag matches: the check says so under which filter, and keeps what it saw last
	pattern := "v2.*"
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.Error, "no tag matches (pattern `v2.*`, pre-releases included)")
	assert.Equal(t, st.Check.RemoteTag, "v1.2.0-rc.1")
	assert.Equal(t, st.Check.RemoteCommit, third)
	assert.Equal(t, gitAppStateOf(st), "idle", "the repository was reached")

	pattern = ""
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)
	tags, err := GitAppTags(ctx, "jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, tags, []GitTag{{Name: "v1.2.0-rc.1", Commit: third}, {Name: "v1.1.0", Commit: second}, {Name: "v1.0.0", Commit: first}})
}

func TestTheTagsOfAnAppAreListedHighestFirstFiftyAtMost(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	head := gitShell(t, work, "git rev-parse HEAD")
	gitShell(t, work, "for i in $(seq 1 55); do git tag v0.1.$i; done && git tag -a -m one v1.0.0 && git tag v1.1.0-rc.1 && git push -q origin --tags")

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none", Follow: gitFollowTags})
	assert.NilError(t, err)
	tags, err := GitAppTags(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, len(tags), 50)
	assert.Equal(t, tags[0], GitTag{Name: "v1.0.0", Commit: head}, "an annotated tag, peeled")
	assert.Equal(t, tags[1].Name, "v0.1.55")
	assert.Equal(t, tags[49].Name, "v0.1.7")

	pattern := "v9.*"
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)
	tags, err = GitAppTags(ctx, "jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, tags, []GitTag{})

	_, err = CreateGitApp(ctx, GitAppRegistration{Name: "other", URL: url, Access: "none"})
	assert.NilError(t, err)
	_, err = GitAppTags(ctx, "other")
	assertBadRequest(t, err, "follows a branch, not tags")
	_, err = GitAppTags(ctx, "nextcloud")
	assert.ErrorIs(t, err, ErrGitAppNotFound)
}
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/`

Expected: FAIL, `undefined: GitAppTags` in `git_tags_check_internal_test.go`. (Were it to compile, the check test would fail on Linux: the check asks for the default branch and never records `remote_tag`.)

- [ ] **Step 3: Implement**

In `service/git_tags.go`, replace the import block with:

```go
import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/Masterminds/semver/v3"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
)
```

and append at the end of the file:

```go

// gitTagsListed is as many tags as GitAppTags gives.
const gitTagsListed = 50

// remoteGitTags is the eligible tags of the app's remote, highest first.
func remoteGitTags(ctx context.Context, st *gitApp, auth git.Auth) ([]GitTag, error) {
	tags, err := git.LsRemoteTags(ctx, st.Remote, auth)
	if err != nil {
		return nil, err
	}

	return eligibleGitTags(tags, st.TagPattern, st.Prereleases), nil
}

// eligibleGitTag is tag among the eligible tags of the app's remote, the highest when tag is
// empty. What is not there is a request error that says so; a remote that cannot be reached
// is git's own error.
func eligibleGitTag(ctx context.Context, st *gitApp, auth git.Auth, tag string) (GitTag, error) {
	tags, err := remoteGitTags(ctx, st, auth)
	if err != nil {
		return GitTag{}, err
	}

	for _, eligible := range tags {
		if tag == "" || eligible.Name == tag {
			return eligible, nil
		}
	}
	if tag != "" {
		return GitTag{}, GitRequestError(fmt.Sprintf("`%s` is not one of the eligible tags of the remote", tag))
	}

	return GitTag{}, GitRequestError(gitNoTagMessage(st.TagPattern, st.Prereleases))
}

// GitAppTags is the eligible tags of an app that follows tags, highest first, at most 50. It
// asks the remote, as a check does.
func GitAppTags(ctx context.Context, name string) ([]GitTag, error) {
	st, err := findGitApp(ctx, name)
	if err != nil {
		return nil, err
	}
	if !st.followsTags() {
		return nil, GitRequestError(fmt.Sprintf("%s follows a branch, not tags", name))
	}

	auth, err := gitAuth(st)
	if err != nil {
		return nil, err
	}
	tags, err := remoteGitTags(ctx, st, auth)
	if err != nil {
		return nil, GitRequestError(fmt.Sprintf("the repository cannot be reached: %v", err))
	}

	return tags[:min(len(tags), gitTagsListed)], nil
}
```

In `service/git_apps.go` (CRLF, keep it), replace `checkGitApp` whole (from `// checkGitApp asks the remote where the branch is and records the answer, then clears the` down to its closing brace) with:

```go
// checkGitApp asks the remote where the branch is, or which is the highest eligible tag, and
// records the answer, then clears the operation. With clone, a registered app that is not
// cloned yet is cloned, at that tag for an app that follows tags, and its compose files
// validated. Whatever fails is recorded in the check, and changes nothing else.
func checkGitApp(ctx context.Context, st *gitApp, clone bool) {
	defer func() {
		st.Operation = nil
		if err := saveGitApp(st); err != nil {
			logger.Error("the check of a git app could not be written down", zap.Error(err), zap.String("app", st.App))
		}
	}()

	previous := gitCheck{}
	if st.Check != nil {
		previous = *st.Check
	}
	st.Check = &gitCheck{At: time.Now().UTC(), RemoteCommit: previous.RemoteCommit, RemoteTag: previous.RemoteTag}
	st.NoComposeFile = false

	auth, err := gitAuth(st)
	var remote GitTag
	if err == nil && st.followsTags() {
		// never the default branch: an app that follows tags has none
		remote, err = eligibleGitTag(ctx, st, auth, "")
	} else if err == nil {
		if st.Branch == "" {
			st.Branch, err = git.DefaultBranch(ctx, st.Remote, auth)
		}
		if err == nil {
			remote.Commit, err = git.LsRemote(ctx, st.Remote, st.Branch, auth)
		}
	}
	if err != nil {
		st.Check.Error = err.Error()
		return
	}
	st.Check.RemoteCommit, st.Check.RemoteTag = remote.Commit, remote.Name

	if st.Cloned || !clone {
		return
	}

	ref := st.Branch
	if st.followsTags() {
		ref = remote.Name
	}
	if err := cloneGitApp(ctx, st, ref, auth); err != nil {
		st.Check.Error = err.Error()
	}
}
```

Replace the head of `cloneGitApp`:

```go
// cloneGitApp clones a created app into its folder, which must be absent or empty, and
// keeps the clone only when it holds a compose file.
func cloneGitApp(ctx context.Context, st *gitApp, auth git.Auth) error {
```

with:

```go
// cloneGitApp clones a created app into its folder, which must be absent or empty, at ref, its
// branch or a tag, and keeps the clone only when it holds a compose file.
func cloneGitApp(ctx context.Context, st *gitApp, ref string, auth git.Auth) error {
```

and in its body replace:

```go
	if err := git.Clone(ctx, st.Remote, st.Branch, partial, auth); err != nil {
```

with:

```go
	if err := git.Clone(ctx, st.Remote, ref, partial, auth); err != nil {
```

In `service/git_restore.go` (CRLF, keep it), replace:

```go
		if err := cloneGitApp(ctx, st, auth); err != nil {
```

with:

```go
		if err := cloneGitApp(ctx, st, st.Branch, auth); err != nil {
```

In `service/git_app_view.go` (CRLF, keep it), in `gitAppStateOf`, replace:

```go
	case st.Check != nil && st.Check.Error != "":
		return "unreachable"
```

with:

```go
	case st.Check != nil && st.Check.Error != "" && !strings.HasPrefix(st.Check.Error, gitNoTag):
		// a check that found no eligible tag reached the repository: its message says why
		// nothing deploys
		return "unreachable"
```

(`strings` is imported there already.)

- [ ] **Step 4: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && for f in service/git_tags.go service/git_tags_check_internal_test.go service/git_apps.go service/git_restore.go service/git_app_view.go; do tr -d '\r' < "$f" | gofmt -l | sed "s|<standard input>|$f|"; done; git ls-files --eol service/git_apps.go service/git_restore.go service/git_app_view.go | grep mixed; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/ ./route/...`

Expected: no output.

Run (Linux, after `go generate ./...`): `go test ./service/ -run 'TestAnAppThatFollowsTagsIsClonedAtItsHighestTag|TestTheTagsOfAnAppAreListedHighestFirstFiftyAtMost|TestGitAppStateFollowsTheRule|TestACheckClonesAndReviewsARegisteredAppInTheBackground|TestARepositoryWithoutAComposeFileEndsWithAnExampleAndNoClone|TestAnUnreachableRepositoryIsRecordedAndNothingElseChanges|TestThePeriodicCheckDeploysWhatItMayAndClonesNothing|TestAGitAppIsInstalledFromItsBackupAtTheBackedUpCommit' -count=1 -v`

Expected: `--- PASS` for each test named, `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`.

- [ ] **Step 5: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add service/git_tags.go service/git_tags_check_internal_test.go service/git_apps.go service/git_restore.go service/git_app_view.go
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): check an app that follows tags, clone it at its tag, list its tags"
```

---

### Task 5: Deploying a tag by hand, and refusing a tag that moved

**Files:**
- Modify: `service/git_deploy.go:3-19` (imports: add `"errors"`), `:37-293` (from `// DeployGitApp deploys commit` to the closing brace of `fetchAndBuild`), `:349-395` (`rollBackGitDeploy`, `failGitDeploy`), `:410-426` (`deployGitAppAutomatically`)
- Modify: `service/git_app_state.go:389-408` (after `historyEntry`: `gitRan`) — CRLF
- Modify: `service/git_lifecycle.go:77-78` (the interrupted entry keeps the tag)
- Modify: `service/git_restore.go:103` (the `runGitDeploy` call gains its tag argument; Task 7 rewrites this function) — CRLF
- Test: `service/git_tags_deploy_internal_test.go` (create)

**Interfaces:**
- Consumes: Task 1 (`git.FetchTag`, `git.ResetKeep`), Task 2 (`gitTagHigher`, `st.followsTags()`, `GitTag`, the `Tag` fields), Task 4 (`eligibleGitTag`, `taggedTestApp`, `pushTestTag`), `findGitApp`, `adoptGitApp`, `gitAuth`, `buildGitCommit`, `gitFolderContainedIn`, `finishGitDeploy`, `handOver`, `Begin`, `RecoverGitApps`; test helpers `deployTestApp`, `deployedTestApp`, `waitForGitApp`, `folderHead`, `checkTestApp`, `assertBadRequest`, `gitShell`, `pushTestCommit`.
- Produces:
  - `func DeployGitAppTag(ctx context.Context, name, tag string, env *string) (*GitAppView, error)` (empty tag: the highest eligible).
  - `DeployGitApp(ctx, name, commit string, env *string)` unchanged signature; in tag mode an empty commit deploys the highest eligible tag, a commit must be the running one (a repair, under `deployed.tag`) or a history version that ran (a revert, under its entry's tag). On a branch, a commit given by hand runs under the tag it ran as (`deployed.tag` for the running one, its history entry's `tag` for another); the branch's head records `""`.
  - Tag mode: the running commit deployed by hand (by its tag, as the highest, or by hash) is never a revert: a repair when blocked, moved or under another tag, else 400 `<12> is already deployed: there is nothing new to build`.
  - Branch mode, first deployment after tags (`previous.Tag != ""`): `fetchGitBranch` skips the "descends from the deployed" rule and `runGitDeploy` moves with `git.ResetKeep`.
  - Pause in `runGitDeploy` (manual, not a redeployment): `st.AutoPaused = (revert && (previousTag != "" || !st.followsTags())) || gitTagHigher(previousTag, tag)`, `previousTag` being `previous.Tag` or `""`.
  - `fetchAndBuild` in tag mode fetches the tag unless `previous != nil && target == previous.Commit` (a repair).
  - Internal signatures: `startGitDeploy(ctx, name, commit, tag string, env *string, trigger gitTrigger)`, `deployHolding(ctx, name, commit, tag string, env *string, trigger gitTrigger, end func())`, `gitTagToDeploy(ctx, st *gitApp, tag string) (GitTag, error)`, `gitDeployPreconditions(ctx, st, target, tag string, env *string, revert bool) error`, `runGitDeploy(ctx, st, target, tag string, revert bool, trigger gitTrigger)`, `fetchAndBuild(ctx, st, target, tag string, previous *gitDeployment, auth git.Auth)`, `fetchGitBranch(ctx, st, target string, previous *gitDeployment, auth git.Auth) error`, `fetchGitTag(ctx, st, target, tag string, auth git.Auth) error`, `rollBackGitDeploy(ctx, st, target, tag, subject string, images map[string]string, previous *gitDeployment, cause error)`, `failGitDeploy(ctx, st, target, tag, subject string, images map[string]string, outcome string, cause error)`.
  - `func gitRan(entry *gitHistoryEntry) bool` (outcome `deployed` or `adopted`).
  - Messages: `<app> follows a branch: deploy a commit, not a tag`; `<12> is no version of the history that ran: <app> follows tags, deploy one of them`; `<dir> is on the branch <b>: an app that follows tags deploys from a detached HEAD`; `the tag <tag> moved since the check, from <12> to <12>: check again`.
  - test helper `func deployedTaggedTestApp(t *testing.T, autoDeploy bool) (fake *fakeGitRuntime, work, first string)`: `taggedTestApp` deployed by hand at `v1.0.0` (`first`), auto as asked, `fake.calls` cleared.

- [ ] **Step 1: Write the failing test**

Create `service/git_tags_deploy_internal_test.go`:

```go
package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

// deployedTaggedTestApp is taggedTestApp deployed by hand at v1.0.0, with its automatic
// deployment set as asked.
func deployedTaggedTestApp(t *testing.T, autoDeploy bool) (fake *fakeGitRuntime, work, first string) {
	t.Helper()

	fake, work, first = taggedTestApp(t)
	st := deployTestApp(t)
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, st.Deployed.Tag, "v1.0.0")

	st.AutoDeploy = autoDeploy
	assert.NilError(t, saveGitApp(st))
	fake.calls = nil

	return fake, work, first
}

func TestAnyEligibleTagIsDeployedByHand(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, true)
	ctx := context.Background()
	second := pushTestCommit(t, work, "index.html", "v2")
	pushTestTag(t, work, "v1.1.0")
	// a fix of 1.0 on a line of its own: tags do not form a line
	hotfix := gitShell(t, work, "git checkout -q -b hotfix "+first+" && echo fix > fix.txt && git add fix.txt && git commit -qm fix && git tag -a -m v1.0.1 v1.0.1 && git push -q origin v1.0.1 && git rev-parse HEAD && git checkout -q main")

	// the highest, when no tag is given
	view, err := DeployGitApp(ctx, "jarvis", "", nil)
	assert.NilError(t, err)
	assert.Equal(t, view.Operation.Tag, "v1.1.0", "the tag is known as the deployment starts")
	st := waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Commit, second)
	assert.Equal(t, st.Deployed.Tag, "v1.1.0")
	assert.Equal(t, st.History[0].Tag, "v1.1.0")
	assert.Equal(t, st.AutoPaused, false)

	// an older tag that never ran is built from its own line, and pauses the automatic deployment
	fake.calls = nil
	_, err = DeployGitAppTag(ctx, "jarvis", "v1.0.1", nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + hotfix[:12], "retag " + hotfix[:12], "start " + hotfix[:12]})
	assert.Equal(t, st.Deployed.Tag, "v1.0.1")
	assert.Equal(t, folderHead(t, st.Dir), hotfix)
	assert.Equal(t, st.AutoPaused, true, "or the next check would upgrade it again")

	// a tag whose commit ran is a revert: nothing is built
	fake.calls = nil
	_, err = DeployGitAppTag(ctx, "jarvis", "v1.1.0", nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Commit, second)
	assert.Equal(t, st.Deployed.Tag, "v1.1.0")

	// a commit of the history, given as such, runs again under the tag it ran as
	fake.calls = nil
	_, err = DeployGitApp(ctx, "jarvis", hotfix, nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"retag " + hotfix[:12], "start " + hotfix[:12]})
	assert.Equal(t, st.Deployed.Tag, "v1.0.1")

	// first left the history: a commit no version of the history ran is refused
	_, err = DeployGitApp(ctx, "jarvis", first, nil)
	assertBadRequest(t, err, "no version of the history that ran")
}

func TestADeploymentOfATagRefusesWhatItCannotDo(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, false)
	ctx := context.Background()
	pushTestCommit(t, work, "index.html", "v2")
	gitShell(t, work, "git tag -a -m rc v1.1.0-rc.1 && git tag latest && git push -q origin --tags")

	// what runs, by its tag, as the highest tag, or by its commit: nothing new to build
	_, err := DeployGitAppTag(ctx, "jarvis", "v1.0.0", nil)
	assertBadRequest(t, err, "is already deployed")
	_, err = DeployGitApp(ctx, "jarvis", "", nil)
	assertBadRequest(t, err, "is already deployed")
	_, err = DeployGitApp(ctx, "jarvis", first, nil)
	assertBadRequest(t, err, "is already deployed")

	for _, tag := range []string{"v9.9.9", "v1.1.0-rc.1", "latest", "-v1.0.0"} {
		_, err := DeployGitAppTag(ctx, "jarvis", tag, nil)
		assertBadRequest(t, err, "not one of the eligible tags")
	}

	pattern := "v2.*"
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)
	_, err = DeployGitApp(ctx, "jarvis", "", nil)
	assertBadRequest(t, err, "no tag matches (pattern `v2.*`, pre-releases excluded)")

	// a folder put on a branch by hand
	pattern = ""
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	gitShell(t, st.Dir, "git checkout -q -b mine")
	_, err = DeployGitApp(ctx, "jarvis", "", nil)
	assertBadRequest(t, err, "is on the branch mine")

	assert.DeepEqual(t, fake.Calls(), []string{})
}

func TestABranchAppIsDeployedByCommitNotByTag(t *testing.T) {
	fake, _, _ := deployedTestApp(t, false)

	_, err := DeployGitAppTag(context.Background(), "jarvis", "v1.0.0", nil)

	assertBadRequest(t, err, "follows a branch")
	assert.DeepEqual(t, fake.Calls(), []string{})
}

// A deployment builds what its check or its request saw: a tag moved in between is refused,
// and the commit is not tried again automatically.
func TestATagThatMovedSinceTheCheckIsRefused(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, false)
	ctx := context.Background()
	second := pushTestCommit(t, work, "index.html", "v2")
	pushTestTag(t, work, "v1.1.0")
	st := checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.1.0")
	assert.Equal(t, st.Check.RemoteCommit, second)
	moved := pushTestCommit(t, work, "index.html", "v3")
	gitShell(t, work, "git tag -f -a -m v1.1.0 v1.1.0 >/dev/null && git push -q -f origin v1.1.0")

	st.AutoDeploy = true
	assert.NilError(t, saveGitApp(st))
	end, err := Begin("jarvis", gitOperationCheck)
	assert.NilError(t, err)
	deployGitAppAutomatically(ctx, "jarvis", end)
	st = waitForGitApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Equal(t, st.History[0].Tag, "v1.1.0")
	assert.Assert(t, strings.Contains(st.History[0].Reason, "moved since the check"), st.History[0].Reason)
	assert.Assert(t, strings.Contains(st.History[0].Reason, moved[:12]), st.History[0].Reason)
	assert.DeepEqual(t, st.Attempted, []string{second})
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first)

	// by hand the same: the request found v1.1.0 on second, and the tag moved before the
	// fetch. This is what the deployment DeployGitAppTag starts runs, holding the app.
	end, err = Begin("jarvis", gitOperationDeploy)
	assert.NilError(t, err)
	st.Operation = &gitOperation{Kind: gitOperationBuild, Commit: second, Tag: "v1.1.0", StartedAt: time.Now().UTC()}
	assert.NilError(t, saveGitApp(st))
	runGitDeploy(ctx, st, second, "v1.1.0", false, gitTriggerManual)
	end()
	st = waitForGitApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Equal(t, st.History[0].Commit, second)
	assert.Assert(t, strings.Contains(st.History[0].Reason, "moved since the check"), st.History[0].Reason)
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first)
}

// A repair rebuilds the running version given by hand, even once it left the history and
// its tag moved on the remote: the folder holds its commit, and no tag is fetched.
func TestARepairOfAnAppThatFollowsTagsNeedsNeitherItsHistoryNorItsTag(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, false)
	pushTestCommit(t, work, "index.html", "v2")
	gitShell(t, work, "git tag -f -a -m v1.0.0 v1.0.0 >/dev/null && git push -q -f origin v1.0.0")
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	// failed attempts pushed it out of the history, a rollback failed, and its images went
	st.History, st.Blocked = []gitHistoryEntry{}, true
	assert.NilError(t, saveGitApp(st))
	fake.missing["jarvis-web:git-"+first[:12]] = true

	_, err = DeployGitApp(context.Background(), "jarvis", first, nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{"build " + first[:12], "retag " + first[:12], "start " + first[:12]})
	assert.Equal(t, st.Blocked, false)
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, st.Deployed.Tag, "v1.0.0")
	assert.Equal(t, st.History[0].Outcome, gitOutcomeDeployed)
}

func TestAnInterruptedDeploymentOfATagKeepsItsTag(t *testing.T) {
	_, work, _ := deployedTaggedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	st.Operation = &gitOperation{Kind: gitOperationBuild, Commit: second, Tag: "v1.1.0", StartedAt: time.Now().UTC()}
	assert.NilError(t, saveGitApp(st))

	RecoverGitApps(context.Background())

	st, err = loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeInterrupted)
	assert.Equal(t, st.History[0].Commit, second)
	assert.Equal(t, st.History[0].Tag, "v1.1.0")
}
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/`

Expected: FAIL, `undefined: DeployGitAppTag` in `git_tags_deploy_internal_test.go`. (On Linux, `deployedTaggedTestApp` would also fail: a deployment of an app that follows tags asks the remote for its branch.)

- [ ] **Step 3: Implement**

In `service/git_deploy.go`, replace the import block (lines 3-19) with:

```go
import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/samber/lo"
	"go.uber.org/zap"
)
```

Replace everything from the line `// DeployGitApp deploys commit, or the remote's latest when commit is empty, in the` down to the closing brace of `fetchAndBuild` (the line before `// gitFolderContainedIn refuses a folder holding commits target does not contain, made there`) with:

```go
// DeployGitApp deploys commit, or the remote's latest when commit is empty (for an app that
// follows tags, the highest eligible tag), in the background. A commit of the history is a
// revert. env is written as .env first.
func DeployGitApp(ctx context.Context, name, commit string, env *string) (*GitAppView, error) {
	return startGitDeploy(ctx, name, commit, "", env, gitTriggerManual)
}

// DeployGitAppTag deploys tag, one of the eligible tags of an app that follows tags, lower ones
// included, or the highest when tag is empty, in the background. A tag whose commit ran is a
// revert. env is written as .env first.
func DeployGitAppTag(ctx context.Context, name, tag string, env *string) (*GitAppView, error) {
	return startGitDeploy(ctx, name, "", tag, env, gitTriggerManual)
}

// startGitDeploy checks what must hold, records the operation and starts the deployment,
// and returns the app as the deployment starts. The guard is released when it ends.
func startGitDeploy(ctx context.Context, name, commit, tag string, env *string, trigger gitTrigger) (*GitAppView, error) {
	if commit != "" && !gitCommitPattern.MatchString(commit) {
		return nil, GitRequestError(fmt.Sprintf("`%s` is not a full commit hash", commit))
	}

	end, err := Begin(name, gitOperationDeploy)
	if err != nil {
		return nil, err
	}

	return deployHolding(ctx, name, commit, tag, env, trigger, end)
}

// deployHolding is startGitDeploy for a caller that holds the app already: end is released
// when the deployment ends, or before returning when it does not start. The automatic
// deployment gives the commit and the tag its check saw; by hand, an app that follows tags is
// given a tag, a commit of its history, or neither for the highest tag.
func deployHolding(ctx context.Context, name, commit, tag string, env *string, trigger gitTrigger, end func()) (*GitAppView, error) {
	handedOff := false
	defer func() {
		if !handedOff {
			end()
		}
	}()

	st, err := findGitApp(ctx, name)
	if err != nil {
		return nil, err
	}
	if st.Origin == gitOriginAdoptable {
		if err := adoptGitApp(ctx, st); err != nil {
			return nil, err
		}
	}

	revert := commit != "" && st.historyEntry(commit) != nil
	target := commit
	switch {
	case !st.followsTags() && tag != "":
		return nil, GitRequestError(fmt.Sprintf("%s follows a branch: deploy a commit, not a tag", st.App))
	case !st.followsTags() && target == "":
		// the branch's head, which runs under no tag
		auth, err := gitAuth(st)
		if err != nil {
			return nil, err
		}
		if target, err = git.LsRemote(ctx, st.Remote, st.Branch, auth); err != nil {
			return nil, GitRequestError(fmt.Sprintf("the repository cannot be reached: %v", err))
		}
	case !st.followsTags() && trigger == gitTriggerManual:
		// a commit given by hand runs as it ran: a version a tag brought stays one, whatever
		// the branch holds, until the branch's head is deployed
		if st.Deployed != nil && target == st.Deployed.Commit {
			tag = st.Deployed.Tag
		} else if entry := st.historyEntry(target); entry != nil {
			tag = entry.Tag
		}
	case st.followsTags() && target == "":
		// by hand: the tag asked for, or the highest, as the remote has it now
		chosen, err := gitTagToDeploy(ctx, st, tag)
		if err != nil {
			return nil, err
		}
		target, tag = chosen.Commit, chosen.Name
		// a tag whose commit ran before is a revert: its images are there. The running
		// version is none: it is deployed again only as a repair
		revert = gitRan(st.historyEntry(target)) && (st.Deployed == nil || target != st.Deployed.Commit)
	case st.followsTags() && tag == "":
		// by hand, a commit: the running version, deployed again under its tag as a repair,
		// or a version of the history that ran, under the tag it ran as
		entry := st.historyEntry(target)
		switch {
		case st.Deployed != nil && target == st.Deployed.Commit:
			tag, revert = st.Deployed.Tag, false
		case gitRan(entry):
			tag = entry.Tag
		default:
			return nil, GitRequestError(fmt.Sprintf("%s is no version of the history that ran: %s follows tags, deploy one of them", target[:12], st.App))
		}
	}

	if err := gitDeployPreconditions(ctx, st, target, tag, env, revert); err != nil {
		if trigger == gitTriggerAutomatic {
			// nothing changes but the reason, which the dashboard is told, and the commit is
			// not tried again
			failGitDeploy(common.WithProperties(ctx, map[string]string{common.PropertyTypeAppName.Name: st.App}), st, target, tag, "", nil, gitOutcomeFailed, err)
		}

		return nil, err
	}
	// the deployed commit gets past the preconditions only as a repair: its version deployed
	// again, which is no revert
	redeploy := st.Deployed != nil && target == st.Deployed.Commit
	revert = revert && !redeploy

	if env != nil {
		if err := (&ComposeApp{WorkingDir: st.Dir}).WriteEnvFile([]byte(*env)); err != nil {
			return nil, err
		}
	}

	kind := gitOperationBuild
	switch {
	case revert:
		kind = gitOperationRevert
	case redeploy && gitDocker.ImagesExist(ctx, st.Deployed.Images):
		kind = gitOperationDeploy
	}
	st.Operation = &gitOperation{Kind: kind, Commit: target, Tag: tag, StartedAt: time.Now().UTC()}
	if err := saveGitApp(st); err != nil {
		return nil, err
	}

	// read before the deployment runs: it changes st from here on
	view := newGitAppView(ctx, st)

	handedOff = true
	go func() {
		defer end()
		runGitDeploy(context.Background(), st, target, tag, revert, trigger)
	}()

	return view, nil
}

// gitTagToDeploy is tag among the eligible tags of the remote, the highest when tag is empty:
// what a deployment by hand of an app that follows tags goes to.
func gitTagToDeploy(ctx context.Context, st *gitApp, tag string) (GitTag, error) {
	auth, err := gitAuth(st)
	if err != nil {
		return GitTag{}, err
	}

	chosen, err := eligibleGitTag(ctx, st, auth, tag)
	if err != nil && !errors.As(err, new(GitRequestError)) {
		err = GitRequestError(fmt.Sprintf("the repository cannot be reached: %v", err))
	}

	return chosen, err
}

// gitDeployPreconditions is what must hold before anything is touched.
func gitDeployPreconditions(ctx context.Context, st *gitApp, target, tag string, env *string, revert bool) error {
	if !st.Cloned {
		return GitRequestError(fmt.Sprintf("%s is not cloned yet: check it first", st.App))
	}

	info, err := git.Describe(ctx, st.Dir)
	switch {
	case err != nil:
		return GitRequestError(fmt.Sprintf("%s is not a git work tree any more: %v", st.Dir, err))
	case info.Head == "":
		return GitRequestError(fmt.Sprintf("%s holds no commit any more", st.Dir))
	case info.Branch != st.Branch && st.followsTags():
		return GitRequestError(fmt.Sprintf("%s is on the branch %s: an app that follows tags deploys from a detached HEAD", st.Dir, info.Branch))
	case info.Branch != st.Branch:
		return GitRequestError(fmt.Sprintf("%s is not on the branch %s", st.Dir, st.Branch))
	case !info.TrackedFilesClean:
		return GitRequestError(fmt.Sprintf("tracked files were modified in %s: commit or discard those changes first", st.Dir))
	}

	if env != nil {
		if st.Deployed != nil {
			return GitRequestError(".env is written with the first deployment only; change it from the app's .env settings")
		}
		if tracked, _ := git.IsTracked(ctx, st.Dir, ".env"); tracked {
			return GitRequestError("the repository tracks .env, and CasaOS never writes a tracked file")
		}
		if _, err := ParseEnvFile([]byte(*env)); err != nil {
			return GitRequestError(err.Error())
		}
	}

	if st.Deployed != nil && target == st.Deployed.Commit && (st.Blocked || info.Head != target || tag != st.Deployed.Tag) {
		// a repair: the version that runs, deployed again over a rollback that failed, a folder
		// an interrupted revert moved, or under another tag (or none, back on a branch), puts
		// the folder back on its commit. On a branch that commit must contain whatever the
		// folder is on; tags do not form a line
		if st.followsTags() {
			return nil
		}
		if err := gitFolderContainedIn(ctx, st.Dir, target, target); err != nil {
			return GitRequestError(err.Error())
		}

		return nil
	}
	if st.Deployed != nil && !revert && target == st.Deployed.Commit {
		// a build tags the running version's own images: a rollback would find the new ones
		return GitRequestError(fmt.Sprintf("%s is already deployed: there is nothing new to build", target[:12]))
	}
	if st.Deployed != nil && revert && info.Head != st.Deployed.Commit {
		return GitRequestError(fmt.Sprintf("%s is on %s, not on the deployed %s: a revert would drop the commits made there", st.Dir, info.Head[:12], st.Deployed.Commit[:12]))
	}
	if revert && !gitRevertable(ctx, st, *st.historyEntry(target)) {
		return GitRequestError(fmt.Sprintf("%s cannot be reverted to: it is the running version, it never ran, or its images are gone", target[:12]))
	}

	return nil
}

// runGitDeploy carries the deployment out and records how it ended.
func runGitDeploy(ctx context.Context, st *gitApp, target, tag string, revert bool, trigger gitTrigger) {
	ctx = common.WithProperties(ctx, map[string]string{common.PropertyTypeAppName.Name: st.App})
	previous := st.Deployed
	// the deployed commit again, never a revert: an app repaired, or restored at the version its
	// state names
	redeploy := previous != nil && target == previous.Commit

	auth, err := gitAuth(st)
	if err != nil {
		failGitDeploy(ctx, st, target, tag, "", nil, gitOutcomeFailed, err)
		return
	}

	var images map[string]string
	switch {
	case revert:
		images = st.historyEntry(target).Images
	case redeploy && gitDocker.ImagesExist(ctx, previous.Images):
		// nothing to build: what that version ran is still there
		images = previous.Images
	default:
		// for a redeployment, only once its images are gone: a build tags them anew
		if images, err = fetchAndBuild(ctx, st, target, tag, previous, auth); err != nil {
			return
		}

		st.Operation.Kind = gitOperationDeploy
		if err := saveGitApp(st); err != nil {
			logger.Error("the deployment could not be written down", zap.Error(err), zap.String("app", st.App))
		}
		// once the state says deploying: the dashboard reads the app again when it hears this
		go PublishEventWrapper(ctx, common.EventTypeAppGitBuildEnd, nil)
	}

	subject, _ := git.Subject(ctx, st.Dir, target)

	move := git.FastForward
	if revert || redeploy || st.followsTags() || (previous != nil && previous.Tag != "") {
		// tags do not form a line: an app that follows them is moved, never fast-forwarded,
		// and so is the first deployment on a branch after them
		move = git.ResetKeep
	}
	if err := move(ctx, st.Dir, target); err != nil {
		// nothing moved: the folder and the running version are as they were
		failGitDeploy(ctx, st, target, tag, subject, images, gitOutcomeFailed, err)
		return
	}

	err = gitDocker.Retag(ctx, st.App, st.Dir, target)
	if err == nil {
		err = gitDocker.Start(ctx, st.App, st.Dir)
	}
	if err != nil {
		rollBackGitDeploy(ctx, st, target, tag, subject, images, previous, err)
		return
	}

	st.Deployed = &gitDeployment{Commit: target, Tag: tag, Subject: subject, At: time.Now().UTC(), Images: images}
	st.Blocked = false
	if trigger == gitTriggerManual && !redeploy {
		// a revert pauses the automatic rebuild, and so does an older tag deployed by hand, or
		// the next check would undo them. The first deployment of an app that follows tags, no
		// tag deployed before it, pauses nothing even when its commit ran before: automatic
		// deployment resumes after it. The version that runs, deployed again, leaves the
		// switch as it was
		previousTag := ""
		if previous != nil {
			previousTag = previous.Tag
		}
		st.AutoPaused = (revert && (previousTag != "" || !st.followsTags())) || gitTagHigher(previousTag, tag)
	}
	st.EnvTracked, _ = git.IsTracked(ctx, st.Dir, ".env")
	finishGitDeploy(ctx, st, gitHistoryEntry{Commit: target, Tag: tag, Subject: subject, At: st.Deployed.At, Outcome: gitOutcomeDeployed, Images: images})

	go PublishEventWrapper(ctx, common.EventTypeAppGitDeployEnd, nil)
}

// fetchAndBuild fetches what target comes from and checks it may be deployed, then builds
// it: on a branch, target is on it and descends from what runs; for tags, the tag still
// names target. A failure is recorded here.
func fetchAndBuild(ctx context.Context, st *gitApp, target, tag string, previous *gitDeployment, auth git.Auth) (map[string]string, error) {
	var err error
	switch {
	case !st.followsTags():
		err = fetchGitBranch(ctx, st, target, previous, auth)
	case previous == nil || target != previous.Commit:
		// a repair builds the commit that runs, which the folder holds, wherever its tag went
		err = fetchGitTag(ctx, st, target, tag, auth)
	}
	if err != nil {
		failGitDeploy(ctx, st, target, tag, "", nil, gitOutcomeFailed, err)
		return nil, err
	}

	subject, _ := git.Subject(ctx, st.Dir, target)

	images, err := buildGitCommit(ctx, st, target, auth)
	if err != nil {
		failGitDeploy(ctx, st, target, tag, subject, nil, gitOutcomeBuildFailed, err)
		return nil, err
	}

	return images, nil
}

// fetchGitBranch fetches the branch, and checks target is on it, descends from what runs, and
// contains whatever the folder is on. The first deployment on the branch after tags descends
// from nothing: the tag that ran may come from another line.
func fetchGitBranch(ctx context.Context, st *gitApp, target string, previous *gitDeployment, auth git.Auth) error {
	head, err := git.Fetch(ctx, st.Dir, st.Remote, st.Branch, auth)
	if err != nil {
		return err
	}

	onBranch, err := git.IsAncestor(ctx, st.Dir, target, head)
	if err == nil && !onBranch {
		err = fmt.Errorf("%s is not on the branch %s", target[:12], st.Branch)
	}
	if err == nil && previous != nil && previous.Tag == "" {
		var descends bool
		if descends, err = git.IsAncestor(ctx, st.Dir, previous.Commit, target); err == nil && !descends {
			err = fmt.Errorf("%s does not descend from the deployed %s", target[:12], previous.Commit[:12])
		}
	}
	if err == nil && previous != nil {
		err = gitFolderContainedIn(ctx, st.Dir, previous.Commit, target)
	}

	return err
}

// fetchGitTag fetches the tag alone and checks it still names target, the commit the check or
// the request saw. None of a branch's rules holds: tags do not form a line.
func fetchGitTag(ctx context.Context, st *gitApp, target, tag string, auth git.Auth) error {
	fetched, err := git.FetchTag(ctx, st.Dir, st.Remote, tag, auth)
	if err == nil && fetched != target {
		err = fmt.Errorf("the tag %s moved since the check, from %.12s to %.12s: check again", tag, target, fetched)
	}

	return err
}
```

Replace `rollBackGitDeploy` and `failGitDeploy` whole (from `// rollBackGitDeploy puts the previous version back after target did not start: its` down to the closing brace of `failGitDeploy`, the line before `// finishGitDeploy records how a deployment ended`) with:

```go
// rollBackGitDeploy puts the previous version back after target did not start: its
// commit, its images, and a start. A created app's first version has nothing before it
// and stays stopped with its log.
func rollBackGitDeploy(ctx context.Context, st *gitApp, target, tag, subject string, images map[string]string, previous *gitDeployment, cause error) {
	if previous == nil {
		if err := gitDocker.Stop(ctx, st.App); err != nil {
			logger.Error("a first version that did not start could not be stopped", zap.Error(err), zap.String("app", st.App))
		}
		failGitDeploy(ctx, st, target, tag, subject, images, gitOutcomeFailed, cause)

		return
	}

	err := git.ResetKeep(ctx, st.Dir, previous.Commit)
	if err == nil {
		err = gitDocker.Retag(ctx, st.App, st.Dir, previous.Commit)
	}
	if err == nil {
		err = gitDocker.Start(ctx, st.App, st.Dir)
	}

	if err != nil {
		st.Blocked = true
		failGitDeploy(ctx, st, target, tag, subject, images, gitOutcomeFailed,
			fmt.Errorf("%w; the rollback to %s failed too: %v", cause, previous.Commit[:12], err))

		return
	}

	st.Blocked = false
	failGitDeploy(ctx, st, target, tag, subject, images, gitOutcomeRolledBack, cause)
}

// failGitDeploy records a deployment that did not end with target running, and says so.
func failGitDeploy(ctx context.Context, st *gitApp, target, tag, subject string, images map[string]string, outcome string, cause error) {
	st.attempt(target)
	finishGitDeploy(ctx, st, gitHistoryEntry{
		Commit: target, Tag: tag, Subject: subject, At: time.Now().UTC(), Outcome: outcome, Reason: cause.Error(), Images: images,
	})

	// after the state is written down: the dashboard reads the app again when it hears this
	event := common.EventTypeAppGitDeployError
	if outcome == gitOutcomeBuildFailed {
		event = common.EventTypeAppGitBuildError
	}
	go PublishEventWrapper(ctx, event, map[string]string{common.PropertyTypeMessage.Name: cause.Error()})
}
```

In `deployGitAppAutomatically`, replace:

```go
	if _, err := deployHolding(ctx, name, st.Check.RemoteCommit, nil, gitTriggerAutomatic, end); err != nil {
```

with:

```go
	if _, err := deployHolding(ctx, name, st.Check.RemoteCommit, st.Check.RemoteTag, nil, gitTriggerAutomatic, end); err != nil {
```

In `service/git_app_state.go` (CRLF, keep it), after the closing brace of `historyEntry` (anchor: `return newest\n}` at the end of the file), append:

```go

// gitRan reports whether entry is of a version that ran, deployed or adopted: one a revert can
// go back to.
func gitRan(entry *gitHistoryEntry) bool {
	return entry != nil && (entry.Outcome == gitOutcomeDeployed || entry.Outcome == gitOutcomeAdopted)
}
```

In `service/git_lifecycle.go`, replace:

```go
				Commit: operation.Commit, Subject: subject, At: time.Now().UTC(), Outcome: gitOutcomeInterrupted,
```

with:

```go
				Commit: operation.Commit, Tag: operation.Tag, Subject: subject, At: time.Now().UTC(), Outcome: gitOutcomeInterrupted,
```

In `service/git_restore.go` (CRLF, keep it), replace:

```go
	runGitDeploy(ctx, st, origin.Commit, false, gitTriggerManual)
```

with:

```go
	runGitDeploy(ctx, st, origin.Commit, "", false, gitTriggerManual)
```

- [ ] **Step 4: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && for f in service/git_deploy.go service/git_app_state.go service/git_lifecycle.go service/git_restore.go service/git_tags_deploy_internal_test.go; do tr -d '\r' < "$f" | gofmt -l | sed "s|<standard input>|$f|"; done; git ls-files --eol service/git_app_state.go service/git_restore.go | grep mixed; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/ ./route/...`

Expected: no output.

Run (Linux, after `go generate ./...`): `go test ./service/ -run 'TestAnyEligibleTagIsDeployedByHand|TestADeploymentOfATagRefusesWhatItCannotDo|TestABranchAppIsDeployedByCommitNotByTag|TestATagThatMovedSinceTheCheckIsRefused|TestARepairOfAnAppThatFollowsTagsNeedsNeitherItsHistoryNorItsTag|TestAnInterruptedDeploymentOfATagKeepsItsTag' -count=1 -v`

Expected: `--- PASS` for each, `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`.

Then once, for everything the deployment touches (Linux, after `go generate ./...`): `go test ./service/ -count=1`

Expected: `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`.

- [ ] **Step 5: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add service/git_deploy.go service/git_app_state.go service/git_lifecycle.go service/git_restore.go service/git_tags_deploy_internal_test.go
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): deploy any eligible tag by hand, refusing a tag that moved"
```

---

### Task 6: Upgrades only, and the commits of the history kept

**Files:**
- Modify: `service/git_deploy.go` (`finishGitDeploy`, `deployGitAppAutomatically`: replace both; add `keepGitCommits`, `gitMayDeployAutomatically`; the revert refusal's message in `gitDeployPreconditions`)
- Modify: `service/git_app_view.go:172-175` (the `ponytail:` comment of `newGitAppView`), `:236-247` (`gitRevertable`) — CRLF
- Modify: `service/git_lifecycle.go:77-85` (recovery reconciles the kept refs)
- Test: `service/git_tags_auto_internal_test.go` (create)

**Interfaces:**
- Consumes: Task 1 (`git.KeepCommit`, `git.DropKeptCommit`, `git.KeptCommits`, `git.HasCommit`), Task 2 (`gitTagHigher`, `gitNewCommits`), Task 3 (`GitAppChanges.Follow`, `.TagPattern`, `followGitFolder`), Task 5 (`DeployGitAppTag`, `gitRan`, `deployedTaggedTestApp`, the first branch deployment after tags, the pause rule), Task 4 (`pushTestTag`); test helpers `deployedTestApp`, `deployTestApp`, `checkTestApp`, `waitForGitApp`, `folderHead`, `gitShell`, `pushTestCommit`, `assertBadRequest`.
- Produces:
  - `func gitMayDeployAutomatically(st *gitApp) bool` (the gate of the Global Constraints).
  - `func keepGitCommits(ctx context.Context, st *gitApp)`: reconciles `refs/recasaos/kept/*` of a cloned app with `deployed.commit` and every history entry that ran; called by `finishGitDeploy` and `RecoverGitApps`.
  - `gitRevertable` also requires `git.HasCommit(ctx, st.Dir, entry.Commit)`; the refusal reads `... it never ran, or its images or its commit are gone`.

- [ ] **Step 1: Write the failing test**

Create `service/git_tags_auto_internal_test.go`:

```go
package service

import (
	"context"
	"sort"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"gotest.tools/v3/assert"
)

func TestTheAutomaticDeploymentOfTagsOnlyEverGoesUp(t *testing.T) {
	ready := func(follow, deployedTag, remoteTag string) *gitApp {
		return &gitApp{
			Follow: follow, AutoDeploy: true, Attempted: []string{},
			Deployed: &gitDeployment{Commit: "a", Tag: deployedTag},
			Check:    &gitCheck{RemoteCommit: "b", RemoteTag: remoteTag},
		}
	}
	refused := func(change func(st *gitApp)) bool {
		st := ready(gitFollowTags, "v1.0.0", "v1.1.0")
		change(st)
		return !gitMayDeployAutomatically(st)
	}

	assert.Assert(t, gitMayDeployAutomatically(ready(gitFollowTags, "v1.0.0", "v1.1.0")))
	assert.Assert(t, !gitMayDeployAutomatically(ready(gitFollowTags, "v1.1.0", "v1.0.0")), "a lower tag: the higher one deleted, or the pattern narrowed")
	assert.Assert(t, !gitMayDeployAutomatically(ready(gitFollowTags, "v1.1.0", "v1.1.0")), "the deployed tag, moved")
	assert.Assert(t, !gitMayDeployAutomatically(ready(gitFollowTags, "", "v1.1.0")), "no deployment in this mode yet")
	assert.Assert(t, gitMayDeployAutomatically(ready(gitFollowBranch, "", "")))
	assert.Assert(t, !gitMayDeployAutomatically(ready(gitFollowBranch, "v1.1.0", "")), "no deployment on the branch yet")

	assert.Assert(t, refused(func(st *gitApp) { st.AutoDeploy = false }), "off")
	assert.Assert(t, refused(func(st *gitApp) { st.AutoPaused = true }), "paused")
	assert.Assert(t, refused(func(st *gitApp) { st.Blocked = true }), "blocked")
	assert.Assert(t, refused(func(st *gitApp) { st.Check.Error = "offline" }), "a failed check")
	assert.Assert(t, refused(func(st *gitApp) { st.Attempted = []string{"b"} }), "tried already")
	assert.Assert(t, refused(func(st *gitApp) { st.Check.RemoteCommit = "a" }), "what runs")
	assert.Assert(t, refused(func(st *gitApp) { st.Deployed = nil }), "never deployed")
}

func TestAnAppThatFollowsTagsIsUpgradedAutomaticallyAndNeverDowngraded(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, true)
	ctx := context.Background()

	// a higher tag is deployed
	second := pushTestCommit(t, work, "index.html", "v2")
	pushTestTag(t, work, "v1.1.0")
	st := checkTestApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Tag, "v1.1.0")

	// a pre-release is not
	fake.calls = nil
	pushTestCommit(t, work, "index.html", "v3")
	pushTestTag(t, work, "v1.2.0-rc.1")
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.1.0")

	// nor the lower tag a narrowed pattern leaves
	pattern := "v1.0.*"
	_, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.0.0")
	assert.Equal(t, gitNewCommits(st), false)
	pattern = ""
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{TagPattern: &pattern})
	assert.NilError(t, err)

	// a moved tag is reported, never redeployed by itself
	moved := pushTestCommit(t, work, "index.html", "v4")
	gitShell(t, work, "git tag -f -a -m v1.1.0 v1.1.0 >/dev/null && git push -q -f origin v1.1.0")
	checkTestApp(t, "jarvis")
	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.Check.RemoteCommit, moved)
	assert.Equal(t, view.Check.TagMoved, true)
	assert.Equal(t, view.NewCommits, false)
	assert.DeepEqual(t, fake.Calls(), []string{})

	// by hand, the moved tag builds its new commit
	_, err = DeployGitAppTag(ctx, "jarvis", "v1.1.0", nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + moved[:12], "retag " + moved[:12], "start " + moved[:12]})
	assert.Equal(t, st.AutoPaused, false, "the same version: nothing to pause")

	// a deleted tag brings nothing back
	fake.calls = nil
	gitShell(t, work, "git push -q origin :refs/tags/v1.1.0")
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.0.0")
	assert.Equal(t, st.Check.RemoteCommit, first)
	assert.DeepEqual(t, fake.Calls(), []string{})
	assert.Equal(t, st.Deployed.Commit, moved)
	assert.Equal(t, st.Deployed.Tag, "v1.1.0")
}

// After a change of mode nothing deploys by itself until one deployment by hand in the new
// mode, which may be the running commit under its tag.
func TestAfterAChangeOfModeTheFirstDeploymentIsByHand(t *testing.T) {
	fake, work, first := deployedTestApp(t, true)
	ctx := context.Background()
	pushTestTag(t, work, "v1.0.0")
	tags, branch := gitFollowTags, gitFollowBranch

	_, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	second := pushTestCommit(t, work, "index.html", "v2")
	pushTestTag(t, work, "v1.1.0")
	st := checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteTag, "v1.1.0")
	assert.DeepEqual(t, fake.Calls(), []string{})
	assert.Equal(t, gitNewCommits(st), true, "a version to deploy by hand")

	// the running commit, deployed by hand under its tag, starts again from its images
	_, err = DeployGitAppTag(ctx, "jarvis", "v1.0.0", nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"retag " + first[:12], "start " + first[:12]})
	assert.Equal(t, st.Deployed.Tag, "v1.0.0")

	// and automatic deployment resumes
	fake.calls = nil
	st = checkTestApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Tag, "v1.1.0")

	// back on the branch: nothing deploys by itself until a deployment by hand there
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch})
	assert.NilError(t, err)
	third := pushTestCommit(t, work, "index.html", "v3")
	fake.calls = nil
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteCommit, third)
	assert.DeepEqual(t, fake.Calls(), []string{})

	st = deployTestApp(t)
	assert.Equal(t, st.Deployed.Commit, third)
	assert.Equal(t, st.Deployed.Tag, "")
	fourth := pushTestCommit(t, work, "index.html", "v4")
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Deployed.Commit, fourth, "and automatic deployment resumes")

	// on tags again, the first deployment by hand goes back to a version that ran: a revert,
	// which pauses nothing, and automatic deployment resumes after it
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &tags})
	assert.NilError(t, err)
	fake.calls = nil
	_, err = DeployGitAppTag(ctx, "jarvis", "v1.1.0", nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Tag, "v1.1.0")
	assert.Equal(t, st.AutoPaused, false)
	fifth := pushTestCommit(t, work, "index.html", "v5")
	pushTestTag(t, work, "v1.2.0")
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Deployed.Commit, fifth)
	assert.Equal(t, st.Deployed.Tag, "v1.2.0")
}

// Back on a branch after a tag of another line ran, the first deployment by hand moves the
// folder to the branch's head, which does not descend from that tag, and automatic
// deployment resumes after it. The running version given by hand stays what its tag made it.
func TestBackOnABranchAfterATagOfAnotherLineTheBranchIsFollowedAgain(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, true)
	ctx := context.Background()
	branch := gitFollowBranch

	// a fix of 1.0 on a line of its own, higher: deployed by itself
	hotfix := gitShell(t, work, "git checkout -q -b hotfix "+first+" && echo fix > fix.txt && git add fix.txt && git commit -qm fix && git tag -a -m v1.0.1 v1.0.1 && git push -q origin v1.0.1 && git rev-parse HEAD && git checkout -q main")
	st := checkTestApp(t, "jarvis")
	assert.Equal(t, st.Deployed.Commit, hotfix)
	assert.Equal(t, st.Deployed.Tag, "v1.0.1")

	_, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{Follow: &branch})
	assert.NilError(t, err)
	second := pushTestCommit(t, work, "index.html", "v2")
	fake.calls = nil
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteCommit, second)
	assert.DeepEqual(t, fake.Calls(), []string{}, "nothing deploys by itself after a change of mode")

	// the running version given by hand deploys nothing, and keeps its tag
	_, err = DeployGitApp(ctx, "jarvis", hotfix, nil)
	assertBadRequest(t, err, "it is the running version")
	st, err = loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, st.Deployed.Tag, "v1.0.1")

	// the branch's head, by hand: built and moved to, though it does not descend from the fix
	st = deployTestApp(t)
	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Commit, second)
	assert.Equal(t, st.Deployed.Tag, "")
	assert.Equal(t, st.AutoPaused, false)
	assert.Equal(t, folderHead(t, st.Dir), second)

	// and automatic deployment resumes on the branch
	fake.calls = nil
	third := pushTestCommit(t, work, "index.html", "v3")
	st = checkTestApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + third[:12], "retag " + third[:12], "start " + third[:12]})
	assert.Equal(t, st.Deployed.Commit, third)
}

// A revert goes back to a commit whose tag is gone: the folder keeps a ref for every version
// the history offers, and a commit git no longer holds is offered no more.
func TestTheCommitsOfTheHistoryAreKeptWhateverTheTagsDo(t *testing.T) {
	fake, work, first := deployedTaggedTestApp(t, false)
	ctx := context.Background()
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	kept := func() []string {
		t.Helper()
		commits, err := git.KeptCommits(ctx, st.Dir)
		assert.NilError(t, err)
		sort.Strings(commits)
		return commits
	}
	sorted := func(commits ...string) []string {
		sort.Strings(commits)
		return commits
	}
	// what git collects once the last fetch, the last move and every reflog are forgotten:
	// whatever no ref names
	collect := func() {
		t.Helper()
		gitShell(t, st.Dir, "rm -f .git/FETCH_HEAD .git/ORIG_HEAD && git reflog expire --expire=now --all && git gc -q --prune=now")
	}
	assert.DeepEqual(t, kept(), []string{first})

	// a fix of 1.0 on a line of its own, then 1.1 on the main line
	hotfix := gitShell(t, work, "git checkout -q -b hotfix "+first+" && echo fix > fix.txt && git add fix.txt && git commit -qm fix && git tag -a -m v1.0.1 v1.0.1 && git push -q origin v1.0.1 && git rev-parse HEAD && git checkout -q main")
	_, err = DeployGitAppTag(ctx, "jarvis", "v1.0.1", nil)
	assert.NilError(t, err)
	waitForGitApp(t, "jarvis")
	second := pushTestCommit(t, work, "index.html", "v2")
	pushTestTag(t, work, "v1.1.0")
	deployTestApp(t)
	assert.DeepEqual(t, kept(), sorted(first, hotfix, second))

	// the fix's tag deleted: nothing in the folder names its commit but the kept ref
	gitShell(t, work, "git push -q origin :refs/tags/v1.0.1")
	collect()
	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.History[1].Commit, hotfix)
	assert.Equal(t, view.History[1].Revertable, true)

	fake.calls = nil
	_, err = DeployGitApp(ctx, "jarvis", hotfix, nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"retag " + hotfix[:12], "start " + hotfix[:12]})
	assert.Equal(t, folderHead(t, st.Dir), hotfix)
	// first left the history with this deployment, and its ref with it
	assert.DeepEqual(t, kept(), sorted(hotfix, second))

	// a commit git no longer holds is no version to revert to
	assert.NilError(t, git.DropKeptCommit(ctx, st.Dir, second))
	collect()
	view, err = GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.History[1].Commit, second)
	assert.Equal(t, view.History[1].Revertable, false)
	_, err = DeployGitApp(ctx, "jarvis", second, nil)
	assertBadRequest(t, err, "cannot be reverted to")
}
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/`

Expected: FAIL, `undefined: gitMayDeployAutomatically` in `git_tags_auto_internal_test.go`. (On Linux the other four tests fail too: the gate deploys a lower tag and a moved one, a branch app auto-deploys after a switch, and no commit is kept.)

- [ ] **Step 3: Implement**

In `service/git_deploy.go`, replace `finishGitDeploy` and `deployGitAppAutomatically` (from `// finishGitDeploy records how a deployment ended, clears the operation, and removes the` down to the closing brace of `deployGitAppAutomatically`, the line before `// gitBuildLog is a build's output`) with:

```go
// finishGitDeploy records how a deployment ended, clears the operation, removes the images
// no remembered version names any more, and keeps the commits the history offers.
func finishGitDeploy(ctx context.Context, st *gitApp, entry gitHistoryEntry) {
	st.Operation = nil
	if removable := st.record(entry); len(removable) > 0 {
		gitDocker.RemoveImages(ctx, removable)
	}
	keepGitCommits(ctx, st)

	if err := saveGitApp(st); err != nil {
		logger.Error("the end of a deployment could not be written down", zap.Error(err), zap.String("app", st.App))
	}
}

// keepGitCommits keeps a ref in the folder for the running version and every version of the
// history that ran, and drops the others: git never collects a commit a revert may go back
// to, whatever became of the tag or the branch that brought it.
func keepGitCommits(ctx context.Context, st *gitApp) {
	if !st.Cloned {
		return
	}

	wanted := map[string]bool{}
	if st.Deployed != nil {
		wanted[st.Deployed.Commit] = true
	}
	for i := range st.History {
		if gitRan(&st.History[i]) {
			wanted[st.History[i].Commit] = true
		}
	}

	kept, err := git.KeptCommits(ctx, st.Dir)
	if err != nil {
		logger.Error("the commits a git app keeps could not be listed", zap.Error(err), zap.String("app", st.App))
		return
	}
	for _, commit := range kept {
		if wanted[commit] {
			delete(wanted, commit)
		} else if err := git.DropKeptCommit(ctx, st.Dir, commit); err != nil {
			logger.Error("a commit a git app kept could not be dropped", zap.Error(err), zap.String("app", st.App))
		}
	}
	for commit := range wanted {
		if err := git.KeepCommit(ctx, st.Dir, commit); err != nil {
			logger.Error("a commit of a git app could not be kept", zap.Error(err), zap.String("app", st.App))
		}
	}
}

// deployGitAppAutomatically starts the deployment of what the check holding the app saw,
// when the app's automatic rebuild may act on it. The check's hold passes to the
// deployment under the deployment's name, so nothing starts in between and what is refused
// meanwhile is told a deployment runs; end is released either way.
func deployGitAppAutomatically(ctx context.Context, name string, end func()) {
	st, err := loadGitApp(name)
	if err != nil || !gitMayDeployAutomatically(st) {
		end()
		return
	}

	handOver(name, gitOperationDeploy)
	if _, err := deployHolding(ctx, name, st.Check.RemoteCommit, st.Check.RemoteTag, nil, gitTriggerAutomatic, end); err != nil {
		logger.Info("no automatic deployment", zap.String("app", name), zap.Error(err))
	}
}

// gitMayDeployAutomatically reports whether the automatic rebuild may deploy what the last
// check saw: a commit other than the running one and never tried, on an app neither paused
// nor blocked. An app that follows tags only ever goes up, from a tag deployed in that mode:
// a deleted, a lower or a moved tag deploys nothing. A branch app acts once a version of its
// branch runs, never over one a tag deployed.
func gitMayDeployAutomatically(st *gitApp) bool {
	if !st.AutoDeploy || st.AutoPaused || st.Blocked || st.Deployed == nil || st.Check == nil || st.Check.Error != "" ||
		st.Check.RemoteCommit == "" || st.Check.RemoteCommit == st.Deployed.Commit || lo.Contains(st.Attempted, st.Check.RemoteCommit) {
		return false
	}
	if st.followsTags() {
		return st.Deployed.Tag != "" && gitTagHigher(st.Check.RemoteTag, st.Deployed.Tag)
	}

	return st.Deployed.Tag == ""
}
```

In `gitDeployPreconditions`, replace:

```go
		return GitRequestError(fmt.Sprintf("%s cannot be reverted to: it is the running version, it never ran, or its images are gone", target[:12]))
```

with:

```go
		return GitRequestError(fmt.Sprintf("%s cannot be reverted to: it is the running version, it never ran, or its images or its commit are gone", target[:12]))
```

In `service/git_app_view.go` (CRLF, keep it), replace:

```go
// ponytail: a view of a cloned app runs git four times; cache it per HEAD if polling it
// ever shows up on a small box.
```

with:

```go
// ponytail: a view of a cloned app runs git four times, and once more per version of the
// history that may be reverted to; cache it per HEAD if polling it ever shows up on a small
// box.
```

and replace `gitRevertable`:

```go
// gitRevertable reports whether the history can go back to entry: a version that ran,
// that is not the one running, and whose images are still there.
func gitRevertable(ctx context.Context, st *gitApp, entry gitHistoryEntry) bool {
	if entry.Outcome != gitOutcomeDeployed && entry.Outcome != gitOutcomeAdopted {
		return false
	}
	if st.Deployed != nil && entry.Commit == st.Deployed.Commit {
		return false
	}

	return gitDocker.ImagesExist(ctx, entry.Images)
}
```

with:

```go
// gitRevertable reports whether the history can go back to entry: a version that ran, that
// is not the one running, whose images are still there, and whose commit git still holds.
func gitRevertable(ctx context.Context, st *gitApp, entry gitHistoryEntry) bool {
	if entry.Outcome != gitOutcomeDeployed && entry.Outcome != gitOutcomeAdopted {
		return false
	}
	if st.Deployed != nil && entry.Commit == st.Deployed.Commit {
		return false
	}

	return gitDocker.ImagesExist(ctx, entry.Images) && git.HasCommit(ctx, st.Dir, entry.Commit)
}
```

In `service/git_lifecycle.go`, replace:

```go
			if len(removable) > 0 {
				gitDocker.RemoveImages(ctx, removable)
			}
		}
```

with:

```go
			if len(removable) > 0 {
				gitDocker.RemoveImages(ctx, removable)
			}
			keepGitCommits(ctx, st)
		}
```

- [ ] **Step 4: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && for f in service/git_deploy.go service/git_app_view.go service/git_lifecycle.go service/git_tags_auto_internal_test.go; do tr -d '\r' < "$f" | gofmt -l | sed "s|<standard input>|$f|"; done; git ls-files --eol service/git_app_view.go | grep mixed; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/ ./route/...`

Expected: no output.

Run (Linux, after `go generate ./...`): `go test ./service/ -run 'TestTheAutomaticDeploymentOfTagsOnlyEverGoesUp|TestAnAppThatFollowsTagsIsUpgradedAutomaticallyAndNeverDowngraded|TestAfterAChangeOfModeTheFirstDeploymentIsByHand|TestBackOnABranchAfterATagOfAnotherLineTheBranchIsFollowedAgain|TestTheCommitsOfTheHistoryAreKeptWhateverTheTagsDo' -count=1 -v`

Expected: `--- PASS` for each, `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`.

Then once (Linux, after `go generate ./...`): `go test ./service/ -count=1`

Expected: `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`.

- [ ] **Step 5: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add service/git_deploy.go service/git_app_view.go service/git_lifecycle.go service/git_tags_auto_internal_test.go
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): upgrade tags automatically only, and keep the commits of the history"
```

---

### Task 7: Backups and restore of an app that follows tags

**Files:**
- Modify: `service/backup_plan.go:57-62` (`BackupGit`)
- Modify: `service/git_restore.go:19-114` (`gitBackupOrigin`, `installGitAppFromBackup`; new `gitRestoreRef` after it) — CRLF
- Modify: `service/git_deploy.go` (`gitTrigger` constants; `runGitDeploy`: the pause condition and the `fetchAndBuild` call; `fetchAndBuild`: signature and the tag fetch)
- Test: `service/git_tags_restore_internal_test.go` (create)

**Interfaces:**
- Consumes: Task 1 (`git.LsRemoteTags`, `git.FetchCommit`, `git.HasCommit`, `git.ResetKeep`, `git.Detach`), `git.DefaultBranch`, `validGitBranch`, Task 2 (`checkGitFollow`, `checkGitTagPattern`, `eligibleGitTags`, `gitFollowTags`, `gitFollowBranch`, `st.followsTags()`), Task 4 (`cloneGitApp(ctx, st, ref, auth)`, `pushTestTag`), Task 5 (`runGitDeploy(ctx, st, target, tag, revert, trigger)`, `fetchAndBuild`, `fetchGitTag`, `fetchGitBranch`), `gitAccessRequired`, `setGitWebhook`, `RunBackup`; test fixtures `logInTempDir`, `appWithOneBindAndOneVolume`, `BackupOptions`, `restoreStamp`, `fakeBackupDocker`, `demoMountpoints`, `fakeCopier`.
- Produces:
  - `BackupGit.Follow` (`follow,omitempty`), `.Tag` (`tag,omitempty`), `.TagPattern` (`tag_pattern,omitempty`), `.Prereleases` (`prereleases,omitempty`); written only for a tag-mode app.
  - `const gitTriggerRestore gitTrigger` (after `gitTriggerAutomatic`): a manual deployment that builds without fetching or checking the tag.
  - `fetchAndBuild(ctx, st, target, tag string, previous *gitDeployment, trigger gitTrigger, auth git.Auth)`.
  - `func gitRestoreRef(ctx context.Context, st *gitApp, tag string, auth git.Auth) (string, error)`.
  - Restore rules: a tag-mode manifest's `tag` is checked only when non-empty (an empty one restores with `deployed.tag` empty); `gitRestoreRef` answers the backup's tag, else the highest eligible tag, else the remote's default branch (`validGitBranch`); a fresh clone of a tag-mode app is detached (`git.Detach`) before the commit is looked for.
  - Restore messages: `the backup's follow `x` is neither branch nor tags`, `the backup's tag `x` is not a tag name`, `the backup's tag pattern `x` is not one`, `the remote has no tag to clone the repository at, and its default branch cannot be read: <git>`, `the tag <t> no longer points at the backed-up commit, and the forge does not give that commit: <git>`, and with no tag in the manifest `the backup names no tag for its commit, and the forge does not give that commit: <git>`.

- [ ] **Step 1: Write the failing test**

Create `service/git_tags_restore_internal_test.go`:

```go
package service

import (
	"context"
	stdjson "encoding/json"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

// A branch app's origin is what older versions wrote, byte for byte; an app that follows tags
// adds its mode, its tag and its filter.
func TestABackupOfAnAppThatFollowsTagsRecordsItsTag(t *testing.T) {
	logInTempDir(t)
	gitAppsIn(t)
	app, _ := appWithOneBindAndOneVolume(t)
	options := BackupOptions{Destination: "offsite", Stamp: restoreStamp}
	const commit = "4f5f60c16eba0123456789abcdef0123456789ab"
	backup := func() string {
		t.Helper()
		manifest, err := RunBackup(context.Background(), app, &fakeBackupDocker{mountpoints: demoMountpoints()}, &fakeCopier{}, options)
		assert.NilError(t, err)
		raw, err := stdjson.Marshal(manifest.Git)
		assert.NilError(t, err)
		return string(raw)
	}

	// on a branch, its tag filter kept from a time on tags
	assert.NilError(t, saveGitApp(&gitApp{
		App: "demo", Remote: "git@github.com:owner/demo.git", Branch: "main", TagPattern: "v1.*", Prereleases: true,
		Deployed: &gitDeployment{Commit: commit},
	}))
	assert.Equal(t, backup(), `{"remote":"git@github.com:owner/demo.git","branch":"main","commit":"`+commit+`"}`)

	assert.NilError(t, saveGitApp(&gitApp{
		App: "demo", Remote: "git@github.com:owner/demo.git", Follow: gitFollowTags, TagPattern: "v1.*", Prereleases: true,
		Deployed: &gitDeployment{Commit: commit, Tag: "v1.4.2"},
	}))
	assert.Equal(t, backup(), `{"remote":"git@github.com:owner/demo.git","branch":"","commit":"`+commit+`","follow":"tags","tag":"v1.4.2","tag_pattern":"v1.*","prereleases":true}`)
}

func TestAnAppThatFollowsTagsIsRestoredAtItsTag(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	first := pushTestTag(t, work, "v1.0.0")
	pushTestCommit(t, work, "index.html", "v2")
	pushTestTag(t, work, "v1.1.0")

	_, err := installGitAppFromBackup(ctx, "jarvis", BackupGit{Remote: url, Commit: first, Follow: gitFollowTags, Tag: "v1.0.0", TagPattern: "v1.*"}, nil)
	assert.NilError(t, err)

	assert.DeepEqual(t, fake.Calls(), []string{"build " + first[:12], "retag " + first[:12], "start " + first[:12]})
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, st.Follow, gitFollowTags)
	assert.Equal(t, st.Branch, "")
	assert.Equal(t, st.TagPattern, "v1.*")
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, st.Deployed.Tag, "v1.0.0")
	info, err := git.Describe(ctx, st.Dir)
	assert.NilError(t, err)
	assert.Equal(t, info.Branch, "", "in detached HEAD, as an app that follows tags is")
	assert.Equal(t, info.Head, first)
}

// The backed-up commit comes back whatever its tag did: fetched by its hash when the tag moved
// or went, and never another in its place.
func TestARestoreFetchesTheBackedUpCommitWhenItsTagMovedOrWent(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	base := gitShell(t, work, "git rev-parse HEAD")
	backedUp := pushTestCommit(t, work, "index.html", "v1")
	pushTestTag(t, work, "v1.0.0")
	// v1.0.0 moved to a commit of another line, which does not contain the backed-up one; that
	// one is still on main
	// (`git tag -f` prints `Updated tag ...` on stdout: silenced, so that the output is the hash)
	moved := gitShell(t, work, "git checkout -q -b other "+base+" && echo other > other.txt && git add other.txt && git commit -qm other && git tag -f -a -m v1.0.0 v1.0.0 >/dev/null && git push -q -f origin v1.0.0 && git rev-parse HEAD && git checkout -q main")

	_, err := installGitAppFromBackup(ctx, "jarvis", BackupGit{Remote: url, Commit: backedUp, Follow: gitFollowTags, Tag: "v1.0.0"}, nil)
	assert.NilError(t, err)
	assert.DeepEqual(t, fake.Calls(), []string{"build " + backedUp[:12], "retag " + backedUp[:12], "start " + backedUp[:12]})
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, st.Deployed.Commit, backedUp)
	assert.Equal(t, st.Deployed.Tag, "v1.0.0")
	// the next check finds the tag elsewhere, and says so
	checkTestApp(t, "jarvis")
	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.Check.RemoteCommit, moved)
	assert.Equal(t, view.Check.TagMoved, true)

	// a tag that went: cloned at the highest eligible one, the commit fetched all the same
	fake.calls = nil
	_, err = installGitAppFromBackup(ctx, "gone", BackupGit{Remote: url, Commit: backedUp, Follow: gitFollowTags, Tag: "v0.9.0"}, nil)
	assert.NilError(t, err)
	assert.DeepEqual(t, fake.Calls(), []string{"build " + backedUp[:12], "retag " + backedUp[:12], "start " + backedUp[:12]})

	// a commit the forge does not give: refused, and nothing deployed instead
	unpushed := gitShell(t, work, "echo local > local.txt && git add local.txt && git commit -qm local && git rev-parse HEAD")
	fake.calls = nil
	_, err = installGitAppFromBackup(ctx, "lost", BackupGit{Remote: url, Commit: unpushed, Follow: gitFollowTags, Tag: "v1.0.0"}, nil)
	assert.ErrorContains(t, err, "the tag v1.0.0 no longer points at the backed-up commit, and the forge does not give that commit")
	assert.DeepEqual(t, fake.Calls(), []string{})
	st, err = loadGitApp("lost")
	assert.NilError(t, err)
	assert.Assert(t, st.Deployed == nil)
}

// A backup taken before the first deployment in tag mode names no tag, and the remote may have
// no eligible tag left: the app comes back in tag mode all the same, cloned at the highest
// eligible tag or else at the default branch, detached at the backed-up commit.
func TestAnAppThatFollowsTagsIsRestoredWithoutItsTag(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	pushTestTag(t, work, "v1.0.0")
	// on main, under no tag: a clone at v1.0.0 does not hold it
	backedUp := pushTestCommit(t, work, "index.html", "v2")

	for _, c := range []struct {
		name   string
		origin BackupGit
	}{
		// cloned at v1.0.0, the commit fetched by its hash
		{"untagged", BackupGit{Remote: url, Commit: backedUp, Follow: gitFollowTags}},
		// its tag gone and no tag eligible: cloned at the default branch
		{"unmatched", BackupGit{Remote: url, Commit: backedUp, Follow: gitFollowTags, Tag: "v0.9.0", TagPattern: "v9.*"}},
	} {
		fake.calls = nil
		_, err := installGitAppFromBackup(ctx, c.name, c.origin, nil)
		assert.NilError(t, err, c.name)
		assert.DeepEqual(t, fake.Calls(), []string{"build " + backedUp[:12], "retag " + backedUp[:12], "start " + backedUp[:12]})

		st, err := loadGitApp(c.name)
		assert.NilError(t, err)
		assert.Equal(t, st.Follow, gitFollowTags, c.name)
		assert.Equal(t, st.Branch, "", c.name)
		assert.Equal(t, st.Deployed.Commit, backedUp, c.name)
		assert.Equal(t, st.Deployed.Tag, c.origin.Tag, c.name)
		info, err := git.Describe(ctx, st.Dir)
		assert.NilError(t, err)
		assert.Equal(t, info.Branch, "", "%s: in detached HEAD, as an app that follows tags is", c.name)
		assert.Equal(t, info.Head, backedUp, c.name)
	}
}

// A manifest is a file anyone with the destination's credentials can edit: its tag fields are
// checked before git is given them, and nothing is registered.
func TestARestoreRefusesATagOriginGitCannotBeGiven(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	commit := strings.Repeat("a", 40)
	remote := "https://127.0.0.1:1/owner/demo.git"

	for _, c := range []struct {
		origin  BackupGit
		message string
	}{
		{BackupGit{Remote: remote, Commit: commit, Follow: "tag"}, "neither branch nor tags"},
		{BackupGit{Remote: remote, Commit: commit, Follow: gitFollowTags, Tag: "-v1.0.0"}, "is not a tag name"},
		{BackupGit{Remote: remote, Commit: commit, Follow: gitFollowTags, Tag: "v1.0.0", TagPattern: "["}, "tag pattern"},
	} {
		_, err := installGitAppFromBackup(ctx, "jarvis", c.origin, nil)
		assert.ErrorContains(t, err, c.message)
	}
	_, err := loadGitApp("jarvis")
	assert.ErrorIs(t, err, ErrGitAppNotFound, "nothing is registered")
}
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/`

Expected: FAIL, compile errors in `git_tags_restore_internal_test.go`: `unknown field Follow in struct literal of type BackupGit`, `unknown field Tag in struct literal of type BackupGit`, `unknown field TagPattern in struct literal of type BackupGit`.

- [ ] **Step 3: Implement**

In `service/backup_plan.go`, replace `BackupGit`:

```go
// BackupGit is a git app's origin as a backup records it. Never a key or a token.
type BackupGit struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}
```

with:

```go
// BackupGit is a git app's origin as a backup records it. Never a key or a token.
type BackupGit struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
	// An app that follows tags: the mode, the tag the commit was deployed as, and the tag
	// filter. Omitted for a branch app, whose origin stays what older versions wrote.
	Follow      string `json:"follow,omitempty"`
	Tag         string `json:"tag,omitempty"`
	TagPattern  string `json:"tag_pattern,omitempty"`
	Prereleases bool   `json:"prereleases,omitempty"`
}
```

In `service/git_deploy.go`, replace:

```go
const (
	gitTriggerManual gitTrigger = iota
	gitTriggerAutomatic
)
```

with:

```go
const (
	gitTriggerManual gitTrigger = iota
	gitTriggerAutomatic
	// gitTriggerRestore is a restore's deployment of the backed-up commit: by hand, and built
	// wherever the commit's tag went since.
	gitTriggerRestore
)
```

In `runGitDeploy`, replace:

```go
		if images, err = fetchAndBuild(ctx, st, target, tag, previous, auth); err != nil {
```

with:

```go
		if images, err = fetchAndBuild(ctx, st, target, tag, previous, trigger, auth); err != nil {
```

and replace:

```go
	if trigger == gitTriggerManual && !redeploy {
```

with:

```go
	if trigger != gitTriggerAutomatic && !redeploy {
```

Replace the head of `fetchAndBuild`:

```go
func fetchAndBuild(ctx context.Context, st *gitApp, target, tag string, previous *gitDeployment, auth git.Auth) (map[string]string, error) {
	var err error
	switch {
	case !st.followsTags():
		err = fetchGitBranch(ctx, st, target, previous, auth)
	case previous == nil || target != previous.Commit:
		// a repair builds the commit that runs, which the folder holds, wherever its tag went
		err = fetchGitTag(ctx, st, target, tag, auth)
	}
```

with:

```go
func fetchAndBuild(ctx context.Context, st *gitApp, target, tag string, previous *gitDeployment, trigger gitTrigger, auth git.Auth) (map[string]string, error) {
	var err error
	switch {
	case !st.followsTags():
		err = fetchGitBranch(ctx, st, target, previous, auth)
	case trigger != gitTriggerRestore && (previous == nil || target != previous.Commit):
		// a repair builds the commit that runs, and a restore the backed-up commit, which the
		// folder holds, wherever their tag went
		err = fetchGitTag(ctx, st, target, tag, auth)
	}
```

In `service/git_restore.go` (CRLF, keep it), replace everything from `// gitBackupOrigin is what a backup records of a git app, nil for any other app and for one` down to the closing brace of `installGitAppFromBackup` (the line before `// gitAccessRequired prepares the access a restore could not reach the repository without,`) with:

```go
// gitBackupOrigin is what a backup records of a git app, nil for any other app and for one
// never deployed. The tag fields are there for an app that follows tags only: a branch app's
// origin is what older versions wrote.
func gitBackupOrigin(app string) *BackupGit {
	st, err := loadGitApp(app)
	if err != nil || st.Deployed == nil {
		return nil
	}

	origin := &BackupGit{Remote: st.Remote, Branch: st.Branch, Commit: st.Deployed.Commit}
	if st.followsTags() {
		origin.Follow, origin.Tag, origin.TagPattern, origin.Prereleases = gitFollowTags, st.Deployed.Tag, st.TagPattern, st.Prereleases
	}

	return origin
}

// restoreGitApp is installGitAppFromBackup; a var so a restore's test replaces it.
var restoreGitApp = installGitAppFromBackup

// installGitAppFromBackup puts a git app back on a machine that does not have it: it is
// registered with the backup's remote and branch, or its mode and tag filter, cloned at the
// backed-up commit, given the backup's .env, then built and started, while the restore holds
// it. An app that follows tags is cloned at the backup's tag (see gitRestoreRef), detached,
// and the commit fetched by its hash when the clone does not hold it. A repository that
// cannot be reached leaves the app registered with the access it needs.
func installGitAppFromBackup(ctx context.Context, name string, origin BackupGit, env []byte) (*ComposeApp, error) {
	// a manifest is a file anyone with the destination's credentials can edit. A tag-mode
	// manifest may name no tag: taken before the first deployment in that mode
	byTags := origin.Follow == gitFollowTags
	switch {
	case !gitCommitPattern.MatchString(origin.Commit):
		return nil, fmt.Errorf("the backup's commit `%s` is not a full commit hash", origin.Commit)
	case gitURLKind(origin.Remote) == "":
		return nil, fmt.Errorf("the backup's remote `%s` is not a URL CasaOS can clone", redactGitURL(origin.Remote))
	case !validGitBranch(origin.Branch):
		return nil, fmt.Errorf("the backup's branch `%s` is not a branch name", origin.Branch)
	case origin.Follow != "" && checkGitFollow(origin.Follow) != nil:
		return nil, fmt.Errorf("the backup's follow `%s` is neither branch nor tags", origin.Follow)
	case byTags && !validGitBranch(origin.Tag):
		return nil, fmt.Errorf("the backup's tag `%s` is not a tag name", origin.Tag)
	case checkGitTagPattern(origin.TagPattern) != nil:
		return nil, fmt.Errorf("the backup's tag pattern `%s` is not one", origin.TagPattern)
	}

	// a backup holds no webhook secret: whatever this machine kept, the app comes back with
	// its webhook off, turned off before anything is registered so that no delivery signed
	// with an older secret is taken meanwhile
	off := false
	if err := setGitWebhook(name, &off, false); err != nil {
		return nil, err
	}

	st, err := loadGitApp(name)
	if errors.Is(err, ErrGitAppNotFound) {
		st = &gitApp{
			App: name, Origin: gitOriginCreated, Dir: filepath.Join(gitAppsDataRoot, name),
			Remote: origin.Remote, Branch: origin.Branch, Access: git.AccessNone,
			Follow: gitFollowBranch, TagPattern: origin.TagPattern, Prereleases: origin.Prereleases,
			History: []gitHistoryEntry{}, Attempted: []string{},
		}
		if byTags {
			st.Follow, st.Branch = gitFollowTags, ""
		}
		err = saveGitApp(st)
	}
	if err != nil {
		return nil, err
	}

	auth, err := gitAuth(st)
	if err != nil {
		return nil, err
	}
	ref, tag := st.Branch, ""
	if st.followsTags() {
		if ref, err = gitRestoreRef(ctx, st, origin.Tag, auth); err != nil {
			return nil, err
		}
		tag = origin.Tag
	} else if _, err := git.LsRemote(ctx, st.Remote, st.Branch, auth); err != nil {
		return nil, gitAccessRequired(st, err)
	}

	if !st.Cloned {
		if err := cloneGitApp(ctx, st, ref, auth); err != nil {
			return nil, err
		}
		// cloned at the default branch when no tag was left: an app that follows tags deploys
		// from a detached HEAD
		if st.followsTags() {
			if err := git.Detach(ctx, st.Dir); err != nil {
				return nil, err
			}
		}
		if err := saveGitApp(st); err != nil {
			return nil, err
		}
	}

	if !git.HasCommit(ctx, st.Dir, origin.Commit) {
		if !st.followsTags() {
			return nil, fmt.Errorf("the backed-up commit %s is not on %s any more", origin.Commit[:12], st.Branch)
		}
		// the tag moved or went: the commit itself, from a forge that gives it, and never
		// another in its place
		err := git.FetchCommit(ctx, st.Dir, st.Remote, origin.Commit, auth)
		if err == nil && !git.HasCommit(ctx, st.Dir, origin.Commit) {
			err = errors.New("the fetch did not bring it")
		}
		if err != nil && origin.Tag == "" {
			return nil, fmt.Errorf("the backup names no tag for its commit, and the forge does not give that commit: %w", err)
		}
		if err != nil {
			return nil, fmt.Errorf("the tag %s no longer points at the backed-up commit, and the forge does not give that commit: %w", origin.Tag, err)
		}
	}
	if err := git.ResetKeep(ctx, st.Dir, origin.Commit); err != nil {
		return nil, err
	}
	// a repository that tracks .env restores its own, and CasaOS never writes a tracked file
	if tracked, _ := git.IsTracked(ctx, st.Dir, ".env"); len(env) > 0 && !tracked {
		if err := (&ComposeApp{WorkingDir: st.Dir}).WriteEnvFile(env); err != nil {
			return nil, err
		}
	}

	st.Operation = &gitOperation{Kind: gitOperationBuild, Commit: origin.Commit, Tag: tag, StartedAt: time.Now().UTC()}
	if err := saveGitApp(st); err != nil {
		return nil, err
	}
	runGitDeploy(ctx, st, origin.Commit, tag, false, gitTriggerRestore)
	if st.Deployed == nil || st.Deployed.Commit != origin.Commit {
		return nil, fmt.Errorf("`%s` could not be deployed at %s: %s", name, origin.Commit[:12], st.History[0].Reason)
	}

	files, err := gitComposeFiles(st.Dir)
	if err != nil {
		return nil, err
	}

	return LoadComposeAppFromConfigFile(name, strings.Join(files, ","))
}

// gitRestoreRef is what a restore clones an app that follows tags at: the backup's tag while
// the remote has it (an empty tag never is: no listed tag has an empty name), else the
// highest eligible tag, else the remote's default branch. The backed-up commit is fetched
// afterwards when that clone does not hold it. A remote that cannot be reached asks for
// access.
func gitRestoreRef(ctx context.Context, st *gitApp, tag string, auth git.Auth) (string, error) {
	tags, err := git.LsRemoteTags(ctx, st.Remote, auth)
	if err != nil {
		return "", gitAccessRequired(st, err)
	}
	if tags[tag] != "" {
		return tag, nil
	}
	if eligible := eligibleGitTags(tags, st.TagPattern, st.Prereleases); len(eligible) > 0 {
		return eligible[0].Name, nil
	}

	branch, err := git.DefaultBranch(ctx, st.Remote, auth)
	if err == nil && (branch == "" || !validGitBranch(branch)) {
		err = fmt.Errorf("`%s` is not a branch name", branch)
	}
	if err != nil {
		return "", fmt.Errorf("the remote has no tag to clone the repository at, and its default branch cannot be read: %w", err)
	}

	return branch, nil
}
```

- [ ] **Step 4: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && for f in service/backup_plan.go service/git_restore.go service/git_deploy.go service/git_tags_restore_internal_test.go; do tr -d '\r' < "$f" | gofmt -l | sed "s|<standard input>|$f|"; done; git ls-files --eol service/git_restore.go | grep mixed; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/ ./route/...`

Expected: no output.

Run (Linux, after `go generate ./...`): `go test ./service/ -run 'TestABackupOfAnAppThatFollowsTagsRecordsItsTag|TestAnAppThatFollowsTagsIsRestoredAtItsTag|TestARestoreFetchesTheBackedUpCommitWhenItsTagMovedOrWent|TestAnAppThatFollowsTagsIsRestoredWithoutItsTag|TestARestoreRefusesATagOriginGitCannotBeGiven|TestABackupOfAGitAppRecordsWhereItsCodeComesFrom|TestARestoreOfAGitAppNotInstalledComesBackFromItsRepository|TestAGitAppIsInstalledFromItsBackupAtTheBackedUpCommit|TestARestoreOfTheDeployedCommitStartsItFromItsImages|TestARestoreThatCannotReachTheRepositoryAsksForAccess|TestARestoreRefusesAnOriginGitCannotBeGiven|TestARestoreNeverWritesATrackedEnv|TestARestoredGitAppComesBackWithItsWebhookOff' -count=1 -v`

Expected: `--- PASS` for each test named, `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`. (The filter names whole tests on purpose: a looser `Backup` also matches `TestABackupThatHoldsTheAppStillWaitsForItsTurn`, which panics when run first.)

Then once (Linux, after `go generate ./...`): `go test ./service/ -count=1`

Expected: `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`.

- [ ] **Step 5: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add service/backup_plan.go service/git_restore.go service/git_deploy.go service/git_tags_restore_internal_test.go
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): back up and restore an app that follows tags"
```

---

### Task 8: The API: mode fields, the tag list, deploy by tag

**Files:**
- Modify: `api/app_management/openapi.yaml` (CRLF): `:1266-1273` (POST `/git` description), `:1309-1319` (PUT summary and description), `:1364-1371` (check description), `:1392-1399` (deploy description), before `:1419` (new path `/git/{app}/tags`), before `:1920` (response `GitAppTagsOK`), before `:3286` (schema `GitFollow`), `:3294-3296` (`GitApp.required`), `:3330-3333` (`GitApp.branch` and the new properties), `:3362-3364` (`new_commits`), `:3459-3533` (`GitAppDeployed`, `GitAppCheck`, `GitAppOperation`, `GitAppHistoryEntry`), `:3608-3616` (`GitAppCreateRequest`), `:3632-3634` (`GitAppUpdateRequest`), `:3636-3644` (`GitAppDeployRequest`; `GitTag` after it)
- Regenerate: `codegen/app_management_api.go` (gitignored)
- Modify: `route/v2/git.go:17-35` (`gitAppTagsOK`, `CreateGitApp`, `gitAppRegistration`), `:55-61` (`gitAppChanges`), `:77-88` (`DeployGitApp`; then `GetGitAppTags`, `gitAppTagsAnswer`)
- Test: `route/v2/git_internal_test.go` (appended tests); `route/v2_git_test.go:32-41` (rows of `TestTheGitRoutesAreServed`)

**Interfaces:**
- Consumes: `service.GitAppRegistration{Follow, TagPattern, Prereleases}`, `service.GitAppChanges{Follow, TagPattern, Prereleases}` (Task 3), `service.GitAppTags`, `service.GitTag` (Task 4), `service.DeployGitAppTag` (Task 5), `service.GitCheckView`, `service.GitAppView` tag fields (Task 2), `gitAppAnswer`, `gitAppError`.
- Produces:
  - Schemas: `GitFollow` (string, no enum), `GitTag` (`name`, `commit` required), response `GitAppTagsOK` (`BaseResponse` + `data: GitTag[]`); `GitApp.follow` / `.tag_pattern` / `.prereleases` required; `GitAppCheck.remote_tag` / `.tag_moved` required; `GitAppDeployed.tag`, `GitAppOperation.tag`, `GitAppHistoryEntry.tag` required; `GitAppCreateRequest` and `GitAppUpdateRequest` gain `follow` (`$ref GitFollow`), `tag_pattern` (string), `prereleases` (boolean); `GitAppDeployRequest.tag` (string).
  - Path `GET /git/{app}/tags`, operationId `getGitAppTags`, responses 200 `GitAppTagsOK`, 400, 404, 500.
  - Generated: `codegen.GitFollow = string`, `codegen.GitTag{Commit, Name string}`, `codegen.GitAppTagsOK{Data *[]GitTag; Message *string}`, `codegen.GitApp.Follow/TagPattern/Prereleases`, `codegen.GitAppCheck.RemoteTag/TagMoved`, `codegen.GitAppCreateRequest.Follow *GitFollow`, `.TagPattern *string`, `.Prereleases *bool` (same on `GitAppUpdateRequest`), `codegen.GitAppDeployRequest.Tag *string`, `ServerInterface.GetGitAppTags(ctx echo.Context, app GitAppName) error`.
  - Package `v2`: `type gitAppTagsOK struct{ Data []service.GitTag; Message *string }`, `func gitAppRegistration(body codegen.GitAppCreateRequest) service.GitAppRegistration`, `func (a *AppManagement) GetGitAppTags(ctx echo.Context, app codegen.GitAppName) error`, `func gitAppTagsAnswer(ctx echo.Context, tags []service.GitTag, err error) error`; `DeployGitApp` answers 400 `give a tag or a commit, not both`.

- [ ] **Step 1: Write the failing tests**

In `route/v2/git_internal_test.go`, append at the end of the file, after a blank line:

```go
// The service builds the view itself: the tag fields it answers are those the spec declares.
func TestAGitAppsTagFieldsAreAnsweredAsTheSpecDeclaresThem(t *testing.T) {
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	view := &service.GitAppView{
		App: "jarvis", Follow: "tags", TagPattern: "v1.*", Prereleases: true,
		Deployed: &service.GitDeployedView{Commit: "abc", Subject: "fix", At: at, Tag: "v1.4.1"},
		Check:    &service.GitCheckView{At: at, RemoteCommit: "def", RemoteTag: "v1.4.1", TagMoved: true},
		History:  []service.GitHistoryView{{Commit: "abc", At: at, Outcome: "deployed", Tag: "v1.4.1"}},
	}
	// the operation's type is the service's own: it comes the way a state file holds it
	assert.NilError(t, json.Unmarshal([]byte(`{"kind":"build","commit":"def","started_at":"2026-09-23T10:00:00Z","tag":"v1.4.2"}`), &view.Operation))
	raw, err := json.Marshal(view)
	assert.NilError(t, err)

	var answered codegen.GitApp
	assert.NilError(t, json.Unmarshal(raw, &answered))
	assert.Equal(t, answered.Follow, "tags")
	assert.Equal(t, answered.TagPattern, "v1.*")
	assert.Equal(t, answered.Prereleases, true)
	assert.Equal(t, answered.Deployed.Tag, "v1.4.1")
	assert.Equal(t, answered.Check.RemoteTag, "v1.4.1")
	assert.Equal(t, answered.Check.TagMoved, true)
	assert.Equal(t, answered.History[0].Tag, "v1.4.1")
	assert.Equal(t, answered.Operation.Tag, "v1.4.2")
}

func TestAPostAndAPutPassTheTagFieldsOn(t *testing.T) {
	var create codegen.GitAppCreateRequest
	assert.NilError(t, json.Unmarshal([]byte(`{"name":"jarvis","url":"https://example.invalid/r.git","access":"none","follow":"tags","tag_pattern":"v2.*","prereleases":true}`), &create))
	assert.DeepEqual(t, gitAppRegistration(create), service.GitAppRegistration{
		Name: "jarvis", URL: "https://example.invalid/r.git", Access: "none", Follow: "tags", TagPattern: "v2.*", Prereleases: true,
	})

	var update codegen.GitAppUpdateRequest
	assert.NilError(t, json.Unmarshal([]byte(`{"follow":"branch","branch":"main","tag_pattern":"","prereleases":false}`), &update))
	assert.DeepEqual(t, gitAppChanges(update), service.GitAppChanges{
		Follow: lo.ToPtr("branch"), Branch: lo.ToPtr("main"), TagPattern: lo.ToPtr(""), Prereleases: lo.ToPtr(false),
	})
}

func TestTheTagsOfAGitAppAreAnsweredUnderData(t *testing.T) {
	answer := func(tags []service.GitTag, err error) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), recorder)
		assert.NilError(t, gitAppTagsAnswer(ctx, tags, err))

		return recorder
	}
	const commit = "4f5f60c16eba0123456789abcdef0123456789ab"

	recorder := answer([]service.GitTag{{Name: "v1.4.2", Commit: commit}}, nil)
	assert.Equal(t, recorder.Code, http.StatusOK)
	var answered codegen.GitAppTagsOK
	assert.NilError(t, json.Unmarshal(recorder.Body.Bytes(), &answered))
	assert.DeepEqual(t, *answered.Data, []codegen.GitTag{{Name: "v1.4.2", Commit: commit}})

	recorder = answer(nil, service.GitRequestError("jarvis follows a branch, not tags"))
	assert.Equal(t, recorder.Code, http.StatusBadRequest)
	assert.Equal(t, recorder.Body.String(), "{\"message\":\"jarvis follows a branch, not tags\"}\n")
}
```

In `route/v2_git_test.go`, in `TestTheGitRoutesAreServed`, after the row `{http.MethodPost, "/v2/app_management/git/no-such-app/deploy", `{"commit":"4f5f60c"}`, http.StatusBadRequest},` add:

```go
		{http.MethodPost, "/v2/app_management/git/no-such-app/deploy", `{"tag":"v1.0.0"}`, http.StatusNotFound},
		{http.MethodPost, "/v2/app_management/git/no-such-app/deploy", `{"tag":"v1.0.0","commit":"4f5f60c16eba0123456789abcdef0123456789ab"}`, http.StatusBadRequest},
		{http.MethodGet, "/v2/app_management/git/no-such-app/tags", "", http.StatusNotFound},
		{http.MethodPut, "/v2/app_management/git/no-such-app", `{"follow":"tags","tag_pattern":"v2.*","prereleases":true}`, http.StatusNotFound},
		{http.MethodPut, "/v2/app_management/git/no-such-app", `{"prereleases":"yes"}`, http.StatusBadRequest},
		{http.MethodPost, "/v2/app_management/git", `{"name":"Not A Name","url":"https://example.invalid/r.git","access":"none","follow":1}`, http.StatusBadRequest},
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./route/...`

Expected: FAIL, compile errors in `route/v2/git_internal_test.go`: `answered.Follow undefined (type codegen.GitApp has no field or method Follow)`, `undefined: gitAppRegistration`, `undefined: codegen.GitAppTagsOK`, `undefined: gitAppTagsAnswer`. (On Linux the `{"tag","commit"}` row of `TestTheGitRoutesAreServed` answers 404 instead of 400 until the handler checks it.)

- [ ] **Step 3: Implement the spec**

In `api/app_management/openapi.yaml` (CRLF line endings, keep them), make these replacements.

POST `/git`: replace

```yaml
        400 for a name that is not a compose project name, a URL CasaOS cannot clone (HTTPS, SSH
        or file, without a password), or an access the URL cannot carry. 409 for a name used by
        an app, a compose project or another git app.
      operationId: createGitApp
```

with:

```yaml
        With `follow` `tags` the app follows the highest version tag of its repository instead of
        a branch: `branch` is then refused, and `tag_pattern` and `prereleases` choose the tags.

        400 for a name that is not a compose project name, a URL CasaOS cannot clone (HTTPS, SSH
        or file, without a password), an access the URL cannot carry, a `follow` other than
        `branch` or `tags`, or a tag pattern that is not one. 409 for a name used by an app, a
        compose project or another git app.
      operationId: createGitApp
```

PUT `/git/{app}`: replace

```yaml
      summary: Change the branch, the automatic rebuild, the access or the webhook of a git app
```

with:

```yaml
      summary: Change the branch, the automatic rebuild, the access, the webhook or the mode of a git app
```

and replace

```yaml
        `webhook_enabled` turns the webhook on, generating its secret when there is none, or
        off, forgetting the secret. `regenerate_webhook_secret` replaces the secret at once, the
        old one refused from then on; 400 while the webhook is off.
      operationId: updateGitApp
```

with:

```yaml
        `webhook_enabled` turns the webhook on, generating its secret when there is none, or
        off, forgetting the secret. `regenerate_webhook_secret` replaces the secret at once, the
        old one refused from then on; 400 while the webhook is off.

        `follow` changes the mode. The folder of a cloned app is put on the branch (`branch`,
        or the remote's default when absent) or detached at its commit, its files untouched;
        `tag_pattern` and `prereleases` are kept either way. After a change, nothing deploys
        automatically until one deployment by hand in the new mode. 400 for a `branch` while
        the app follows tags.
      operationId: updateGitApp
```

`/git/{app}/check`: replace

```yaml
        `GET /git/{app}` until `operation` is null. The check asks the remote where the branch
        is; for a registered app not cloned yet it clones it and validates its compose files. A
```

with:

```yaml
        `GET /git/{app}` until `operation` is null. The check asks the remote where the branch
        is, or for an app that follows tags which is the highest eligible tag (`check.error`
        names the filter when none is); for a registered app not cloned yet it clones it, at
        that tag for an app that follows tags, and validates its compose files. A
```

`/git/{app}/deploy`: replace

```yaml
        Starts the deployment in the background and answers 202 with the operation running. No
        commit: the remote's latest. A commit of the history: a revert, which pauses the
        automatic rebuild. `env` is written as `.env` first, while no deployment has succeeded
```

with:

```yaml
        Starts the deployment in the background and answers 202 with the operation running. No
        commit: the remote's latest. A commit of the history: a revert, which pauses the
        automatic rebuild. For an app that follows tags, `tag` deploys any eligible tag, lower
        ones included (a lower one pauses the automatic deployment); neither `tag` nor `commit`
        deploys the highest eligible tag; a commit must be a version of the history. `tag` with
        `commit`: 400. `env` is written as `.env` first, while no deployment has succeeded
```

Insert before the line `  /git/{app}/webhook:`:

```yaml
  /git/{app}/tags:
    get:
      summary: The tags an app that follows tags may deploy
      description: |
        Asks the remote, as a check does. The eligible tags: those matching `tag_pattern` when
        there is one, strict semver after one leading `v`, pre-releases only with
        `prereleases`. Highest first, equal versions by name, at most 50. 400 for an app that
        follows a branch, or a repository that cannot be reached.
      operationId: getGitAppTags
      tags:
        - Git methods
      parameters:
        - $ref: "#/components/parameters/GitAppName"
      responses:
        "200":
          $ref: "#/components/responses/GitAppTagsOK"
        "400":
          $ref: "#/components/responses/ResponseBadRequest"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "500":
          $ref: "#/components/responses/ResponseInternalServerError"

```

Insert before the line `    ComposeAppUpdatePlanOK:`:

```yaml
    GitAppTagsOK:
      description: OK
      content:
        application/json:
          schema:
            allOf:
              - $ref: "#/components/schemas/BaseResponse"
              - properties:
                  data:
                    type: array
                    items:
                      $ref: "#/components/schemas/GitTag"

```

Replace (the end of `GitAccess` and the start of `GitApp`):

```yaml
      type: string
      example: key

    GitApp:
```

with:

```yaml
      type: string
      example: key

    GitFollow:
      description: |
        What the app deploys: `branch`, the head of its branch; `tags`, the highest version tag
        eligible under `tag_pattern` and `prereleases`, and automatically only a higher one.

        One of `branch`, `tags`. Not declared as an enum, for the reason given on
        `GitAppState`.
      type: string
      example: tags

    GitApp:
```

In `GitApp.required`, replace:

```yaml
        - remote
        - branch
        - access
```

with:

```yaml
        - remote
        - branch
        - follow
        - tag_pattern
        - prereleases
        - access
```

Replace the `branch` property of `GitApp`:

```yaml
        branch:
          type: string
          description: Empty for a folder in detached HEAD, which cannot be adopted.
          example: main
```

with:

```yaml
        branch:
          type: string
          description: |
            Empty for a folder in detached HEAD, which cannot be adopted, and for an app that
            follows tags.
          example: main
        follow:
          $ref: "#/components/schemas/GitFollow"
        tag_pattern:
          type: string
          description: |
            A glob on the full tag name (`*`, `?`, `[...]`), empty for every version tag. Kept
            while the app follows a branch.
          example: v2.*
        prereleases:
          type: boolean
          description: Whether pre-releases (`-rc`, `-beta`, ...) are eligible. Kept while the app follows a branch.
```

Replace:

```yaml
        new_commits:
          type: boolean
          description: False while `deployed` is null.
```

with:

```yaml
        new_commits:
          type: boolean
          description: |
            False while `deployed` is null. For an app that follows tags, true only for a tag
            higher than `deployed.tag`, or any other commit while `deployed.tag` is empty.
```

Replace the four schemas `GitAppDeployed`, `GitAppCheck`, `GitAppOperation` and `GitAppHistoryEntry` (from `    GitAppDeployed:` down to the `revertable` property's `description: Whether this version can be reverted to, its images still being there.`) with:

```yaml
    GitAppDeployed:
      description: The version running. Null before the first successful deployment.
      nullable: true
      required:
        - commit
        - subject
        - at
        - tag
      properties:
        commit:
          type: string
        subject:
          type: string
        at:
          type: string
          format: date-time
        tag:
          type: string
          description: The tag the commit was deployed as; empty for a branch's commit.
          example: v1.4.2

    GitAppCheck:
      description: The last check of the remote. Null before the first check.
      nullable: true
      required:
        - at
        - remote_commit
        - remote_tag
        - tag_moved
        - error
      properties:
        at:
          type: string
          format: date-time
        remote_commit:
          type: string
        remote_tag:
          type: string
          description: The highest eligible tag, which names `remote_commit`; empty for an app that follows a branch.
          example: v1.4.2
        tag_moved:
          type: boolean
          description: |
            `remote_tag` is `deployed.tag` on another commit than `deployed.commit`. Never
            redeployed automatically; deploying that tag by hand builds its new commit.
        error:
          type: string

    GitAppOperation:
      description: The operation running. Null when none runs.
      nullable: true
      required:
        - kind
        - commit
        - started_at
        - tag
      properties:
        kind:
          type: string
          description: One of `check`, `build`, `deploy`, `revert`.
          example: build
        commit:
          type: string
        started_at:
          type: string
          format: date-time
        tag:
          type: string
          description: The tag `commit` is deployed as; empty for a branch's commit.

    GitAppHistoryEntry:
      required:
        - commit
        - subject
        - at
        - outcome
        - reason
        - revertable
        - tag
      properties:
        commit:
          type: string
        subject:
          type: string
        at:
          type: string
          format: date-time
        outcome:
          type: string
          description: One of `adopted`, `deployed`, `build_failed`, `rolled_back`, `failed`, `interrupted`.
          example: deployed
        reason:
          type: string
        revertable:
          type: boolean
          description: Whether this version can be reverted to, its images and its commit still being there.
        tag:
          type: string
          description: The tag the commit was deployed as; empty for a branch's commit.
```

In `GitAppCreateRequest`, replace:

```yaml
        branch:
          type: string
          description: Empty or absent for the branch the remote's HEAD points to.
          example: main
        access:
          $ref: "#/components/schemas/GitAccess"
        token:
          type: string
          description: Required with `access` `token`; never returned.
```

with:

```yaml
        branch:
          type: string
          description: Empty or absent for the branch the remote's HEAD points to; refused with `follow` `tags`.
          example: main
        access:
          $ref: "#/components/schemas/GitAccess"
        token:
          type: string
          description: Required with `access` `token`; never returned.
        follow:
          $ref: "#/components/schemas/GitFollow"
        tag_pattern:
          type: string
          description: At most 100 characters, no space; a `path.Match` glob such as `v2.*`.
        prereleases:
          type: boolean
```

In `GitAppUpdateRequest`, replace:

```yaml
        regenerate_webhook_secret:
          type: boolean
          description: Replaces the secret at once; 400 while the webhook is off.
```

with:

```yaml
        regenerate_webhook_secret:
          type: boolean
          description: Replaces the secret at once; 400 while the webhook is off.
        follow:
          $ref: "#/components/schemas/GitFollow"
        tag_pattern:
          type: string
          description: At most 100 characters, no space; a `path.Match` glob such as `v2.*`.
        prereleases:
          type: boolean
```

Replace `GitAppDeployRequest`:

```yaml
    GitAppDeployRequest:
      properties:
        commit:
          type: string
          description: Absent for the remote's latest commit; a commit of the history reverts to it.
          pattern: "^[0-9a-f]{40}$"
        env:
          type: string
          description: The `.env` to write, accepted while no deployment has succeeded and never when the repository tracks `.env`.
```

with:

```yaml
    GitAppDeployRequest:
      properties:
        commit:
          type: string
          description: |
            Absent for the remote's latest commit (the highest eligible tag for an app that
            follows tags); a commit of the history reverts to it.
          pattern: "^[0-9a-f]{40}$"
        tag:
          type: string
          description: |
            For an app that follows tags, instead of `commit` (both: 400): an eligible tag,
            lower ones included.
          example: v1.4.2
        env:
          type: string
          description: The `.env` to write, accepted while no deployment has succeeded and never when the repository tracks `.env`.

    GitTag:
      required:
        - name
        - commit
      properties:
        name:
          type: string
          example: v1.4.2
        commit:
          type: string
          description: The commit the tag names, an annotated tag peeled.
```

Regenerate (Git Bash):

```bash
cd D:/clients/casaos/CasaOS-AppManagement && go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,server,spec -package codegen api/app_management/openapi.yaml > codegen/app_management_api.go && grep -cE '^[[:blank:]][A-Z][[:alnum:]_]* +[[:alnum:]_]+ = "' codegen/app_management_api.go
```

Expected: `34` (another number means an enum constant was renamed: stop and look at `codegen/app_management_api.go`). Until the route below is written, `go vet ./route/...` also reports `*AppManagement does not implement codegen.ServerInterface (missing method GetGitAppTags)`.

- [ ] **Step 4: Implement the route**

In `route/v2/git.go`, replace:

```go
// gitAppOK is `GitAppOK`. The service builds the `GitApp` itself, to the contract.
type gitAppOK struct {
	Data    *service.GitAppView `json:"data"`
	Message *string             `json:"message,omitempty"`
}

func (a *AppManagement) CreateGitApp(ctx echo.Context) error {
	var body codegen.GitAppCreateRequest
	if err := ctx.Bind(&body); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	view, err := service.CreateGitApp(ctx.Request().Context(), service.GitAppRegistration{
		Name: body.Name, URL: body.Url, Branch: lo.FromPtr(body.Branch), Access: body.Access, Token: lo.FromPtr(body.Token),
	})

	return gitAppAnswer(ctx, http.StatusOK, view, err)
}
```

with:

```go
// gitAppOK is `GitAppOK`. The service builds the `GitApp` itself, to the contract.
type gitAppOK struct {
	Data    *service.GitAppView `json:"data"`
	Message *string             `json:"message,omitempty"`
}

// gitAppTagsOK is `GitAppTagsOK`.
type gitAppTagsOK struct {
	Data    []service.GitTag `json:"data"`
	Message *string          `json:"message,omitempty"`
}

func (a *AppManagement) CreateGitApp(ctx echo.Context) error {
	var body codegen.GitAppCreateRequest
	if err := ctx.Bind(&body); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	view, err := service.CreateGitApp(ctx.Request().Context(), gitAppRegistration(body))

	return gitAppAnswer(ctx, http.StatusOK, view, err)
}

// gitAppRegistration is the app a POST asks the service to register.
func gitAppRegistration(body codegen.GitAppCreateRequest) service.GitAppRegistration {
	return service.GitAppRegistration{
		Name: body.Name, URL: body.Url, Branch: lo.FromPtr(body.Branch), Access: body.Access, Token: lo.FromPtr(body.Token),
		Follow: lo.FromPtr(body.Follow), TagPattern: lo.FromPtr(body.TagPattern), Prereleases: lo.FromPtr(body.Prereleases),
	}
}
```

Replace `gitAppChanges`:

```go
// gitAppChanges is what a PUT asks the service to change.
func gitAppChanges(body codegen.GitAppUpdateRequest) service.GitAppChanges {
	return service.GitAppChanges{
		Branch: body.Branch, AutoDeploy: body.AutoDeploy, Access: body.Access, Token: body.Token,
		WebhookEnabled: body.WebhookEnabled, RegenerateWebhookSecret: body.RegenerateWebhookSecret,
	}
}
```

with:

```go
// gitAppChanges is what a PUT asks the service to change.
func gitAppChanges(body codegen.GitAppUpdateRequest) service.GitAppChanges {
	return service.GitAppChanges{
		Branch: body.Branch, AutoDeploy: body.AutoDeploy, Access: body.Access, Token: body.Token,
		WebhookEnabled: body.WebhookEnabled, RegenerateWebhookSecret: body.RegenerateWebhookSecret,
		Follow: body.Follow, TagPattern: body.TagPattern, Prereleases: body.Prereleases,
	}
}
```

Replace `DeployGitApp`:

```go
func (a *AppManagement) DeployGitApp(ctx echo.Context, app codegen.GitAppName) error {
	// the body is optional: absent, it deploys the remote's latest
	var body codegen.GitAppDeployRequest
	if err := ctx.Bind(&body); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	view, err := service.DeployGitApp(ctx.Request().Context(), app, lo.FromPtr(body.Commit), body.Env)

	return gitAppAnswer(ctx, http.StatusAccepted, view, err)
}
```

with:

```go
func (a *AppManagement) DeployGitApp(ctx echo.Context, app codegen.GitAppName) error {
	// the body is optional: absent, it deploys the remote's latest
	var body codegen.GitAppDeployRequest
	if err := ctx.Bind(&body); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	var view *service.GitAppView
	var err error
	switch {
	case body.Tag != nil && body.Commit != nil:
		err = service.GitRequestError("give a tag or a commit, not both")
	case body.Tag != nil:
		view, err = service.DeployGitAppTag(ctx.Request().Context(), app, *body.Tag, body.Env)
	default:
		view, err = service.DeployGitApp(ctx.Request().Context(), app, lo.FromPtr(body.Commit), body.Env)
	}

	return gitAppAnswer(ctx, http.StatusAccepted, view, err)
}

func (a *AppManagement) GetGitAppTags(ctx echo.Context, app codegen.GitAppName) error {
	tags, err := service.GitAppTags(ctx.Request().Context(), app)

	return gitAppTagsAnswer(ctx, tags, err)
}

// gitAppTagsAnswer answers the tags of a git app, or what the service refused.
func gitAppTagsAnswer(ctx echo.Context, tags []service.GitTag, err error) error {
	if err != nil {
		return gitAppError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, gitAppTagsOK{Data: tags})
}
```

- [ ] **Step 5: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && for f in route/v2/git.go route/v2/git_internal_test.go route/v2_git_test.go; do tr -d '\r' < "$f" | gofmt -l | sed "s|<standard input>|$f|"; done; git ls-files --eol api/app_management/openapi.yaml | grep mixed; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./... && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./... && echo build-ok`

Expected: `build-ok` and nothing else.

Run (Linux, after `go generate ./...`): `go test ./route/ ./route/v2/ -run 'Git|TagFieldsOn|WebhookFieldsOn' -count=1 -v`

Expected: `--- PASS` for `TestTheGitRoutesAreServed`, `TestOnlyAGitWebhookIsServedWithoutAToken`, `TestAGitAppsTagFieldsAreAnsweredAsTheSpecDeclaresThem`, `TestAPostAndAPutPassTheTagFieldsOn`, `TestTheTagsOfAGitAppAreAnsweredUnderData`, `TestAGitAppRefusalIsAnsweredWithItsStatus`, `TestAGitAppIsAnsweredUnderData`, `TestAGitAppsWebhookIsAnsweredAsTheSpecDeclaresIt`, `TestAPutPassesTheWebhookFieldsOn`, `TestAGitWebhookIsAnsweredWithItsStatus`; `ok` for both packages.

- [ ] **Step 6: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add api/app_management/openapi.yaml route/v2/git.go route/v2/git_internal_test.go route/v2_git_test.go
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(api): tag mode fields, the tag list and deploy by tag"
```

(`codegen/` is gitignored: it is not staged.)

---

### Task 9: Whole-branch verification

**Files:** none changed.

**Interfaces:** Consumes everything above; produces nothing.

- [ ] **Step 1: Regenerate and count from the committed spec**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,server,spec -package codegen api/app_management/openapi.yaml > codegen/app_management_api.go && grep -cE '^[[:blank:]][A-Z][[:alnum:]_]* +[[:alnum:]_]+ = "' codegen/app_management_api.go`

Expected: `34`.

- [ ] **Step 2: Vet and build for Linux**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./... && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./... && echo build-ok`

Expected: `build-ok`.

- [ ] **Step 3: Format and line endings of every file the branch touched**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && for f in $(git diff --name-only e5d53ef -- '*.go'); do tr -d '\r' < "$f" | gofmt -l | sed "s|<standard input>|$f|"; done; git ls-files --eol $(git diff --name-only e5d53ef) | grep mixed`

Expected: no output.

- [ ] **Step 4: Nothing left over in the tree**

Run (Git Bash): `git -C D:/clients/casaos/CasaOS-AppManagement status --porcelain`

Expected: nothing tracked is modified (untracked files other agents left, such as other plans, stay untouched).

- [ ] **Step 5: The whole suite on Linux**

Run (Linux, as CI does): `go generate ./... && go test -race ./...`

Expected: `ok` for every package with tests. A failure of a test unrelated to git apps that also fails on the parent commit `e5d53ef` is reported, not fixed here.

No commit in this task.

---

## Spec coverage

| Spec requirement | Task(s) | Test(s) |
|---|---|---|
| `follow` `branch`/`tags`, a plain string validated in code, never an enum (count 34) | 2, 3, 8, 9 | `TestAModeAndATagPatternAreChecked`, `TestAnAppThatFollowsTagsIsRegisteredWithoutABranch`, count command |
| `tag_pattern`: `path.Match` glob, ≤ 100 chars, no whitespace/control, valid pattern | 2, 3 | `TestAModeAndATagPatternAreChecked`, `TestTheModeOfAClonedAppChangesAndItsFolderFollows` |
| `prereleases` bool, default false | 2, 3, 4 | `TestAnAppThatFollowsTagsIsClonedAtItsHighestTag` |
| Tag mode: `branch` empty, the check never resolves the default branch | 3, 4 | `TestAnAppThatFollowsTagsIsClonedAtItsHighestTag` (`st.Branch == ""`) |
| `deployed`, `history[]`, `operation` gain `tag` | 2, 5 | `TestTheViewOfAnAppThatFollowsTagsSaysWhenItsTagMoved` (the `operation.tag` key), `TestAnyEligibleTagIsDeployedByHand`, `TestAnInterruptedDeploymentOfATagKeepsItsTag` |
| View: `follow`, `tag_pattern`, `prereleases`; `check.remote_tag`, `check.tag_moved`; `operation.tag`; contract test | 2, 8 | `TestTheViewOfAGitAppIsTheContract`, `TestTheViewOfAnAppThatFollowsTagsSaysWhenItsTagMoved`, `TestAGitAppsTagFieldsAreAnsweredAsTheSpecDeclaresThem` (against the generated `GitApp`, `Operation.Tag` included) |
| POST/PUT accept `follow`, `tag_pattern`, `prereleases`: OpenAPI, codegen, hand copy | 3, 8 | `TestAPostAndAPutPassTheTagFieldsOn`, `TestTheGitRoutesAreServed` |
| `GET …/tags`: eligible, highest first, ≤ 50, `{name, commit}`, 400 in branch mode | 4, 8 | `TestTheTagsOfAnAppAreListedHighestFirstFiftyAtMost`, `TestTheTagsOfAGitAppAreAnsweredUnderData`, `TestTheGitRoutesAreServed` |
| Deploy `{tag}`; `tag`+`commit` 400; neither in tag mode: highest at request time | 5, 8 | `TestAnyEligibleTagIsDeployedByHand`, `TestTheGitRoutesAreServed` |
| Changing the mode; pattern and pre-releases kept; first deployment in the new mode by hand, automatic deployment resuming after it (a revert included, and a branch whose head does not descend from the tag that ran) | 3, 5, 6 | `TestTheModeOfAClonedAppChangesAndItsFolderFollows`, `TestAfterAChangeOfModeTheFirstDeploymentIsByHand`, `TestBackOnABranchAfterATagOfAnotherLineTheBranchIsFollowedAgain` |
| `ls-remote --tags`: annotated (peeled line), lightweight, invalid names dropped, empty listing | 1 | `TestLsRemoteTagsGivesEachTagTheCommitItNames` |
| Eligibility: pattern, strict semver after one `v`, pre-releases, non-semver ignored | 2 | `TestAnEligibleTagIsStrictSemverAfterOneLeadingV` |
| Highest wins; equal versions ordered by name | 2 | `TestEqualVersionsAreOrderedByName`, `TestATagIsHigherOnlyByItsVersion` |
| Check records `remote_tag`/`remote_commit`; no eligible tag: `check.error` with the filter, nothing else changes (state `idle`) | 4 | `TestAnAppThatFollowsTagsIsClonedAtItsHighestTag`, `TestNoEligibleTagSaysWhichFilterIsInForce`, `TestGitAppStateFollowsTheRule` |
| First clone at the tag (detached HEAD), compose check unchanged | 1, 4 | `TestAFetchedTagIsItsCommitAndACommitIsFetchedByItsHash`, `TestAnAppThatFollowsTagsIsClonedAtItsHighestTag` |
| Fetch of the tag alone, peeled; refused, asking for a new check, when it moved (automatic and by hand) | 1, 5 | `TestAFetchedTagIsItsCommitAndACommitIsFetchedByItsHash`, `TestATagThatMovedSinceTheCheckIsRefused` |
| Preconditions: detached HEAD, clean, `.env` rules; branch-only rules dropped | 5 | `TestADeploymentOfATagRefusesWhatItCannotDo`, `TestAnyEligibleTagIsDeployedByHand` (a tag from another line) |
| Automatic: strictly higher, `deployed.tag` non-empty (tags) / empty (branch), not attempted/paused/blocked | 6 | `TestTheAutomaticDeploymentOfTagsOnlyEverGoesUp`, `TestAnAppThatFollowsTagsIsUpgradedAutomaticallyAndNeverDowngraded` |
| Nothing automatic for a deleted tag, a narrowed pattern, a moved tag, an empty `deployed.tag` after a switch | 6 | `TestAnAppThatFollowsTagsIsUpgradedAutomaticallyAndNeverDowngraded`, `TestAfterAChangeOfModeTheFirstDeploymentIsByHand` |
| Manual: any eligible tag, lower ones built; a history commit a revert; the running version is nothing new to build, or a repair | 5 | `TestAnyEligibleTagIsDeployedByHand`, `TestADeploymentOfATagRefusesWhatItCannotDo`, `TestARepairOfAnAppThatFollowsTagsNeedsNeitherItsHistoryNorItsTag` |
| A moved tag deployed by hand builds its new commit | 6 | `TestAnAppThatFollowsTagsIsUpgradedAutomaticallyAndNeverDowngraded` |
| `reset --keep` instead of a fast-forward; rollback and blocking unchanged | 1, 5 | `TestAnyEligibleTagIsDeployedByHand` (hotfix line), existing rollback tests in `go test ./service/` |
| `refs/recasaos/kept/<commit>` made and removed; `revertable` needs the commit; revert after the tag is deleted | 1, 6 | `TestAKeptCommitOutlivesTheRefsThatBroughtIt`, `TestTheCommitsOfTheHistoryAreKeptWhateverTheTagsDo` |
| `new_commits`/grid badge in semver, never lit for a lower or moved tag | 2, 6 | `TestNewCommitsOfAnAppThatFollowsTagsIsAHigherTag`, `TestAnAppThatFollowsTagsIsUpgradedAutomaticallyAndNeverDowngraded` |
| Webhooks: no server change; poll every five minutes unchanged | none changed | `go test ./service/` in Tasks 5-7 (webhook and periodic tests) |
| Backup manifest gains `follow`, `tag`, `tag_pattern`, `prereleases`, omitted when empty; branch manifest byte-identical | 7 | `TestABackupOfAnAppThatFollowsTagsRecordsItsTag` |
| Restore in the same mode: at the tag; by commit when the tag moved or went (no eligible tag left included), or when the manifest names no tag; clear error when the forge refuses; webhook off | 7 | `TestAnAppThatFollowsTagsIsRestoredAtItsTag`, `TestARestoreFetchesTheBackedUpCommitWhenItsTagMovedOrWent`, `TestAnAppThatFollowsTagsIsRestoredWithoutItsTag`, `TestARestoredGitAppComesBackWithItsWebhookOff` |
| Restore on a box that has the app: unchanged | 7 | `TestARestoreOfTheDeployedCommitStartsItFromItsImages` |
| Message bus: no new event | none changed | — |
| Dashboard, Core telemetry, install check | other plans | — |
