# Git apps: CasaOS-AppManagement — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** AppManagement deploys an app from its git repository: it registers or adopts the app, follows its branch, builds a commit beside the running version, switches to it, rolls back a version that does not start, and answers for all of it under `/v2/app_management/git`.

**Architecture:** `pkg/git` runs the host's `git` binary. One JSON state per app lives under `/var/lib/casaos/git_apps`; a deployment builds the target commit in a worktree through compose v5's library under a `git-<commit>` image tag, fast-forwards the folder, points the names compose uses at that tag, starts through the path every app takes, judges the built services, and rolls back to the previous tag when they fail. Everything Docker is behind one interface (`gitRuntime`) so the deployment's decisions are tested against real git repositories and a fake Docker. A small registry (`service/app_operations.go`) lets one operation run per app, across installs, updates, saves, backups, restores and git operations.

**Tech Stack:** Go 1.26, the host's git (2.47 in `golang:1.26`), Docker Compose v5.5.1 and compose-go v2.15.0 as libraries, moby client v0.5.1, `golang.org/x/crypto/ssh` v0.57.0, echo v4 with oapi-codegen v1.12.4, gotest.tools/v3.

**Spec:** `docs/superpowers/specs/2026-09-16-git-apps-design.md`, every section including its last one, "Clarifications made while planning", which amends the API below and binds this plan. Companion plans: `2026-09-16-git-apps-installer.md` (its install check drives the API sequence given in the contract below) and `2026-09-16-git-apps-dashboard.md`.

## Global Constraints

- Repository `D:/clients/casaos/CasaOS-AppManagement`, Go module `github.com/ReCasaOS/CasaOS-AppManagement`, branch `feat/git-apps`. Never push. Never run `git checkout`, `git switch`, `git stash`, `git reset`, `git checkout --` or `rm -rf` in the checkout; the only folder this plan removes is the throwaway `spike` of Task 1.
- Commits: stage files by name, then `git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "..."`. No `Co-Authored-By` and no tool attribution anywhere.
- The host is Windows and the Go code does not build there. Every build and test runs in `golang:1.26` through the PowerShell tool, with the Docker socket and the two cache volumes, as in every command of this plan. Tests that talk to the daemon (`pkg/docker`, `route`, `service`) need the socket; `CASAOS_INTEGRATION=1` opts into Task 13's test.
- `codegen/` is gitignored and generated from `api/app_management/openapi.yaml` by `go generate ./...` inside the container, which downloads the message bus spec (network needed).
- oapi-codegen v1.12.4 prunes schemas no route references, and renames the constants of existing enums whenever an enum is added (adding a `GitAppState` enum turns `codegen.UpgradableAppInfoStatusIdle` into `codegen.Idle` and breaks `route/v2/appstore.go`). So the new schemas declare no `enum`: their values are listed in `description`, the service validates them, and after every regeneration the enum-constant count stays **34** and `go build ./...` passes.
- These working-tree files are CRLF: `service/compose_app.go`, `service/compose_service.go`, `service/image_update.go`, `route/v2/compose_app.go`, `route/v2/internal_web.go`, `api/app_management/openapi.yaml`, `go.sum`. Edit them with the Edit tool (it keeps their line endings) and judge gofmt on LF copies, as the gofmt steps do.
- In package `service`: `encoding/json` is imported as `stdjson` (`json` is taken); tests use `gotest.tools/v3/assert`; a test that parses compose YAML calls `logger.LogInitConsoleOnly()`; test helpers must not be named `run` or `running` (both exist).
- Under `set -o pipefail`, never pipe into a reader that stops early (`head`, `grep -q`).
- Line numbers in **Files** blocks are those of commit `e6c31b9`; earlier tasks shift them, and the text to replace is the anchor.
- Paths: state `/var/lib/casaos/git_apps/<app>.json` and, beside it and readable by root only, `<app>.key`, `<app>.key.pub`, `<app>.token`, `<app>.known_hosts`, `<app>.build.log`, and for the length of one git command with a token its askpass helper in a folder `askpass-*`; build worktrees under `/var/lib/casaos/git_apps/work/`; a created app is cloned into `/DATA/AppData/<app>`.
- git: every command runs with `-c credential.helper= -c core.hooksPath=/dev/null`, `-c safe.directory=<dir>` when it runs in a folder, `GIT_TERMINAL_PROMPT=0` and `GIT_OPTIONAL_LOCKS=0`; 30 seconds for `ls-remote`, 10 minutes for a clone, a fetch or a submodule update. A key becomes `GIT_SSH_COMMAND="ssh -i <key> -o IdentitiesOnly=yes -o UserKnownHostsFile=<known_hosts> -o StrictHostKeyChecking=accept-new"`; a token reaches git through a `GIT_ASKPASS` helper written in a `0700` folder `askpass-*` under the state folder (the variable tests point elsewhere) and removed after each command, never under `/tmp`, which may be mounted noexec; the token is never in an argument, a URL, `.git/config` or captured output. Key access without `ssh` on `PATH` fails with "the ssh client is not installed on this machine".
- Deploy keys are ed25519, generated with `golang.org/x/crypto/ssh` (`ssh.MarshalPrivateKey`, `ssh.MarshalAuthorizedKey`), the public line ending in ` casaos-<app>`.
- Builds go through compose v5's `api.Compose.Build`, which uses buildx when the host has the plugin and the daemon's builder otherwise.
- One operation per app: `service.Begin(app, kind string) (end func(), err error)` and `service.ErrAppBusy{App, Running string}`, answered 409 with the message "`<app>` is busy: <kind> in progress".

### API contract (binding, as amended by the design's "Clarifications made while planning")

Routes under `/v2/app_management/git`, the owner's token required like every v2 route, errors as `{"message": string}`:

1. `POST /v2/app_management/git`, body `{"name": string, "url": string, "branch": string (optional), "access": "none"|"key"|"token", "token": string (required when access is token)}`: 200 `{"data": GitApp}` with origin `created`, `cloned` false, `public_key` when access is key. 400 invalid name, URL or access. 409 when the name is used by an app, a compose project or a registered git app.
2. `GET /v2/app_management/git/{app}`: 200 `{"data": GitApp}`. 404 when the app is neither registered nor adoptable. An adoptable folder in detached HEAD answers `"branch": ""`, one without a remote `"remote": ""`.
3. `PUT /v2/app_management/git/{app}`, body `{"branch": string, "auto_deploy": boolean, "access": "none"|"key"|"token", "token": string}`, every field optional: 200 `{"data": GitApp}`. Adopts an adoptable app (400 with the reason for detached HEAD or no remote). 400 invalid, including `"token": ""`. Switching `access` to `none` or `key` forgets a saved token. 409 while an operation runs.
4. `POST /v2/app_management/git/{app}/check`: asynchronous, 202 `{"data": GitApp}` with `operation.kind` `check`; the client polls `GET` until `operation` is null. Adopts an adoptable app (400 with the reason). For a registered app not cloned yet, it clones and validates the compose files. A clone without a compose file ends with the clone removed, `check.error` holding the message and `compose_example` holding a sample compose file (`compose_example` is null otherwise). An automatic deployment the check triggers starts once the check's operation has ended, in the background. 409 while an operation runs.
5. `POST /v2/app_management/git/{app}/deploy`, body `{"commit": string (optional, 40 hex), "env": string (optional)}`: 202 `{"data": GitApp}`, the deployment runs in the background. Adopts an adoptable app (400 with the reason). No commit: the remote's latest. A commit from the history: a revert. `env` is accepted while `deployed` is null and never when `env_tracked` is true. 400 when a precondition fails (the message says which). 409 while an operation runs.
6. `DELETE /v2/app_management/git/{app}`: 200, allowed while `deployed` is null whatever the history; it removes the containers and networks a failed first deployment left, the clone of a created app, the state and the secrets. 409 once a deployment has succeeded (uninstall it instead) or while an operation runs.

`GitApp`:

```json
{
  "app": "jarvis",
  "origin": "created | adopted | adoptable",
  "dir": "/DATA/AppData/jarvis",
  "remote": "git@github.com:owner/jarvis.git",
  "branch": "main",
  "access": "none | key | token",
  "public_key": "ssh-ed25519 AAAA... casaos-jarvis (only when access is key)",
  "token_set": false,
  "auto_deploy": false,
  "auto_paused": false,
  "blocked": false,
  "env_tracked": false,
  "cloned": true,
  "head": { "commit": "40 hex", "subject": "string", "tracked_files_clean": true },
  "deployed": { "commit": "40 hex", "subject": "string", "at": "RFC3339" },
  "check": { "at": "RFC3339", "remote_commit": "40 hex", "error": "" },
  "new_commits": false,
  "state": "idle | building | deploying | build_failed | rolled_back | failed | unreachable | interrupted",
  "operation": { "kind": "check | build | deploy | revert", "commit": "40 hex", "started_at": "RFC3339" },
  "history": [ { "commit": "40 hex", "subject": "string", "at": "RFC3339", "outcome": "adopted | deployed | build_failed | rolled_back | failed | interrupted", "reason": "", "revertable": true } ],
  "compose": { "files": ["compose.yaml"], "services": [ { "name": "web", "build": true, "image": "", "ports": ["8080:8080"], "volumes": ["./data:/data"], "sensitive": ["privileged"] } ] },
  "compose_example": "null, or a sample compose file after a clone without one",
  "env_template": "KEY=value\n",
  "build_log": "the last 64 KiB of <app>.build.log"
}
```

- `head` is null until cloned, `deployed` null before the first successful deployment, `check` null before the first check, `operation` null when none runs, `compose` null until cloned, `env_template` null without `.env.example`/`.env.sample`, `compose_example` null unless the last check found no compose file.
- `compose.services[].sensitive` holds only `privileged`, `network_mode_host`, `pid_host`, `cap_add`, `devices`, `docker_socket`.
- `state`: a running `build` operation gives `building`; a running `deploy` or `revert` gives `deploying`; a running `check` leaves the rest of the rule. Otherwise `failed` when `blocked` or the newest history outcome is `failed`; else the newest history outcome when it is `build_failed`, `rolled_back` or `interrupted`; else `unreachable` when `check.error` is not empty; else `idle`.
- `new_commits`: false while `deployed` is null; otherwise `check.remote_commit` is not empty and differs from `deployed.commit`.
- Grid: `WebAppGridItem` gains `git: {"new_commits": boolean, "state": <the same values>, "deployed": boolean}`, absent for other apps. A registered git app absent from the compose list (no container: never deployed, or none left by a failed first deployment) is synthesized as `{"app_type": "v2app", "name": <app>, "title": {"en_us": <app>}, "status": "", "git": {...}}`, and only such an item has `status: ""`. A git app with containers comes through the normal v2 path with its real status and `git`. Never both for one app.
- Events on the message bus: `app:git-build-begin`, `app:git-build-progress`, `app:git-build-end`, `app:git-build-error`, `app:git-deploy-end`, `app:git-deploy-error`. All carry `app:name`; progress and both error events also carry `message`.
- The installer's install check relies on exactly this sequence: `POST /git` with access `none` and a `file://` URL; `POST /check`, then `GET` until `operation` is null and `cloned` is true; `POST /deploy` with `{"env": ...}`; `PUT {"auto_deploy": true}`; after each push, `POST /check` then polling: `building`, then `idle` on the new version, and for a container that exits `rolled_back` with `history[0].outcome` `rolled_back`; finally the grid item's `git.state` `rolled_back` and `git.new_commits` true.

## Decisions this plan makes where the spec is silent

- A deployment recorded as `build_failed`, `rolled_back` or `failed` adds its commit to `attempted`, not only a failed build: otherwise the check after a rollback deploys the same broken commit again.
- The automatic rebuild acts only once a deployment has succeeded (`deployed` not null). An automatic deployment whose precondition fails records a `failed` history entry with the reason and adds the commit to `attempted`; a manual one answers 400 and records nothing.
- A check's hold on the app passes to the deployment it starts, so no other operation slips in between, and is renamed `deploy` as it passes: a request refused while that deployment runs names the deployment, never the check.
- A created app is cloned into `/DATA/AppData/.<app>.clone` and renamed into place, so a clone cut short never sits where the app goes; startup removes what is left.
- A manual deploy without a commit resolves the remote's latest with `ls-remote` before answering, so `operation.commit` is known in the 202.
- Adoption requires the compose project's folder to be the top of its work tree (the spec leaves compose files outside the repository root out of scope); any other compose app is not adoptable (404).
- AppManagement runs git as root in an adopted folder that usually belongs to a user. What a fetch, a fast-forward, a reset or a worktree writes there (everything under `.git`, and the files and folders a move changed) is handed back to the folder's owner, or that owner's next `git pull` fails on root-owned files.
- A build is tagged under `<repository>:git-<first 12 hex of the commit>` directly, never under the name compose uses, so a build that fails half-way leaves that name where it was; the switch retags. Revert and rollback use the same retag.
- A manifest's `git` field is written for an app with a successful deployment only. A restore that cannot reach the repository registers the app with a new deploy key for an SSH remote (the error carries the public key) or asks for a token for an HTTPS one, and fails with "access required".
- The compose review reads the compose files as written, without interpolation or `extends`/`include`, so it works before `.env` exists.
- `DELETE /git/{app}` also removes the `git-` images its history names, so an abandoned creation leaves nothing behind.
- Uninstalling any compose app takes the app's hold under the kind `uninstall`: it is refused with 409 while any other operation runs on the app, a deployment included, and holds the app until it ends.
- Uninstalling a git app removes its `git-` tags too, and never deletes an adopted folder, including through the config-folder removal that used to delete any folder containing the app's name.

## File structure

| File | Responsibility | Task |
|---|---|---|
| `pkg/git/git.go` | The git binary: ls-remote, default branch, clone, fetch, describe, subject, tracked files, ancestry, worktrees with submodules, fast-forward, reset --keep, ownership given back | 2 |
| `service/app_operations.go` | `Begin`, `ErrAppBusy` and `handOver`, the one place that refuses overlapping operations | 3, 8 |
| `service/git_runtime.go` | `gitRuntime`, everything Docker: build under git- tags, retag, tag running images, start and judge built services, stop, remove, images; compose file discovery and the worktree loader | 4 |
| `service/git_app_state.go` | The state file and its secrets, deploy keys, history and attempted bookkeeping, URL and branch validation | 5 |
| `service/git_app_view.go` | The `GitApp` view, the state rule, the compose review, the grid's `git` field and the synthesized grid items | 6 |
| `service/git_apps.go` | Register, find or adopt, change, delete, check (async) and clone | 7, 8, 11 |
| `service/git_deploy.go` | Preconditions, build, switch, verify, rollback, record, automatic deployment, build log | 8 |
| `service/git_lifecycle.go` | The 5-minute check of every app, and startup recovery | 9 |
| `route/v2/git.go` | The six handlers and their error statuses | 10 |
| `service/git_restore.go` | The manifest's `git` field and the restore of a git app that is not installed | 12 |
| `common/message.go`, `main.go`, `api/app_management/openapi.yaml`, `route/v2/internal_web.go`, `route/v2/compose_app.go`, `service/compose_app.go`, `service/compose_service.go`, `service/backup_*.go` | Wiring into what exists | 3, 6, 9, 10, 11, 12 |

---

### Task 1: Feasibility spike: a build through compose's library, with and without buildx

Everything in "A deployment" depends on this. Throwaway code: nothing of this task is committed.

**Files:**
- Create (removed at the end of the task): `spike/gitbuild/main.go`, `spike/gitbuild/run.sh`

**Interfaces:**
- Consumes: compose v5.5.1 (`compose.NewComposeService`, `api.Compose.Build`, `api.BuildOptions{Progress, Out}`), compose-go v2.15.0 (`cli.NewProjectOptions`, `cli.WithoutEnvironmentResolution`), docker/cli (`command.NewDockerCli(command.WithOutputStream, command.WithErrorStream)`), all in `go.mod` already.
- Produces: the go/no-go for Tasks 2 to 13, and the facts Task 4 relies on: a build under a name of our choosing (`<project>-<service>:git-...`), its output captured through the CLI's streams, `env_file` entries not read when environment resolution is off, and the same code working under `env -i` with the buildx plugin absent (classic builder) and present (bake).

- [ ] **Step 1: Write the spike program**

Create `spike/gitbuild/main.go`:

```go
// Throwaway feasibility spike for git apps: build a local repository through compose's
// library, the way AppManagement will, and print what was built. Deleted once it has run.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/cmd/display"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: gitbuild <repository> <project name>")
		os.Exit(2)
	}
	dir, name := os.Args[1], os.Args[2]
	ctx := context.Background()

	// the repository's .env is not in a worktree, so env_file entries are not read
	options, err := cli.NewProjectOptions([]string{filepath.Join(dir, "compose.yaml")},
		cli.WithWorkingDirectory(dir), cli.WithOsEnv, cli.WithName(name), cli.WithoutEnvironmentResolution)
	check(err)
	project, err := options.LoadProject(ctx)
	check(err)

	// built under a git- tag, never under the name the running app uses
	tag := ""
	for serviceName, service := range project.Services {
		if service.Build != nil {
			tag = api.GetImageNameOrDefault(service, name) + ":git-spike"
			service.Image = tag
			project.Services[serviceName] = service
		}
	}

	var out bytes.Buffer
	dockerCli, err := command.NewDockerCli(command.WithOutputStream(&out), command.WithErrorStream(&out))
	check(err)
	check(dockerCli.Initialize(&flags.ClientOptions{}))
	defer dockerCli.Client().Close()

	backend, err := compose.NewComposeService(dockerCli, compose.WithEventProcessor(display.Plain(&out)))
	check(err)

	err = backend.Build(ctx, project, api.BuildOptions{Progress: "plain", Out: &out})
	fmt.Print(out.String())
	check(err)

	image, err := dockerCli.Client().ImageInspect(ctx, tag)
	check(err)
	fmt.Println("built", tag, image.ID)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "spike failed:", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Write its runner**

Create `spike/gitbuild/run.sh`:

```sh
#!/bin/sh
# Throwaway: a local repository, built through compose's library from a process started
# with a systemd-like minimal environment. Each container starts with an empty /tmp.
set -eu
mkdir -p /tmp/spike-repo && cd /tmp/spike-repo
git init -q
printf 'FROM busybox\nRUN echo built > /built\n' > Dockerfile
printf 'services:\n  web:\n    build: .\n    env_file: .env\n' > compose.yaml
git add -A && git -c user.name=spike -c user.email=spike@example.invalid commit -qm spike
cd /src && go build -o /tmp/gitbuild ./spike/gitbuild
env -i PATH=/usr/local/go/bin:/usr/bin:/bin /tmp/gitbuild /tmp/spike-repo casaos-spike
```

- [ ] **Step 3: Build without the buildx plugin (the golang image has none)**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "sh /src/spike/gitbuild/run.sh" 2>&1 | Select-Object -Last 40
```

Expected: a warning `buildx Docker CLI plugin not found: falling back to the classic builder`, the classic builder's `Step 1/3 : FROM busybox` and `Successfully tagged casaos-spike-web:git-spike`, and as the last line `built casaos-spike-web:git-spike sha256:` followed by 64 hex digits.

- [ ] **Step 4: Fetch the buildx plugin**

Run:

```powershell
New-Item -ItemType Directory -Force D:\clients\casaos\_work\buildx | Out-Null
docker create --name casaos-buildx-bin docker/buildx-bin:latest /buildx
docker cp casaos-buildx-bin:/buildx D:\clients\casaos\_work\buildx\docker-buildx
docker rm casaos-buildx-bin
```

Expected: `Successfully copied` from `docker cp`, then `casaos-buildx-bin` from `docker rm`.

- [ ] **Step 5: Build with the buildx plugin**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v D:\clients\casaos\_work\buildx:/buildx:ro -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "mkdir -p /usr/local/lib/docker/cli-plugins && cp /buildx/docker-buildx /usr/local/lib/docker/cli-plugins/docker-buildx && chmod 755 /usr/local/lib/docker/cli-plugins/docker-buildx && sh /src/spike/gitbuild/run.sh" 2>&1 | Select-Object -Last 40
```

Expected: bake's progress (`#1 [internal] load local bake definitions`, `naming to docker.io/library/casaos-spike-web:git-spike`), and as the last line `built casaos-spike-web:git-spike sha256:` followed by 64 hex digits.

- [ ] **Step 6: Stop condition**

If either run does not end with its `built casaos-spike-web:git-spike sha256:...` line, stop here: report both outputs and do not start Task 2.

- [ ] **Step 7: Remove the spike**

Run:

```powershell
Remove-Item -Recurse -Force D:\clients\casaos\CasaOS-AppManagement\spike
docker image rm casaos-spike-web:git-spike
git -C D:\clients\casaos\CasaOS-AppManagement status --short
```

Expected: `Untagged: casaos-spike-web:git-spike`, and no `spike/` line in the status.

---

### Task 2: `pkg/git`, the host's git binary

**Files:**
- Create: `pkg/git/git.go`
- Test: `pkg/git/git_test.go`

