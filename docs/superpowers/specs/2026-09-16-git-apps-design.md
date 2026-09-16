# Git apps: an app deployed from its git repository

- **Date:** 2026-09-16
- **Status:** design approved section by section; this document awaits review
- **Components:** CasaOS-AppManagement (most of it), CasaOS-UI, CasaOS-Install
- **Base:** AppManagement v0.4.56, CasaOS-UI v0.4.65, distribution v0.4.91

## Why

Since v0.4.90 a stack started by hand with `docker compose up` is a CasaOS app: its `.env`
saves, its containers are listed, a stack CasaOS cannot load says why. What CasaOS still
cannot do is the part that matters to anyone who writes their own project, like the owner's
`jarvis`:

- CasaOS never builds an image. After a code change the owner runs `docker compose up -d --build`
  in the project folder.
- Saving the Settings form rewrites the project's compose file in CasaOS's format and loses its
  comments, so the owner has to avoid that form.
- A backup copies the data and the compose file, but nothing records which version of the code
  was running, and a restore on another machine cannot rebuild the image.

Home-server systems install prebuilt images from a catalogue. Tools that deploy from git target
hosting servers, not a home NAS with disks, shares and backups. This feature makes one's own
repository a first-class app on ReCasaOS.

## Decisions taken while brainstorming

1. **Two entry points in the same release:** create an app from a git URL, and adopt a stack
   started by hand whose folder is a git work tree.
2. **Tracking:** a periodic check shows new commits with a badge and a button. Automatic rebuild
   is a per-app switch. A deployment that does not start correctly is rolled back automatically.
3. **Downtime:** none while building. The running version keeps serving during the build and a
   failed build changes nothing. The rebuilt services restart when the new version is switched
   in. No blue-green deployment and no proxy.
4. **Private repositories:** a per-app SSH deploy key, offered first, or a personal access token.
   A public repository needs nothing.
5. **No compose file:** a repository with only a Dockerfile is refused, with an example compose
   file to add. The repository's compose file stays the only definition of the app.
6. **Architecture:** everything lives in AppManagement. It runs the host's `git` binary, which
   the installer now installs, and builds through the compose v5 library it already embeds.

## What the owner sees

### Creating an app from git

The apps "+" menu gains "From a git repository…", a three-step dialog.