**Interfaces:**
- Consumes: the `git` binary (and `ssh` for key access) on `PATH`.
- Produces, for Tasks 5 to 13:
  - `const AccessNone, AccessKey, AccessToken = "none", "key", "token"`; `var ErrNoSSHClient error`
  - `type Auth struct{ Mode, KeyPath, KnownHostsPath, Token, AskpassDir string }` (`AskpassDir`, required with a token: where each command's askpass helper is written, in a `0700` folder `askpass-*` removed after the command)
  - `type Info struct{ Toplevel, RemoteURL, Branch, Head string; TrackedFilesClean bool }` (`Branch` empty in detached HEAD, `Head` empty before a first commit, `RemoteURL` empty without a remote)
  - `LsRemote(ctx, url, branch string, auth Auth) (string, error)`
  - `DefaultBranch(ctx, url string, auth Auth) (string, error)`
  - `Clone(ctx, url, branch, dir string, auth Auth) error`
  - `Fetch(ctx, dir, url, branch string, auth Auth) (string, error)` (the fetched commit)
  - `Describe(ctx, dir string) (Info, error)`; `Subject(ctx, dir, commit string) (string, error)`; `HasCommit(ctx, dir, commit string) bool`
  - `IsTracked(ctx, dir, path string) (bool, error)`; `IsAncestor(ctx, dir, ancestor, descendant string) (bool, error)`
  - `AddWorktree(ctx, dir, commit, path string, auth Auth) error` (runs `submodule update --init --recursive` when `.gitmodules` exists; `auth` reaches private submodules); `RemoveWorktree(ctx, dir, path string) error`; `Prune(ctx, dir string) error`
  - `FastForward(ctx, dir, commit string) error` (`merge --ff-only`); `ResetKeep(ctx, dir, commit string) error` (`reset --keep`)

- [ ] **Step 1: Write the failing tests**

Create `pkg/git/git_test.go`:

```go
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
```

The token test serves a bare repository with `git-http-backend` behind basic auth; the golang image has it under `/usr/lib/git-core`. It also checks that every helper was written under the `AskpassDir` it was given, not under `/tmp`, and is gone after its command. The last test needs root, which the container is.

- [ ] **Step 2: Run them to see them fail**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go test ./pkg/git/ -count=1" 2>&1 | Select-Object -Last 40
```

Expected: FAIL, the test build stops on `undefined: DefaultBranch` (and the other functions).

- [ ] **Step 3: Write the package**

Create `pkg/git/git.go`:

```go
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

// Clone clones one branch of url into dir.
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

// ResetKeep moves the checked-out branch to commit, keeping local changes git can keep.
// Only a rollback or a revert uses it.
func ResetKeep(ctx context.Context, dir, commit string) error {
	return moveTo(ctx, dir, "reset", "--keep", commit)
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
```

- [ ] **Step 4: Run the tests**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go vet ./pkg/git/ && go test ./pkg/git/ -count=1 -v" 2>&1 | Select-Object -Last 40
```

Expected: `--- PASS` for TestCloneDescribesTheDefaultBranch, TestFetchThenAncestry, TestFastForwardRefusesADivergedOrModifiedTree, TestIsTrackedTellsATrackedEnvFromAnUntrackedOne, TestWorktreeWithASubmoduleRunsNoHook, TestATokenReachesGitThroughAskpassOnly, TestCapturedOutputNeverCarriesTheToken, TestADeployKeyNeedsAnSSHClient, TestWhatRootWritesInSomebodysFolderGoesBackToThem, then `ok  	github.com/ReCasaOS/CasaOS-AppManagement/pkg/git`.

- [ ] **Step 5: Check the formatting**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "tr -d '\r' < pkg/git/git.go | gofmt -l; tr -d '\r' < pkg/git/git_test.go | gofmt -l" 2>&1 | Select-Object -Last 40
```

Expected: no output.

- [ ] **Step 6: Commit**

```powershell
git -C D:\clients\casaos\CasaOS-AppManagement add pkg/git/git.go pkg/git/git_test.go
git -C D:\clients\casaos\CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): run the host git for apps deployed from their repository"
```

---

### Task 3: One operation per app

**Files:**
- Create: `service/app_operations.go`
- Modify: `service/compose_app.go:243-265` (`Update`, `UpdateNow`), `service/compose_app.go:1179-1184` (`apply`, before its goroutine)
- Modify: `service/compose_service.go:50-52` and `:96-99` (`Install`), `:120-121` and `:136-137` (`Uninstall`)
- Modify: `service/backup_run.go:107-108` (`runBackup`, the hold), `service/backup_restore.go:113-116` (`restoreBackup`)
- Modify: `route/v2/compose_app.go:286-291`, `:298`, `:361-364`, `:442-449`, `:484-488`, `:555-559`
- Modify: `api/app_management/openapi.yaml:528-534`, `:581-589`, `:601-607`, `:619-623`, `:675-683` (409 responses)
- Test: `service/app_operations_internal_test.go`, `route/v2/app_operations_internal_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `func Begin(app, kind string) (end func(), err error)` (a second call for the same app returns `nil, ErrAppBusy`; `end` may be called twice); `type ErrAppBusy struct{ App, Running string }` (value type, match with `errors.As(err, new(service.ErrAppBusy))`); in package `v2`, `func conflictOrServerError(ctx echo.Context, err error) error` (409 for `ErrAppBusy`, 500 otherwise). Kinds used: `update`, `save`, `install`, `uninstall`, `backup`, `restore`, and in later tasks `registration`, `settings change`, `removal`, `check`, `deploy`.

- [ ] **Step 1: Write the failing tests**

Create `service/app_operations_internal_test.go`:

```go
package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

func TestBeginRefusesASecondOperationOnTheSameApp(t *testing.T) {
	end, err := Begin("jarvis", "deploy")
	assert.NilError(t, err)

	_, err = Begin("jarvis", "update")
	var busy ErrAppBusy
	assert.Assert(t, errors.As(err, &busy), err)
	assert.Equal(t, busy, ErrAppBusy{App: "jarvis", Running: "deploy"})
	assert.Equal(t, err.Error(), "`jarvis` is busy: deploy in progress")

	other, err := Begin("nextcloud", "backup")
	assert.NilError(t, err, "another app is not held")
	other()

	end()
	end() // twice is harmless

	again, err := Begin("jarvis", "update")
	assert.NilError(t, err)
	again()
}

// holding is what a running deployment looks like to everything else.
func holding(t *testing.T, app string) {
	t.Helper()

	end, err := Begin(app, "deploy")
	assert.NilError(t, err)
	t.Cleanup(end)
}

func assertBusy(t *testing.T, err error) {
	t.Helper()

	assert.Assert(t, errors.As(err, new(ErrAppBusy)), "want ErrAppBusy, got %v", err)
}

func TestASettingsOrEnvSaveIsRefusedWhileTheAppIsBusy(t *testing.T) {
	logger.LogInitConsoleOnly()
	dir := writeStack(t, map[string]string{"docker-compose.yml": handWrittenStack, ".env": "GREETING=hello\n"})
	app := &ComposeApp{Name: "jarvis", WorkingDir: dir, ComposeFiles: []string{filepath.Join(dir, "docker-compose.yml")}}
	holding(t, "jarvis")

	assertBusy(t, app.Apply(context.Background(), []byte(handWrittenStack)))
	assertBusy(t, app.ApplyEnv(context.Background(), []byte("GREETING=bye\n")))
}

func TestAnUpdateIsRefusedWhileTheAppIsBusy(t *testing.T) {
	app := &ComposeApp{Name: "jarvis", ComposeFiles: []string{"/nonexistent/docker-compose.yml"}}
	holding(t, "jarvis")

	assertBusy(t, app.Update(context.Background()))
	assertBusy(t, app.UpdateNow(context.Background()))
}

func TestAnInstallIsRefusedWhileTheAppIsBusy(t *testing.T) {
	holding(t, "jarvis")

	assertBusy(t, NewComposeService().Install(context.Background(), &ComposeApp{Name: "jarvis"}))
}

// An uninstall is refused before it touches anything. The name is one no real compose
// project has: an uninstall that went ahead would reach the daemon the tests run against.
func TestAnUninstallIsRefusedWhileTheAppIsBusy(t *testing.T) {
	holding(t, "casaos-test-uninstall")

	err := NewComposeService().Uninstall(context.Background(), &ComposeApp{Name: "casaos-test-uninstall"}, true)
	assert.Error(t, err, "`casaos-test-uninstall` is busy: deploy in progress")
}

func TestABackupThatHoldsTheAppStillIsRefusedWhileItIsBusy(t *testing.T) {
	logInTempDir(t)
	app, _ := appWithOneBindAndOneVolume(t)
	docker := &fakeBackupDocker{mountpoints: demoMountpoints()}
	holding(t, app.Name)

	_, err := RunBackup(context.Background(), app, docker, &fakeCopier{}, BackupOptions{
		Destination: "offsite", Stamp: restoreStamp, HoldStill: true, Containers: running("c1"),
	})
	assertBusy(t, err)
	assert.Equal(t, strings.Join(docker.stopped, ","), "", "nothing was stopped")
}

func TestARestoreIsRefusedWhileTheAppIsBusy(t *testing.T) {
	logInTempDir(t)
	app, _ := appWithOneBindAndOneVolume(t)
	restorer, _ := backupOf(t, app)
	holding(t, app.Name)

	_, err := RestoreBackup(context.Background(), app, &fakeBackupDocker{mountpoints: demoMountpoints()}, restorer, noInstall, RestoreOptions{
		Destination: "offsite", App: "demo", Stamp: restoreStamp, Containers: running("c1"),
	})
	assertBusy(t, err)
	assert.Equal(t, len(restorer.restored), 0)
}
```

Create `route/v2/app_operations_internal_test.go`:

```go
package v2

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

// An update, a save or an install refused because another operation holds the app is a
// conflict the dashboard can show, not a server error.
func TestAnOperationRefusedForABusyAppIsAConflict(t *testing.T) {
	answer := func(err error) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPut, "/", nil), recorder)
		assert.NilError(t, conflictOrServerError(ctx, err))

		return recorder
	}

	busy := answer(service.ErrAppBusy{App: "jarvis", Running: "deploy"})
	assert.Equal(t, busy.Code, http.StatusConflict)
	assert.Equal(t, busy.Body.String(), "{\"message\":\"`jarvis` is busy: deploy in progress\"}\n")

	assert.Equal(t, answer(errors.New("disk full")).Code, http.StatusInternalServerError)
}
```

- [ ] **Step 2: Run them to see them fail**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go test ./service/ ./route/v2/ -count=1 -run 'Busy|TestBeginRefuses|TestAnOperationRefused'" 2>&1 | Select-Object -Last 40
```

Expected: FAIL, `undefined: Begin`, `undefined: ErrAppBusy` and `undefined: conflictOrServerError`.

- [ ] **Step 3: Write the registry**

Create `service/app_operations.go`:

```go
package service

import (
	"fmt"
	"sync"
)

// One operation per app.
//
// Installs, updates, applies and backup holds each keep a marker of their own for their
// own readers, and none of them looks at the others': a settings save could land in the
// middle of a deployment that is switching the app's containers. This is the one place
// that refuses an overlap. The markers stay for what reads them.

// ErrAppBusy is the refusal, naming what holds the app. The API answers it with 409.
type ErrAppBusy struct {
	App     string
	Running string
}

func (e ErrAppBusy) Error() string {
	return fmt.Sprintf("`%s` is busy: %s in progress", e.App, e.Running)
}

var appOperations = struct {
	sync.Mutex
	running map[string]string
}{running: map[string]string{}}

// Begin claims app for an operation of the given kind, or refuses with ErrAppBusy. end
// releases it; calling it more than once is harmless.
func Begin(app, kind string) (end func(), err error) {
	appOperations.Lock()
	defer appOperations.Unlock()

	if running, ok := appOperations.running[app]; ok {
		return nil, ErrAppBusy{App: app, Running: running}
	}
	appOperations.running[app] = kind

	var once sync.Once

	return func() {
		once.Do(func() {
			appOperations.Lock()
			defer appOperations.Unlock()
			delete(appOperations.running, app)
		})
	}, nil
}
```

- [ ] **Step 4: Hold the app in every caller the design lists, and in an uninstall**

In `service/compose_app.go`, replace:

```go
func (a *ComposeApp) Update(ctx context.Context) error {
	newComposeYAML, ctx, err := a.prepareUpdate(ctx)
	if err != nil {
		return err
	}

	go func() { _ = a.applyUpdate(ctx, newComposeYAML) }()

	return nil
}
```

with:

```go
func (a *ComposeApp) Update(ctx context.Context) error {
	end, err := Begin(a.Name, "update")
	if err != nil {
		return err
	}

	newComposeYAML, ctx, err := a.prepareUpdate(ctx)
	if err != nil {
		end()
		return err
	}

	go func() {
		defer end()
		_ = a.applyUpdate(ctx, newComposeYAML)
	}()

	return nil
}
```

In `service/compose_app.go`, replace:

```go
func (a *ComposeApp) UpdateNow(ctx context.Context) error {
	newComposeYAML, ctx, err := a.prepareUpdate(ctx)
```

with:

```go
func (a *ComposeApp) UpdateNow(ctx context.Context) error {
	end, err := Begin(a.Name, "update")
	if err != nil {
		return err
	}
	defer end()

	newComposeYAML, ctx, err := a.prepareUpdate(ctx)
```

In `service/compose_app.go`, replace:

```go
	common.SetProperties(ctx, eventProperties)

	go func(ctx context.Context) {
		go PublishEventWrapper(ctx, common.EventTypeAppApplyChangesBegin, nil)
```

with:

```go
	common.SetProperties(ctx, eventProperties)

	end, err := Begin(a.Name, "save")
	if err != nil {
		return err
	}

	go func(ctx context.Context) {
		defer end()

		go PublishEventWrapper(ctx, common.EventTypeAppApplyChangesBegin, nil)
```

In `service/compose_service.go`, replace:

```go
func (s *ComposeService) Install(ctx context.Context, composeApp *ComposeApp) error {
	// set store_app_id (by convention is the same as app name at install time if it does not exist)
```

with:

```go
func (s *ComposeService) Install(ctx context.Context, composeApp *ComposeApp) error {
	end, err := Begin(composeApp.Name, "install")
	if err != nil {
		return err
	}
	// released here on every early return, and by the install itself once it runs
	handedOff := false
	defer func() {
		if !handedOff {
			end()
		}
	}()

	// set store_app_id (by convention is the same as app name at install time if it does not exist)
```

In `service/compose_service.go`, replace:

```go
	common.SetProperties(ctx, eventProperties)

	go func(ctx context.Context) {
		s.installationInProgress.Store(composeApp.Name, true)
```

with:

```go
	common.SetProperties(ctx, eventProperties)

	handedOff = true
	go func(ctx context.Context) {
		defer end()

		s.installationInProgress.Store(composeApp.Name, true)
```

In `service/compose_service.go`, replace:

```go
func (s *ComposeService) Uninstall(ctx context.Context, composeApp *ComposeApp, deleteConfigFolder bool) error {
	// prepare for message bus events
```

with:

```go
func (s *ComposeService) Uninstall(ctx context.Context, composeApp *ComposeApp, deleteConfigFolder bool) error {
	// released by the uninstall itself once it runs
	end, err := Begin(composeApp.Name, "uninstall")
	if err != nil {
		return err
	}

	// prepare for message bus events
```

In `service/compose_service.go`, replace:

```go
	go func(ctx context.Context) {
		go PublishEventWrapper(ctx, common.EventTypeAppUninstallBegin, nil)
```

with:

```go
	go func(ctx context.Context) {
		defer end()

		go PublishEventWrapper(ctx, common.EventTypeAppUninstallBegin, nil)
```

In `service/backup_run.go`, replace:

```go
	if opts.HoldStill {
		listContainers := opts.Containers
```

with:

```go
	if opts.HoldStill {
		end, err := Begin(app.Name, "backup")
		if err != nil {
			return manifest, err
		}
		defer end()

		listContainers := opts.Containers
```

In `service/backup_restore.go`, replace:

```go
		return report, fmt.Errorf("the backup at %s is of `%s`, not `%s`", root, manifest.App, opts.App)
	}
	report.Skipped = append(report.Skipped, manifest.Skipped...)
```

with:

```go
		return report, fmt.Errorf("the backup at %s is of `%s`, not `%s`", root, manifest.App, opts.App)
	}

	end, err := Begin(opts.App, "restore")
	if err != nil {
		return report, err
	}
	defer end()

	report.Skipped = append(report.Skipped, manifest.Skipped...)
```

An uninstall is not in the design's list; it holds the app too, so that it cannot remove an app a deployment, an update or a backup is working on. The update-all run goes through `UpdateNow`, and `ApplyEnv` through `apply`, so both are covered. `installFromBackup` in `route/v2/backup.go` runs inside the restore's hold and takes none of its own.

- [ ] **Step 5: Answer a busy app with 409**

In `route/v2/compose_app.go`, replace:

```go
// installedComposeApp resolves id to an installed app, or writes the error response and returns nil.
```

with:

```go
// conflictOrServerError answers an operation that did not start: 409 naming the operation
// that holds the app, 500 for anything else.
func conflictOrServerError(ctx echo.Context, err error) error {
	message := err.Error()
	if errors.As(err, new(service.ErrAppBusy)) {
		return ctx.JSON(http.StatusConflict, codegen.ResponseConflict{Message: &message})
	}

	return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
}

// installedComposeApp resolves id to an installed app, or writes the error response and returns nil.
```

In `route/v2/compose_app.go`, replace:

```go
	if err := composeApp.Apply(backgroundCtx, buf); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{
			Message: &message,
		})
	}
```

with:

```go
	if err := composeApp.Apply(backgroundCtx, buf); err != nil {
		return conflictOrServerError(ctx, err)
	}
```

In `route/v2/compose_app.go`, replace:

```go
	if err := composeApp.ApplyEnv(backgroundCtx, body); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}
```

with:

```go
	if err := composeApp.ApplyEnv(backgroundCtx, body); err != nil {
		return conflictOrServerError(ctx, err)
	}
```

In `route/v2/compose_app.go`, replace:

```go
		message := err.Error()
		if err == service.ErrComposeExtensionNameXCasaOSNotFound {
			return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
		}

		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}
```

with:

```go
		message := err.Error()
		if err == service.ErrComposeExtensionNameXCasaOSNotFound {
			return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
		}

		return conflictOrServerError(ctx, err)
	}
```

In `route/v2/compose_app.go`, replace:

```go
	if err := service.MyService.Compose().Uninstall(backgroundCtx, composeApp, deleteConfigFolder); err != nil {
		logger.Error("failed to uninstall compose app", zap.Error(err), zap.String("appID", id))
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}
```

with:

```go
	if err := service.MyService.Compose().Uninstall(backgroundCtx, composeApp, deleteConfigFolder); err != nil {
		logger.Error("failed to uninstall compose app", zap.Error(err), zap.String("appID", id))
		return conflictOrServerError(ctx, err)
	}
```

In `route/v2/compose_app.go`, replace:

```go
	if err := composeApp.Update(backgroundCtx); err != nil {
		logger.Error("failed to update compose app", zap.Error(err), zap.String("appID", id))
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}
```

with:

```go
	if err := composeApp.Update(backgroundCtx); err != nil {
		logger.Error("failed to update compose app", zap.Error(err), zap.String("appID", id))
		return conflictOrServerError(ctx, err)
	}
```

- [ ] **Step 6: Declare the 409s**

In `api/app_management/openapi.yaml`, replace:

```yaml
      requestBody:
        $ref: "#/components/requestBodies/RequestComposeApp"
      responses:
        "200":
          $ref: "#/components/responses/ComposeAppInstallOK"
        "400":
          $ref: "#/components/responses/ComposeAppBadRequest"
        "500":
```

with:

```yaml
      requestBody:
        $ref: "#/components/requestBodies/RequestComposeApp"
      responses:
        "200":
          $ref: "#/components/responses/ComposeAppInstallOK"
        "400":
          $ref: "#/components/responses/ComposeAppBadRequest"
        "409":
          $ref: "#/components/responses/ResponseConflict"
        "500":
```

In `api/app_management/openapi.yaml`, replace:

```yaml
        "200":
          $ref: "#/components/responses/ComposeAppUpdateSettingsOK"
        "400":
          $ref: "#/components/responses/ComposeAppBadRequest"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "500":
```

with:

```yaml
        "200":
          $ref: "#/components/responses/ComposeAppUpdateSettingsOK"
        "400":
          $ref: "#/components/responses/ComposeAppBadRequest"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "409":
          $ref: "#/components/responses/ResponseConflict"
        "500":
```

In `api/app_management/openapi.yaml`, replace:

```yaml
        "200":
          $ref: "#/components/responses/ComposeAppUpdateOK"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "500":
```

with:

```yaml
        "200":
          $ref: "#/components/responses/ComposeAppUpdateOK"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "409":
          $ref: "#/components/responses/ResponseConflict"
        "500":
```

In `api/app_management/openapi.yaml`, replace:

```yaml
        "200":
          $ref: "#/components/responses/ComposeAppUninstallOK"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "500":
```

with:

```yaml
        "200":
          $ref: "#/components/responses/ComposeAppUninstallOK"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "409":
          $ref: "#/components/responses/ResponseConflict"
        "500":
```

In `api/app_management/openapi.yaml`, replace:

```yaml
        "200":
          $ref: "#/components/responses/ComposeAppUpdateSettingsOK"
        "400":
          $ref: "#/components/responses/ResponseBadRequest"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "500":
```

with:

```yaml
        "200":
          $ref: "#/components/responses/ComposeAppUpdateSettingsOK"
        "400":
          $ref: "#/components/responses/ResponseBadRequest"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "409":
          $ref: "#/components/responses/ResponseConflict"
        "500":
```

The five blocks are, in order, `POST /compose`, `PUT /compose/{id}`, `PATCH /compose/{id}`, `DELETE /compose/{id}` and `PUT /compose/{id}/env`.

- [ ] **Step 7: Regenerate and build**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go generate ./... && grep -cP '^\t[A-Z]\w* +\w+ = \x22' codegen/app_management_api.go && go build ./... && echo build-ok" 2>&1 | Select-Object -Last 40
```

Expected: `34`, then `build-ok`. Another count means an enum constant was renamed: stop and look at `codegen/app_management_api.go`.

- [ ] **Step 8: Run the tests**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go vet ./service/ ./route/... && go test ./service/ ./route/v2/ -count=1 -run 'Busy|TestBeginRefuses|TestAnOperationRefused' -v" 2>&1 | Select-Object -Last 40
```

Expected: `--- PASS` for TestBeginRefusesASecondOperationOnTheSameApp, TestASettingsOrEnvSaveIsRefusedWhileTheAppIsBusy, TestAnUpdateIsRefusedWhileTheAppIsBusy, TestAnInstallIsRefusedWhileTheAppIsBusy, TestAnUninstallIsRefusedWhileTheAppIsBusy, TestABackupThatHoldsTheAppStillIsRefusedWhileItIsBusy, TestARestoreIsRefusedWhileTheAppIsBusy and TestAnOperationRefusedForABusyAppIsAConflict; `ok` for both packages.

- [ ] **Step 9: Run the packages whole**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go test ./service/ ./route/... -count=1" 2>&1 | Select-Object -Last 40
```

Expected: `ok` for `route`, `route/v2` and `service`: the backup, restore, install and update tests still pass with the hold in place.

- [ ] **Step 10: Check the formatting**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "tr -d '\r' < service/app_operations.go | gofmt -l; tr -d '\r' < service/app_operations_internal_test.go | gofmt -l; tr -d '\r' < route/v2/app_operations_internal_test.go | gofmt -l; tr -d '\r' < service/compose_app.go | gofmt -l; tr -d '\r' < service/compose_service.go | gofmt -l; tr -d '\r' < service/backup_run.go | gofmt -l; tr -d '\r' < service/backup_restore.go | gofmt -l; tr -d '\r' < route/v2/compose_app.go | gofmt -l" 2>&1 | Select-Object -Last 40
```

Expected: no output.

- [ ] **Step 11: Commit**

```powershell
git -C D:\clients\casaos\CasaOS-AppManagement add service/app_operations.go service/app_operations_internal_test.go route/v2/app_operations_internal_test.go service/compose_app.go service/compose_service.go service/backup_run.go service/backup_restore.go route/v2/compose_app.go api/app_management/openapi.yaml
git -C D:\clients\casaos\CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): one operation at a time per app, refused with 409"
```

---

### Task 4: What a git app needs of Docker

**Files:**
- Create: `service/git_runtime.go`
- Test: `service/git_runtime_internal_test.go`

**Interfaces:**
- Consumes: `apiService()`, `LoadComposeAppFromConfigFile`, `ComposeApp.Pull`, `ComposeApp.UpWithCheckRequire`, `ComposeApp.Containers`, `errUpNotConfirmed`, `baseInterpolationMap()`, `sortedServiceNames`, `pathExists` (all in package `service`).
- Produces, for Tasks 6 to 13:
  - `var ErrGitNoComposeFile error`
  - `func gitComposeFiles(root string) ([]string, error)` (absolute paths: the first of `compose.yaml`, `compose.yml`, `docker-compose.yaml`, `docker-compose.yml`, plus its `.override` counterpart)
  - `func gitImageTag(image, commit string) (string, error)` (`jarvis-web` + commit gives `jarvis-web:git-<12 hex>`)
  - `func builtServiceNames(services types.Services) []string`
  - `func loadGitProject(ctx context.Context, app, root, envFile string) (*types.Project, error)`
  - `func failedBuiltContainer(containers map[string][]api.ContainerSummary, built []string) error`
  - `type gitRuntime interface` with `Projects(ctx) (map[string]string, error)`, `Build(ctx, app, root, envFile, commit string, out io.Writer) (map[string]string, error)`, `Retag(ctx, app, dir, commit string) error`, `TagRunning(ctx, app, dir, commit string) (map[string]string, error)`, `Start(ctx, app, dir string) error`, `Stop(ctx, app string) error`, `Remove(ctx, app string) error`, `ImagesExist(ctx, images map[string]string) bool`, `RemoveImages(ctx, images []string)`
  - `var gitDocker gitRuntime = composeGitRuntime{}` (tests replace it); `var gitSettleDelay = 10 * time.Second`

- [ ] **Step 1: Write the failing tests**

Create `service/git_runtime_internal_test.go`:

```go
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
```

The real runtime is exercised against the daemon by Task 13; these cover what decides without one.

- [ ] **Step 2: Run them to see them fail**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go test ./service/ -count=1 -run 'TestGitComposeFiles|TestGitImageTag|TestLoadGitProject|TestFailedBuiltContainer'" 2>&1 | Select-Object -Last 40
```

Expected: FAIL, `undefined: gitComposeFiles` and the others.

- [ ] **Step 3: Write the runtime**

Create `service/git_runtime.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/cmd/display"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/client"
	"go.uber.org/zap"
)

// What a git app needs of Docker, behind one interface so that a deployment's decisions
// are tested without a daemon. composeGitRuntime is the real thing; the integration test
// drives it.

// ErrGitNoComposeFile is a repository `docker compose up` would refuse at its root.
var ErrGitNoComposeFile = errors.New("the repository has no compose.yaml, compose.yml, docker-compose.yaml or docker-compose.yml at its root")

// gitComposeFileNames are the files `docker compose up` looks for, in its order.
var gitComposeFileNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// gitComposeFiles is what `docker compose up` reads at root: the first compose file found
// and its .override counterpart when there is one.
func gitComposeFiles(root string) ([]string, error) {
	for _, name := range gitComposeFileNames {
		main := filepath.Join(root, name)
		if _, err := os.Stat(main); err != nil {
			continue
		}

		files := []string{main}
		ext := filepath.Ext(name)
		if override := filepath.Join(root, strings.TrimSuffix(name, ext)+".override"+ext); pathExists(override) {
			files = append(files, override)
		}

		return files, nil
	}

	return nil, ErrGitNoComposeFile
}

// gitImageTag is where a deployment keeps the image of a service: the repository of the
// name compose uses, tagged git- and the commit's first 12 hex digits.
func gitImageTag(image, commit string) (string, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", err
	}

	return reference.FamiliarName(named) + ":git-" + commit[:12], nil
}

// builtServiceNames is every service with a build section, sorted.
func builtServiceNames(services types.Services) []string {
	names := []string{}
	for _, name := range sortedServiceNames(services) {
		if services[name].Build != nil {
			names = append(names, name)
		}
	}

	return names
}

// loadGitProject loads the compose files at root under the app's name, interpolated from
// envFile when it exists. The environment of the services is not resolved: a worktree
// has no untracked file, so an `env_file: .env` would fail to load there, and nothing a
// build or a tag needs comes from it.
func loadGitProject(ctx context.Context, app, root, envFile string) (*types.Project, error) {
	files, err := gitComposeFiles(root)
	if err != nil {
		return nil, err
	}

	env := []string{"AppID=" + app}
	for k, v := range baseInterpolationMap() {
		env = append(env, k+"="+v)
	}

	fns := []cli.ProjectOptionsFn{cli.WithWorkingDirectory(root), cli.WithOsEnv}
	if pathExists(envFile) {
		fns = append(fns, cli.WithEnvFiles(envFile), cli.WithDotEnv)
	}
	fns = append(fns, cli.WithEnv(env), cli.WithName(app), cli.WithoutEnvironmentResolution)

	options, err := cli.NewProjectOptions(files, fns...)
	if err != nil {
		return nil, err
	}

	return options.LoadProject(ctx)
}

// failedBuiltContainer is the stricter judgement a deployment gets: a container of a
// built service that exited with an error, is restarting or reports itself unhealthy
// fails it, even when the start's own wait only ran out of time.
func failedBuiltContainer(containers map[string][]api.ContainerSummary, built []string) error {
	for _, name := range built {
		for _, c := range containers[name] {
			switch {
			case c.State == "restarting":
				return fmt.Errorf("container %s of %s is restarting", c.Name, name)
			case c.State == "exited" && c.ExitCode != 0:
				return fmt.Errorf("container %s of %s exited with code %d", c.Name, name, c.ExitCode)
			case strings.EqualFold(string(c.Health), "unhealthy"):
				return fmt.Errorf("container %s of %s is unhealthy", c.Name, name)
			}
		}
	}

	return nil
}

type gitRuntime interface {
	// Projects is every compose project on this machine, loadable or not, with the folder
	// of its first compose file.
	Projects(ctx context.Context) (map[string]string, error)
	// Build builds the services with a build section of the project at root, each under
	// its git- tag for commit, and returns the tag of each service.
	Build(ctx context.Context, app, root, envFile, commit string, out io.Writer) (map[string]string, error)
	// Retag points the names compose uses for the built services of the project at dir at
	// the images tagged for commit.
	Retag(ctx context.Context, app, dir, commit string) error
	// TagRunning tags, for commit, the images the containers of the built services of the
	// project at dir were created from.
	TagRunning(ctx context.Context, app, dir, commit string) (map[string]string, error)
	// Start pulls what is not built, creates and starts the app from dir through the path
	// every app takes, and judges its built services.
	Start(ctx context.Context, app, dir string) error
	// Stop stops the app's containers.
	Stop(ctx context.Context, app string) error
	// Remove removes the app's containers and networks, and keeps its volumes.
	Remove(ctx context.Context, app string) error
	// ImagesExist reports whether every image is still there.
	ImagesExist(ctx context.Context, images map[string]string) bool
	// RemoveImages removes the tags no container uses.
	RemoveImages(ctx context.Context, images []string)
}

// gitDocker is the runtime in use; tests replace it.
var gitDocker gitRuntime = composeGitRuntime{}

// gitSettleDelay is how long a started app runs before its built services are judged: a
// container that crashes on start is `running` for a moment first. A var for the tests.
var gitSettleDelay = 10 * time.Second

type composeGitRuntime struct{}

func (composeGitRuntime) Projects(ctx context.Context) (map[string]string, error) {
	backend, dockerClient, err := apiService()
	if err != nil {
		return nil, err
	}
	defer dockerClient.Close()

	stacks, err := backend.List(ctx, api.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	projects := map[string]string{}
	for _, stack := range stacks {
		projects[stack.ID] = ""
		if first, _, _ := strings.Cut(stack.ConfigFiles, ","); strings.TrimSpace(first) != "" {
			projects[stack.ID] = filepath.Dir(strings.TrimSpace(first))
		}
	}

	return projects, nil
}

func (composeGitRuntime) Build(ctx context.Context, app, root, envFile, commit string, out io.Writer) (map[string]string, error) {
	project, err := loadGitProject(ctx, app, root, envFile)
	if err != nil {
		return nil, err
	}

	images := map[string]string{}
	built := builtServiceNames(project.Services)
	for _, name := range built {
		service := project.Services[name]
		tag, err := gitImageTag(api.GetImageNameOrDefault(service, app), commit)
		if err != nil {
			return nil, err
		}

		// under this commit's tag, never under the name the running app uses: a build
		// that fails half-way leaves that name where it was
		service.Image = tag
		project.Services[name] = service
		images[name] = tag
	}
	if len(built) == 0 {
		return images, nil
	}

	// the build log goes where the caller says, not to AppManagement's own output
	dockerCli, err := command.NewDockerCli(command.WithOutputStream(out), command.WithErrorStream(out))
	if err != nil {
		return nil, err
	}
	if err := dockerCli.Initialize(&flags.ClientOptions{}); err != nil {
		return nil, err
	}
	defer dockerCli.Client().Close()

	backend, err := compose.NewComposeService(dockerCli, compose.WithEventProcessor(display.Plain(out)))
	if err != nil {
		return nil, err
	}

	// buildx when the host has it, the daemon's builder otherwise (compose decides)
	if err := backend.Build(ctx, project, api.BuildOptions{Services: built, Progress: "plain", Out: out}); err != nil {
		return nil, err
	}

	return images, nil
}

func (composeGitRuntime) Retag(ctx context.Context, app, dir, commit string) error {
	project, err := loadGitProject(ctx, app, dir, filepath.Join(dir, ".env"))
	if err != nil {
		return err
	}

	_, dockerClient, err := apiService()
	if err != nil {
		return err
	}
	defer dockerClient.Close()

	for _, name := range builtServiceNames(project.Services) {
		target := api.GetImageNameOrDefault(project.Services[name], app)
		source, err := gitImageTag(target, commit)
		if err != nil {
			return err
		}

		if _, err := dockerClient.ImageTag(ctx, client.ImageTagOptions{Source: source, Target: target}); err != nil {
			return fmt.Errorf("the image of %s at %s: %w", name, commit[:12], err)
		}
	}

	return nil
}

func (composeGitRuntime) TagRunning(ctx context.Context, app, dir, commit string) (map[string]string, error) {
	project, err := loadGitProject(ctx, app, dir, filepath.Join(dir, ".env"))
	if err != nil {
		return nil, err
	}

	_, dockerClient, err := apiService()
	if err != nil {
		return nil, err
	}
	defer dockerClient.Close()

	containers, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", api.ProjectLabel+"="+app),
	})
	if err != nil {
		return nil, err
	}

	images := map[string]string{}
	for _, name := range builtServiceNames(project.Services) {
		for _, c := range containers.Items {
			if c.Labels[api.ServiceLabel] != name || c.Labels[api.OneoffLabel] == "True" {
				continue
			}

			tag, err := gitImageTag(api.GetImageNameOrDefault(project.Services[name], app), commit)
			if err != nil {
				return nil, err
			}
			// by ID: the name may already point at an image built since the container was made
			if _, err := dockerClient.ImageTag(ctx, client.ImageTagOptions{Source: c.ImageID, Target: tag}); err != nil {
				return nil, err
			}
			images[name] = tag

			break
		}
	}

	return images, nil
}

func (composeGitRuntime) Start(ctx context.Context, app, dir string) error {
	files, err := gitComposeFiles(dir)
	if err != nil {
		return err
	}

	composeApp, err := LoadComposeAppFromConfigFile(app, strings.Join(files, ","))
	if err != nil {
		return err
	}

	backend, dockerClient, err := apiService()
	if err != nil {
		return err
	}
	defer dockerClient.Close()

	if err := composeApp.Pull(ctx); err != nil {
		return err
	}

	// A wait that only ran out of time goes on to the judgement below, like a start that
	// confirmed: keepNewDefinition keeps such a definition for every other apply, and a
	// deployment is judged by its built services instead.
	if err := composeApp.UpWithCheckRequire(ctx, backend); err != nil && !errors.Is(err, errUpNotConfirmed) {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(gitSettleDelay):
	}

	containers, err := composeApp.Containers(ctx)
	if err != nil {
		return err
	}

	return failedBuiltContainer(containers, builtServiceNames(composeApp.Services))
}

func (composeGitRuntime) Stop(ctx context.Context, app string) error {
	backend, dockerClient, err := apiService()
	if err != nil {
		return err
	}
	defer dockerClient.Close()

	return backend.Stop(ctx, app, api.StopOptions{})
}

func (composeGitRuntime) Remove(ctx context.Context, app string) error {
	backend, dockerClient, err := apiService()
	if err != nil {
		return err
	}
	defer dockerClient.Close()

	return backend.Down(ctx, app, api.DownOptions{RemoveOrphans: true})
}

func (composeGitRuntime) ImagesExist(ctx context.Context, images map[string]string) bool {
	_, dockerClient, err := apiService()
	if err != nil {
		return false
	}
	defer dockerClient.Close()

	for _, image := range images {
		if _, err := dockerClient.ImageInspect(ctx, image); err != nil {
			return false
		}
	}

	return true
}

func (composeGitRuntime) RemoveImages(ctx context.Context, images []string) {
	_, dockerClient, err := apiService()
	if err != nil {
		logger.Error("cannot remove the images of old deployments", zap.Error(err))
		return
	}
	defer dockerClient.Close()

	for _, image := range images {
		used, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{
			All:     true,
			Filters: make(client.Filters).Add("ancestor", image),
		})
		if err != nil || len(used.Items) > 0 {
			continue
		}

		if _, err := dockerClient.ImageRemove(ctx, image, client.ImageRemoveOptions{}); err != nil {
			logger.Info("the image of an old deployment stays", zap.String("image", image), zap.Error(err))
		}
	}
}
```

- [ ] **Step 4: Run the tests**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go vet ./service/ && go test ./service/ -count=1 -run 'TestGitComposeFiles|TestGitImageTag|TestLoadGitProject|TestFailedBuiltContainer' -v" 2>&1 | Select-Object -Last 40
```

Expected: `--- PASS` for TestGitComposeFilesReadsWhatComposeUpReads, TestGitImageTagKeepsTheRepositoryOfTheName, TestLoadGitProjectInAWorktreeWithoutItsEnvFile and TestFailedBuiltContainerJudgesOnlyWhatWasBuilt.

- [ ] **Step 5: Check the formatting**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "tr -d '\r' < service/git_runtime.go | gofmt -l; tr -d '\r' < service/git_runtime_internal_test.go | gofmt -l" 2>&1 | Select-Object -Last 40
```

Expected: no output.

- [ ] **Step 6: Commit**

```powershell
git -C D:\clients\casaos\CasaOS-AppManagement add service/git_runtime.go service/git_runtime_internal_test.go
git -C D:\clients\casaos\CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): build under a git tag, retag, start and judge through compose"
```

---

### Task 5: The state of a git app and its secrets

**Files:**
- Create: `service/git_app_state.go`
- Modify: `go.mod:216` (`golang.org/x/crypto` becomes a direct dependency)
- Test: `service/git_app_state_internal_test.go`

**Interfaces:**
- Consumes: `pkg/git` (Task 2), `pathExists`.
- Produces, for Tasks 6 to 13:
  - `var gitAppsDir = "/var/lib/casaos/git_apps"`, `var gitAppsDataRoot = "/DATA/AppData"`
  - constants `gitOriginCreated|Adopted|Adoptable`, `gitOutcomeAdopted|Deployed|BuildFailed|RolledBack|Failed|Interrupted`, `gitOperationCheck|Build|Deploy|Revert` (their string values are the contract's)
  - `var ErrGitAppNotFound error`
  - `type gitApp struct{ App, Origin, Dir, Remote, Branch, Access string; AutoDeploy, AutoPaused, Blocked, EnvTracked, Cloned, NoComposeFile bool; Deployed *gitDeployment; Check *gitCheck; Operation *gitOperation; History []gitHistoryEntry; Attempted []string }`
  - `type gitDeployment struct{ Commit, Subject string; At time.Time; Images map[string]string }`, `type gitCheck struct{ At time.Time; RemoteCommit, Error string }`, `type gitOperation struct{ Kind, Commit string; StartedAt time.Time }`, `type gitHistoryEntry struct{ Commit, Subject string; At time.Time; Outcome, Reason string; Images map[string]string }`
  - `func gitAppFile(app, suffix string) string`, `func loadGitApp(app string) (*gitApp, error)`, `func saveGitApp(st *gitApp) error`, `func GitAppNames() ([]string, error)`, `func forgetGitApp(app string) error`, `func writeGitFile(path string, data []byte) error`
  - `func generateGitKey(app string) (string, error)`, `func gitAuth(st *gitApp) (git.Auth, error)` (with a token, `AskpassDir` is `gitAppsDir`)
  - `func (st *gitApp) record(entry gitHistoryEntry) []string` (removable images), `func (st *gitApp) attempt(commit string)`, `func (st *gitApp) historyEntry(commit string) *gitHistoryEntry`
  - `func gitURLKind(raw string) string` (`http`, `ssh`, `file` or empty), `func redactGitURL(raw string) string`, `func validGitBranch(branch string) bool`
- Test helper produced: `gitAppsIn(t)`, which points `gitAppsDir` and `gitAppsDataRoot` at temporary folders.

- [ ] **Step 1: Write the failing tests**

Create `service/git_app_state_internal_test.go`:

```go
package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"golang.org/x/crypto/ssh"
	"gotest.tools/v3/assert"
)

// gitAppsIn points the git apps' state, secrets and created clones at a test's folders.
func gitAppsIn(t *testing.T) {
	t.Helper()

	dir, data := gitAppsDir, gitAppsDataRoot
	gitAppsDir = filepath.Join(t.TempDir(), "git_apps")
	gitAppsDataRoot = t.TempDir()
	t.Cleanup(func() { gitAppsDir, gitAppsDataRoot = dir, data })
}

func TestAGitAppIsKeptInAFileOnlyRootReads(t *testing.T) {
	gitAppsIn(t)

	st := &gitApp{App: "jarvis", Origin: gitOriginCreated, Dir: "/DATA/AppData/jarvis", Remote: "https://example.invalid/jarvis.git", Branch: "main", Access: "none"}
	assert.NilError(t, saveGitApp(st))

	loaded, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, loaded, st)

	info, err := os.Stat(gitAppFile("jarvis", ".json"))
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))
	folder, err := os.Stat(gitAppsDir)
	assert.NilError(t, err)
	assert.Equal(t, folder.Mode().Perm(), os.FileMode(0o700))

	names, err := GitAppNames()
	assert.NilError(t, err)
	assert.DeepEqual(t, names, []string{"jarvis"})

	_, err = loadGitApp("nextcloud")
	assert.ErrorIs(t, err, ErrGitAppNotFound)

	assert.NilError(t, forgetGitApp("jarvis"))
	names, err = GitAppNames()
	assert.NilError(t, err)
	assert.DeepEqual(t, names, []string{})
}

// A token reaches git through a helper written beside the state, which tests point
// elsewhere, never under /tmp.
func TestAGitAppWithATokenHasItsAskpassHelperBesideTheState(t *testing.T) {
	gitAppsIn(t)
	assert.NilError(t, writeGitFile(gitAppFile("jarvis", ".token"), []byte("ghp_secret\n")))

	auth, err := gitAuth(&gitApp{App: "jarvis", Access: git.AccessToken})
	assert.NilError(t, err)
	assert.DeepEqual(t, auth, git.Auth{Mode: git.AccessToken, Token: "ghp_secret", AskpassDir: gitAppsDir})
}

func TestADeployKeyIsEd25519WithItsPublicHalfCommented(t *testing.T) {
	gitAppsIn(t)

	public, err := generateGitKey("jarvis")
	assert.NilError(t, err)
	assert.Assert(t, strings.HasPrefix(public, "ssh-ed25519 AAAA"), public)
	assert.Assert(t, strings.HasSuffix(public, " casaos-jarvis"), public)

	private, err := os.ReadFile(gitAppFile("jarvis", ".key"))
	assert.NilError(t, err)
	signer, err := ssh.ParsePrivateKey(private)
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))+" casaos-jarvis", public)

	for _, suffix := range []string{".key", ".key.pub", ".known_hosts"} {
		info, err := os.Stat(gitAppFile("jarvis", suffix))
		assert.NilError(t, err, suffix)
		assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600), suffix)
	}
}

func TestHistoryKeepsThreeAndGivesBackWhatNothingNames(t *testing.T) {
	entry := func(commit, outcome string, images map[string]string) gitHistoryEntry {
		return gitHistoryEntry{Commit: commit, Outcome: outcome, At: time.Now(), Images: images}
	}
	st := &gitApp{Deployed: &gitDeployment{Commit: "a", Images: map[string]string{"web": "jarvis-web:git-a"}}}

	assert.Assert(t, len(st.record(entry("a", gitOutcomeDeployed, map[string]string{"web": "jarvis-web:git-a"}))) == 0)
	assert.Assert(t, len(st.record(entry("b", gitOutcomeRolledBack, map[string]string{"web": "jarvis-web:git-b"}))) == 0)
	assert.Assert(t, len(st.record(entry("c", gitOutcomeBuildFailed, nil))) == 0)

	// a leaves the history, but it is what runs
	assert.DeepEqual(t, st.record(entry("d", gitOutcomeBuildFailed, nil)), []string{})
	// b leaves it, and nothing names its image any more
	assert.DeepEqual(t, st.record(entry("e", gitOutcomeBuildFailed, nil)), []string{"jarvis-web:git-b"})
	assert.Equal(t, len(st.History), 3)
	assert.Equal(t, st.History[0].Commit, "e")
}

func TestAttemptedCommitsAreBoundedAndUnique(t *testing.T) {
	st := &gitApp{}
	for i := 0; i < 25; i++ {
		st.attempt(strings.Repeat(string(rune('a'+i)), 40))
	}
	st.attempt(strings.Repeat("y", 40))

	assert.Equal(t, len(st.Attempted), gitAttemptedLength)
	assert.Equal(t, st.Attempted[0], strings.Repeat("f", 40))
	assert.Equal(t, st.Attempted[gitAttemptedLength-1], strings.Repeat("y", 40))
}

func TestAGitURLIsOneGitCanBeGivenSafely(t *testing.T) {
	for raw, kind := range map[string]string{
		"https://github.com/owner/jarvis.git":      "http",
		"http://gitea.local:3000/owner/jarvis.git": "http",
		"git@github.com:owner/jarvis.git":          "ssh",
		"ssh://git@gitea.local:2222/owner/j.git":   "ssh",
		"file:///opt/jarvis.git":                   "file",
		"https://owner:hunter2@github.com/j.git":   "",
		"--upload-pack=touch /tmp/pwned":           "",
		"ext::sh -c touch% /tmp/pwned":             "",
		"ftp://example.com/jarvis.git":             "",
		"":                                         "",
	} {
		assert.Equal(t, gitURLKind(raw), kind, raw)
	}
	assert.Equal(t, redactGitURL("https://owner:hunter2@github.com/j.git"), "https://owner@github.com/j.git")
}
```

- [ ] **Step 2: Run them to see them fail**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go test ./service/ -count=1 -run 'TestAGitApp|TestADeployKey|TestHistoryKeeps|TestAttempted|TestAGitURL'" 2>&1 | Select-Object -Last 40
```

Expected: FAIL, the test build stops on `undefined: gitAppsDir`, `undefined: gitApp` and the others.

- [ ] **Step 3: Write the state**

Create `service/git_app_state.go`:

```go
package service

import (
	"crypto/ed25519"
	"crypto/rand"
	stdjson "encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/samber/lo"
	"golang.org/x/crypto/ssh"
)

// What CasaOS remembers of an app deployed from its git repository: one JSON file per
// app, and its secrets beside it, in a folder only root reads.

var (
	// gitAppsDir holds the state and the secrets. Vars, so tests can point them elsewhere.
	gitAppsDir = "/var/lib/casaos/git_apps"
	// gitAppsDataRoot is where a created app is cloned.
	gitAppsDataRoot = "/DATA/AppData"
)

const (
	gitHistoryLength   = 3
	gitAttemptedLength = 20
)

// Origins of a git app.
const (
	gitOriginCreated   = "created"
	gitOriginAdopted   = "adopted"
	gitOriginAdoptable = "adoptable"
)

// Outcomes of a deployment.
const (
	gitOutcomeAdopted     = "adopted"
	gitOutcomeDeployed    = "deployed"
	gitOutcomeBuildFailed = "build_failed"
	gitOutcomeRolledBack  = "rolled_back"
	gitOutcomeFailed      = "failed"
	gitOutcomeInterrupted = "interrupted"
)

// Kinds of operation recorded while one runs.
const (
	gitOperationCheck  = "check"
	gitOperationBuild  = "build"
	gitOperationDeploy = "deploy"
	gitOperationRevert = "revert"
)

// ErrGitAppNotFound is an app that is neither registered nor adoptable.
var ErrGitAppNotFound = errors.New("not an app deployed from a git repository")

type gitApp struct {
	App        string `json:"app"`
	Origin     string `json:"origin"`
	Dir        string `json:"dir"`
	Remote     string `json:"remote"`
	Branch     string `json:"branch"`
	Access     string `json:"access"`
	AutoDeploy bool   `json:"auto_deploy"`
	AutoPaused bool   `json:"auto_paused"`
	Blocked    bool   `json:"blocked"`
	EnvTracked bool   `json:"env_tracked"`
	// Cloned is set once a created app's clone holds a compose file; an adopted app is.
	Cloned bool `json:"cloned"`
	// NoComposeFile is set when the last check cloned a repository without a compose file.
	NoComposeFile bool              `json:"no_compose_file"`
	Deployed      *gitDeployment    `json:"deployed"`
	Check         *gitCheck         `json:"check"`
	Operation     *gitOperation     `json:"operation"`
	History       []gitHistoryEntry `json:"history"`
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

var scpLikeGitURL = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?[A-Za-z0-9.-]+:\S+$`)

// gitURLKind is `http`, `ssh` or `file` for a URL git may be given, and empty for anything
// else: another transport, an option in disguise, or a password written in the URL, which
// belongs in a token.
func gitURLKind(raw string) string {
	if raw == "" || strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, " \t\r\n") {
		return ""
	}

	if !strings.Contains(raw, "://") {
		if scpLikeGitURL.MatchString(raw) {
			return "ssh"
		}

		return ""
	}

	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if _, password := u.User.Password(); password {
		return ""
	}

	switch {
	case (u.Scheme == "https" || u.Scheme == "http") && u.Host != "":
		return "http"
	case u.Scheme == "ssh" && u.Host != "":
		return "ssh"
	case u.Scheme == "file" && u.Path != "":
		return "file"
	}

	return ""
}

// redactGitURL is a URL fit to show: a password written in it is dropped.
func redactGitURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, password := u.User.Password(); !password {
		return raw
	}
	u.User = url.User(u.User.Username())

	return u.String()
}

func validGitBranch(branch string) bool {
	return !strings.HasPrefix(branch, "-") && !strings.Contains(branch, "..") && !strings.ContainsAny(branch, " \t\r\n~^:?*[\\")
}

// gitAppFile is one of an app's files: .json, .key, .key.pub, .token, .known_hosts or
// .build.log.
func gitAppFile(app, suffix string) string {
	return filepath.Join(gitAppsDir, app+suffix)
}

func loadGitApp(app string) (*gitApp, error) {
	buf, err := os.ReadFile(gitAppFile(app, ".json"))
	if os.IsNotExist(err) {
		return nil, ErrGitAppNotFound
	}
	if err != nil {
		return nil, err
	}

	var st gitApp
	if err := stdjson.Unmarshal(buf, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", gitAppFile(app, ".json"), err)
	}

	return &st, nil
}

func saveGitApp(st *gitApp) error {
	buf, err := stdjson.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}

	return writeGitFile(gitAppFile(st.App, ".json"), buf)
}

// GitAppNames is every registered git app, sorted.
func GitAppNames() ([]string, error) {
	entries, err := os.ReadDir(gitAppsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	names := []string{}
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), ".json"); ok && !entry.IsDir() {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	return names, nil
}

// forgetGitApp removes an app's state and secrets.
func forgetGitApp(app string) error {
	failures := []error{}
	for _, suffix := range []string{".json", ".key", ".key.pub", ".token", ".known_hosts", ".build.log"} {
		if err := os.Remove(gitAppFile(app, suffix)); err != nil && !os.IsNotExist(err) {
			failures = append(failures, err)
		}
	}

	return errors.Join(failures...)
}

// writeGitFile replaces a file of the git apps' folder the way saveImageUpdates replaces
// its own: a temporary file of its own name, a sync, a rename. The next start reads these
// files, and a truncated state is an app CasaOS no longer knows how to deploy.
func writeGitFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp") // 0600
	if err != nil {
		return err
	}

	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}

	return err
}

// generateGitKey writes a new ed25519 deploy key for app and returns its public half, the
// line the owner adds to the repository.
func generateGitKey(app string) (string, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}

	block, err := ssh.MarshalPrivateKey(private, "casaos-"+app)
	if err != nil {
		return "", err
	}

	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		return "", err
	}
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublic))) + " casaos-" + app

	if err := writeGitFile(gitAppFile(app, ".key"), pem.EncodeToMemory(block)); err != nil {
		return "", err
	}
	if err := writeGitFile(gitAppFile(app, ".key.pub"), []byte(authorized+"\n")); err != nil {
		return "", err
	}
	if !pathExists(gitAppFile(app, ".known_hosts")) {
		if err := writeGitFile(gitAppFile(app, ".known_hosts"), nil); err != nil {
			return "", err
		}
	}

	return authorized, nil
}

// gitAuth is how the app's remote is reached.
func gitAuth(st *gitApp) (git.Auth, error) {
	switch st.Access {
	case git.AccessKey:
		return git.Auth{Mode: git.AccessKey, KeyPath: gitAppFile(st.App, ".key"), KnownHostsPath: gitAppFile(st.App, ".known_hosts")}, nil
	case git.AccessToken:
		token, err := os.ReadFile(gitAppFile(st.App, ".token"))
		if err != nil {
			return git.Auth{}, fmt.Errorf("the token of %s cannot be read: %w", st.App, err)
		}

		// the askpass helper goes beside the state, not under /tmp, which may be mounted noexec
		return git.Auth{Mode: git.AccessToken, Token: strings.TrimSpace(string(token)), AskpassDir: gitAppsDir}, nil
	}

	return git.Auth{Mode: git.AccessNone}, nil
}

// record puts entry at the top of the history and keeps the last three. It returns the
// images of the entries that left, except those a remaining entry or the running version
// still names: they are what can be removed.
func (st *gitApp) record(entry gitHistoryEntry) []string {
	st.History = append([]gitHistoryEntry{entry}, st.History...)
	if len(st.History) <= gitHistoryLength {
		return nil
	}

	gone := st.History[gitHistoryLength:]
	st.History = st.History[:gitHistoryLength]

	kept := map[string]bool{}
	for _, e := range st.History {
		for _, image := range e.Images {
			kept[image] = true
		}
	}
	if st.Deployed != nil {
		for _, image := range st.Deployed.Images {
			kept[image] = true
		}
	}

	removable := []string{}
	for _, e := range gone {
		for _, image := range lo.Values(e.Images) {
			if !kept[image] {
				removable = append(removable, image)
				kept[image] = true
			}
		}
	}
	sort.Strings(removable)

	return removable
}

// attempt remembers that commit was tried, so the automatic rebuild leaves it alone.
func (st *gitApp) attempt(commit string) {
	if commit == "" || lo.Contains(st.Attempted, commit) {
		return
	}

	st.Attempted = append(st.Attempted, commit)
	if len(st.Attempted) > gitAttemptedLength {
		st.Attempted = st.Attempted[len(st.Attempted)-gitAttemptedLength:]
	}
}

// historyEntry is the entry for commit, or nil.
func (st *gitApp) historyEntry(commit string) *gitHistoryEntry {
	for i := range st.History {
		if st.History[i].Commit == commit {
			return &st.History[i]
		}
	}

	return nil
}
```

- [ ] **Step 4: Make `golang.org/x/crypto` a direct dependency**

In `go.mod`, replace:

```text
	golang.org/x/crypto v0.57.0 // indirect
```

with:

```text
	golang.org/x/crypto v0.57.0
```

This is the one line `go mod tidy` changes; `go.sum` already holds the module.

- [ ] **Step 5: Run the tests**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go vet ./service/ && go test ./service/ -count=1 -run 'TestAGitApp|TestADeployKey|TestHistoryKeeps|TestAttempted|TestAGitURL' -v" 2>&1 | Select-Object -Last 40
```

Expected: `--- PASS` for TestAGitAppIsKeptInAFileOnlyRootReads, TestAGitAppWithATokenHasItsAskpassHelperBesideTheState, TestADeployKeyIsEd25519WithItsPublicHalfCommented, TestHistoryKeepsThreeAndGivesBackWhatNothingNames, TestAttemptedCommitsAreBoundedAndUnique and TestAGitURLIsOneGitCanBeGivenSafely.

- [ ] **Step 6: Check the formatting**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "tr -d '\r' < service/git_app_state.go | gofmt -l; tr -d '\r' < service/git_app_state_internal_test.go | gofmt -l" 2>&1 | Select-Object -Last 40
```

Expected: no output.

- [ ] **Step 7: Commit**

```powershell
git -C D:\clients\casaos\CasaOS-AppManagement add service/git_app_state.go service/git_app_state_internal_test.go go.mod
git -C D:\clients\casaos\CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): the state of a git app, its deploy key and its token"
```

---

### Task 6: Events, the grid's `git` field and the view of a git app

**Files:**
- Modify: `common/message.go:128` (`EventTypes`) and `:390` (before `// event types for image`)
- Modify: `api/app_management/openapi.yaml:3013-3017` (two schemas before `WebAppGridItem`, and its `git` property)
- Create: `service/git_app_view.go`
- Modify: `route/v2/internal_web.go:129` (`GetAppGrid`) and `:169` (`WebAppGridItemAdapterV2`)
- Test: `common/git_events_test.go`, `service/git_fakes_internal_test.go`, `service/git_app_view_internal_test.go`, `route/v2/internal_web_test.go:68-69`

**Interfaces:**
- Consumes: `gitDocker.ImagesExist`, `gitComposeFiles`, `ErrGitNoComposeFile` (Task 4); `loadGitApp`, `GitAppNames`, `gitAppFile`, `redactGitURL`, the constants and types of Task 5; `git.Describe`, `git.Subject`, `git.IsTracked` (Task 2).
- Produces, for Tasks 7 to 13:
  - `common.EventTypeAppGitBuildBegin`, `EventTypeAppGitBuildProgress`, `EventTypeAppGitBuildEnd`, `EventTypeAppGitBuildError`, `EventTypeAppGitDeployEnd`, `EventTypeAppGitDeployError`
  - `codegen.GitAppGrid{NewCommits bool; State string; Deployed bool}` and `codegen.WebAppGridItem.Git *GitAppGrid`
  - `type GitAppView struct` (the contract's `GitApp`, with `Check *gitCheck` and `Operation *gitOperation` copied), `GitHeadView`, `GitDeployedView`, `GitHistoryView`, `GitComposeView`, `GitComposeServiceView`
  - `const GitComposeExample`; `func gitAppStateOf(st *gitApp) string`; `func gitNewCommits(st *gitApp) bool`; `func gitRevertable(ctx, st *gitApp, entry gitHistoryEntry) bool`
  - `func newGitAppView(ctx context.Context, st *gitApp) *GitAppView`; `func gitComposeSummary(root string) (*GitComposeView, error)`; `func readGitBuildLogTail(app string) string`
  - `func GitAppGrid(app string) *codegen.GitAppGrid`; `func GitAppsWithoutContainers(listed []string) []codegen.WebAppGridItem`
- Test helpers produced (`service/git_fakes_internal_test.go`): `fakeGitRuntime` with fields `projects`, `buildErr`, `startErrs`, `retagErrs`, `missing`, `gate` (when set, `Build` sends on it as it starts, then waits for a value), `calls`, `removed` and the method `Calls() []string` (its `Start` records the commit the folder is on); `withFakeGitDocker(t)`; `waitForGitApp(t, name) *gitApp` (waits until nothing holds the app); `gitShell(t, dir, script) string`; `testComposeFile`; `newTestRemote(t) (url, work string)`; `pushTestCommit(t, work, name, content) string`.

- [ ] **Step 1: Write the failing tests**

Create `common/git_events_test.go`:

```go
package common_test

import (
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"gotest.tools/v3/assert"
)

// The dashboard listens for these by name: each is registered, with what it carries.
func TestGitAppEventsAreRegisteredWithTheirProperties(t *testing.T) {
	registered := map[string][]string{}
	for _, eventType := range common.EventTypes {
		properties := []string{}
		for _, property := range eventType.PropertyTypeList {
			properties = append(properties, property.Name)
		}
		registered[eventType.Name] = properties
	}

	for name, properties := range map[string][]string{
		"app:git-build-begin":    {"app:name"},
		"app:git-build-progress": {"app:name", "message"},
		"app:git-build-end":      {"app:name"},
		"app:git-build-error":    {"app:name", "message"},
		"app:git-deploy-end":     {"app:name"},
		"app:git-deploy-error":   {"app:name", "message"},
	} {
		assert.DeepEqual(t, registered[name], properties)
	}
}
```

Create `service/git_fakes_internal_test.go`:

```go
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
```

Create `service/git_app_view_internal_test.go`:

```go
package service

import (
	"context"
	stdjson "encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"gotest.tools/v3/assert"
)

func TestGitAppStateFollowsTheRule(t *testing.T) {
	failedCheck := &gitCheck{Error: "could not read from remote"}
	outcome := func(o string) []gitHistoryEntry { return []gitHistoryEntry{{Outcome: o}} }

	for want, st := range map[string]*gitApp{
		"building":    {Operation: &gitOperation{Kind: gitOperationBuild}, Blocked: true},
		"deploying":   {Operation: &gitOperation{Kind: gitOperationRevert}},
		"failed":      {Blocked: true, History: outcome(gitOutcomeDeployed)},
		"rolled_back": {History: outcome(gitOutcomeRolledBack), Check: failedCheck},
		"unreachable": {Operation: &gitOperation{Kind: gitOperationCheck}, History: outcome(gitOutcomeDeployed), Check: failedCheck},
		"idle":        {History: outcome(gitOutcomeAdopted), Check: &gitCheck{}},
	} {
		assert.Equal(t, gitAppStateOf(st), want)
	}

	assert.Equal(t, gitAppStateOf(&gitApp{History: outcome(gitOutcomeFailed)}), "failed")
	assert.Equal(t, gitAppStateOf(&gitApp{History: outcome(gitOutcomeBuildFailed)}), "build_failed")
	assert.Equal(t, gitAppStateOf(&gitApp{History: outcome(gitOutcomeInterrupted)}), "interrupted")
	assert.Equal(t, gitAppStateOf(&gitApp{Operation: &gitOperation{Kind: gitOperationDeploy}}), "deploying")
}

func TestNewCommitsIsTheRemoteMovingAwayFromWhatRuns(t *testing.T) {
	deployed := &gitDeployment{Commit: "a"}

	assert.Assert(t, !gitNewCommits(&gitApp{Deployed: deployed}), "never checked")
	assert.Assert(t, !gitNewCommits(&gitApp{Deployed: deployed, Check: &gitCheck{Error: "offline"}}))
	assert.Assert(t, !gitNewCommits(&gitApp{Deployed: deployed, Check: &gitCheck{RemoteCommit: "a"}}))
	assert.Assert(t, !gitNewCommits(&gitApp{Check: &gitCheck{RemoteCommit: "b"}}), "nothing runs yet")
	assert.Assert(t, gitNewCommits(&gitApp{Deployed: deployed, Check: &gitCheck{RemoteCommit: "b"}}))
}

func TestTheComposeReviewShowsTheFilesAsWrittenAndWhatIsSensitive(t *testing.T) {
	root := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(`services:
  web:
    build: .
    ports:
      - "${PORT:-8080}:8080"
    volumes:
      - ./data:/data
      - /var/run/docker.sock:/var/run/docker.sock:ro
  agent:
    image: portainer/agent:2
    privileged: true
    network_mode: host
    pid: host
    cap_add: [NET_ADMIN]
    devices: [/dev/dri]
`), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(root, "compose.override.yaml"), []byte(`services:
  web:
    ports:
      - target: 9090
        published: "9090"
`), 0o644))

	summary, err := gitComposeSummary(root)
	assert.NilError(t, err)
	assert.DeepEqual(t, summary, &GitComposeView{
		Files: []string{"compose.yaml", "compose.override.yaml"},
		Services: []GitComposeServiceView{
			{
				Name: "agent", Image: "portainer/agent:2", Ports: []string{}, Volumes: []string{},
				Sensitive: []string{"privileged", "network_mode_host", "pid_host", "cap_add", "devices"},
			},
			{
				Name: "web", Build: true, Ports: []string{"${PORT:-8080}:8080", "9090:9090"},
				Volumes:   []string{"./data:/data", "/var/run/docker.sock:/var/run/docker.sock:ro"},
				Sensitive: []string{"docker_socket"},
			},
		},
	})
}

// The view is the API's `GitApp`: the fields the dashboard reads, null where the contract
// says null, and the secrets never.
func TestTheViewOfAGitAppIsTheContract(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()

	url, work := newTestRemote(t)
	first := gitShell(t, work, "git rev-parse HEAD")
	second := pushTestCommit(t, work, ".env.example", "GREETING=hello\n")
	dir := filepath.Join(gitAppsDataRoot, "jarvis")
	assert.NilError(t, git.Clone(ctx, url, "main", dir, git.Auth{}))
	assert.NilError(t, writeGitFile(gitAppFile("jarvis", ".token"), []byte("secret")))
	assert.NilError(t, writeGitFile(gitAppFile("jarvis", ".build.log"), []byte("#1 building\n")))

	at := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	st := &gitApp{
		App: "jarvis", Origin: gitOriginCreated, Dir: dir, Remote: url, Branch: "main", Access: git.AccessToken, Cloned: true,
		Deployed: &gitDeployment{Commit: second, Subject: "change .env.example", At: at, Images: map[string]string{"web": "jarvis-web:git-" + second[:12]}},
		Check:    &gitCheck{At: at, RemoteCommit: second},
		History: []gitHistoryEntry{
			{Commit: second, Subject: "change .env.example", At: at, Outcome: gitOutcomeDeployed, Images: map[string]string{"web": "jarvis-web:git-" + second[:12]}},
			{Commit: first, Subject: "change compose.yaml", At: at, Outcome: gitOutcomeDeployed, Images: map[string]string{"web": "jarvis-web:git-" + first[:12]}},
		},
	}

	encoded, err := stdjson.Marshal(newGitAppView(ctx, st))
	assert.NilError(t, err)
	var view map[string]any
	assert.NilError(t, stdjson.Unmarshal(encoded, &view))

	keys := []string{}
	for key := range view {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assert.DeepEqual(t, keys, []string{
		"access", "app", "auto_deploy", "auto_paused", "blocked", "branch", "build_log", "check", "cloned", "compose",
		"compose_example", "deployed", "dir", "env_template", "env_tracked", "head", "history", "new_commits", "operation",
		"origin", "remote", "state", "token_set",
	})
	assert.Equal(t, view["operation"], nil)
	assert.Equal(t, view["compose_example"], nil)
	assert.Equal(t, view["state"], "idle")
	assert.Equal(t, view["token_set"], true)
	assert.Equal(t, view["env_template"], "GREETING=hello\n")
	assert.Equal(t, view["build_log"], "#1 building\n")
	assert.DeepEqual(t, view["head"], map[string]any{"commit": second, "subject": "change .env.example", "tracked_files_clean": true})
	assert.DeepEqual(t, view["deployed"], map[string]any{"commit": second, "subject": "change .env.example", "at": "2026-09-16T10:00:00Z"})
	assert.DeepEqual(t, view["check"], map[string]any{"at": "2026-09-16T10:00:00Z", "remote_commit": second, "error": ""})

	history := view["history"].([]any)
	assert.Equal(t, history[0].(map[string]any)["revertable"], false, "the running version")
	assert.Equal(t, history[1].(map[string]any)["revertable"], true)

	fake.missing["jarvis-web:git-"+first[:12]] = true
	assert.Equal(t, newGitAppView(ctx, st).History[1].Revertable, false, "its image is gone")

	// a key is shown; an app that is not cloned has nothing of its folder
	public, err := generateGitKey("jarvis")
	assert.NilError(t, err)
	st.Access, st.Cloned, st.NoComposeFile = git.AccessKey, false, true
	notCloned := newGitAppView(ctx, st)
	assert.Equal(t, notCloned.PublicKey, public)
	assert.Assert(t, notCloned.Head == nil && notCloned.Compose == nil && notCloned.EnvTemplate == nil)
	assert.Equal(t, *notCloned.ComposeExample, GitComposeExample)
}

func TestTheGridCarriesGitOnlyForGitApps(t *testing.T) {
	gitAppsIn(t)

	assert.Assert(t, GitAppGrid("nextcloud") == nil)

	assert.NilError(t, saveGitApp(&gitApp{
		App: "jarvis", Deployed: &gitDeployment{Commit: "a"}, Check: &gitCheck{RemoteCommit: "b"},
		History: []gitHistoryEntry{{Commit: "b", Outcome: gitOutcomeRolledBack}},
	}))
	assert.DeepEqual(t, GitAppGrid("jarvis"), &codegen.GitAppGrid{NewCommits: true, State: "rolled_back", Deployed: true})
}

// The compose list knows apps by their containers: a git app that has none still reaches
// the grid, so that its owner can see why and delete it.
func TestAGitAppWithoutContainersIsStillInTheGrid(t *testing.T) {
	gitAppsIn(t)
	assert.NilError(t, saveGitApp(&gitApp{App: "jarvis", History: []gitHistoryEntry{{Outcome: gitOutcomeFailed}}}))
	assert.NilError(t, saveGitApp(&gitApp{App: "running", Deployed: &gitDeployment{Commit: "a"}}))

	items := GitAppsWithoutContainers([]string{"running", "nextcloud"})

	assert.Equal(t, len(items), 1)
	assert.Equal(t, items[0].AppType, codegen.V2app)
	assert.Equal(t, *items[0].Name, "jarvis")
	assert.DeepEqual(t, *items[0].Title, map[string]string{"en_us": "jarvis"})
	assert.Equal(t, *items[0].Status, "")
	assert.DeepEqual(t, items[0].Git, &codegen.GitAppGrid{State: "failed", Deployed: false})
}
```

In `route/v2/internal_web_test.go`, replace:

```go
	assert.Equal(t, *gridItem.AuthorType, codegen.ByCasaos)
	assert.Equal(t, *gridItem.IsUncontrolled, false)
}
```

with:

```go
	assert.Equal(t, *gridItem.AuthorType, codegen.ByCasaos)
	assert.Equal(t, *gridItem.IsUncontrolled, false)
	assert.Assert(t, gridItem.Git == nil, "an app that is not deployed from git has no git badge")
}
```

- [ ] **Step 2: Run them to see them fail**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go test ./common/ ./service/ ./route/v2/ -count=1 -run 'TestGitAppEvents|TestGitAppState|TestNewCommits|TestTheComposeReview|TestTheViewOf|TestTheGrid|TestAGitAppWithoutContainers|TestWebAppGridItemAdapter'" 2>&1 | Select-Object -Last 40
```

Expected: FAIL: `common` fails `TestGitAppEventsAreRegisteredWithTheirProperties` on its first assertion, `route/v2` stops on `gridItem.Git undefined (type *codegen.WebAppGridItem has no field or method Git)`, and `service` on `undefined: gitAppStateOf`.

- [ ] **Step 3: Register the six events**

In `common/message.go`, replace:

```go
	EventTypeAppRestartBegin, EventTypeAppRestartEnd, EventTypeAppRestartError,

	// image
```

with:

```go
	EventTypeAppRestartBegin, EventTypeAppRestartEnd, EventTypeAppRestartError,
	EventTypeAppGitBuildBegin, EventTypeAppGitBuildProgress, EventTypeAppGitBuildEnd, EventTypeAppGitBuildError,
	EventTypeAppGitDeployEnd, EventTypeAppGitDeployError,

	// image
```

In `common/message.go`, replace:

```go
// event types for image
var (
```

with:

```go
// event types for apps deployed from their git repository. A build says what it is doing
// line by line in progress events; a deployment says only how it ended.
var (
	EventTypeAppGitBuildBegin = message_bus.EventType{
		SourceID: AppManagementServiceName,
		Name:     "app:git-build-begin",
		PropertyTypeList: []message_bus.PropertyType{
			PropertyTypeAppName,
		},
	}

	EventTypeAppGitBuildProgress = message_bus.EventType{
		SourceID: AppManagementServiceName,
		Name:     "app:git-build-progress",
		PropertyTypeList: []message_bus.PropertyType{
			PropertyTypeAppName,
			PropertyTypeMessage,
		},
	}

	EventTypeAppGitBuildEnd = message_bus.EventType{
		SourceID: AppManagementServiceName,
		Name:     "app:git-build-end",
		PropertyTypeList: []message_bus.PropertyType{
			PropertyTypeAppName,
		},
	}

	EventTypeAppGitBuildError = message_bus.EventType{
		SourceID: AppManagementServiceName,
		Name:     "app:git-build-error",
		PropertyTypeList: []message_bus.PropertyType{
			PropertyTypeAppName,
			PropertyTypeMessage,
		},
	}

	EventTypeAppGitDeployEnd = message_bus.EventType{
		SourceID: AppManagementServiceName,
		Name:     "app:git-deploy-end",
		PropertyTypeList: []message_bus.PropertyType{
			PropertyTypeAppName,
		},
	}

	EventTypeAppGitDeployError = message_bus.EventType{
		SourceID: AppManagementServiceName,
		Name:     "app:git-deploy-error",
		PropertyTypeList: []message_bus.PropertyType{
			PropertyTypeAppName,
			PropertyTypeMessage,
		},
	}
)

// event types for image
var (
```

- [ ] **Step 4: Declare the grid's `git` field**

In `api/app_management/openapi.yaml`, replace:

```yaml
    WebAppGridItem:
      description: (internal use ONLY)
      required:
        - app_type
      properties:
```

with:

```yaml
    GitAppState:
      description: |
        `building` and `deploying` while a deployment runs. Otherwise `failed` when automatic
        actions are blocked or the last deployment failed; `build_failed`, `rolled_back` or
        `interrupted` when that is how the last deployment ended; `unreachable` when the last
        check could not reach the repository; `idle` otherwise.

        One of `idle`, `building`, `deploying`, `build_failed`, `rolled_back`, `failed`,
        `unreachable`, `interrupted`. Not declared as an enum: oapi-codegen v1.12.4 renames the
        constants of existing enums when one is added.
      type: string
      example: idle

    GitAppGrid:
      description: |
        What the app card shows of a git app. A git app with no container, never deployed or
        left without one by a failed first deployment, is in the grid all the same, with an
        empty `status`.
      required:
        - new_commits
        - state
        - deployed
      properties:
        new_commits:
          type: boolean
        state:
          $ref: "#/components/schemas/GitAppState"
        deployed:
          type: boolean
          description: False until a deployment has succeeded.

    WebAppGridItem:
      description: (internal use ONLY)
      required:
        - app_type
      properties:
        git:
          $ref: "#/components/schemas/GitAppGrid"
```

Both schemas are generated because `WebAppGridItem` references them; `GitAppState` becomes `type GitAppState = string`.

- [ ] **Step 5: Regenerate and build**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go generate ./... && grep -cP '^\t[A-Z]\w* +\w+ = \x22' codegen/app_management_api.go && go build ./... && echo build-ok" 2>&1 | Select-Object -Last 40
```

Expected: `34`, then `build-ok`.

- [ ] **Step 6: Write the view**

Create `service/git_app_view.go`:

```go
package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/samber/lo"
	"gopkg.in/yaml.v3"
)

// A git app as the API answers it: what CasaOS keeps, and what the folder says now.

// GitAppView is the `GitApp` of the API.
type GitAppView struct {
	App        string           `json:"app"`
	Origin     string           `json:"origin"`
	Dir        string           `json:"dir"`
	Remote     string           `json:"remote"`
	Branch     string           `json:"branch"`
	Access     string           `json:"access"`
	PublicKey  string           `json:"public_key,omitempty"`
	TokenSet   bool             `json:"token_set"`
	AutoDeploy bool             `json:"auto_deploy"`
	AutoPaused bool             `json:"auto_paused"`
	Blocked    bool             `json:"blocked"`
	EnvTracked bool             `json:"env_tracked"`
	Cloned     bool             `json:"cloned"`
	Head       *GitHeadView     `json:"head"`
	Deployed   *GitDeployedView `json:"deployed"`
	Check      *gitCheck        `json:"check"`
	NewCommits bool             `json:"new_commits"`
	State      string           `json:"state"`
	Operation  *gitOperation    `json:"operation"`
	History    []GitHistoryView `json:"history"`
	Compose    *GitComposeView  `json:"compose"`
	// ComposeExample is set when the last check found no compose file in the repository.
	ComposeExample *string `json:"compose_example"`
	EnvTemplate    *string `json:"env_template"`
	BuildLog       string  `json:"build_log"`
}

type GitHeadView struct {
	Commit            string `json:"commit"`
	Subject           string `json:"subject"`
	TrackedFilesClean bool   `json:"tracked_files_clean"`
}

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

type GitComposeView struct {
	Files    []string                `json:"files"`
	Services []GitComposeServiceView `json:"services"`
}

type GitComposeServiceView struct {
	Name      string   `json:"name"`
	Build     bool     `json:"build"`
	Image     string   `json:"image"`
	Ports     []string `json:"ports"`
	Volumes   []string `json:"volumes"`
	Sensitive []string `json:"sensitive"`
}

// GitComposeExample is what a repository without a compose file is told to add.
const GitComposeExample = `services:
  app:
    build: .
    ports:
      - "8080:8080"
    restart: unless-stopped
`

// gitBuildLogTail is how much of the last build's log the API returns.
const gitBuildLogTail = 64 << 10

// gitAppStateOf is the one state the card and the tab show.
func gitAppStateOf(st *gitApp) string {
	if st.Operation != nil {
		switch st.Operation.Kind {
		case gitOperationBuild:
			return "building"
		case gitOperationDeploy, gitOperationRevert:
			return "deploying"
		}
	}

	newest := ""
	if len(st.History) > 0 {
		newest = st.History[0].Outcome
	}

	switch {
	case st.Blocked || newest == gitOutcomeFailed:
		return "failed"
	case newest == gitOutcomeBuildFailed, newest == gitOutcomeRolledBack, newest == gitOutcomeInterrupted:
		return newest
	case st.Check != nil && st.Check.Error != "":
		return "unreachable"
	}

	return "idle"
}

// gitNewCommits reports whether the last check saw the branch somewhere else than what runs.
// Nothing runs before the first deployment, so nothing is new either.
func gitNewCommits(st *gitApp) bool {
	return st.Deployed != nil && st.Check != nil && st.Check.RemoteCommit != "" && st.Check.RemoteCommit != st.Deployed.Commit
}

// GitAppGrid is what the app card shows of a registered git app, nil for any other app.
// It reads the state file alone: the grid never waits on git or on a registry.
func GitAppGrid(app string) *codegen.GitAppGrid {
	st, err := loadGitApp(app)
	if err != nil {
		return nil
	}

	return &codegen.GitAppGrid{NewCommits: gitNewCommits(st), State: gitAppStateOf(st), Deployed: st.Deployed != nil}
}

// GitAppsWithoutContainers are the grid items of the registered git apps missing from the
// compose list, which only knows apps that have containers: never deployed, or left
// without one by a failed first deployment. The owner still reaches their Repository tab.
func GitAppsWithoutContainers(listed []string) []codegen.WebAppGridItem {
	names, err := GitAppNames()
	if err != nil {
		return nil
	}

	items := []codegen.WebAppGridItem{}
	for _, name := range names {
		if lo.Contains(listed, name) {
			continue
		}

		if grid := GitAppGrid(name); grid != nil {
			items = append(items, codegen.WebAppGridItem{
				AppType: codegen.V2app,
				Name:    lo.ToPtr(name),
				Title:   &map[string]string{common.DefaultLanguage: name},
				Status:  lo.ToPtr(""),
				Git:     grid,
			})
		}
	}

	return items
}

// newGitAppView reads what the API answers for st.
//
// ponytail: a view of a cloned app runs git four times; cache it per HEAD if polling it
// ever shows up on a small box.
func newGitAppView(ctx context.Context, st *gitApp) *GitAppView {
	view := &GitAppView{
		App: st.App, Origin: st.Origin, Dir: st.Dir, Remote: redactGitURL(st.Remote), Branch: st.Branch, Access: st.Access,
		TokenSet:   pathExists(gitAppFile(st.App, ".token")),
		AutoDeploy: st.AutoDeploy, AutoPaused: st.AutoPaused, Blocked: st.Blocked,
		EnvTracked: st.EnvTracked, Cloned: st.Cloned,
		NewCommits: gitNewCommits(st), State: gitAppStateOf(st),
		History:  []GitHistoryView{},
		BuildLog: readGitBuildLogTail(st.App),
	}

	// copies: a deployment goes on changing st once its view is taken
	if st.Check != nil {
		check := *st.Check
		view.Check = &check
	}
	if st.Operation != nil {
		operation := *st.Operation
		view.Operation = &operation
	}

	if st.NoComposeFile {
		view.ComposeExample = lo.ToPtr(GitComposeExample)
	}

	if st.Access == git.AccessKey {
		if public, err := os.ReadFile(gitAppFile(st.App, ".key.pub")); err == nil {
			view.PublicKey = strings.TrimSpace(string(public))
		}
	}

	if st.Deployed != nil {
		view.Deployed = &GitDeployedView{Commit: st.Deployed.Commit, Subject: st.Deployed.Subject, At: st.Deployed.At}
	}

	for _, entry := range st.History {
		view.History = append(view.History, GitHistoryView{
			Commit: entry.Commit, Subject: entry.Subject, At: entry.At, Outcome: entry.Outcome, Reason: entry.Reason,
			Revertable: gitRevertable(ctx, st, entry),
		})
	}

	if !st.Cloned {
		return view
	}

	if info, err := git.Describe(ctx, st.Dir); err == nil && info.Head != "" {
		subject, _ := git.Subject(ctx, st.Dir, info.Head)
		view.Head = &GitHeadView{Commit: info.Head, Subject: subject, TrackedFilesClean: info.TrackedFilesClean}
	}
	if tracked, err := git.IsTracked(ctx, st.Dir, ".env"); err == nil {
		view.EnvTracked = tracked
	}
	view.Compose, _ = gitComposeSummary(st.Dir)
	view.EnvTemplate = gitEnvTemplate(st.Dir)

	return view
}

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

func readGitBuildLogTail(app string) string {
	file, err := os.Open(gitAppFile(app, ".build.log"))
	if err != nil {
		return ""
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return ""
	}

	offset := max(info.Size()-gitBuildLogTail, 0)
	buf := make([]byte, info.Size()-offset)
	n, _ := file.ReadAt(buf, offset)

	return string(buf[:n])
}

func gitEnvTemplate(root string) *string {
	for _, name := range []string{".env.example", ".env.sample"} {
		if buf, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			text := string(buf)
			return &text
		}
	}

	return nil
}

// gitComposeService is what the review shows of a service, read from the file as written:
// before its .env exists a reference cannot be resolved, and what the owner reviews is
// the file.
type gitComposeService struct {
	Image       string `yaml:"image"`
	Build       any    `yaml:"build"`
	Ports       []any  `yaml:"ports"`
	Volumes     []any  `yaml:"volumes"`
	Privileged  any    `yaml:"privileged"`
	NetworkMode string `yaml:"network_mode"`
	Pid         string `yaml:"pid"`
	CapAdd      []any  `yaml:"cap_add"`
	Devices     []any  `yaml:"devices"`
}

// gitComposeSummary is what the compose files at root will run.
//
// ponytail: `extends` and `include` are not followed; follow them when a repository
// relies on them for what it runs.
func gitComposeSummary(root string) (*GitComposeView, error) {
	files, err := gitComposeFiles(root)
	if err != nil {
		return nil, err
	}

	merged := map[string]*gitComposeService{}
	view := &GitComposeView{Files: []string{}, Services: []GitComposeServiceView{}}
	for _, file := range files {
		view.Files = append(view.Files, filepath.Base(file))

		buf, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}

		var parsed struct {
			Services map[string]gitComposeService `yaml:"services"`
		}
		if err := yaml.Unmarshal(buf, &parsed); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(file), err)
		}

		for name, service := range parsed.Services {
			if merged[name] == nil {
				service := service
				merged[name] = &service
				continue
			}
			merged[name].override(service)
		}
	}

	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		view.Services = append(view.Services, merged[name].view(name))
	}

	return view, nil
}

// override applies an override file's service, the way compose merges these fields: what
// is set replaces, lists add up.
func (s *gitComposeService) override(o gitComposeService) {
	if o.Image != "" {
		s.Image = o.Image
	}
	if o.Build != nil {
		s.Build = o.Build
	}
	if o.Privileged != nil {
		s.Privileged = o.Privileged
	}
	if o.NetworkMode != "" {
		s.NetworkMode = o.NetworkMode
	}
	if o.Pid != "" {
		s.Pid = o.Pid
	}
	s.Ports = append(s.Ports, o.Ports...)
	s.Volumes = append(s.Volumes, o.Volumes...)
	s.CapAdd = append(s.CapAdd, o.CapAdd...)
	s.Devices = append(s.Devices, o.Devices...)
}

func (s *gitComposeService) view(name string) GitComposeServiceView {
	view := GitComposeServiceView{
		Name: name, Build: s.Build != nil, Image: s.Image,
		Ports: []string{}, Volumes: []string{}, Sensitive: []string{},
	}

	for _, port := range s.Ports {
		view.Ports = append(view.Ports, composeEntry(port, "published"))
	}

	socket := false
	for _, volume := range s.Volumes {
		entry := composeEntry(volume, "source")
		view.Volumes = append(view.Volumes, entry)

		source, _, _ := strings.Cut(entry, ":")
		socket = socket || source == "/var/run/docker.sock" || source == "/run/docker.sock"
	}

	// anything but an explicit false: a reference may well resolve to true
	if s.Privileged != nil && s.Privileged != false && s.Privileged != "false" {
		view.Sensitive = append(view.Sensitive, "privileged")
	}
	if s.NetworkMode == "host" {
		view.Sensitive = append(view.Sensitive, "network_mode_host")
	}
	if s.Pid == "host" {
		view.Sensitive = append(view.Sensitive, "pid_host")
	}
	if len(s.CapAdd) > 0 {
		view.Sensitive = append(view.Sensitive, "cap_add")
	}
	if len(s.Devices) > 0 {
		view.Sensitive = append(view.Sensitive, "devices")
	}
	if socket {
		view.Sensitive = append(view.Sensitive, "docker_socket")
	}

	return view
}

// composeEntry is a port or a volume as written: the short syntax itself, the long one
// folded into it.
func composeEntry(entry any, source string) string {
	long, ok := entry.(map[string]any)
	if !ok {
		return fmt.Sprint(entry)
	}

	target := fmt.Sprint(long["target"])
	if value, ok := long[source]; ok && fmt.Sprint(value) != "" {
		return fmt.Sprint(value) + ":" + target
	}

	return target
}
```

- [ ] **Step 7: Put `git` on the grid**

In `route/v2/internal_web.go`, replace:

```go
		UpdateAvailable: service.ImageUpdateAvailable(composeApp.Name),
	}
```

with:

```go
		UpdateAvailable: service.ImageUpdateAvailable(composeApp.Name),

		// from the git app's state file alone, absent for any other app
		Git: service.GitAppGrid(composeApp.Name),
	}
```

In `route/v2/internal_web.go`, replace:

```go
	appGridItems = append(appGridItems, v2AppGridItems...)
```

with:

```go
	appGridItems = append(appGridItems, v2AppGridItems...)
	appGridItems = append(appGridItems, service.GitAppsWithoutContainers(lo.Keys(composeAppsWithStoreInfo))...)
```

`composeAppsWithStoreInfo` is the compose list, keyed by app name: a git app in it keeps its own item and real status, and only a registered git app missing from it is synthesized, with `status: ""`.

- [ ] **Step 8: Run the tests**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go vet ./common/ ./service/ ./route/... && go test ./common/ ./service/ ./route/v2/ -count=1 -run 'TestGitAppEvents|TestGitAppState|TestNewCommits|TestTheComposeReview|TestTheViewOf|TestTheGrid|TestAGitAppWithoutContainers|TestWebAppGridItemAdapter' -v" 2>&1 | Select-Object -Last 40
```

Expected: `--- PASS` for TestGitAppEventsAreRegisteredWithTheirProperties, TestGitAppStateFollowsTheRule, TestNewCommitsIsTheRemoteMovingAwayFromWhatRuns, TestTheComposeReviewShowsTheFilesAsWrittenAndWhatIsSensitive, TestTheViewOfAGitAppIsTheContract, TestTheGridCarriesGitOnlyForGitApps, TestAGitAppWithoutContainersIsStillInTheGrid and the three TestWebAppGridItemAdapter tests.

- [ ] **Step 9: Check the formatting**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "tr -d '\r' < common/message.go | gofmt -l; tr -d '\r' < common/git_events_test.go | gofmt -l; tr -d '\r' < service/git_app_view.go | gofmt -l; tr -d '\r' < service/git_fakes_internal_test.go | gofmt -l; tr -d '\r' < service/git_app_view_internal_test.go | gofmt -l; tr -d '\r' < route/v2/internal_web.go | gofmt -l; tr -d '\r' < route/v2/internal_web_test.go | gofmt -l" 2>&1 | Select-Object -Last 40
```

Expected: no output.

- [ ] **Step 10: Commit**

```powershell
git -C D:\clients\casaos\CasaOS-AppManagement add common/message.go common/git_events_test.go api/app_management/openapi.yaml service/git_app_view.go service/git_fakes_internal_test.go service/git_app_view_internal_test.go route/v2/internal_web.go route/v2/internal_web_test.go
git -C D:\clients\casaos\CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): events, the grid badge and the view of a git app"
```

---

### Task 7: Registering, adopting, changing, removing and checking a git app

**Files:**
- Create: `service/git_apps.go`
- Test: `service/git_apps_internal_test.go`

**Interfaces:**
- Consumes: everything of Tasks 2, 4, 5 and 6; `loader.NormalizeProjectName` from compose-go; `Begin` (Task 3).
- Produces, for Tasks 8 to 13:
  - `type GitRequestError string` (answered 400), `var ErrGitAppNameTaken, ErrGitAppDeployed error` (answered 409)
  - `type GitAppRegistration struct{ Name, URL, Branch, Access, Token string }`, `type GitAppChanges struct{ Branch, Access, Token *string; AutoDeploy *bool }`
  - `func CreateGitApp(ctx, GitAppRegistration) (*GitAppView, error)`, `func GetGitApp(ctx, name string) (*GitAppView, error)`, `func UpdateGitApp(ctx, name string, changes GitAppChanges) (*GitAppView, error)`, `func DeleteGitApp(ctx, name string) error`, `func CheckGitApp(ctx, name string) (*GitAppView, error)` (asynchronous: it returns the app with the check's operation running)
  - `func checkGitAccess(remote, access string, hasToken bool) error`, `func setGitAccess(st *gitApp, access, token string) error`, `func findGitApp(ctx, name string) (*gitApp, error)`, `func adoptGitApp(ctx, st *gitApp) error`, `func gitImagesOf(st *gitApp) []string`
  - `func beginGitCheck(ctx, name string) (*gitApp, func(), error)`, `func checkGitApp(ctx, st *gitApp, clone bool)`, `func cloneGitApp(ctx, st *gitApp, auth git.Auth) error`, `func gitPartialClone(st *gitApp) string`
- Test helpers produced: `assertBadRequest(t, err, contains)`, `checkTestApp(t, name) *gitApp` (runs "Check now" and waits for it and for what it started).

- [ ] **Step 1: Write the failing tests**

Create `service/git_apps_internal_test.go`:

```go
package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

func assertBadRequest(t *testing.T, err error, contains string) {
	t.Helper()

	var bad GitRequestError
	assert.Assert(t, errors.As(err, &bad), "want a GitRequestError, got %v", err)
	assert.ErrorContains(t, err, contains)
}

// checkTestApp runs "Check now" and waits for it, and for what it started, to end.
func checkTestApp(t *testing.T, name string) *gitApp {
	t.Helper()

	_, err := CheckGitApp(context.Background(), name)
	assert.NilError(t, err)

	return waitForGitApp(t, name)
}

func TestRegisteringAGitAppChecksWhatItIsGiven(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	register := func(r GitAppRegistration) error {
		_, err := CreateGitApp(ctx, r)
		return err
	}

	assertBadRequest(t, register(GitAppRegistration{Name: "Jarvis", URL: "https://github.com/o/j.git", Access: "none"}), "not an app name")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: "https://o:p@github.com/o/j.git", Access: "none"}), "without a password")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: "https://github.com/o/j.git", Access: "key"}), "SSH URL")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: "git@github.com:o/j.git", Access: "token", Token: "t"}), "HTTPS URL")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: "https://github.com/o/j.git", Access: "token"}), "needs the token")
	assertBadRequest(t, register(GitAppRegistration{Name: "jarvis", URL: "https://github.com/o/j.git", Access: "none", Branch: "--orphan"}), "not a branch name")

	fake.projects["nextcloud"] = "/var/lib/casaos/apps/nextcloud"
	assert.ErrorIs(t, register(GitAppRegistration{Name: "nextcloud", URL: "https://github.com/o/j.git", Access: "none"}), ErrGitAppNameTaken)

	view, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: "git@github.com:owner/jarvis.git", Access: "key"})
	assert.NilError(t, err)
	assert.Equal(t, view.Origin, "created")
	assert.Equal(t, view.Dir, filepath.Join(gitAppsDataRoot, "jarvis"))
	assert.Equal(t, view.Cloned, false)
	assert.Assert(t, strings.HasSuffix(view.PublicKey, " casaos-jarvis"))
	_, err = os.Stat(view.Dir)
	assert.Assert(t, os.IsNotExist(err), "nothing is cloned before a check")

	assert.ErrorIs(t, register(GitAppRegistration{Name: "jarvis", URL: "https://github.com/o/j.git", Access: "none"}), ErrGitAppNameTaken)
}

func TestACheckClonesAndReviewsARegisteredAppInTheBackground(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	pushTestCommit(t, work, ".env.example", "GREETING=hello\n")
	head := gitShell(t, work, "git rev-parse HEAD")

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none"})
	assert.NilError(t, err)

	started, err := CheckGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, started.Operation.Kind, gitOperationCheck)

	waitForGitApp(t, "jarvis")
	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Assert(t, view.Operation == nil)
	assert.Equal(t, view.Branch, "main", "the branch the remote's HEAD points to")
	assert.Equal(t, view.Cloned, true)
	assert.Equal(t, view.Check.RemoteCommit, head)
	assert.Equal(t, view.Head.Commit, head)
	assert.Equal(t, view.Compose.Services[0].Name, "web")
	assert.Equal(t, *view.EnvTemplate, "GREETING=hello\n")
	assert.Equal(t, view.State, "idle")
	assert.Equal(t, view.NewCommits, false, "nothing runs yet")
	_, err = os.Stat(filepath.Join(gitAppsDataRoot, ".jarvis.clone"))
	assert.Assert(t, os.IsNotExist(err), "the clone was moved in whole")
}

func TestARepositoryWithoutAComposeFileEndsWithAnExampleAndNoClone(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	gitShell(t, work, "git rm -q compose.yaml && echo 'FROM busybox' > Dockerfile && git add Dockerfile && git commit -qm dockerfile && git push -q origin main")

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none"})
	assert.NilError(t, err)
	checkTestApp(t, "jarvis")

	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.Cloned, false)
	assert.Assert(t, strings.Contains(view.Check.Error, "no compose.yaml"), view.Check.Error)
	assert.Equal(t, *view.ComposeExample, GitComposeExample)
	_, err = os.Stat(filepath.Join(gitAppsDataRoot, "jarvis"))
	assert.Assert(t, os.IsNotExist(err), "the clone is removed")

	// the owner adds one: the next check clones, and the example goes
	pushTestCommit(t, work, "compose.yaml", testComposeFile)
	checkTestApp(t, "jarvis")
	view, err = GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.Cloned, true)
	assert.Assert(t, view.ComposeExample == nil)
	assert.Equal(t, view.Check.Error, "")
}

func TestAFolderInTheWayIsNotClonedInto(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, _ := newTestRemote(t)
	assert.NilError(t, os.MkdirAll(filepath.Join(gitAppsDataRoot, "jarvis"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(gitAppsDataRoot, "jarvis", "notes.txt"), []byte("mine"), 0o644))

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none"})
	assert.NilError(t, err)
	st := checkTestApp(t, "jarvis")
	assert.Assert(t, strings.Contains(st.Check.Error, "not empty"), st.Check.Error)

	notes, err := os.ReadFile(filepath.Join(gitAppsDataRoot, "jarvis", "notes.txt"))
	assert.NilError(t, err)
	assert.Equal(t, string(notes), "mine")

	// and removing the app leaves the folder that was there
	assert.NilError(t, DeleteGitApp(ctx, "jarvis"))
	_, err = os.Stat(filepath.Join(gitAppsDataRoot, "jarvis", "notes.txt"))
	assert.NilError(t, err)
}

func TestAnUnreachableRepositoryIsRecordedAndNothingElseChanges(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	head := gitShell(t, work, "git rev-parse HEAD")

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none"})
	assert.NilError(t, err)
	checkTestApp(t, "jarvis")

	remote := strings.TrimPrefix(url, "file://")
	assert.NilError(t, os.Rename(remote, remote+".gone"))

	st := checkTestApp(t, "jarvis")
	assert.Equal(t, gitAppStateOf(st), "unreachable")
	assert.Assert(t, st.Check.Error != "")
	assert.Equal(t, st.Check.RemoteCommit, head, "what was last seen stays")
	assert.Equal(t, st.Cloned, true)
}

// A stack started by hand in a folder that is a git work tree is a git app to adopt, and
// the first thing the owner does with it adopts it.
func TestAStackStartedByHandInAGitFolderIsAdoptedByTheFirstAction(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	head := gitShell(t, work, "git rev-parse HEAD")
	dir := filepath.Join(t.TempDir(), "jarvis")
	assert.NilError(t, git.Clone(ctx, url, "main", dir, git.Auth{}))
	fake.projects["jarvis"] = dir

	_, err := GetGitApp(ctx, "nextcloud")
	assert.ErrorIs(t, err, ErrGitAppNotFound)

	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.Origin, "adoptable")
	assert.Equal(t, view.Remote, url)
	assert.Assert(t, view.Deployed == nil)
	_, err = loadGitApp("jarvis")
	assert.ErrorIs(t, err, ErrGitAppNotFound, "looking adopts nothing")

	on := true
	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{AutoDeploy: &on})
	assert.NilError(t, err)
	assert.Equal(t, view.Origin, "adopted")
	assert.Equal(t, view.AutoDeploy, true)
	assert.Equal(t, view.Deployed.Commit, head)
	assert.Equal(t, view.History[0].Outcome, "adopted")
	assert.DeepEqual(t, fake.Calls(), []string{"tag running " + head[:12]})

	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, st.Deployed.Images, map[string]string{"web": "jarvis-web:git-" + head[:12]})
}

func TestAFolderInDetachedHeadOrWithoutARemoteIsShownButNotAdopted(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	head := gitShell(t, work, "git rev-parse HEAD")

	detached := filepath.Join(t.TempDir(), "detached")
	assert.NilError(t, git.Clone(ctx, url, "main", detached, git.Auth{}))
	gitShell(t, detached, "git checkout -q --detach "+head)
	fake.projects["detached"] = detached

	alone := filepath.Join(t.TempDir(), "alone")
	assert.NilError(t, git.Clone(ctx, url, "main", alone, git.Auth{}))
	gitShell(t, alone, "git remote remove origin")
	fake.projects["alone"] = alone

	view, err := GetGitApp(ctx, "detached")
	assert.NilError(t, err)
	assert.Equal(t, view.Branch, "")
	view, err = GetGitApp(ctx, "alone")
	assert.NilError(t, err)
	assert.Equal(t, view.Remote, "")

	on := true
	_, err = CheckGitApp(ctx, "detached")
	assertBadRequest(t, err, "detached HEAD")
	_, err = UpdateGitApp(ctx, "alone", GitAppChanges{AutoDeploy: &on})
	assertBadRequest(t, err, "no remote")

	_, err = loadGitApp("detached")
	assert.ErrorIs(t, err, ErrGitAppNotFound)
	_, err = loadGitApp("alone")
	assert.ErrorIs(t, err, ErrGitAppNotFound)
}

func TestTheAccessOfAGitAppChangesAndItsTokenIsNeverShown(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: "https://github.com/owner/jarvis.git", Access: "token", Token: "ghp_secret"})
	assert.NilError(t, err)
	info, err := os.Stat(gitAppFile("jarvis", ".token"))
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))

	empty := ""
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Token: &empty})
	assertBadRequest(t, err, "cannot be empty")

	other := "ghp_other"
	view, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{Token: &other})
	assert.NilError(t, err)
	assert.Equal(t, view.TokenSet, true)
	token, err := os.ReadFile(gitAppFile("jarvis", ".token"))
	assert.NilError(t, err)
	assert.Equal(t, string(token), other)

	none := "none"
	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Access: &none})
	assert.NilError(t, err)
	assert.Equal(t, view.TokenSet, false, "a token that is not used any more is forgotten")

	key := "key"
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Access: &key})
	assertBadRequest(t, err, "SSH URL")

	branch := "develop"
	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{Branch: &branch})
	assert.NilError(t, err, "a branch changes until the app is cloned")
	assert.Equal(t, view.Branch, "develop")
}

// Removing is for an app no deployment succeeded for, whatever its history: what a failed
// first deployment left goes with it.
func TestRemovingAGitAppThatNeverRan(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, _ := newTestRemote(t)

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: url, Access: "none"})
	assert.NilError(t, err)
	st := checkTestApp(t, "jarvis")
	st.History = []gitHistoryEntry{{Commit: st.Check.RemoteCommit, Outcome: gitOutcomeFailed, Images: map[string]string{"web": "jarvis-web:git-0123456789ab"}}}

	st.Deployed = &gitDeployment{Commit: st.Check.RemoteCommit}
	assert.NilError(t, saveGitApp(st))
	assert.ErrorIs(t, DeleteGitApp(ctx, "jarvis"), ErrGitAppDeployed)

	st.Deployed = nil
	assert.NilError(t, saveGitApp(st))
	assert.NilError(t, DeleteGitApp(ctx, "jarvis"))
	assert.DeepEqual(t, fake.Calls(), []string{"remove"})
	assert.DeepEqual(t, fake.removed, []string{"jarvis-web:git-0123456789ab"})
	_, err = os.Stat(st.Dir)
	assert.Assert(t, os.IsNotExist(err), "the clone goes")
	_, err = loadGitApp("jarvis")
	assert.ErrorIs(t, err, ErrGitAppNotFound)
}
```

- [ ] **Step 2: Run them to see them fail**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go test ./service/ -count=1 -run 'TestRegistering|TestACheck|TestARepositoryWithout|TestAFolderInTheWay|TestAnUnreachable|TestAStackStartedByHand|TestAFolderInDetachedHead|TestTheAccessOf|TestRemovingAGitApp'" 2>&1 | Select-Object -Last 40
```

Expected: FAIL, the test build stops on `undefined: GitRequestError`, `undefined: CreateGitApp` and the others.

- [ ] **Step 3: Write registration, adoption and checks**

Create `service/git_apps.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

// Registering, adopting, changing and checking apps deployed from their git repository.

// GitRequestError is a request that cannot be carried out as it is; the API answers 400
// with its message, which says what to change.
type GitRequestError string

func (e GitRequestError) Error() string { return string(e) }

var (
	// ErrGitAppNameTaken is answered 409.
	ErrGitAppNameTaken = errors.New("the name is already used by an app, a compose project or an app deployed from git")
	// ErrGitAppDeployed is answered 409: a deployed app goes with its uninstall.
	ErrGitAppDeployed = errors.New("the app has been deployed: uninstall it instead")
)

// GitAppRegistration is an app to create from a repository.
type GitAppRegistration struct {
	Name   string
	URL    string
	Branch string
	Access string
	Token  string
}

// GitAppChanges changes a git app; nil leaves a field as it is.
type GitAppChanges struct {
	Branch     *string
	AutoDeploy *bool
	Access     *string
	Token      *string
}

// checkGitAccess refuses an access mode the URL cannot carry. hasToken says whether a
// token is given or already kept.
func checkGitAccess(remote, access string, hasToken bool) error {
	kind := gitURLKind(remote)

	switch {
	case access != git.AccessNone && access != git.AccessKey && access != git.AccessToken:
		return GitRequestError(fmt.Sprintf("access is none, key or token, not `%s`", access))
	case kind == "":
		return GitRequestError(fmt.Sprintf("`%s` is not a URL CasaOS can clone: HTTPS, SSH or file, without a password in it (a password is a token)", redactGitURL(remote)))
	case access == git.AccessKey && kind != "ssh":
		return GitRequestError("a deploy key works over SSH: give the SSH URL of the repository")
	case access == git.AccessToken && kind != "http":
		return GitRequestError("a token works over HTTPS: give the HTTPS URL of the repository")
	case access == git.AccessToken && !hasToken:
		return GitRequestError("access by token needs the token")
	}

	return nil
}

// setGitAccess changes how the app reaches its remote: a key is generated when it has
// none, a token given is kept, and a token is removed when access is not by token.
func setGitAccess(st *gitApp, access, token string) error {
	st.Access = access

	if access == git.AccessKey && !pathExists(gitAppFile(st.App, ".key")) {
		if _, err := generateGitKey(st.App); err != nil {
			return err
		}
	}

	if access == git.AccessToken && token != "" {
		return writeGitFile(gitAppFile(st.App, ".token"), []byte(token))
	}

	if access != git.AccessToken {
		if err := os.Remove(gitAppFile(st.App, ".token")); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	return nil
}

// CreateGitApp registers an app to deploy from a repository. Nothing is cloned: a check
// does that, once the owner has given the repository the deploy key.
func CreateGitApp(ctx context.Context, registration GitAppRegistration) (*GitAppView, error) {
	name := registration.Name
	if name == "" || name != loader.NormalizeProjectName(name) {
		return nil, GitRequestError(fmt.Sprintf("`%s` is not an app name: lowercase letters, digits, - and _, starting with a letter or a digit", name))
	}
	if !validGitBranch(registration.Branch) {
		return nil, GitRequestError(fmt.Sprintf("`%s` is not a branch name", registration.Branch))
	}
	if err := checkGitAccess(registration.URL, registration.Access, registration.Token != ""); err != nil {
		return nil, err
	}

	end, err := Begin(name, "registration")
	if err != nil {
		return nil, err
	}
	defer end()

	projects, err := gitDocker.Projects(ctx)
	if err != nil {
		return nil, err
	}
	if _, taken := projects[name]; taken || pathExists(gitAppFile(name, ".json")) {
		return nil, ErrGitAppNameTaken
	}

	st := &gitApp{
		App: name, Origin: gitOriginCreated, Dir: filepath.Join(gitAppsDataRoot, name),
		Remote: registration.URL, Branch: registration.Branch,
		History: []gitHistoryEntry{}, Attempted: []string{},
	}
	if err := setGitAccess(st, registration.Access, registration.Token); err != nil {
		return nil, err
	}
	if err := saveGitApp(st); err != nil {
		return nil, err
	}

	return newGitAppView(ctx, st), nil
}

// findGitApp is a registered app, or an app to adopt: a compose project whose folder is
// the top of a git work tree, described from that folder and not saved.
func findGitApp(ctx context.Context, name string) (*gitApp, error) {
	st, err := loadGitApp(name)
	if !errors.Is(err, ErrGitAppNotFound) {
		return st, err
	}

	projects, err := gitDocker.Projects(ctx)
	if err != nil {
		return nil, err
	}

	// the top only: a compose file below the root of a repository is not one this reads
	dir := projects[name]
	if dir == "" || !pathExists(filepath.Join(dir, ".git")) {
		return nil, ErrGitAppNotFound
	}

	info, err := git.Describe(ctx, dir)
	if err != nil {
		return nil, err
	}

	return &gitApp{
		App: name, Origin: gitOriginAdoptable, Dir: dir, Remote: info.RemoteURL, Branch: info.Branch,
		Access: git.AccessNone, Cloned: true, History: []gitHistoryEntry{}, Attempted: []string{},
	}, nil
}

// GetGitApp is the app as the API answers it.
func GetGitApp(ctx context.Context, name string) (*GitAppView, error) {
	st, err := findGitApp(ctx, name)
	if err != nil {
		return nil, err
	}

	return newGitAppView(ctx, st), nil
}

// adoptGitApp makes an adoptable app a git app. Its commit becomes the deployed version,
// and the images its containers run are kept under that commit's tags, so that the first
// deployment CasaOS makes has a version to roll back to.
func adoptGitApp(ctx context.Context, st *gitApp) error {
	switch {
	case st.Branch == "":
		return GitRequestError(fmt.Sprintf("%s is in detached HEAD: check a branch out to adopt it", st.Dir))
	case st.Remote == "":
		return GitRequestError(fmt.Sprintf("%s has no remote to follow", st.Dir))
	case gitURLKind(st.Remote) == "":
		return GitRequestError(fmt.Sprintf("the remote of %s, `%s`, is not a URL CasaOS can fetch: HTTPS, SSH or file, without a password in it", st.Dir, redactGitURL(st.Remote)))
	}

	info, err := git.Describe(ctx, st.Dir)
	if err != nil {
		return err
	}
	if info.Head == "" {
		return GitRequestError(fmt.Sprintf("%s has no commit yet", st.Dir))
	}

	images, err := gitDocker.TagRunning(ctx, st.App, st.Dir, info.Head)
	if err != nil {
		return err
	}

	subject, _ := git.Subject(ctx, st.Dir, info.Head)
	now := time.Now().UTC()
	st.Origin = gitOriginAdopted
	st.Deployed = &gitDeployment{Commit: info.Head, Subject: subject, At: now, Images: images}
	st.record(gitHistoryEntry{Commit: info.Head, Subject: subject, At: now, Outcome: gitOutcomeAdopted, Images: images})
	st.EnvTracked, _ = git.IsTracked(ctx, st.Dir, ".env")

	return saveGitApp(st)
}

// UpdateGitApp changes the branch, the automatic rebuild or the access of a git app, and
// adopts an adoptable one.
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

	if changes.Branch != nil && *changes.Branch != st.Branch {
		if st.Cloned {
			return nil, GitRequestError(fmt.Sprintf("the branch of %s is the one its folder is on: check another branch out there instead", name))
		}
		if !validGitBranch(*changes.Branch) {
			return nil, GitRequestError(fmt.Sprintf("`%s` is not a branch name", *changes.Branch))
		}
		st.Branch = *changes.Branch
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

	if err := saveGitApp(st); err != nil {
		return nil, err
	}

	return newGitAppView(ctx, st), nil
}

// DeleteGitApp removes a registered app no deployment has succeeded for: what a failed
// first deployment left (its containers, networks and images), the clone of a created app,
// its state and its secrets.
func DeleteGitApp(ctx context.Context, name string) error {
	end, err := Begin(name, "removal")
	if err != nil {
		return err
	}
	defer end()

	st, err := loadGitApp(name)
	if err != nil {
		return err
	}
	if st.Deployed != nil {
		return ErrGitAppDeployed
	}

	if err := gitDocker.Remove(ctx, name); err != nil {
		return err
	}
	if images := gitImagesOf(st); len(images) > 0 {
		gitDocker.RemoveImages(ctx, images)
	}

	if st.Origin == gitOriginCreated && st.Cloned {
		if err := os.RemoveAll(st.Dir); err != nil {
			return err
		}
	}

	return forgetGitApp(name)
}

// gitImagesOf is every git- tag the app's remembered versions name, sorted.
func gitImagesOf(st *gitApp) []string {
	images := []string{}
	if st.Deployed != nil {
		images = append(images, lo.Values(st.Deployed.Images)...)
	}
	for _, entry := range st.History {
		images = append(images, lo.Values(entry.Images)...)
	}
	images = lo.Uniq(images)
	sort.Strings(images)

	return images
}

// CheckGitApp is "Check now". An adoptable app is adopted here, so that a folder that
// cannot be adopted is refused with its reason; the check itself runs in the background,
// and the app is returned as the check starts.
func CheckGitApp(ctx context.Context, name string) (*GitAppView, error) {
	st, end, err := beginGitCheck(ctx, name)
	if err != nil {
		return nil, err
	}

	// read before the check runs: it changes st from here on
	view := newGitAppView(ctx, st)

	go func() {
		defer end()
		checkGitApp(context.Background(), st, true)
	}()

	return view, nil
}

// beginGitCheck claims the app for a check, adopts it when it is adoptable, and records
// the operation. The caller releases end once the check and what it starts are over.
func beginGitCheck(ctx context.Context, name string) (*gitApp, func(), error) {
	end, err := Begin(name, gitOperationCheck)
	if err != nil {
		return nil, nil, err
	}

	st, err := findGitApp(ctx, name)
	if err == nil && st.Origin == gitOriginAdoptable {
		err = adoptGitApp(ctx, st)
	}
	if err == nil {
		st.Operation = &gitOperation{Kind: gitOperationCheck, StartedAt: time.Now().UTC()}
		err = saveGitApp(st)
	}
	if err != nil {
		end()
		return nil, nil, err
	}

	return st, end, nil
}

// checkGitApp asks the remote where the branch is and records the answer, then clears the
// operation. With clone, a registered app that is not cloned yet is cloned and its compose
// files validated. Whatever fails is recorded in the check, and changes nothing else.
func checkGitApp(ctx context.Context, st *gitApp, clone bool) {
	defer func() {
		st.Operation = nil
		if err := saveGitApp(st); err != nil {
			logger.Error("the check of a git app could not be written down", zap.Error(err), zap.String("app", st.App))
		}
	}()

	previous := ""
	if st.Check != nil {
		previous = st.Check.RemoteCommit
	}
	st.Check = &gitCheck{At: time.Now().UTC(), RemoteCommit: previous}
	st.NoComposeFile = false

	auth, err := gitAuth(st)
	if err == nil && st.Branch == "" {
		st.Branch, err = git.DefaultBranch(ctx, st.Remote, auth)
	}
	commit := ""
	if err == nil {
		commit, err = git.LsRemote(ctx, st.Remote, st.Branch, auth)
	}
	if err != nil {
		st.Check.Error = err.Error()
		return
	}
	st.Check.RemoteCommit = commit

	if st.Cloned || !clone {
		return
	}

	if err := cloneGitApp(ctx, st, auth); err != nil {
		st.Check.Error = err.Error()
	}
}

// cloneGitApp clones a created app into its folder, which must be absent or empty, and
// keeps the clone only when it holds a compose file.
func cloneGitApp(ctx context.Context, st *gitApp, auth git.Auth) error {
	if entries, err := os.ReadDir(st.Dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s already exists and is not empty", st.Dir)
	}

	// beside the folder, then moved in whole: a clone cut short leaves nothing where the
	// app goes, and what is left beside it is CasaOS's own to remove
	partial := gitPartialClone(st)
	_ = os.RemoveAll(partial)
	if err := git.Clone(ctx, st.Remote, st.Branch, partial, auth); err != nil {
		_ = os.RemoveAll(partial)
		return err
	}

	if _, err := gitComposeSummary(partial); err != nil {
		_ = os.RemoveAll(partial)
		st.NoComposeFile = errors.Is(err, ErrGitNoComposeFile)

		return err
	}

	_ = os.Remove(st.Dir) // an empty folder in the way
	if err := os.Rename(partial, st.Dir); err != nil {
		_ = os.RemoveAll(partial)
		return err
	}

	st.Cloned = true
	st.EnvTracked, _ = git.IsTracked(ctx, st.Dir, ".env")

	return nil
}

// gitPartialClone is where a created app is cloned before it is moved into its folder.
func gitPartialClone(st *gitApp) string {
	return filepath.Join(filepath.Dir(st.Dir), "."+st.App+".clone")
}
```

The goroutine of `CheckGitApp` releases the app when the check ends; Task 8 hands that hold to the automatic deployment instead.

- [ ] **Step 4: Run the tests**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go vet ./service/ && go test ./service/ -count=1 -race -run 'TestRegistering|TestACheck|TestARepositoryWithout|TestAFolderInTheWay|TestAnUnreachable|TestAStackStartedByHand|TestAFolderInDetachedHead|TestTheAccessOf|TestRemovingAGitApp' -v" 2>&1 | Select-Object -Last 40
```

Expected: `--- PASS` for TestRegisteringAGitAppChecksWhatItIsGiven, TestACheckClonesAndReviewsARegisteredAppInTheBackground, TestARepositoryWithoutAComposeFileEndsWithAnExampleAndNoClone, TestAFolderInTheWayIsNotClonedInto, TestAnUnreachableRepositoryIsRecordedAndNothingElseChanges, TestAStackStartedByHandInAGitFolderIsAdoptedByTheFirstAction, TestAFolderInDetachedHeadOrWithoutARemoteIsShownButNotAdopted, TestTheAccessOfAGitAppChangesAndItsTokenIsNeverShown and TestRemovingAGitAppThatNeverRan, with no `DATA RACE`.

- [ ] **Step 5: Check the formatting**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "tr -d '\r' < service/git_apps.go | gofmt -l; tr -d '\r' < service/git_apps_internal_test.go | gofmt -l" 2>&1 | Select-Object -Last 40
```

Expected: no output.

- [ ] **Step 6: Commit**

```powershell
git -C D:\clients\casaos\CasaOS-AppManagement add service/git_apps.go service/git_apps_internal_test.go
git -C D:\clients\casaos\CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): register, adopt, change, remove and check an app deployed from git"
```

---

### Task 8: Deploying, reverting and rolling back

**Files:**
- Create: `service/git_deploy.go`
- Modify: `service/git_apps.go` (`CheckGitApp`, from Task 7)
- Modify: `service/app_operations.go` (after `Begin`, from Task 3)
- Test: `service/git_deploy_internal_test.go`

**Interfaces:**
- Consumes: Tasks 2 to 7; `ParseEnvFile` and `ComposeApp.WriteEnvFile` (`service/compose_env.go`); `PublishEventWrapper`, `common.WithProperties`.
- Produces, for Tasks 9 to 13:
  - `func DeployGitApp(ctx, name, commit string, env *string) (*GitAppView, error)` (asynchronous; `commit` empty for the remote's latest, a commit of the history for a revert)
  - `func startGitDeploy(ctx, name, commit string, env *string, trigger gitTrigger) (*GitAppView, error)`, `func deployHolding(ctx, name, commit string, env *string, trigger gitTrigger, end func()) (*GitAppView, error)`
  - `func runGitDeploy(ctx, st *gitApp, target string, revert bool, trigger gitTrigger)` (synchronous; the restore of Task 12 calls it while it holds the app)
  - `func deployGitAppAutomatically(ctx, name string, end func())` (takes the caller's hold, renamed `deploy`)
  - `func handOver(app, kind string)` (renames what holds an app; nothing happens when nothing holds it)
  - `type gitTrigger int` with `gitTriggerManual`, `gitTriggerAutomatic`; `const gitBuildLogCap = 1 << 20`
- Test helpers produced: `folderHead(t, dir) string`, `clonedTestApp(t)`, `deployTestApp(t) *gitApp`, `deployedTestApp(t, autoDeploy bool) (fake, work, first)`.

- [ ] **Step 1: Write the failing tests**

Create `service/git_deploy_internal_test.go`:

```go
package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

func folderHead(t *testing.T, dir string) string {
	t.Helper()

	info, err := git.Describe(context.Background(), dir)
	assert.NilError(t, err)

	return info.Head
}

// clonedTestApp is a created app, checked and cloned, not deployed yet.
func clonedTestApp(t *testing.T) (fake *fakeGitRuntime, work string) {
	t.Helper()

	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake = withFakeGitDocker(t)
	url, work := newTestRemote(t)

	_, err := CreateGitApp(context.Background(), GitAppRegistration{Name: "jarvis", URL: url, Access: "none"})
	assert.NilError(t, err)
	assert.Equal(t, checkTestApp(t, "jarvis").Cloned, true)

	return fake, work
}

// deployTestApp deploys the remote's latest by hand and waits for the deployment to end.
func deployTestApp(t *testing.T) *gitApp {
	t.Helper()

	_, err := DeployGitApp(context.Background(), "jarvis", "", nil)
	assert.NilError(t, err)

	return waitForGitApp(t, "jarvis")
}

// deployedTestApp is a created app whose first version runs, with its automatic rebuild
// set as asked.
func deployedTestApp(t *testing.T, autoDeploy bool) (fake *fakeGitRuntime, work, first string) {
	t.Helper()

	fake, work = clonedTestApp(t)
	first = gitShell(t, work, "git rev-parse HEAD")
	st := deployTestApp(t)
	assert.Equal(t, st.Deployed.Commit, first)

	st.AutoDeploy = autoDeploy
	assert.NilError(t, saveGitApp(st))
	fake.calls = nil

	return fake, work, first
}

func TestAFirstDeploymentWritesEnvBuildsAndStarts(t *testing.T) {
	fake, work := clonedTestApp(t)
	first := gitShell(t, work, "git rev-parse HEAD")
	env := "GREETING=hello\n"

	view, err := DeployGitApp(context.Background(), "jarvis", "", &env)
	assert.NilError(t, err)
	assert.Equal(t, view.Operation.Kind, gitOperationBuild)
	assert.Equal(t, view.Operation.Commit, first, "the commit is known as the deployment starts")
	assert.Equal(t, view.State, "building")

	st := waitForGitApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + first[:12], "retag " + first[:12], "start " + first[:12]})
	assert.Assert(t, st.Operation == nil)
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, st.Deployed.Subject, "change compose.yaml")
	assert.DeepEqual(t, st.Deployed.Images, map[string]string{"web": "jarvis-web:git-" + first[:12]})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeDeployed)

	written, err := os.ReadFile(filepath.Join(st.Dir, ".env"))
	assert.NilError(t, err)
	assert.Equal(t, string(written), env)

	log, err := os.ReadFile(gitAppFile("jarvis", ".build.log"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(log), "building "+first[:12]))

	entries, err := os.ReadDir(filepath.Join(gitAppsDir, "work"))
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 0, "the build's worktree is gone")
}

func TestANewCommitIsBuiltThenSwitchedIn(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")

	st := deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Commit, second)
	assert.Equal(t, folderHead(t, st.Dir), second)
	assert.Equal(t, len(st.History), 2)
	assert.Equal(t, st.History[1].Commit, first)
}

// The running version is never touched by a build that fails.
func TestAFailedBuildChangesNothing(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")
	fake.buildErr = errors.New("RUN make: exit status 2")

	st := deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12]})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeBuildFailed)
	assert.Assert(t, strings.Contains(st.History[0].Reason, "exit status 2"))
	assert.DeepEqual(t, st.Attempted, []string{second})
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first, "the folder did not move")
	assert.Equal(t, gitAppStateOf(st), "build_failed")

	log, err := os.ReadFile(gitAppFile("jarvis", ".build.log"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(log), "exit status 2"))
}

func TestAVersionThatDoesNotStartIsRolledBack(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")
	fake.startErrs = []error{errors.New("container jarvis-web-1 of web exited with code 1")}

	st := deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{
		"build " + second[:12], "retag " + second[:12], "start " + second[:12],
		"retag " + first[:12], "start " + first[:12],
	})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeRolledBack)
	assert.Assert(t, strings.Contains(st.History[0].Reason, "exited with code 1"))
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first)
	assert.DeepEqual(t, st.Attempted, []string{second})
	assert.Equal(t, st.Blocked, false)
	assert.Equal(t, gitAppStateOf(st), "rolled_back")
}

func TestAFirstDeploymentThatFailsHasNothingToRollBackTo(t *testing.T) {
	fake, work := clonedTestApp(t)
	first := gitShell(t, work, "git rev-parse HEAD")
	fake.startErrs = []error{errors.New("container jarvis-web-1 of web is restarting")}

	st := deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{"build " + first[:12], "retag " + first[:12], "start " + first[:12], "stop"})
	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Assert(t, st.Deployed == nil)
	assert.Equal(t, st.Blocked, false)

	// .env is still accepted: no deployment has succeeded
	env := "GREETING=again\n"
	_, err := DeployGitApp(context.Background(), "jarvis", "", &env)
	assert.NilError(t, err)
	waitForGitApp(t, "jarvis")
}

func TestARollbackThatFailsBlocksEveryAutomaticAction(t *testing.T) {
	fake, work, first := deployedTestApp(t, true)
	pushTestCommit(t, work, "index.html", "v2")
	fake.startErrs = []error{errors.New("container exited with code 1"), errors.New("port 8080 is already allocated")}

	st := deployTestApp(t)

	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Assert(t, strings.Contains(st.History[0].Reason, "exited with code 1"))
	assert.Assert(t, strings.Contains(st.History[0].Reason, "already allocated"))
	assert.Equal(t, st.Blocked, true)
	assert.Equal(t, gitAppStateOf(st), "failed")

	// a third commit, with the automatic rebuild on: nothing happens while blocked
	pushTestCommit(t, work, "index.html", "v3")
	fake.calls = nil
	checkTestApp(t, "jarvis")
	assert.DeepEqual(t, fake.Calls(), []string{})

	// a manual deployment that works lifts it
	st = deployTestApp(t)
	assert.Equal(t, st.Blocked, false)
	assert.Assert(t, st.Deployed.Commit != first)
}