1. **Repository.** URL, optional branch (default: the branch the remote's HEAD points to), app
   name (default: the repository name made into a valid compose project name), and access: none,
   deploy key or token. A name already used by an app or by a compose project on the machine is
   refused. With a deploy key, CasaOS generates the key at this step and shows its public half
   with a copy button; the owner adds it to the repository as a read-only key and continues.
2. **Review.** CasaOS clones into `/DATA/AppData/<name>`. It refuses a folder that exists and is
   not empty. It reads the compose files `docker compose up` would read at the repository root:
   the first of `compose.yaml`, `compose.yml`, `docker-compose.yaml`, `docker-compose.yml`, plus
   its `.override` counterpart when present. Without a compose file, the clone is deleted and the
   dialog explains what to add, with an example. With one, the dialog shows:
   - every service, whether it is built or pulled, its published ports and its volumes;
   - sensitive options, called out: `privileged`, `network_mode: host`, `pid: host`, `cap_add`,
     `devices`, and any bind of the Docker socket;
   - the `.env` editor, prefilled from `.env.example` or `.env.sample` when the repository has one.
     When the repository tracks `.env` itself, the editor is read-only and says so: an edit would
     modify a tracked file and block every later deployment.
3. **Deploy.** CasaOS writes `.env`, builds and starts the app, and shows the build log live.

### Adopting a stack started by hand

A compose app whose working directory is inside a git work tree shows a Repository tab by itself,
with its remote, branch, commit and whether tracked files were modified. Nothing changes until the
owner acts: sets access, turns on automatic rebuild, checks or deploys.

The first such action adopts the stack:

- its current commit becomes `deployed`, recorded in `history` with the outcome `adopted`;
- the images its running containers use for the services that have a build section are tagged
  `git-<commit>`, so the first deployment CasaOS makes has a version to roll back to.

A folder in detached HEAD, or with no remote, shows its state in the tab and cannot be adopted
until it is on a branch with a remote.

### The Repository tab

- Remote URL, branch, deployed commit (short hash, subject, date), last check (time, remote commit,
  error).
- Access: the mode, the public key with a copy button, a token field that is write-only, and a
  "Test access" button, which runs a check.
- The automatic rebuild switch, with its warning: it runs whatever is pushed to the branch.
- "Check now" and "Fetch and rebuild".
- The log of the last build.
- The last three deployments: commit, date, outcome (adopted, deployed, build failed, rolled back,
  failed, interrupted), and "Revert to this version" when that version's images still exist.

For a git app, and for an adoptable one, the Settings and Compose tabs show a notice that points
to the repository instead of an editor: an edit there would modify tracked files and block every
later fetch. The `.env` tab
works as today, except when the repository tracks `.env`, where it is read-only with the same
notice. Containers, logs, terminal and backups are unchanged.

### The app card

Badges, following the existing "Update available" badge:

| State | Badge |
|---|---|
| a newer commit on the branch | New commits |
| `building` | Building |
| `deploying` | Deploying |
| `build_failed` | Build failed |
| `rolled_back` | Rolled back |
| `failed` | Deployment failed |
| `unreachable` | Repository unreachable |
| `interrupted` | Interrupted |

## Architecture

### `pkg/git` (new)

A thin wrapper around the `git` binary through `exec.CommandContext`. It knows nothing about
CasaOS apps.

- `LsRemote(ctx, url, branch, auth) (commit, error)`
- `DefaultBranch(ctx, url, auth) (branch, error)`, from `ls-remote --symref <url> HEAD`
- `Clone(ctx, url, branch, dir, auth) error`
- `Fetch(ctx, dir, url, branch, auth) (commit, error)`
- `Describe(ctx, dir) (Info{Toplevel, RemoteURL, Branch, Head, TrackedFilesClean}, error)`
- `IsTracked(ctx, dir, path) (bool, error)`
- `IsAncestor(ctx, dir, ancestor, descendant) (bool, error)`
- `AddWorktree(ctx, dir, commit, path) error`, which also runs
  `submodule update --init --recursive` when `.gitmodules` exists, and `RemoveWorktree`
- `FastForward(ctx, dir, commit) error`, as `merge --ff-only`
- `ResetKeep(ctx, dir, commit) error`, as `reset --keep`, used only to roll back

Every command runs with:

- `GIT_TERMINAL_PROMPT=0`, and `-c credential.helper=` so no stored helper answers;
- `-c core.hooksPath=/dev/null`, so no hook of an adopted repository runs as root;
- `-c safe.directory=<dir>` for that command only, since AppManagement runs as root and an adopted
  folder usually belongs to a user;
- a timeout: 30 seconds for `ls-remote`, 10 minutes for a clone or a fetch.

`Auth{Mode, KeyPath, KnownHostsPath, Token}` becomes environment, never arguments:

- **Key:** `GIT_SSH_COMMAND="ssh -i <key> -o IdentitiesOnly=yes -o UserKnownHostsFile=<known_hosts> -o StrictHostKeyChecking=accept-new"`.
  The first connection records the server's host key; a changed host key fails.
- **Token:** `GIT_ASKPASS` points at a helper script written into a `0700` temporary folder that
  prints the token from an environment variable. The token never appears in a URL, an argument,
  `.git/config` or a log; captured output has it replaced before it is stored or returned.

### Git app state

One JSON file per git app, `/var/lib/casaos/git_apps/<app>.json`, written atomically (temporary
file and rename), like `/var/lib/casaos/image_updates.json`. Commit hashes are abbreviated in this
example:

```json
{
  "app": "jarvis",
  "origin": "created",
  "dir": "/DATA/AppData/jarvis",
  "remote": "git@github.com:owner/jarvis.git",
  "branch": "main",
  "access": "key",
  "auto_deploy": true,
  "auto_paused": false,
  "blocked": false,
  "env_tracked": false,
  "deployed": { "commit": "4f5f60c16eba", "at": "2026-09-16T10:00:00Z" },
  "check": { "at": "2026-09-16T10:05:00Z", "remote_commit": "a1b2c3d4e5f6", "error": "" },
  "operation": { "kind": "", "commit": "", "started_at": "" },
  "history": [
    {
      "commit": "4f5f60c16eba",
      "subject": "feat: voice wake word",
      "at": "2026-09-16T10:00:00Z",
      "outcome": "deployed",
      "reason": "",
      "images": { "web": "jarvis-web:git-4f5f60c16eba" }
    }
  ],
  "attempted": ["9e8d7c6b5a4f"]
}
```

- `origin` is `created` or `adopted`. `history` holds the last three deployments, newest first.
  `attempted` holds commits the automatic rebuild must not retry; it is bounded to the last 20.
  In the file, commits are stored in full; tags use their first 12 hex digits.
- Beside it, readable by root only (`0600`): `<app>.key`, `<app>.token`; and `<app>.key.pub`,
  `<app>.known_hosts`, `<app>.build.log` (the last build, capped at 1 MiB).
- An adoptable app with no state file is detected on the fly from its working directory and is not
  persisted until the owner acts.

### One operation per app (new, shared)

AppManagement has no common lock today: installs, updates, applies and backup holds each keep
their own marker. A small registry, `service/app_operations.go`, maps an app to its running
operation:

- `Begin(app, kind) (end func(), error)`; an overlap returns `ErrAppBusy{App, Running}`, answered
  as HTTP 409 naming the running operation.
- Callers: git check-and-deploy, deploy and revert; `ComposeApp.Update` and the update-all run;
  `apply` (settings save and `.env` save); install; a backup or a restore that holds an app still.
- The existing markers stay for their current readers. The registry is the one place that refuses
  an overlap.

### A deployment, `service/git_deploy.go`

Input: the app, a target commit, and a trigger (manual, automatic or revert).

1. **Guard.** `Begin(app, "deploy")`, and `operation` is recorded in the state file.
2. **Preconditions.** The folder is a git work tree on the tracked branch, its tracked files are
   unmodified (`status --porcelain --untracked-files=no` is empty), and the target descends from the
   deployed commit. For a revert, the target is in `history` and its images exist. A failed
   precondition changes nothing but the recorded reason.
3. **Build**, skipped for a revert.
   - Fetch the target and add a detached worktree for it under `/var/lib/casaos/git_apps/work/`.
   - Load the project from the worktree's compose files with the app's name as project name and
     `<dir>/.env` for interpolation.
   - Call compose's `Build` for the services that have a build section. It uses buildx when the
     host has it and the daemon's builder otherwise.
   - Tag each built image `<image>:git-<commit>`, then remove the worktree.
   - Progress goes out as `app:git-build-begin`, `app:git-build-progress`, `app:git-build-end` and
     `app:git-build-error`, and to `<app>.build.log`.
   - A failed build records `build_failed`, adds the commit to `attempted` and stops. The running
     app was never touched.
4. **Switch.** Fast-forward the folder to the target; for a revert, `reset --keep` to it and
   retag its `git-` images to the names compose uses. Load every compose file, pull the services
   that are not built, then create and start through the existing up path, which passes the
   project to compose so hooks run. An unchanged image leaves its service untouched: a commit that
   changes no build input restarts nothing.
5. **Verify.** The existing start wait runs. Afterwards, the deployment has failed when any
   container of a rebuilt service has exited with a non-zero code, is restarting, or is
   unhealthy. This is stricter than `keepNewDefinition`, which keeps a definition whose wait only
   timed out; that rule stays for every other apply.
6. **Roll back on failure.** `reset --keep` to the previously deployed commit, retag its images
   (for an adopted app's first deployment, the images tagged at adoption), and start again. The
   outcome is `rolled_back` with the reason, and `app:git-deploy-error` goes out. If the rollback
   fails too, the outcome is `failed` with both errors, and `blocked` stops every automatic action
   until the owner deploys or reverts by hand. A created app's first deployment has no previous
   version: a failure there is `failed`, and the app stays stopped with its log.
7. **Record success.** `deployed` becomes the target, `history` keeps three entries, and
   `app:git-deploy-end` goes out. `git-` tags of deployments that left the history are removed
   when no container uses them.
8. **Release.** `operation` is cleared and the guard is released.

A manual revert sets `auto_paused`. Otherwise the next check would redeploy the version the owner
just stepped back from. Turning the switch on again, or a manual deploy, clears it.

### Checks

A cron entry in `main.go`, `@every 5m`, beside the existing ones. For every app with a state file:

- `LsRemote` the branch and record the result in `check`;
- when the remote commit differs from `deployed.commit`, and `auto_deploy` is on, and neither
  `auto_paused` nor `blocked` is set, and the commit is not in `attempted`: deploy it
  automatically.

"Check now" runs the same for one app. An unreachable repository records its error and changes
nothing else.

### Startup

When AppManagement starts, a state with a non-empty `operation` gets an `interrupted` outcome and
the operation is cleared. Every worktree under `/var/lib/casaos/git_apps/work/` is removed and
`worktree prune` runs in its repository. Nothing is retried automatically.

### API

Declared in `api/app_management/openapi.yaml`, generated with oapi-codegen like the rest:

| Route | Purpose |
|---|---|
| `POST /v2/app_management/git` | Register an app: name, URL, branch, access mode. Generates the key in key mode. Does not clone. |
| `GET /v2/app_management/git/{app}` | The state, the public key, and once cloned the compose summary and the `.env` template. Also answers for an adoptable app. |
| `PUT /v2/app_management/git/{app}` | Branch, automatic rebuild, access mode, token (accepted, never returned). Adopts an adoptable app. |
| `POST /v2/app_management/git/{app}/check` | Start a check in the background and answer 202; the client polls `GET` until `operation` is null. Adopts an adoptable app. For a registered app that is not cloned yet: clone it and validate its compose files. |
| `POST /v2/app_management/git/{app}/deploy` | Body `{commit?, env?}`, answers 202. Adopts an adoptable app. No commit: the remote's latest. A commit from the history: a revert. `env` is accepted until the first successful deployment, and never when `.env` is tracked. |
| `DELETE /v2/app_management/git/{app}` | Remove a registered app while no deployment has succeeded: the containers and networks a failed first deployment left, the clone of a created app, the state and the secrets. |

The design review presented five routes; the sixth, `DELETE`, exists so that a creation abandoned
before its first deployment leaves nothing behind.

`WebAppGridItem` gains `git: {new_commits: boolean, state: idle | building | deploying |
build_failed | rolled_back | failed | unreachable | interrupted, deployed: boolean}` for the card.
A registered git app with no container, never deployed or left without one by a failed first
deployment, still appears in the grid, so the owner can reach its Repository tab and delete it. The six event types
`app:git-build-begin`, `app:git-build-progress`, `app:git-build-end`, `app:git-build-error`,
`app:git-deploy-end` and `app:git-deploy-error` join `common.EventTypes` with the `app:name`
property; the progress event also carries `message`, the log lines.

Uninstalling a git app removes its state and secrets. With "delete the config folder", the clone of
a created app goes too. An adopted folder is never deleted.

## Backups and restore

- A backup of a git app adds `git: {remote, branch, commit}` to its manifest. The field is
  optional, so the manifest format version does not change and older readers ignore it. Data and
  `.env` are copied as today. Nothing adds the source tree.
- **Restore on a machine where the app is installed:** unchanged. The data comes back and the
  deployed commit stays, as for every app: the definition is not data.
- **Restore where the app is not installed:** a manifest with `git` registers the app with its
  remote and branch, clones it at the recorded commit, writes `.env` from the backup, builds,
  restores the data and starts. A private repository needs access again: the restore answers with
  "access required" and a new public key, since keys and tokens are never in a backup.

## Disk

- The images of the last three deployments are kept; older `git-` tags are removed once unused.
- Worktrees are removed after each build and at startup.
- Docker's build cache is left to Docker and to the existing image cleanup.

## Installer

`install.sh` adds `git` to `CASA_DEPANDS_PACKAGE` and to `CASA_DEPANDS_COMMAND`. An upgrade
installs it like any missing dependency.

## Dashboard

- `src/service/gitApps.js`, a hand-written client like `service/updates.js`, registered as
  `$api.gitApps`. The generated client lags the spec, and regenerating it rewrites thousands of
  lines.
- `Apps/GitAppModal.vue`: the three steps, then the live log.
- `Apps/GitRepoTab.vue`: the Repository tab.
- `Apps/gitApps.js`: pure helpers (state to badge, outcome labels, compose summary flags), with
  specs.
- `AppPanel.vue` gains the Repository tab for git and adoptable apps; its Settings and Compose
  tabs show the notice for git apps, and its `.env` tab turns read-only when `.env` is tracked.
- `AppCard.vue` shows the git badges; `AppSection.vue` gains the menu entry.
- New strings go to `en_US.json` and `fr_FR.json` only.

## Testing

- **First, a feasibility spike:** build a local repository through compose's library from
  AppManagement's process, under systemd, on a runner with buildx and on one without. Everything
  in "A deployment" depends on it; it is throwaway code.
- **`pkg/git`:** unit tests against local bare repositories (`file://`), with no network: clone,
  default branch, fetch, ancestry, fast-forward refusal on a diverged or modified tree, tracked
  `.env` detection, worktrees with a submodule, hooks never running. A local HTTP git server that
  requires basic auth checks that the token reaches git through the askpass helper and appears in
  no argument and no stored output.
- **Deployment:** unit tests with a fake builder and a fake starter: success; build failure;
  start failure and rollback; a first deployment failing with nothing to roll back to; rollback
  failure and `blocked`; adoption tagging the running images; a revert pausing automatic rebuild;
  an attempted commit not retried; the guard refusing an overlap; startup marking an operation
  interrupted.
- **Integration**, with `CASAOS_INTEGRATION=1` against the daemon: a local repository with a
  Dockerfile and a compose file is created, deployed, gets a new commit deployed automatically,
  then a broken commit rolled back, then a revert.
- **Install check**, a new step on the three legs:
  - create an app from a bare repository on the runner through the API;
  - push a second commit and poll the app's port through the whole automatic rebuild, failing on
    any unanswered request before the switch;
  - push a commit whose container exits and assert `rolled_back`, with the previous version
    answering;
  - assert the grid's `git` state along the way.
- **Dashboard:** vitest specs for the dialog steps, the tab states, the badges and the client's
  methods and URLs.

## Out of scope for this release

- Webhooks.
- Repositories without a compose file, and a compose file outside the repository root.
- Zero-downtime switching.
- Following tags or several branches of one repository.
- Managing Docker's build cache.

## Clarifications made while planning

Writing the implementation plans surfaced these points; they are binding for all three plans.

1. `POST /git/{app}/deploy` adopts an adoptable app, like `PUT` and `check`.
2. An adoptable folder in detached HEAD answers `branch: ""`, one without a remote answers
   `remote: ""`, and `PUT`, `check` and `deploy` on it answer 400 with the reason.
3. A registered git app with no container appears in the grid as
   `{app_type: "v2app", name, title: {en_us: name}, status: "", git: {..., deployed: false}}`.
   Its card offers the Repository tab and deletion only.
4. `DELETE /git/{app}` is allowed while `deployed` is null, whatever the history, and answers 409
   once a deployment has succeeded or while an operation runs.
5. `compose.services[].sensitive` holds only these values: `privileged`, `network_mode_host`,
   `pid_host`, `cap_add`, `devices`, `docker_socket`.
6. `check` runs in the background. A clone without a compose file ends with the clone removed,
   `check.error` holding the message, and `compose_example` holding a sample compose file;
   `compose_example` is null otherwise. An automatic deployment a check triggers starts once the
   check's operation has ended, in the background too.
7. `new_commits` is false while `deployed` is null.
8. In `PUT`, `"token": ""` is invalid; switching `access` to `none` or `key` forgets a saved token.