// Adopting tags what the stack runs, so the first deployment CasaOS makes of it has a
// version to roll back to.
func TestAnAdoptedStackRollsBackToTheImagesItRanWhenAdopted(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	first := gitShell(t, work, "git rev-parse HEAD")
	dir := filepath.Join(t.TempDir(), "jarvis")
	assert.NilError(t, git.Clone(ctx, url, "main", dir, git.Auth{}))
	fake.projects["jarvis"] = dir
	second := pushTestCommit(t, work, "index.html", "v2")
	fake.startErrs = []error{errors.New("container exited with code 1")}

	st := deployTestApp(t)

	assert.DeepEqual(t, fake.Calls(), []string{
		"tag running " + first[:12],
		"build " + second[:12], "retag " + second[:12], "start " + second[:12],
		"retag " + first[:12], "start " + first[:12],
	})
	assert.Equal(t, st.Origin, gitOriginAdopted)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeRolledBack)
	assert.Equal(t, st.History[1].Outcome, gitOutcomeAdopted)
}

func TestADeploymentOfAFolderInDetachedHeadSaysWhy(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	dir := filepath.Join(t.TempDir(), "jarvis")
	assert.NilError(t, git.Clone(ctx, url, "main", dir, git.Auth{}))
	gitShell(t, dir, "git checkout -q --detach "+gitShell(t, work, "git rev-parse HEAD"))
	fake.projects["jarvis"] = dir

	_, err := DeployGitApp(ctx, "jarvis", "", nil)
	assertBadRequest(t, err, "detached HEAD")
	assert.DeepEqual(t, fake.Calls(), []string{})
}

func TestARevertPausesTheAutomaticRebuild(t *testing.T) {
	fake, work, first := deployedTestApp(t, true)
	second := pushTestCommit(t, work, "index.html", "v2")
	checkTestApp(t, "jarvis")
	fake.calls = nil

	view, err := DeployGitApp(context.Background(), "jarvis", first, nil)
	assert.NilError(t, err)
	assert.Equal(t, view.State, "deploying")
	st := waitForGitApp(t, "jarvis")

	// nothing is built
	assert.DeepEqual(t, fake.Calls(), []string{"retag " + first[:12], "start " + first[:12]})
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first)
	assert.Equal(t, st.AutoPaused, true)

	// the check sees the branch ahead, and leaves it
	fake.calls = nil
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, gitNewCommits(st), true)
	assert.DeepEqual(t, fake.Calls(), []string{})

	// turning the switch on again resumes it
	on := true
	_, err = UpdateGitApp(context.Background(), "jarvis", GitAppChanges{AutoDeploy: &on})
	assert.NilError(t, err)
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.Deployed.Commit, second)
}

// A check that finds a new commit ends, then the automatic rebuild starts: the owner sees
// the check end and the build begin.
func TestACheckStartsTheAutomaticRebuildOnceItHasEnded(t *testing.T) {
	fake, work, _ := deployedTestApp(t, true)
	second := pushTestCommit(t, work, "index.html", "v2")

	st := checkTestApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	assert.Equal(t, st.Deployed.Commit, second)
	assert.Equal(t, st.AutoPaused, false)
}

func TestAnAttemptedCommitIsNotRetriedAutomatically(t *testing.T) {
	fake, work, _ := deployedTestApp(t, true)
	second := pushTestCommit(t, work, "index.html", "v2")
	fake.buildErr = errors.New("no space left on device")

	checkTestApp(t, "jarvis")
	fake.buildErr = nil

	checkTestApp(t, "jarvis")

	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12]})
}

// Every precondition refuses before anything moves; an automatic deployment records why.
func TestADeploymentRefusesWhatWouldBreakTheApp(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	ctx := context.Background()
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	env := "GREETING=hello\n"

	_, err = DeployGitApp(ctx, "jarvis", "", &env)
	assertBadRequest(t, err, "first deployment only")
	_, err = DeployGitApp(ctx, "jarvis", "4f5f60c", nil)
	assertBadRequest(t, err, "not a full commit hash")
	_, err = DeployGitApp(ctx, "jarvis", first, nil)
	assertBadRequest(t, err, "cannot be reverted to")

	assert.NilError(t, os.WriteFile(filepath.Join(st.Dir, "compose.yaml"), []byte("services: {}\n"), 0o644))
	_, err = DeployGitApp(ctx, "jarvis", "", nil)
	assertBadRequest(t, err, "tracked files were modified")

	st.AutoDeploy = true
	assert.NilError(t, saveGitApp(st))
	second := pushTestCommit(t, work, "index.html", "v2")
	st = checkTestApp(t, "jarvis")
	assert.Equal(t, st.History[0].Outcome, gitOutcomeFailed)
	assert.Assert(t, strings.Contains(st.History[0].Reason, "tracked files were modified"))
	assert.DeepEqual(t, st.Attempted, []string{second})
	assert.DeepEqual(t, fake.Calls(), []string{})
}

func TestATrackedEnvIsNeverWritten(t *testing.T) {
	_, work := clonedTestApp(t)
	pushTestCommit(t, work, ".env", "GREETING=tracked\n")
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	_, err = git.Fetch(context.Background(), st.Dir, st.Remote, st.Branch, git.Auth{})
	assert.NilError(t, err)
	assert.NilError(t, git.FastForward(context.Background(), st.Dir, gitShell(t, work, "git rev-parse HEAD")))

	env := "GREETING=mine\n"
	_, err = DeployGitApp(context.Background(), "jarvis", "", &env)
	assertBadRequest(t, err, "tracks .env")
}

func TestTheGuardRefusesAnOverlap(t *testing.T) {
	deployedTestApp(t, false)
	holding(t, "jarvis")
	ctx := context.Background()
	on := true

	_, err := DeployGitApp(ctx, "jarvis", "", nil)
	assertBusy(t, err)
	_, err = CheckGitApp(ctx, "jarvis")
	assertBusy(t, err)
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{AutoDeploy: &on})
	assertBusy(t, err)
	assertBusy(t, DeleteGitApp(ctx, "jarvis"))
}

// The check's hold passes to the deployment it starts under that deployment's name: what is
// refused meanwhile is told a deployment runs, never a check.
func TestTheGuardRefusesInTheNameOfTheDeploymentACheckStarted(t *testing.T) {
	fake, work, _ := deployedTestApp(t, true)
	second := pushTestCommit(t, work, "index.html", "v2")
	fake.gate = make(chan struct{})

	_, err := CheckGitApp(context.Background(), "jarvis")
	assert.NilError(t, err)
	<-fake.gate // the automatic deployment is building

	_, err = Begin("jarvis", "update")
	fake.gate <- struct{}{}

	assert.Error(t, err, "`jarvis` is busy: deploy in progress")
	assert.Equal(t, waitForGitApp(t, "jarvis").Deployed.Commit, second)
}

func TestTheBuildLogKeepsItsLastMegabyte(t *testing.T) {
	gitAppsIn(t)
	assert.NilError(t, os.MkdirAll(gitAppsDir, 0o700))
	file, err := os.OpenFile(gitAppFile("jarvis", ".build.log"), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	assert.NilError(t, err)
	out := &gitBuildLog{ctx: context.Background(), file: file, lastSent: time.Now().Add(time.Hour)}

	line := strings.Repeat("x", 1023) + "\n"
	for i := 0; i < 3000; i++ {
		_, err := out.Write([]byte(line))
		assert.NilError(t, err)
	}
	_, err = out.Write([]byte("the last line\n"))
	assert.NilError(t, err)
	assert.NilError(t, out.Close())

	info, err := os.Stat(gitAppFile("jarvis", ".build.log"))
	assert.NilError(t, err)
	assert.Assert(t, info.Size() <= 2*gitBuildLogCap, info.Size())
	assert.Assert(t, strings.HasSuffix(readGitBuildLogTail("jarvis"), "x\nthe last line\n"))
}
```

- [ ] **Step 2: Run them to see them fail**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go test ./service/ -count=1 -run 'TestAFirstDeployment|TestANewCommit|TestAFailedBuild|TestAVersionThat|TestARollbackThat|TestAnAdoptedStack|TestADeploymentOf|TestARevertPauses|TestACheckStarts|TestAnAttemptedCommit|TestADeploymentRefuses|TestATrackedEnv|TestTheGuardRefuses|TestTheBuildLog'" 2>&1 | Select-Object -Last 40
```

Expected: FAIL, the test build stops on `undefined: DeployGitApp`.

- [ ] **Step 3: Write the deployment**

Create `service/git_deploy.go`:

```go
package service

import (
	"bytes"
	"context"
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

// A deployment of a git app: the target commit is built beside the running version,
// switched in, judged, and rolled back when it does not start. The running app is not
// touched until the build has succeeded.

type gitTrigger int

const (
	gitTriggerManual gitTrigger = iota
	gitTriggerAutomatic
)

// gitBuildLogCap is how much of the last build's log is kept.
const gitBuildLogCap = 1 << 20

var gitCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// DeployGitApp deploys commit, or the remote's latest when commit is empty, in the
// background. A commit of the history is a revert. env is written as .env first.
func DeployGitApp(ctx context.Context, name, commit string, env *string) (*GitAppView, error) {
	return startGitDeploy(ctx, name, commit, env, gitTriggerManual)
}

// startGitDeploy checks what must hold, records the operation and starts the deployment,
// and returns the app as the deployment starts. The guard is released when it ends.
func startGitDeploy(ctx context.Context, name, commit string, env *string, trigger gitTrigger) (*GitAppView, error) {
	if commit != "" && !gitCommitPattern.MatchString(commit) {
		return nil, GitRequestError(fmt.Sprintf("`%s` is not a full commit hash", commit))
	}

	end, err := Begin(name, gitOperationDeploy)
	if err != nil {
		return nil, err
	}

	return deployHolding(ctx, name, commit, env, trigger, end)
}

// deployHolding is startGitDeploy for a caller that holds the app already: end is released
// when the deployment ends, or before returning when it does not start.
func deployHolding(ctx context.Context, name, commit string, env *string, trigger gitTrigger, end func()) (*GitAppView, error) {
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
	if target == "" {
		auth, err := gitAuth(st)
		if err != nil {
			return nil, err
		}
		if target, err = git.LsRemote(ctx, st.Remote, st.Branch, auth); err != nil {
			return nil, GitRequestError(fmt.Sprintf("the repository cannot be reached: %v", err))
		}
	}

	if err := gitDeployPreconditions(ctx, st, target, env, revert); err != nil {
		if trigger == gitTriggerAutomatic {
			// nothing changes but the reason, and the commit is not tried again
			st.attempt(target)
			finishGitDeploy(ctx, st, gitHistoryEntry{Commit: target, At: time.Now().UTC(), Outcome: gitOutcomeFailed, Reason: err.Error()})
		}

		return nil, err
	}

	if env != nil {
		if err := (&ComposeApp{WorkingDir: st.Dir}).WriteEnvFile([]byte(*env)); err != nil {
			return nil, err
		}
	}

	kind := gitOperationBuild
	if revert {
		kind = gitOperationRevert
	}
	st.Operation = &gitOperation{Kind: kind, Commit: target, StartedAt: time.Now().UTC()}
	if err := saveGitApp(st); err != nil {
		return nil, err
	}

	// read before the deployment runs: it changes st from here on
	view := newGitAppView(ctx, st)

	handedOff = true
	go func() {
		defer end()
		runGitDeploy(context.Background(), st, target, revert, trigger)
	}()

	return view, nil
}

// gitDeployPreconditions is what must hold before anything is touched.
func gitDeployPreconditions(ctx context.Context, st *gitApp, target string, env *string, revert bool) error {
	if !st.Cloned {
		return GitRequestError(fmt.Sprintf("%s is not cloned yet: check it first", st.App))
	}

	info, err := git.Describe(ctx, st.Dir)
	switch {
	case err != nil:
		return GitRequestError(fmt.Sprintf("%s is not a git work tree any more: %v", st.Dir, err))
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

	if revert && !gitRevertable(ctx, st, *st.historyEntry(target)) {
		return GitRequestError(fmt.Sprintf("%s cannot be reverted to: it is the running version, it never ran, or its images are gone", target[:12]))
	}

	return nil
}

// runGitDeploy carries the deployment out and records how it ended.
func runGitDeploy(ctx context.Context, st *gitApp, target string, revert bool, trigger gitTrigger) {
	ctx = common.WithProperties(ctx, map[string]string{common.PropertyTypeAppName.Name: st.App})
	previous := st.Deployed

	auth, err := gitAuth(st)
	if err != nil {
		failGitDeploy(ctx, st, target, "", nil, gitOutcomeFailed, err)
		return
	}

	var images map[string]string
	if revert {
		images = st.historyEntry(target).Images
	} else {
		if images, err = fetchAndBuild(ctx, st, target, previous, auth); err != nil {
			return
		}

		st.Operation.Kind = gitOperationDeploy
		if err := saveGitApp(st); err != nil {
			logger.Error("the deployment could not be written down", zap.Error(err), zap.String("app", st.App))
		}
	}

	subject, _ := git.Subject(ctx, st.Dir, target)

	move := git.FastForward
	if revert {
		move = git.ResetKeep
	}
	err = move(ctx, st.Dir, target)
	if err == nil {
		err = gitDocker.Retag(ctx, st.App, st.Dir, target)
	}
	if err == nil {
		err = gitDocker.Start(ctx, st.App, st.Dir)
	}
	if err != nil {
		rollBackGitDeploy(ctx, st, target, subject, images, previous, err)
		return
	}

	st.Deployed = &gitDeployment{Commit: target, Subject: subject, At: time.Now().UTC(), Images: images}
	st.Blocked = false
	if trigger == gitTriggerManual {
		// a revert pauses the automatic rebuild, or the next check would undo it
		st.AutoPaused = revert
	}
	st.EnvTracked, _ = git.IsTracked(ctx, st.Dir, ".env")
	finishGitDeploy(ctx, st, gitHistoryEntry{Commit: target, Subject: subject, At: st.Deployed.At, Outcome: gitOutcomeDeployed, Images: images})

	go PublishEventWrapper(ctx, common.EventTypeAppGitDeployEnd, nil)
}

// fetchAndBuild fetches the branch, checks target is on it and descends from what runs,
// and builds it. A failure is recorded here.
func fetchAndBuild(ctx context.Context, st *gitApp, target string, previous *gitDeployment, auth git.Auth) (map[string]string, error) {
	head, err := git.Fetch(ctx, st.Dir, st.Remote, st.Branch, auth)
	if err != nil {
		failGitDeploy(ctx, st, target, "", nil, gitOutcomeFailed, err)
		return nil, err
	}

	onBranch, err := git.IsAncestor(ctx, st.Dir, target, head)
	if err == nil && !onBranch {
		err = fmt.Errorf("%s is not on the branch %s", target[:12], st.Branch)
	}
	if err == nil && previous != nil {
		var descends bool
		if descends, err = git.IsAncestor(ctx, st.Dir, previous.Commit, target); err == nil && !descends {
			err = fmt.Errorf("%s does not descend from the deployed %s", target[:12], previous.Commit[:12])
		}
	}
	if err != nil {
		failGitDeploy(ctx, st, target, "", nil, gitOutcomeFailed, err)
		return nil, err
	}

	subject, _ := git.Subject(ctx, st.Dir, target)

	images, err := buildGitCommit(ctx, st, target, auth)
	if err != nil {
		failGitDeploy(ctx, st, target, subject, nil, gitOutcomeBuildFailed, err)
		return nil, err
	}

	return images, nil
}

// buildGitCommit builds commit in a worktree of its own, with the build's output going to
// the app's build log and to the bus.
func buildGitCommit(ctx context.Context, st *gitApp, commit string, auth git.Auth) (map[string]string, error) {
	file, err := os.OpenFile(gitAppFile(st.App, ".build.log"), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	out := &gitBuildLog{ctx: ctx, file: file}
	defer out.Close()

	go PublishEventWrapper(ctx, common.EventTypeAppGitBuildBegin, nil)

	images, err := func() (map[string]string, error) {
		worktree := filepath.Join(gitAppsDir, "work", st.App+"-"+commit[:12])
		if err := git.AddWorktree(ctx, st.Dir, commit, worktree, auth); err != nil {
			return nil, err
		}
		defer func() {
			if err := git.RemoveWorktree(context.Background(), st.Dir, worktree); err != nil {
				logger.Error("a build's worktree could not be removed", zap.Error(err), zap.String("path", worktree))
			}
		}()

		return gitDocker.Build(ctx, st.App, worktree, filepath.Join(st.Dir, ".env"), commit, out)
	}()
	if err != nil {
		fmt.Fprintf(out, "\nthe build of %s failed: %v\n", commit[:12], err)
		go PublishEventWrapper(ctx, common.EventTypeAppGitBuildError, map[string]string{common.PropertyTypeMessage.Name: err.Error()})

		return nil, err
	}

	go PublishEventWrapper(ctx, common.EventTypeAppGitBuildEnd, nil)

	return images, nil
}

// rollBackGitDeploy puts the previous version back after target did not start: its
// commit, its images, and a start. A created app's first version has nothing before it
// and stays stopped with its log.
func rollBackGitDeploy(ctx context.Context, st *gitApp, target, subject string, images map[string]string, previous *gitDeployment, cause error) {
	if previous == nil {
		if err := gitDocker.Stop(ctx, st.App); err != nil {
			logger.Error("a first version that did not start could not be stopped", zap.Error(err), zap.String("app", st.App))
		}
		failGitDeploy(ctx, st, target, subject, images, gitOutcomeFailed, cause)

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
		failGitDeploy(ctx, st, target, subject, images, gitOutcomeFailed,
			fmt.Errorf("%w; the rollback to %s failed too: %v", cause, previous.Commit[:12], err))

		return
	}

	st.Blocked = false
	failGitDeploy(ctx, st, target, subject, images, gitOutcomeRolledBack, cause)
}

// failGitDeploy records a deployment that did not end with target running, and says so.
func failGitDeploy(ctx context.Context, st *gitApp, target, subject string, images map[string]string, outcome string, cause error) {
	st.attempt(target)
	finishGitDeploy(ctx, st, gitHistoryEntry{
		Commit: target, Subject: subject, At: time.Now().UTC(), Outcome: outcome, Reason: cause.Error(), Images: images,
	})

	if outcome != gitOutcomeBuildFailed {
		go PublishEventWrapper(ctx, common.EventTypeAppGitDeployError, map[string]string{common.PropertyTypeMessage.Name: cause.Error()})
	}
}

// finishGitDeploy records how a deployment ended, clears the operation, and removes the
// images no remembered version names any more.
func finishGitDeploy(ctx context.Context, st *gitApp, entry gitHistoryEntry) {
	st.Operation = nil
	if removable := st.record(entry); len(removable) > 0 {
		gitDocker.RemoveImages(ctx, removable)
	}

	if err := saveGitApp(st); err != nil {
		logger.Error("the end of a deployment could not be written down", zap.Error(err), zap.String("app", st.App))
	}
}

// deployGitAppAutomatically starts the deployment of what the check holding the app saw,
// when the app's automatic rebuild may act on it. The check's hold passes to the
// deployment under the deployment's name, so nothing starts in between and what is refused
// meanwhile is told a deployment runs; end is released either way.
func deployGitAppAutomatically(ctx context.Context, name string, end func()) {
	st, err := loadGitApp(name)
	if err != nil || !st.AutoDeploy || st.AutoPaused || st.Blocked || st.Deployed == nil || st.Check == nil || st.Check.Error != "" ||
		st.Check.RemoteCommit == "" || st.Check.RemoteCommit == st.Deployed.Commit || lo.Contains(st.Attempted, st.Check.RemoteCommit) {
		end()
		return
	}

	handOver(name, gitOperationDeploy)
	if _, err := deployHolding(ctx, name, st.Check.RemoteCommit, nil, gitTriggerAutomatic, end); err != nil {
		logger.Info("no automatic deployment", zap.String("app", name), zap.Error(err))
	}
}

// gitBuildLog is a build's output: written to the app's build log, whose last megabyte is
// kept, and sent to the bus a batch of lines at most every second.
type gitBuildLog struct {
	mu       sync.Mutex
	ctx      context.Context
	file     *os.File
	written  int64
	pending  bytes.Buffer
	lastSent time.Time
}

func (l *gitBuildLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// a log that cannot be written is not a build that failed
	if n, err := l.file.Write(p); err == nil {
		l.written += int64(n)
	}
	if l.written > 2*gitBuildLogCap {
		l.keepTail()
	}

	l.pending.Write(p)
	if time.Since(l.lastSent) >= time.Second {
		l.send(false)
	}

	return len(p), nil
}

// keepTail cuts the log down to its last megabyte.
func (l *gitBuildLog) keepTail() {
	tail := make([]byte, gitBuildLogCap)
	n, _ := l.file.ReadAt(tail, l.written-gitBuildLogCap)
	if err := l.file.Truncate(0); err != nil {
		return
	}
	n, _ = l.file.WriteAt(tail[:n], 0)
	l.written = int64(n)
	_, _ = l.file.Seek(l.written, 0)
}

// send publishes the complete lines written since the last time, or everything at the end.
func (l *gitBuildLog) send(everything bool) {
	text := l.pending.String()
	if !everything {
		text = text[:strings.LastIndexByte(text, '\n')+1]
	}
	if text == "" {
		return
	}

	l.pending.Next(len(text))
	l.lastSent = time.Now()
	PublishEventWrapper(l.ctx, common.EventTypeAppGitBuildProgress, map[string]string{common.PropertyTypeMessage.Name: text})
}

func (l *gitBuildLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.send(true)

	return l.file.Close()
}
```

- [ ] **Step 4: Hand the check's hold to the automatic deployment**

In `service/app_operations.go`, replace:

```go
			delete(appOperations.running, app)
		})
	}, nil
}
```

with:

```go
			delete(appOperations.running, app)
		})
	}, nil
}

// handOver renames what holds app when an operation passes its hold to one it starts, so
// that a refusal names what really runs.
func handOver(app, kind string) {
	appOperations.Lock()
	defer appOperations.Unlock()

	if _, ok := appOperations.running[app]; ok {
		appOperations.running[app] = kind
	}
}
```

In `service/git_apps.go`, replace:

```go
// CheckGitApp is "Check now". An adoptable app is adopted here, so that a folder that
// cannot be adopted is refused with its reason; the check itself runs in the background,
// and the app is returned as the check starts.
func CheckGitApp(ctx context.Context, name string) (*GitAppView, error) {
	st, end, err := beginGitCheck(ctx, name)
	if err != nil {
		return nil, err
	}

	// read before the check runs: it changes st from here on
	view := newGitAppView(ctx, st)

	go func() {
		defer end()
		checkGitApp(context.Background(), st, true)
	}()

	return view, nil
}
```

with:

```go
// CheckGitApp is "Check now". An adoptable app is adopted here, so that a folder that
// cannot be adopted is refused with its reason; the check itself runs in the background,
// then whatever the automatic rebuild may deploy, and the app is returned as the check
// starts.
func CheckGitApp(ctx context.Context, name string) (*GitAppView, error) {
	st, end, err := beginGitCheck(ctx, name)
	if err != nil {
		return nil, err
	}

	// read before the check runs: it changes st from here on
	view := newGitAppView(ctx, st)

	go func() {
		ctx := context.Background()
		checkGitApp(ctx, st, true)
		deployGitAppAutomatically(ctx, name, end)
	}()

	return view, nil
}
```

The check has cleared its operation by then, so the owner sees the check end and, when the automatic rebuild acts, `building` begin; nothing else can start in between. `deployGitAppAutomatically` renames the hold `deploy` with `handOver` as it passes, so a request refused while the deployment runs names the deployment, not the check.

- [ ] **Step 5: Run the tests with the race detector**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go vet ./service/ && go test ./service/ -count=1 -race -run 'Git|TestA|TestAn|TestRegistering|TestRemoving|TestNewCommits|TestThe' -v" 2>&1 | Select-Object -Last 40
```

Expected: `--- PASS` for every test of Tasks 4 to 8, among them TestAFirstDeploymentWritesEnvBuildsAndStarts, TestANewCommitIsBuiltThenSwitchedIn, TestAFailedBuildChangesNothing, TestAVersionThatDoesNotStartIsRolledBack, TestAFirstDeploymentThatFailsHasNothingToRollBackTo, TestARollbackThatFailsBlocksEveryAutomaticAction, TestAnAdoptedStackRollsBackToTheImagesItRanWhenAdopted, TestADeploymentOfAFolderInDetachedHeadSaysWhy, TestARevertPausesTheAutomaticRebuild, TestACheckStartsTheAutomaticRebuildOnceItHasEnded, TestAnAttemptedCommitIsNotRetriedAutomatically, TestADeploymentRefusesWhatWouldBreakTheApp, TestATrackedEnvIsNeverWritten, TestTheGuardRefusesAnOverlap, TestTheGuardRefusesInTheNameOfTheDeploymentACheckStarted and TestTheBuildLogKeepsItsLastMegabyte; no `DATA RACE`; `ok` for `service`.

- [ ] **Step 6: Check the formatting**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "tr -d '\r' < service/git_deploy.go | gofmt -l; tr -d '\r' < service/git_deploy_internal_test.go | gofmt -l; tr -d '\r' < service/git_apps.go | gofmt -l; tr -d '\r' < service/app_operations.go | gofmt -l" 2>&1 | Select-Object -Last 40
```

Expected: no output.

- [ ] **Step 7: Commit**

```powershell
git -C D:\clients\casaos\CasaOS-AppManagement add service/git_deploy.go service/git_deploy_internal_test.go service/git_apps.go service/app_operations.go
git -C D:\clients\casaos\CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): build beside the running version, switch, roll back, revert"
```

---

### Task 9: The check every five minutes, and what startup finds

**Files:**
- Create: `service/git_lifecycle.go`
- Modify: `main.go:89-91` (after `RecoverHeldApps`), `main.go:149` (a cron entry before the backups')
- Test: `service/git_lifecycle_internal_test.go`

**Interfaces:**
- Consumes: `GitAppNames`, `loadGitApp`, `saveGitApp`, `gitPartialClone` (Tasks 5, 7); `beginGitCheck`, `checkGitApp` (Task 7); `deployGitAppAutomatically` (Task 8); `git.Subject`, `git.Prune` (Task 2).
- Produces: `func CheckGitApps(ctx context.Context)` (every registered app, one after another, never cloning); `func RecoverGitApps(ctx context.Context)` (an operation left running becomes an `interrupted` history entry whose commit joins `attempted`, a check left running is cleared, `work/` is emptied, `worktree prune` runs in every cloned app, a partial clone is removed).

- [ ] **Step 1: Write the failing tests**

Create `service/git_lifecycle_internal_test.go`:

```go
package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"gotest.tools/v3/assert"
)

// The schedule checks every registered app: what the automatic rebuild may deploy starts,
// and an app waiting for its owner's first check is not cloned behind their back.
func TestThePeriodicCheckDeploysWhatItMayAndClonesNothing(t *testing.T) {
	fake, work, _ := deployedTestApp(t, true)
	second := pushTestCommit(t, work, "index.html", "v2")
	url, _ := newTestRemote(t)
	_, err := CreateGitApp(context.Background(), GitAppRegistration{Name: "waiting", URL: url, Access: "none"})
	assert.NilError(t, err)

	CheckGitApps(context.Background())
	st := waitForGitApp(t, "jarvis")

	assert.Equal(t, st.Deployed.Commit, second)
	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})

	waiting, err := loadGitApp("waiting")
	assert.NilError(t, err)
	assert.Assert(t, waiting.Check != nil && waiting.Check.Error == "", "a registered app is checked")
	assert.Equal(t, waiting.Cloned, false, "and not cloned: that is for its owner's check")
}

func TestStartupRecordsAnInterruptedOperationAndCleansUp(t *testing.T) {
	_, work, first := deployedTestApp(t, false)
	second := pushTestCommit(t, work, "index.html", "v2")
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	st.Operation = &gitOperation{Kind: gitOperationBuild, Commit: second, StartedAt: time.Now().UTC()}
	assert.NilError(t, saveGitApp(st))
	_, err = git.Fetch(context.Background(), st.Dir, st.Remote, st.Branch, git.Auth{})
	assert.NilError(t, err)
	worktree := filepath.Join(gitAppsDir, "work", "jarvis-"+second[:12])
	assert.NilError(t, git.AddWorktree(context.Background(), st.Dir, second, worktree, git.Auth{}))
	partial := filepath.Join(gitAppsDataRoot, ".other.clone")
	assert.NilError(t, os.MkdirAll(partial, 0o755))
	assert.NilError(t, saveGitApp(&gitApp{App: "other", Origin: gitOriginCreated, Dir: filepath.Join(gitAppsDataRoot, "other")}))

	RecoverGitApps(context.Background())

	st, err = loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Assert(t, st.Operation == nil)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeInterrupted)
	assert.Equal(t, st.History[0].Commit, second)
	assert.DeepEqual(t, st.Attempted, []string{second})
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, gitAppStateOf(st), "interrupted")
	_, err = os.Stat(worktree)
	assert.Assert(t, os.IsNotExist(err))
	assert.Equal(t, gitShell(t, st.Dir, "git worktree list | wc -l"), "1")
	_, err = os.Stat(partial)
	assert.Assert(t, os.IsNotExist(err), "a clone cut short is removed")
}
```

- [ ] **Step 2: Run them to see them fail**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go test ./service/ -count=1 -run 'TestThePeriodicCheck|TestStartupRecords'" 2>&1 | Select-Object -Last 40
```

Expected: FAIL, `undefined: CheckGitApps` and `undefined: RecoverGitApps`.

- [ ] **Step 3: Write the check of every app and the startup recovery**

Create `service/git_lifecycle.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// Git apps across the life of the process: the periodic check, and what a stop in the
// middle of an operation left behind.

// CheckGitApps checks every registered git app, one after another, and starts what the
// automatic rebuild may deploy. An app busy with another operation is checked next time.
func CheckGitApps(ctx context.Context) {
	names, err := GitAppNames()
	if err != nil {
		logger.Error("cannot list the git apps to check", zap.Error(err))
		return
	}

	for _, name := range names {
		st, end, err := beginGitCheck(ctx, name)
		if err != nil {
			if !errors.As(err, new(ErrAppBusy)) {
				logger.Error("cannot check a git app", zap.Error(err), zap.String("app", name))
			}
			continue
		}

		checkGitApp(ctx, st, false)
		deployGitAppAutomatically(ctx, name, end)
	}
}

// RecoverGitApps runs once, at startup. An operation the process did not live to finish is
// recorded as interrupted and cleared, and nothing is retried: the commit joins those the
// automatic rebuild leaves alone. The worktrees of builds and the clones cut short are
// removed.
func RecoverGitApps(ctx context.Context) {
	names, err := GitAppNames()
	if err != nil {
		logger.Error("cannot list the git apps to recover", zap.Error(err))
		return
	}

	for _, name := range names {
		st, err := loadGitApp(name)
		if err != nil {
			logger.Error("cannot read a git app", zap.Error(err), zap.String("app", name))
			continue
		}

		if st.Origin == gitOriginCreated && !st.Cloned {
			_ = os.RemoveAll(gitPartialClone(st))
		}

		if st.Operation == nil {
			continue
		}

		operation := st.Operation
		st.Operation = nil
		if operation.Kind != gitOperationCheck {
			subject := ""
			if st.Cloned && operation.Commit != "" {
				subject, _ = git.Subject(ctx, st.Dir, operation.Commit)
			}
			st.attempt(operation.Commit)
			st.record(gitHistoryEntry{
				Commit: operation.Commit, Subject: subject, At: time.Now().UTC(), Outcome: gitOutcomeInterrupted,
				Reason: fmt.Sprintf("AppManagement stopped during the %s started at %s", operation.Kind, operation.StartedAt.Format(time.RFC3339)),
			})
		}

		if err := saveGitApp(st); err != nil {
			logger.Error("cannot record an interrupted operation", zap.Error(err), zap.String("app", name))
		}
	}

	work := filepath.Join(gitAppsDir, "work")
	entries, _ := os.ReadDir(work)
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(work, entry.Name())); err != nil {
			logger.Error("cannot remove a build's worktree", zap.Error(err), zap.String("path", entry.Name()))
		}
	}

	for _, name := range names {
		if st, err := loadGitApp(name); err == nil && st.Cloned {
			if err := git.Prune(ctx, st.Dir); err != nil {
				logger.Error("cannot prune the worktrees of a git app", zap.Error(err), zap.String("app", name))
			}
		}
	}
}
```

- [ ] **Step 4: Wire both into `main.go`**

In `main.go`, replace:

```go
		service.RecoverHeldApps(service.MyService.Docker())

		config.RemoveRuntimeIfNoNvidiaGPUFlag = *removeRuntimeIfNoNvidiaGPUFlag
```

with:

```go
		service.RecoverHeldApps(service.MyService.Docker())

		// A deployment or a check of a git app the last process did not live to finish is
		// recorded as interrupted, and what its build left is removed. Once, here, for the
		// same reason as above: an operation running now would be taken for one to undo.
		service.RecoverGitApps(ctx)

		config.RemoveRuntimeIfNoNvidiaGPUFlag = *removeRuntimeIfNoNvidiaGPUFlag
```

In `main.go`, replace:

```go
		// Backups that go off by themselves. A minute because the smallest thing
```

with:

```go
		// Apps deployed from git: where each branch is now, and what the automatic rebuild
		// may deploy. One ls-remote per app, which any forge answers in a second.
		if _, err := crontab.AddFunc("@every 5m", func() {
			service.CheckGitApps(ctx)
		}); err != nil {
			panic(err)
		}

		// Backups that go off by themselves. A minute because the smallest thing
```

A run that overlaps the previous one is harmless: an app still held by its check or its deployment answers `ErrAppBusy` and is checked next time.

- [ ] **Step 5: Build and run the tests**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go build ./... && go vet . ./service/ && go test ./service/ -count=1 -race -run 'TestThePeriodicCheck|TestStartupRecords' -v" 2>&1 | Select-Object -Last 40
```

Expected: `--- PASS` for TestThePeriodicCheckDeploysWhatItMayAndClonesNothing and TestStartupRecordsAnInterruptedOperationAndCleansUp.

- [ ] **Step 6: Check the formatting**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "tr -d '\r' < service/git_lifecycle.go | gofmt -l; tr -d '\r' < service/git_lifecycle_internal_test.go | gofmt -l; tr -d '\r' < main.go | gofmt -l" 2>&1 | Select-Object -Last 40
```

Expected: no output.

- [ ] **Step 7: Commit**

```powershell
git -C D:\clients\casaos\CasaOS-AppManagement add service/git_lifecycle.go service/git_lifecycle_internal_test.go main.go
git -C D:\clients\casaos\CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): check every git app every five minutes, recover at startup"
```

---

### Task 10: The routes

**Files:**
- Modify: `api/app_management/openapi.yaml:31-33` (tags), `:91-98` (tag groups), `:1248` (paths, before the `/web/sync/appstore` comment), `:1372` (parameter), `:1457` (request bodies), `:1657` (response), and the schemas before `GitAppState` (added in Task 6)
- Create: `route/v2/git.go`
- Test: `route/v2/git_internal_test.go`, `route/v2_git_test.go`

**Interfaces:**
- Consumes: `CreateGitApp`, `GetGitApp`, `UpdateGitApp`, `DeleteGitApp`, `CheckGitApp`, `GitRequestError`, `ErrGitAppNotFound`, `ErrGitAppNameTaken`, `ErrGitAppDeployed` (Task 7); `DeployGitApp` (Task 8); `conflictOrServerError` (Task 3).
- Produces: the six operations `createGitApp`, `getGitApp`, `updateGitApp`, `deleteGitApp`, `checkGitApp`, `deployGitApp`; generated `codegen.GitAppName = string`, `codegen.GitAppCreateRequest{Access GitAccess; Branch *string; Name string; Token *string; Url string}`, `codegen.GitAppUpdateRequest{Access *GitAccess; AutoDeploy *bool; Branch *string; Token *string}`, `codegen.GitAppDeployRequest{Commit *string; Env *string}` with `GitAccess = string`; handlers `(*AppManagement).CreateGitApp(ctx)`, `GetGitApp(ctx, app)`, `UpdateGitApp(ctx, app)`, `DeleteGitApp(ctx, app)`, `CheckGitApp(ctx, app)`, `DeployGitApp(ctx, app)`; `func gitAppError(ctx echo.Context, err error) error`.

- [ ] **Step 1: Write the failing tests**

Create `route/v2/git_internal_test.go`:

```go
package v2

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

// Each refusal of the service reaches the dashboard as the status the API promises, with
// the service's own words.
func TestAGitAppRefusalIsAnsweredWithItsStatus(t *testing.T) {
	answer := func(err error) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), recorder)
		assert.NilError(t, gitAppError(ctx, err))

		return recorder
	}

	for err, status := range map[error]int{
		service.GitRequestError("tracked files were modified in /DATA/AppData/jarvis"): http.StatusBadRequest,
		fmt.Errorf("jarvis: %w", service.ErrGitAppNotFound):                            http.StatusNotFound,
		service.ErrGitAppNameTaken:                                                     http.StatusConflict,
		service.ErrGitAppDeployed:                                                      http.StatusConflict,
		service.ErrAppBusy{App: "jarvis", Running: "deploy"}:                           http.StatusConflict,
		errors.New("disk full"):                                                        http.StatusInternalServerError,
	} {
		recorder := answer(err)
		assert.Equal(t, recorder.Code, status, err.Error())
		assert.Equal(t, recorder.Body.String(), fmt.Sprintf("{\"message\":%q}\n", err.Error()))
	}
}

func TestAGitAppIsAnsweredUnderData(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), recorder)

	assert.NilError(t, gitAppAnswer(ctx, http.StatusAccepted, &service.GitAppView{App: "jarvis", State: "building"}, nil))

	assert.Equal(t, recorder.Code, http.StatusAccepted)
	assert.Assert(t, len(recorder.Body.String()) > 0)
	assert.Equal(t, recorder.Body.String()[:36], `{"data":{"app":"jarvis","origin":"",`)
}
```

Create `route/v2_git_test.go`:

```go
package route

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"gotest.tools/v3/assert"
)

// The git routes go through the same router as every v2 route: its authentication and its
// validation of the request against the spec, which a route missing from the spec fails.
func TestTheGitRoutesAreServed(t *testing.T) {
	runtime := t.TempDir()
	previous := config.CommonInfo.RuntimePath
	config.CommonInfo.RuntimePath = runtime
	t.Cleanup(func() { config.CommonInfo.RuntimePath = previous })
	assert.NilError(t, os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret"), 0o600))

	router := InitV2Router()

	for _, call := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPost, "/v2/app_management/git", `{"name":"Not A Name","url":"https://example.invalid/r.git","access":"none"}`, http.StatusBadRequest},
		{http.MethodGet, "/v2/app_management/git/no-such-app", "", http.StatusNotFound},
		{http.MethodPut, "/v2/app_management/git/no-such-app", `{"auto_deploy":true}`, http.StatusNotFound},
		{http.MethodPost, "/v2/app_management/git/no-such-app/check", "", http.StatusNotFound},
		{http.MethodPost, "/v2/app_management/git/no-such-app/deploy", "", http.StatusNotFound},
		{http.MethodPost, "/v2/app_management/git/no-such-app/deploy", `{"commit":"4f5f60c"}`, http.StatusBadRequest},
		{http.MethodDelete, "/v2/app_management/git/no-such-app", "", http.StatusNotFound},
	} {
		request := httptest.NewRequest(call.method, call.path, strings.NewReader(call.body))
		request.RemoteAddr = "127.0.0.1:40000"
		request.Header.Set("Authorization", "Internal s3cret")
		if call.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)

		assert.Equal(t, recorder.Code, call.want, "%s %s: %s", call.method, call.path, recorder.Body.String())
	}
}
```

The second test goes through the real router: its internal authentication, and the validation of each request against the spec. Its 404s ask the daemon for the compose projects, so it needs the socket.

- [ ] **Step 2: Run them to see them fail**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go test ./route/ ./route/v2/ -count=1 -run 'TestTheGitRoutes|TestAGitApp'" 2>&1 | Select-Object -Last 40
```

Expected: FAIL: `route/v2` stops on `undefined: gitAppError`; `route` fails with `400 (recorder.Code int) != 404` for `GET /v2/app_management/git/no-such-app`, whose body is `{"message":"no matching operation was found"}`: the spec does not have the routes yet.

- [ ] **Step 3: Declare the routes and their schemas**

In `api/app_management/openapi.yaml`, replace:

```yaml
  - name: Image methods
    description: |-
      methods for managing container app images
```

with:

```yaml
  - name: Image methods
    description: |-
      methods for managing container app images

  - name: Git methods
    description: |-
      methods for apps deployed from their git repository
```

In `api/app_management/openapi.yaml`, replace:

```yaml
      - Container methods
      - Image methods

  - name: Schemas
```

with:

```yaml
      - Container methods
      - Image methods
      - Git methods

  - name: Schemas
```

In `api/app_management/openapi.yaml`, replace:

```yaml
  # the endpoint is newer version of POST /appstore for the demand of @ETWang1991 
```

with:

```yaml
  /git:
    post:
      summary: Register an app deployed from its git repository
      description: |
        Registers the app; nothing is cloned until `POST /git/{app}/check`. With `access: key` a
        deploy key is generated and its public half returned, to be added to the repository as a
        read-only key. With `access: token` the token is kept on this machine and never returned.

        400 for a name that is not a compose project name, a URL CasaOS cannot clone (HTTPS, SSH
        or file, without a password), or an access the URL cannot carry. 409 for a name used by
        an app, a compose project or another git app.
      operationId: createGitApp
      tags:
        - Git methods
      requestBody:
        $ref: "#/components/requestBodies/RequestGitAppCreate"
      responses:
        "200":
          $ref: "#/components/responses/GitAppOK"
        "400":
          $ref: "#/components/responses/ResponseBadRequest"
        "409":
          $ref: "#/components/responses/ResponseConflict"
        "500":
          $ref: "#/components/responses/ResponseInternalServerError"

  /git/{app}:
    get:
      summary: A git app, or a compose app whose folder is a git work tree
      description: |
        The state, the public key, and once cloned the compose summary and the `.env` template.
        A compose app whose folder is the top of a git work tree answers with `origin: adoptable`;
        the first `PUT`, `check` or `deploy` adopts it. 404 for any other app.
      operationId: getGitApp
      tags:
        - Git methods
      parameters:
        - $ref: "#/components/parameters/GitAppName"
      responses:
        "200":
          $ref: "#/components/responses/GitAppOK"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "500":
          $ref: "#/components/responses/ResponseInternalServerError"

    put:
      summary: Change the branch, the automatic rebuild or the access of a git app
      description: |
        Every field is optional. The branch changes until the app is cloned. Turning
        `auto_deploy` on clears `auto_paused`. A token is accepted and never returned; an empty
        one is refused, and switching `access` to `none` or `key` forgets the one kept. Adopts an
        adoptable app: 400 when its folder is in detached HEAD or has no remote.
      operationId: updateGitApp
      tags:
        - Git methods
      parameters:
        - $ref: "#/components/parameters/GitAppName"
      requestBody:
        $ref: "#/components/requestBodies/RequestGitAppUpdate"
      responses:
        "200":
          $ref: "#/components/responses/GitAppOK"
        "400":
          $ref: "#/components/responses/ResponseBadRequest"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "409":
          $ref: "#/components/responses/ResponseConflict"
        "500":
          $ref: "#/components/responses/ResponseInternalServerError"

    delete:
      summary: Remove a git app no deployment has succeeded for
      description: |
        Allowed while `deployed` is null, whatever the history: the containers, networks and
        images a failed first deployment left, the clone of a created app, the state and the
        secrets go. 409 once a deployment has succeeded (uninstall the app instead) or while an
        operation runs.
      operationId: deleteGitApp
      tags:
        - Git methods
      parameters:
        - $ref: "#/components/parameters/GitAppName"
      responses:
        "200":
          $ref: "#/components/responses/ResponseOK"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "409":
          $ref: "#/components/responses/ResponseConflict"
        "500":
          $ref: "#/components/responses/ResponseInternalServerError"

  /git/{app}/check:
    post:
      summary: Check the repository now
      description: |
        Starts a check in the background and answers 202 with `operation.kind` `check`; poll
        `GET /git/{app}` until `operation` is null. The check asks the remote where the branch
        is; for a registered app not cloned yet it clones it and validates its compose files. A
        clone without a compose file is removed, `check.error` says so and `compose_example`
        holds a compose file to add. When the automatic rebuild may act on a new commit, its
        deployment starts once the check has ended. Adopts an adoptable app: 400 when its folder
        is in detached HEAD or has no remote.
      operationId: checkGitApp
      tags:
        - Git methods
      parameters:
        - $ref: "#/components/parameters/GitAppName"
      responses:
        "202":
          $ref: "#/components/responses/GitAppOK"
        "400":
          $ref: "#/components/responses/ResponseBadRequest"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "409":
          $ref: "#/components/responses/ResponseConflict"
        "500":
          $ref: "#/components/responses/ResponseInternalServerError"

  /git/{app}/deploy:
    post:
      summary: Deploy a git app
      description: |
        Starts the deployment in the background and answers 202 with the operation running. No
        commit: the remote's latest. A commit of the history: a revert, which pauses the
        automatic rebuild. `env` is written as `.env` first, while no deployment has succeeded
        and never when the repository tracks `.env`. 400 when a precondition fails, the message
        saying which: the folder not on its branch, tracked files modified, a revert to a version
        whose images are gone. Adopts an adoptable app.
      operationId: deployGitApp
      tags:
        - Git methods
      parameters:
        - $ref: "#/components/parameters/GitAppName"
      requestBody:
        $ref: "#/components/requestBodies/RequestGitAppDeploy"
      responses:
        "202":
          $ref: "#/components/responses/GitAppOK"
        "400":
          $ref: "#/components/responses/ResponseBadRequest"
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "409":
          $ref: "#/components/responses/ResponseConflict"
        "500":
          $ref: "#/components/responses/ResponseInternalServerError"

  # the endpoint is newer version of POST /appstore for the demand of @ETWang1991 
```

In `api/app_management/openapi.yaml`, replace:

```yaml
    ComposeContainerID:
      name: containerId
```

with:

```yaml
    GitAppName:
      name: app
      description: Name of a git app, which is its compose project name
      in: path
      required: true
      schema:
        type: string
        example: jarvis

    ComposeContainerID:
      name: containerId
```

In `api/app_management/openapi.yaml`, replace:

```yaml
  requestBodies:
    RequestComposeAppUpdates:
```

with:

```yaml
  requestBodies:
    RequestGitAppCreate:
      required: true
      content:
        application/json:
          schema:
            $ref: "#/components/schemas/GitAppCreateRequest"

    RequestGitAppUpdate:
      required: true
      content:
        application/json:
          schema:
            $ref: "#/components/schemas/GitAppUpdateRequest"

    RequestGitAppDeploy:
      required: false
      content:
        application/json:
          schema:
            $ref: "#/components/schemas/GitAppDeployRequest"

    RequestComposeAppUpdates:
```

In `api/app_management/openapi.yaml`, replace:

```yaml
    ComposeAppUpdatePlanOK:
      description: OK
```

with:

```yaml
    GitAppOK:
      description: OK
      content:
        application/json:
          schema:
            allOf:
              - $ref: "#/components/schemas/BaseResponse"
              - properties:
                  data:
                    $ref: "#/components/schemas/GitApp"

    ComposeAppUpdatePlanOK:
      description: OK
```

In `api/app_management/openapi.yaml`, replace:

```yaml
    GitAppState:
      description: |
        `building` and `deploying` while a deployment runs.
```

with:

```yaml
    GitAccess:
      description: |
        How the repository is reached: `none` for a public one, `key` for a deploy key CasaOS
        generates (SSH URLs), `token` for a personal access token (HTTPS URLs).

        One of `none`, `key`, `token`. Not declared as an enum, for the reason given on
        `GitAppState`.
      type: string
      example: key

    GitApp:
      description: |
        An app deployed from its git repository (`created` or `adopted`), or a compose app
        whose folder is a git work tree and that can be adopted (`adoptable`).
      required:
        - app
        - origin
        - dir
        - remote
        - branch
        - access
        - token_set
        - auto_deploy
        - auto_paused
        - blocked
        - env_tracked
        - cloned
        - head
        - deployed
        - check
        - new_commits
        - state
        - operation
        - history
        - compose
        - compose_example
        - env_template
        - build_log
      properties:
        app:
          type: string
          example: jarvis
        origin:
          type: string
          description: One of `created`, `adopted`, `adoptable`.
          example: created
        dir:
          type: string
          example: /DATA/AppData/jarvis
        remote:
          type: string
          description: Empty for a folder with no remote, which cannot be adopted.
          example: git@github.com:owner/jarvis.git
        branch:
          type: string
          description: Empty for a folder in detached HEAD, which cannot be adopted.
          example: main
        access:
          $ref: "#/components/schemas/GitAccess"
        public_key:
          type: string
          description: The deploy key to add to the repository, read-only. Only with `access` `key`.
          example: ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... casaos-jarvis
        token_set:
          type: boolean
          description: Whether a token is kept. The token itself is never returned.
        auto_deploy:
          type: boolean
        auto_paused:
          type: boolean
          description: Set by a manual revert; cleared by turning `auto_deploy` on again or by a manual deployment.
        blocked:
          type: boolean
          description: A rollback failed; nothing automatic runs until a manual deployment or revert.
        env_tracked:
          type: boolean
          description: The repository tracks `.env`, which CasaOS then never writes.
        cloned:
          type: boolean
        head:
          $ref: "#/components/schemas/GitAppHead"
        deployed:
          $ref: "#/components/schemas/GitAppDeployed"
        check:
          $ref: "#/components/schemas/GitAppCheck"
        new_commits:
          type: boolean
          description: False while `deployed` is null.
        state:
          $ref: "#/components/schemas/GitAppState"
        operation:
          $ref: "#/components/schemas/GitAppOperation"
        history:
          type: array
          description: The last three deployments, newest first.
          items:
            $ref: "#/components/schemas/GitAppHistoryEntry"
        compose:
          $ref: "#/components/schemas/GitAppCompose"
        compose_example:
          type: string
          nullable: true
          description: |
            A compose file to add at the root of the repository, when the last check cloned a
            repository without one (the clone is removed and `check.error` says so). Null otherwise.
        env_template:
          type: string
          nullable: true
          description: The repository's `.env.example` or `.env.sample`.
        build_log:
          type: string
          description: The last 64 KiB of the last build's log.

    GitAppHead:
      description: The commit the folder is on. Null until cloned.
      nullable: true
      required:
        - commit
        - subject
        - tracked_files_clean
      properties:
        commit:
          type: string
        subject:
          type: string
        tracked_files_clean:
          type: boolean

    GitAppDeployed:
      description: The version running. Null before the first successful deployment.
      nullable: true
      required:
        - commit
        - subject
        - at
      properties:
        commit:
          type: string
        subject:
          type: string
        at:
          type: string
          format: date-time

    GitAppCheck:
      description: The last check of the remote. Null before the first check.
      nullable: true
      required:
        - at
        - remote_commit
        - error
      properties:
        at:
          type: string
          format: date-time
        remote_commit:
          type: string
        error:
          type: string

    GitAppOperation:
      description: The operation running. Null when none runs.
      nullable: true
      required:
        - kind
        - commit
        - started_at
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

    GitAppHistoryEntry:
      required:
        - commit
        - subject
        - at
        - outcome
        - reason
        - revertable
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
          description: Whether this version can be reverted to, its images still being there.

    GitAppCompose:
      description: What the repository's compose files will run. Null until cloned.
      nullable: true
      required:
        - files
        - services
      properties:
        files:
          type: array
          items:
            type: string
          example:
            - compose.yaml
        services:
          type: array
          items:
            $ref: "#/components/schemas/GitAppComposeService"

    GitAppComposeService:
      required:
        - name
        - build
        - image
        - ports
        - volumes
        - sensitive
      properties:
        name:
          type: string
          example: web
        build:
          type: boolean
          description: Built from the repository rather than pulled.
        image:
          type: string
        ports:
          type: array
          description: As written in the compose file.
          items:
            type: string
          example:
            - "8080:8080"
        volumes:
          type: array
          description: As written in the compose file.
          items:
            type: string
          example:
            - ./data:/data
        sensitive:
          type: array
          description: |
            Options that give the service power over the host, only these values: `privileged`,
            `network_mode_host`, `pid_host`, `cap_add`, `devices`, `docker_socket` (a bind of the
            Docker socket).
          items:
            type: string
          example:
            - privileged

    GitAppCreateRequest:
      required:
        - name
        - url
        - access
      properties:
        name:
          type: string
          description: A compose project name, which also names the folder under /DATA/AppData.
          example: jarvis
        url:
          type: string
          example: git@github.com:owner/jarvis.git
        branch:
          type: string
          description: Empty or absent for the branch the remote's HEAD points to.
          example: main
        access:
          $ref: "#/components/schemas/GitAccess"
        token:
          type: string
          description: Required with `access` `token`; never returned.

    GitAppUpdateRequest:
      properties:
        branch:
          type: string
        auto_deploy:
          type: boolean
        access:
          $ref: "#/components/schemas/GitAccess"
        token:
          type: string
          description: Never returned; an empty token is refused.

    GitAppDeployRequest:
      properties:
        commit:
          type: string
          description: Absent for the remote's latest commit; a commit of the history reverts to it.
          pattern: "^[0-9a-f]{40}$"
        env:
          type: string
          description: The `.env` to write, accepted while no deployment has succeeded and never when the repository tracks `.env`.

    GitAppState:
      description: |
        `building` and `deploying` while a deployment runs.
```

The `nullable: true` on `GitAppHead`, `GitAppDeployed`, `GitAppCheck`, `GitAppOperation` and `GitAppCompose` is what makes the properties referencing them nullable: siblings of a `$ref` are ignored.

- [ ] **Step 4: Regenerate**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go generate ./... && grep -cP '^\t[A-Z]\w* +\w+ = \x22' codegen/app_management_api.go && go build ./... && echo build-ok" 2>&1 | Select-Object -Last 40
```

Expected: `34`, then the build stops on `*AppManagement does not implement codegen.ServerInterface (missing method CheckGitApp)`: the six handlers come next.

- [ ] **Step 5: Write the handlers**

Create `route/v2/git.go`:

```go
package v2

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/labstack/echo/v4"
	"github.com/samber/lo"
)

// Apps deployed from their git repository. See service/git_apps.go and git_deploy.go.

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

func (a *AppManagement) GetGitApp(ctx echo.Context, app codegen.GitAppName) error {
	view, err := service.GetGitApp(ctx.Request().Context(), app)

	return gitAppAnswer(ctx, http.StatusOK, view, err)
}

func (a *AppManagement) UpdateGitApp(ctx echo.Context, app codegen.GitAppName) error {
	var body codegen.GitAppUpdateRequest
	if err := ctx.Bind(&body); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	view, err := service.UpdateGitApp(ctx.Request().Context(), app, service.GitAppChanges{
		Branch: body.Branch, AutoDeploy: body.AutoDeploy, Access: body.Access, Token: body.Token,
	})

	return gitAppAnswer(ctx, http.StatusOK, view, err)
}

func (a *AppManagement) DeleteGitApp(ctx echo.Context, app codegen.GitAppName) error {
	if err := service.DeleteGitApp(ctx.Request().Context(), app); err != nil {
		return gitAppError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, codegen.ResponseOK{Message: utils.Ptr(fmt.Sprintf("`%s` is removed", app))})
}

func (a *AppManagement) CheckGitApp(ctx echo.Context, app codegen.GitAppName) error {
	view, err := service.CheckGitApp(ctx.Request().Context(), app)

	return gitAppAnswer(ctx, http.StatusAccepted, view, err)
}

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

func gitAppAnswer(ctx echo.Context, status int, view *service.GitAppView, err error) error {
	if err != nil {
		return gitAppError(ctx, err)
	}

	return ctx.JSON(status, gitAppOK{Data: view})
}

// gitAppError answers what the service refused with the status the API promises.
func gitAppError(ctx echo.Context, err error) error {
	message := err.Error()

	switch {
	case errors.As(err, new(service.GitRequestError)):
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	case errors.Is(err, service.ErrGitAppNotFound):
		return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
	case errors.Is(err, service.ErrGitAppNameTaken), errors.Is(err, service.ErrGitAppDeployed):
		return ctx.JSON(http.StatusConflict, codegen.ResponseConflict{Message: &message})
	}

	return conflictOrServerError(ctx, err)
}
```

The service builds the `GitApp` itself (`service.GitAppView`), so the handlers write it under `data` rather than converting it to the generated `codegen.GitApp`, which stays unused by Go code.

- [ ] **Step 6: Build and run the tests**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go build ./... && go vet ./route/... && go test ./route/ ./route/v2/ -count=1 -v -run 'TestTheGitRoutes|TestAGitApp|TestAnOperationRefused|TestWebAppGridItem'" 2>&1 | Select-Object -Last 40
```

Expected: `--- PASS` for TestTheGitRoutesAreServed, TestAGitAppRefusalIsAnsweredWithItsStatus, TestAGitAppIsAnsweredUnderData, TestAnOperationRefusedForABusyAppIsAConflict and the three TestWebAppGridItemAdapter tests; `ok` for `route` and `route/v2`.

- [ ] **Step 7: Check the formatting**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "tr -d '\r' < route/v2/git.go | gofmt -l; tr -d '\r' < route/v2/git_internal_test.go | gofmt -l; tr -d '\r' < route/v2_git_test.go | gofmt -l" 2>&1 | Select-Object -Last 40
```

Expected: no output.

- [ ] **Step 8: Commit**

```powershell
git -C D:\clients\casaos\CasaOS-AppManagement add api/app_management/openapi.yaml route/v2/git.go route/v2/git_internal_test.go route/v2_git_test.go
git -C D:\clients\casaos\CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): the routes of apps deployed from their repository"
```

---

### Task 11: Uninstalling a git app

**Files:**
- Modify: `service/compose_app.go:1104-1129` (`ComposeApp.Uninstall`, from the working-folder removal to the end)
- Modify: `service/git_apps.go` (before `gitImagesOf`)
- Test: `service/git_uninstall_internal_test.go`

**Interfaces:**
- Consumes: `loadGitApp`, `forgetGitApp`, `gitOriginAdopted` (Task 5); `gitImagesOf` (Task 7); `gitDocker.RemoveImages` (Task 4); `sortedServiceNames`; the hold `ComposeService.Uninstall` takes (Task 3). In the tests: `gitAppsIn` (Task 5); `withFakeGitDocker` and its `gate`, `newTestRemote`, `gitShell`, `waitForGitApp` (Task 6); `CreateGitApp`, `checkTestApp` (Task 7); `DeployGitApp` (Task 8).
- Produces: `func (a *ComposeApp) uninstalledFolders(gitState *gitApp, deleteConfigFolder bool) []string`; `func forgetUninstalledGitApp(ctx context.Context, st *gitApp)`.

- [ ] **Step 1: Write the failing tests**

Create `service/git_uninstall_internal_test.go`:

```go
package service

import (
	"context"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/types"
	"gotest.tools/v3/assert"
)

// An adopted folder is its owner's and never goes; a created app's clone goes with the
// config folder only; any other app is uninstalled as before.
func TestUninstallingAGitAppDeletesOnlyWhatIsCasaOSToDelete(t *testing.T) {
	volumes := func(sources ...string) types.Services {
		service := types.ServiceConfig{Name: "web"}
		for _, source := range sources {
			service.Volumes = append(service.Volumes, types.ServiceVolumeConfig{Source: source, Target: "/data"})
		}
		return types.Services{"web": service}
	}

	byHand := &ComposeApp{Name: "jarvis", WorkingDir: "/home/gary/jarvis", Services: volumes("/home/gary/jarvis/data", "/DATA/AppData/jarvis/cache")}
	adopted := &gitApp{Origin: gitOriginAdopted, Dir: "/home/gary/jarvis"}

	assert.DeepEqual(t, byHand.uninstalledFolders(nil, false), []string{"/home/gary/jarvis"})
	assert.DeepEqual(t, byHand.uninstalledFolders(nil, true), []string{"/home/gary/jarvis", "/DATA/AppData/jarvis"})
	assert.DeepEqual(t, byHand.uninstalledFolders(adopted, false), []string{})
	assert.DeepEqual(t, byHand.uninstalledFolders(adopted, true), []string{"/DATA/AppData/jarvis"})

	cloned := &ComposeApp{Name: "jarvis", WorkingDir: "/DATA/AppData/jarvis", Services: volumes("/DATA/AppData/jarvis/data")}
	created := &gitApp{Origin: gitOriginCreated, Dir: "/DATA/AppData/jarvis"}

	assert.DeepEqual(t, cloned.uninstalledFolders(created, false), []string{})
	assert.DeepEqual(t, cloned.uninstalledFolders(created, true), []string{"/DATA/AppData/jarvis"})
}

// An uninstall is refused while the app deploys, naming the deployment, which goes on. The
// name is one no real compose project has: an uninstall that went ahead would reach the
// daemon the tests run against.
func TestUninstallingAGitAppIsRefusedWhileItDeploys(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	url, work := newTestRemote(t)
	ctx := context.Background()
	const name = "casaos-test-uninstall"

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: name, URL: url, Access: "none"})
	assert.NilError(t, err)
	assert.Equal(t, checkTestApp(t, name).Cloned, true)
	fake.gate = make(chan struct{})

	_, err = DeployGitApp(ctx, name, "", nil)
	assert.NilError(t, err)
	<-fake.gate // the deployment is building

	err = NewComposeService().Uninstall(ctx, &ComposeApp{Name: name}, true)
	fake.gate <- struct{}{}

	assert.Error(t, err, "`casaos-test-uninstall` is busy: deploy in progress")
	st := waitForGitApp(t, name)
	assert.Equal(t, st.Deployed.Commit, gitShell(t, work, "git rev-parse HEAD"), "the deployment went on and the app is still known")
}

func TestAnUninstalledGitAppIsForgottenWithItsImages(t *testing.T) {
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	st := &gitApp{
		App:      "jarvis",
		Deployed: &gitDeployment{Commit: "b", Images: map[string]string{"web": "jarvis-web:git-b"}},
		History: []gitHistoryEntry{
			{Commit: "b", Images: map[string]string{"web": "jarvis-web:git-b"}},
			{Commit: "a", Images: map[string]string{"web": "jarvis-web:git-a"}},
		},
	}
	assert.NilError(t, saveGitApp(st))
	_, err := generateGitKey("jarvis")
	assert.NilError(t, err)

	forgetUninstalledGitApp(context.Background(), st)

	assert.DeepEqual(t, fake.removed, []string{"jarvis-web:git-a", "jarvis-web:git-b"})
	_, err = loadGitApp("jarvis")
	assert.ErrorIs(t, err, ErrGitAppNotFound)
	assert.Assert(t, !pathExists(gitAppFile("jarvis", ".key")))
}
```

A git app is uninstalled through `ComposeService.Uninstall`, which holds the app since Task 3: the second test shows that uninstall refused while the app deploys, naming the deployment, which goes on.

- [ ] **Step 2: Run them to see them fail**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go test ./service/ -count=1 -run 'TestUninstallingAGitApp|TestAnUninstalledGitApp'" 2>&1 | Select-Object -Last 40
```

Expected: FAIL, the test build stops on `byHand.uninstalledFolders undefined (type *ComposeApp has no field or method uninstalledFolders)` and `undefined: forgetUninstalledGitApp`.

- [ ] **Step 3: Keep what is not CasaOS's to delete, and forget the rest**

In `service/compose_app.go`, replace:

```go
	if err := file.RMDir(a.WorkingDir); err != nil {
		go PublishEventWrapper(ctx, common.EventTypeImageRemoveError, map[string]string{
			common.PropertyTypeMessage.Name: err.Error(),
		})
	}

	if !deleteConfigFolder {
		return nil
	}

	for _, app := range a.Services {
		for _, volume := range app.Volumes {
			if strings.Contains(volume.Source, a.Name) {
				path := filepath.Join(strings.Split(volume.Source, a.Name)[0], a.Name)
				if err := file.RMDir(path); err != nil {
					logger.Error("failed to remove compose app config folder", zap.Error(err), zap.String("path", path))

					go PublishEventWrapper(ctx, common.EventTypeImageRemoveError, map[string]string{
						common.PropertyTypeMessage.Name: err.Error(),
					})
				}
			}
		}
	}

	return nil
}
```

with:

```go
	// nil for an app not deployed from git
	gitState, _ := loadGitApp(a.Name)

	for _, path := range a.uninstalledFolders(gitState, deleteConfigFolder) {
		if err := file.RMDir(path); err != nil {
			logger.Error("failed to remove compose app folder", zap.Error(err), zap.String("path", path))

			go PublishEventWrapper(ctx, common.EventTypeImageRemoveError, map[string]string{
				common.PropertyTypeMessage.Name: err.Error(),
			})
		}
	}

	if gitState != nil {
		forgetUninstalledGitApp(ctx, gitState)
	}

	return nil
}

// uninstalledFolders is what an uninstall deletes: the app's folder and, with
// deleteConfigFolder, the folders its volumes name after it. A git app's folder is its
// repository: a created app's goes with the config folder only, and an adopted app's is
// its owner's and never goes.
func (a *ComposeApp) uninstalledFolders(gitState *gitApp, deleteConfigFolder bool) []string {
	keep := func(path string) bool {
		switch {
		case gitState == nil:
			return false
		case gitState.Origin == gitOriginAdopted:
			return path == gitState.Dir || strings.HasPrefix(path, gitState.Dir+"/") || strings.HasPrefix(gitState.Dir, path+"/")
		}

		return path == gitState.Dir && !deleteConfigFolder
	}

	folders := []string{}
	if !keep(a.WorkingDir) {
		folders = append(folders, a.WorkingDir)
	}
	if !deleteConfigFolder {
		return folders
	}

	for _, name := range sortedServiceNames(a.Services) {
		for _, volume := range a.Services[name].Volumes {
			if !strings.Contains(volume.Source, a.Name) {
				continue
			}
			if path := filepath.Join(strings.Split(volume.Source, a.Name)[0], a.Name); !keep(path) {
				folders = append(folders, path)
			}
		}
	}

	return lo.Uniq(folders)
}
```

In `service/git_apps.go`, replace:

```go
// gitImagesOf is every git- tag the app's remembered versions name, sorted.
```

with:

```go
// forgetUninstalledGitApp removes what CasaOS kept of a git app that was uninstalled: the
// git- tags of its versions, its state and its secrets.
func forgetUninstalledGitApp(ctx context.Context, st *gitApp) {
	if images := gitImagesOf(st); len(images) > 0 {
		gitDocker.RemoveImages(ctx, images)
	}

	if err := forgetGitApp(st.App); err != nil {
		logger.Error("the state of an uninstalled git app could not be removed", zap.Error(err), zap.String("app", st.App))
	}
}

// gitImagesOf is every git- tag the app's remembered versions name, sorted.
```

For every other app the folders are the ones removed before, in a stable order and without duplicates.

- [ ] **Step 4: Run the tests**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go vet ./service/ && go test ./service/ -count=1 -run 'TestUninstallingAGitApp|TestAnUninstalledGitApp' -v" 2>&1 | Select-Object -Last 40
```

Expected: `--- PASS` for TestUninstallingAGitAppDeletesOnlyWhatIsCasaOSToDelete, TestUninstallingAGitAppIsRefusedWhileItDeploys and TestAnUninstalledGitAppIsForgottenWithItsImages.

- [ ] **Step 5: Check the formatting**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "tr -d '\r' < service/compose_app.go | gofmt -l; tr -d '\r' < service/git_apps.go | gofmt -l; tr -d '\r' < service/git_uninstall_internal_test.go | gofmt -l" 2>&1 | Select-Object -Last 40
```

Expected: no output.

- [ ] **Step 6: Commit**

```powershell
git -C D:\clients\casaos\CasaOS-AppManagement add service/compose_app.go service/git_apps.go service/git_uninstall_internal_test.go
git -C D:\clients\casaos\CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): an uninstall forgets a git app and never deletes an adopted folder"
```

---

### Task 12: Backups and restores of a git app

**Files:**
- Modify: `service/backup_plan.go:51-52` (`BackupManifest`)
- Modify: `service/backup_run.go:105` (`runBackup`)
- Modify: `service/backup_restore.go:130` (`restoreBackup`, the install of an app that is not installed)
- Create: `service/git_restore.go`
- Test: `service/git_restore_internal_test.go`

**Interfaces:**
- Consumes: `loadGitApp`, `saveGitApp`, `gitAuth`, `gitURLKind`, `gitAppFile` (Task 5); `setGitAccess`, `cloneGitApp` (Task 7); `runGitDeploy`, `gitTriggerManual` (Task 8); `gitComposeFiles` (Task 4); `LoadComposeAppFromConfigFile`.
- Produces: `type BackupGit struct{ Remote, Branch, Commit string }` and `BackupManifest.Git *BackupGit` (`json:"git,omitempty"`); `func gitBackupOrigin(app string) *BackupGit`; `var restoreGitApp = installGitAppFromBackup`; `func installGitAppFromBackup(ctx context.Context, name string, origin BackupGit, env []byte) (*ComposeApp, error)`; `func gitAccessRequired(st *gitApp, cause error) error`.

- [ ] **Step 1: Write the failing tests**

Create `service/git_restore_internal_test.go`:

```go
package service

import (
	"context"
	stdjson "encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

func TestABackupOfAGitAppRecordsWhereItsCodeComesFrom(t *testing.T) {
	logInTempDir(t)
	gitAppsIn(t)
	app, _ := appWithOneBindAndOneVolume(t)
	options := BackupOptions{Destination: "offsite", Stamp: restoreStamp}

	manifest, err := RunBackup(context.Background(), app, &fakeBackupDocker{mountpoints: demoMountpoints()}, &fakeCopier{}, options)
	assert.NilError(t, err)
	assert.Assert(t, manifest.Git == nil, "an app not deployed from git")

	const commit = "4f5f60c16eba0123456789abcdef0123456789ab"
	assert.NilError(t, saveGitApp(&gitApp{App: "demo", Remote: "git@github.com:owner/demo.git", Branch: "main", Deployed: &gitDeployment{Commit: commit}}))

	manifest, err = RunBackup(context.Background(), app, &fakeBackupDocker{mountpoints: demoMountpoints()}, &fakeCopier{}, options)
	assert.NilError(t, err)
	assert.DeepEqual(t, manifest.Git, &BackupGit{Remote: "git@github.com:owner/demo.git", Branch: "main", Commit: commit})
}

func TestARestoreOfAGitAppNotInstalledComesBackFromItsRepository(t *testing.T) {
	logInTempDir(t)
	app, _ := appWithOneBindAndOneVolume(t)
	restorer, manifest := backupOf(t, app)
	origin := BackupGit{Remote: "https://example.invalid/owner/demo.git", Branch: "main", Commit: strings.Repeat("a", 40)}
	manifest.Git = &origin
	raw, err := stdjson.Marshal(manifest)
	assert.NilError(t, err)
	restorer.files[path.Join(RootFor("demo", restoreStamp), ManifestFileName)] = raw

	var restoredWith BackupGit
	var envWith []byte
	previous := restoreGitApp
	restoreGitApp = func(_ context.Context, name string, git BackupGit, env []byte) (*ComposeApp, error) {
		restoredWith, envWith = git, env
		return app, nil
	}
	t.Cleanup(func() { restoreGitApp = previous })

	report, err := RestoreBackup(context.Background(), nil, &fakeBackupDocker{mountpoints: demoMountpoints()}, restorer, noInstall, RestoreOptions{
		Destination: "offsite", App: "demo", Stamp: restoreStamp, Containers: running("c1"),
	})
	assert.NilError(t, err)
	assert.DeepEqual(t, restoredWith, origin)
	assert.Equal(t, string(envWith), "content of compose/.env")
	assert.Assert(t, report.Installed)
	assert.Equal(t, len(report.Restored), 2)
}

func TestAGitAppIsInstalledFromItsBackupAtTheBackedUpCommit(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	fake := withFakeGitDocker(t)
	url, work := newTestRemote(t)
	first := gitShell(t, work, "git rev-parse HEAD")
	pushTestCommit(t, work, "index.html", "v2")

	app, err := installGitAppFromBackup(context.Background(), "jarvis", BackupGit{Remote: url, Branch: "main", Commit: first}, []byte("GREETING=hello\n"))
	assert.NilError(t, err)

	assert.Equal(t, app.Name, "jarvis")
	assert.Equal(t, app.WorkingDir, filepath.Join(gitAppsDataRoot, "jarvis"))
	assert.DeepEqual(t, fake.Calls(), []string{"build " + first[:12], "retag " + first[:12], "start " + first[:12]})

	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, st.Deployed.Commit, first)
	assert.Equal(t, folderHead(t, st.Dir), first)
	env, err := os.ReadFile(filepath.Join(st.Dir, ".env"))
	assert.NilError(t, err)
	assert.Equal(t, string(env), "GREETING=hello\n")
}

// A backup holds no key and no token: a repository the restore cannot reach leaves the app
// registered with what it needs, and the restore says what to do.
func TestARestoreThatCannotReachTheRepositoryAsksForAccess(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	commit := strings.Repeat("a", 40)

	_, err := installGitAppFromBackup(context.Background(), "jarvis", BackupGit{Remote: "ssh://git@127.0.0.1:1/owner/jarvis.git", Branch: "main", Commit: commit}, nil)
	assert.ErrorContains(t, err, "access required: add this deploy key")
	assert.ErrorContains(t, err, "ssh-ed25519 ")

	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Equal(t, st.Access, git.AccessKey)
	assert.Assert(t, st.Deployed == nil)

	_, err = installGitAppFromBackup(context.Background(), "private", BackupGit{Remote: "https://127.0.0.1:1/owner/private.git", Branch: "main", Commit: commit}, nil)
	assert.ErrorContains(t, err, "give `private` a token")
}
```

The last test reaches `127.0.0.1:1`, where nothing listens: git fails at once, over SSH (the golang image has `ssh`) and over HTTPS.

- [ ] **Step 2: Run them to see them fail**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go test ./service/ -count=1 -run 'TestABackupOfAGitApp|TestARestoreOfAGitApp|TestAGitAppIsInstalledFromItsBackup|TestARestoreThatCannotReach'" 2>&1 | Select-Object -Last 40
```

Expected: FAIL, `manifest.Git undefined`, `undefined: BackupGit` and the others.

- [ ] **Step 3: Record the origin in the manifest, and restore a git app from it**

In `service/backup_plan.go`, replace:

```go
	ContainersStopped bool `json:"containers_stopped"`
}
```

with:

```go
	ContainersStopped bool `json:"containers_stopped"`
	// Git is where the code of an app deployed from its repository comes from. Optional, so
	// the format version stays: an older reader ignores it.
	Git *BackupGit `json:"git,omitempty"`
}

// BackupGit is a git app's origin as a backup records it. Never a key or a token.
type BackupGit struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}
```

In `service/backup_run.go`, replace:

```go
	manifest.ContainersStopped = opts.HoldStill
```

with:

```go
	manifest.ContainersStopped = opts.HoldStill
	manifest.Git = gitBackupOrigin(app.Name)
```

In `service/backup_restore.go`, replace:

```go
		app, err = install(ctx, opts.App, compose, env)
		if err != nil {
```

with:

```go
		// an app deployed from git comes back from its repository, not from a copy of its
		// compose file
		if manifest.Git != nil {
			app, err = restoreGitApp(ctx, opts.App, *manifest.Git, env)
		} else {
			app, err = install(ctx, opts.App, compose, env)
		}
		if err != nil {
```

Create `service/git_restore.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/git"
)

// A git app in a backup: its origin goes in the manifest, and a restore on a machine that
// does not have the app clones and builds it from there. A backup holds no key and no
// token, so a private repository needs access again.

// gitBackupOrigin is what a backup records of a git app, nil for any other app and for one
// never deployed.
func gitBackupOrigin(app string) *BackupGit {
	st, err := loadGitApp(app)
	if err != nil || st.Deployed == nil {
		return nil
	}

	return &BackupGit{Remote: st.Remote, Branch: st.Branch, Commit: st.Deployed.Commit}
}

// restoreGitApp is installGitAppFromBackup; a var so a restore's test replaces it.
var restoreGitApp = installGitAppFromBackup

// installGitAppFromBackup puts a git app back on a machine that does not have it: it is
// registered with the backup's remote and branch, cloned at the backed-up commit, given
// the backup's .env, then built and started, while the restore holds it. A repository that
// cannot be reached leaves the app registered with the access it needs.
func installGitAppFromBackup(ctx context.Context, name string, origin BackupGit, env []byte) (*ComposeApp, error) {
	st, err := loadGitApp(name)
	if errors.Is(err, ErrGitAppNotFound) {
		st = &gitApp{
			App: name, Origin: gitOriginCreated, Dir: filepath.Join(gitAppsDataRoot, name),
			Remote: origin.Remote, Branch: origin.Branch, Access: git.AccessNone,
			History: []gitHistoryEntry{}, Attempted: []string{},
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
	if _, err := git.LsRemote(ctx, st.Remote, st.Branch, auth); err != nil {
		return nil, gitAccessRequired(st, err)
	}

	if !st.Cloned {
		if err := cloneGitApp(ctx, st, auth); err != nil {
			return nil, err
		}
		if err := saveGitApp(st); err != nil {
			return nil, err
		}
	}

	if !git.HasCommit(ctx, st.Dir, origin.Commit) {
		return nil, fmt.Errorf("the backed-up commit %s is not on %s any more", origin.Commit[:12], st.Branch)
	}
	if err := git.ResetKeep(ctx, st.Dir, origin.Commit); err != nil {
		return nil, err
	}
	if len(env) > 0 {
		if err := (&ComposeApp{WorkingDir: st.Dir}).WriteEnvFile(env); err != nil {
			return nil, err
		}
	}

	st.Operation = &gitOperation{Kind: gitOperationBuild, Commit: origin.Commit, StartedAt: time.Now().UTC()}
	if err := saveGitApp(st); err != nil {
		return nil, err
	}
	runGitDeploy(ctx, st, origin.Commit, false, gitTriggerManual)
	if st.Deployed == nil || st.Deployed.Commit != origin.Commit {
		return nil, fmt.Errorf("`%s` could not be deployed at %s: %s", name, origin.Commit[:12], st.History[0].Reason)
	}

	files, err := gitComposeFiles(st.Dir)
	if err != nil {
		return nil, err
	}

	return LoadComposeAppFromConfigFile(name, strings.Join(files, ","))
}

// gitAccessRequired prepares the access a restore could not reach the repository without,
// and says what the owner has to do: add a new deploy key for an SSH remote, give a token
// for any other.
func gitAccessRequired(st *gitApp, cause error) error {
	if gitURLKind(st.Remote) != "ssh" {
		return fmt.Errorf("access required: give `%s` a token in its Repository tab, then restore again (%v)", st.App, cause)
	}

	if st.Access != git.AccessKey {
		if err := setGitAccess(st, git.AccessKey, ""); err != nil {
			return err
		}
		if err := saveGitApp(st); err != nil {
			return err
		}
	}

	public, err := os.ReadFile(gitAppFile(st.App, ".key.pub"))
	if err != nil {
		return err
	}

	return fmt.Errorf("access required: add this deploy key to %s as a read-only key, then restore again: %s (%v)",
		st.Remote, strings.TrimSpace(string(public)), cause)
}
```

The restore installs the app while it holds it (Task 3), then copies the data into its volumes and starts it again as for any app. A restore that fails for access leaves the app registered: it reaches the grid as a git app without containers, whose Repository tab shows the key.

- [ ] **Step 4: Run the tests**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go vet ./service/ && go test ./service/ -count=1 -run 'TestABackupOfAGitApp|TestARestoreOfAGitApp|TestAGitAppIsInstalledFromItsBackup|TestARestoreThatCannotReach|TestARestore|TestARun' -v" 2>&1 | Select-Object -Last 40
```

Expected: `--- PASS` for TestABackupOfAGitAppRecordsWhereItsCodeComesFrom, TestARestoreOfAGitAppNotInstalledComesBackFromItsRepository, TestAGitAppIsInstalledFromItsBackupAtTheBackedUpCommit and TestARestoreThatCannotReachTheRepositoryAsksForAccess, and for the backup and restore tests that were there.

- [ ] **Step 5: Check the formatting**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "tr -d '\r' < service/backup_plan.go | gofmt -l; tr -d '\r' < service/backup_run.go | gofmt -l; tr -d '\r' < service/backup_restore.go | gofmt -l; tr -d '\r' < service/git_restore.go | gofmt -l; tr -d '\r' < service/git_restore_internal_test.go | gofmt -l" 2>&1 | Select-Object -Last 40
```

Expected: no output.

- [ ] **Step 6: Commit**

```powershell
git -C D:\clients\casaos\CasaOS-AppManagement add service/backup_plan.go service/backup_run.go service/backup_restore.go service/git_restore.go service/git_restore_internal_test.go
git -C D:\clients\casaos\CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): a backup records where the code comes from, a restore clones and builds it"
```

---

### Task 13: Against the daemon, then the whole suite

**Files:**
- Test: `service/git_integration_internal_test.go`

**Interfaces:**
- Consumes: everything above, with the real `composeGitRuntime`.
- Produces: evidence that the chain holds against Docker: a local repository with a Dockerfile and a compose file is created, deployed, gets a new commit deployed automatically, then a broken commit rolled back, then a revert; and the whole suite green.

- [ ] **Step 1: Write the integration test**

Create `service/git_integration_internal_test.go`:

```go
package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	appdocker "github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"
	"gotest.tools/v3/assert"
)

// A git app against the daemon: created from a local repository, deployed, a new commit
// deployed automatically, a commit whose container exits rolled back, then a revert.
func TestAGitAppAgainstTheDaemon(t *testing.T) {
	if os.Getenv("CASAOS_INTEGRATION") == "" {
		t.Skip("set CASAOS_INTEGRATION=1 to run against a Docker daemon")
	}
	if !appdocker.IsDaemonRunning() {
		t.Skip("Docker daemon is not running")
	}

	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	settle := gitSettleDelay
	gitSettleDelay = 3 * time.Second
	t.Cleanup(func() { gitSettleDelay = settle })

	ctx := context.Background()
	name := fmt.Sprintf("casaos-git-%d", time.Now().UnixNano())

	root := t.TempDir()
	gitShell(t, root, "git init -q --bare --initial-branch=main remote.git && git clone -q remote.git work 2>/dev/null")
	work := filepath.Join(root, "work")
	url := "file://" + filepath.Join(root, "remote.git")
	gitShell(t, work, "git symbolic-ref HEAD refs/heads/main")
	assert.NilError(t, os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM busybox:1.36\nCOPY version /version\nCMD [\"sh\", \"-c\", \"cat /version; exec sleep 3600\"]\n"), 0o644))
	pushTestCommit(t, work, "compose.yaml", "services:\n  web:\n    build: .\n")
	first := pushTestCommit(t, work, "version", "v1\n")

	backend, dockerClient, err := apiService()
	assert.NilError(t, err)
	defer dockerClient.Close()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = backend.Down(cleanup, name, api.DownOptions{RemoveOrphans: true, Images: "all"})
		if st, err := loadGitApp(name); err == nil {
			gitDocker.RemoveImages(cleanup, gitImagesOf(st))
		}
	})

	// what the running container was created from, as the tag of a commit names it
	runs := func(commit string) {
		t.Helper()
		containers, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{
			Filters: make(client.Filters).Add("label", api.ProjectLabel+"="+name),
		})
		assert.NilError(t, err)
		assert.Equal(t, len(containers.Items), 1)
		image, err := dockerClient.ImageInspect(ctx, name+"-web:git-"+commit[:12])
		assert.NilError(t, err)
		assert.Equal(t, containers.Items[0].ImageID, image.ID, "the container runs %s", commit[:12])
	}

	_, err = CreateGitApp(ctx, GitAppRegistration{Name: name, URL: url, Access: "none"})
	assert.NilError(t, err)
	assert.Equal(t, checkTestApp(t, name).Cloned, true)

	_, err = DeployGitApp(ctx, name, "", nil)
	assert.NilError(t, err)
	st := waitForGitApp(t, name)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeDeployed, st.History[0].Reason)
	runs(first)

	on := true
	_, err = UpdateGitApp(ctx, name, GitAppChanges{AutoDeploy: &on})
	assert.NilError(t, err)
	second := pushTestCommit(t, work, "version", "v2\n")
	st = checkTestApp(t, name)
	assert.Equal(t, st.Deployed.Commit, second, st.History[0].Reason)
	runs(second)

	assert.NilError(t, os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM busybox:1.36\nCMD [\"sh\", \"-c\", \"exit 1\"]\n"), 0o644))
	broken := pushTestCommit(t, work, "version", "broken\n")
	st = checkTestApp(t, name)
	assert.Equal(t, st.History[0].Commit, broken)
	assert.Equal(t, st.History[0].Outcome, gitOutcomeRolledBack, st.History[0].Reason)
	assert.Equal(t, st.Deployed.Commit, second)
	runs(second)

	_, err = DeployGitApp(ctx, name, first, nil)
	assert.NilError(t, err)
	st = waitForGitApp(t, name)
	assert.Equal(t, st.Deployed.Commit, first, st.History[0].Reason)
	assert.Equal(t, st.AutoPaused, true)
	runs(first)
}
```

Without `CASAOS_INTEGRATION=1` it skips. The app publishes no port, so nothing on the host can collide with it; what runs is read from the image ID of its container.

- [ ] **Step 2: Run it against the daemon**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false -e CASAOS_INTEGRATION=1 golang:1.26 sh -c "go test ./service/ -count=1 -run TestAGitAppAgainstTheDaemon -v -timeout 15m" 2>&1 | Select-Object -Last 40
```

Expected: compose's own lines (`Container casaos-git-...-web-1 Recreated`, a `failed to start original compose app ... exited (1)` error for the broken commit), then `--- PASS: TestAGitAppAgainstTheDaemon` and `ok`. It pulls `busybox:1.36` once.

- [ ] **Step 3: Run the whole suite**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v /var/run/docker.sock:/var/run/docker.sock -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "go vet ./... && go test ./... -count=1" 2>&1 | Select-Object -Last 40
```

Expected: `ok` for `cmd/validator/pkg`, `common`, `model`, `pkg/docker`, `pkg/git`, `pkg/rclone`, `pkg/utils/downloadHelper`, `route`, `route/v2` and `service`, and no `FAIL`.

- [ ] **Step 4: Check the formatting of the test**

Run:

```powershell
docker run --rm -v D:\clients\casaos\CasaOS-AppManagement:/src -v casaos-gocache:/root/.cache/go-build -v casaos-gomod:/go/pkg/mod -w /src -e GOFLAGS=-buildvcs=false golang:1.26 sh -c "tr -d '\r' < service/git_integration_internal_test.go | gofmt -l" 2>&1 | Select-Object -Last 40
```

Expected: no output.

- [ ] **Step 5: Commit**

```powershell
git -C D:\clients\casaos\CasaOS-AppManagement add service/git_integration_internal_test.go
git -C D:\clients\casaos\CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "test(git): create, deploy, rebuild, roll back and revert against the daemon"
```

