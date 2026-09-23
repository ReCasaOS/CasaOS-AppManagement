# Git apps that follow tags — design

Status: approved in conversation on 2026-09-23, section by section. Builds on
`2026-09-16-git-apps-design.md` (git apps), whose "Out of scope" listed "Following tags or several
branches", and on `2026-09-23-git-webhooks-design.md`.

## Goal

A git app can follow the newest release of its repository instead of the head of a branch: it
deploys `v1.4.2`, then `v1.5.0` when it is tagged, never a commit pushed between two releases, and
never goes back to an older release by itself.

## Decisions (from the owner)

- The version followed is the **highest semver tag**. Pre-releases (`-rc`, `-beta`, …) are
  ignored unless the app includes them. An optional **pattern** (a glob on the tag name, such as
  `v2.*`) keeps the app on one line of releases.
- **Upgrades only.** Automatic deployment takes a tag strictly higher than the deployed one. A
  deleted tag, a narrowed pattern or a moved tag never makes the app go back or redeploy by
  itself. By hand, the owner can deploy any eligible tag, older ones included.
- A tag that **moved** (same name, another commit) is reported, never redeployed automatically.
- The mode (branch or tags) is chosen at registration and can be **changed later** in the
  Repository tab. After a change, the first deployment in the new mode is manual; automatic
  deployment resumes after it.
- Approach A: the commit stays the identity of a version everywhere (images `git-<commit12>`,
  history, reverts); the tag is recorded beside it. No new component, no new dependency
  (`Masterminds/semver/v3` is already a direct dependency).

## Model and API

### State of an app

- `follow`: `"branch"` (the default, today's behaviour) or `"tags"`. A plain string in the API,
  validated in code, never an OpenAPI enum: the codegen's enum-constant count stays at 34.
- `tag_pattern`: optional glob on the full tag name (`path.Match` syntax: `*`, `?`, `[...]`).
  Empty means every semver tag. Validated: at most 100 characters, no whitespace or control
  character, and `path.Match` must accept it as a pattern.
- `prereleases`: bool, default false.
- In tag mode `branch` stays empty, and the check never resolves the remote's default branch
  (today an empty branch is filled with it on the first check, which would make every later
  deployment in tag mode fail "not on the branch").
- `deployed`, every `history` entry and `operation` gain `tag`: the tag name the commit was
  deployed as, empty for a branch deployment.

### The app's view

- `follow`, `tag_pattern`, `prereleases` at the top level.
- `check` gains `remote_tag` (the highest eligible tag; empty in branch mode; when none is
  eligible, the check's error says so and the previous `remote_tag` and `remote_commit` are
  kept) and `tag_moved` (true when `remote_tag` equals `deployed.tag` and `remote_commit`
  differs from `deployed.commit`).
- `deployed.tag`, `history[].tag` and `operation.tag`.
- The view contract test (`git_app_view_internal_test.go`) lists the new keys.

### Routes

- `POST /v2/app_management/git` and `PUT /v2/app_management/git/{app}` accept `follow`,
  `tag_pattern` and `prereleases`. Every request field goes into the OpenAPI schema, the
  codegen and the route's hand copy.
- `GET /v2/app_management/git/{app}/tags`: the eligible tags of the remote, highest first, at most
  50: `{name, commit}` each. It asks the remote (like "Check now"), so it is its own route and
  the view stays local. Refused with 400 in branch mode.
- `POST /v2/app_management/git/{app}/deploy` accepts `tag` as an alternative to `commit` (giving
  both is a 400). In tag mode a request with neither deploys the highest eligible tag at the time of the
  request, as a branch app deploys the branch's head today.

### Changing the mode

`PUT` with `follow` changes the mode; `tag_pattern` and `prereleases` are kept when switching back
and forth. Switching to branches sets `branch` from the request, or leaves it empty for the
remote's default. No extra state: automatic deployment in tag mode requires a non-empty
`deployed.tag`, and in branch mode an empty `deployed.tag`. So after a switch, nothing deploys by
itself until the owner deploys once in the new mode.

## The check and the choice of the tag

- `git ls-remote --tags -- <url>` lists the tags. For an annotated tag the line
  `refs/tags/<name>^{}` gives the commit and the plain line gives the tag object, which is ignored
  once the peeled line is seen; a lightweight tag has only the plain line, which is the commit.
- A tag name from the remote that is not a valid ref name (the rules `validGitBranch` applies) is
  ignored and never passed to git.
- Eligible: the name matches `tag_pattern` when there is one; the name without one leading `v` is
  strict semver (`semver.StrictNewVersion`: `1.4.2`, `1.4.2-rc.1`, `1.4.2+build`); a pre-release
  counts only when `prereleases` is on. Other tags (`latest`, `stable`, `2024-09`) are ignored.
- The highest version wins. Equal versions (`v1.2.3` and `1.2.3`, or build metadata only) are
  ordered by name, so the choice is always the same.
- The check records `remote_tag` and `remote_commit`. With no eligible tag, `check.error` says so
  with the filter in force ("no tag matches (pattern `v2.*`, pre-releases excluded)") and nothing
  else changes.
- The first clone in tag mode clones at the chosen tag
  (`git clone --branch <tag> --single-branch -- <url> <dir>`), which leaves a detached HEAD; the
  compose-file check of the clone is unchanged.
- Before building, the deployment fetches that tag alone
  (`git fetch --no-tags -- <url> refs/tags/<tag>`), peels it to its commit and refuses, asking for
  a new check, when it no longer is the commit it was asked to deploy.
- One tag listing per check replaces the branch's `ls-remote`; the five-minute poll and the
  webhooks are unchanged.

## Deployment, history and reverts

- Preconditions in tag mode: the folder is on a detached HEAD (today's `info.Branch != st.Branch`
  test with an empty branch), tracked files are clean, and the `.env` rules are unchanged.
- The branch-only rules do not apply in tag mode: "the target is on the branch", "the target
  descends from the deployed commit", "the folder is contained in the branch". In their place:
  - **automatic**: `remote_tag` strictly higher in semver than `deployed.tag` (which must not be
    empty), `remote_commit` not in `attempted`, not paused, not blocked;
  - **manual**: any eligible tag, lower ones included. A tag whose commit is not in the history
    is built; a commit in the history stays a revert (no build, images reused), as today.
- The switch uses `reset --keep` to the target commit instead of a fast-forward, since tags do not
  form a line. A failed start rolls back to the previous commit as today, and blocks the app if
  that fails too.
- Automatic deployment never targets a lower tag, so a deleted newest tag cannot silently revert
  the app to a history entry. Branch mode keeps its current behaviour.
- The folder keeps one local ref per history entry, `refs/recasaos/kept/<commit>`, created when
  the commit is deployed and removed when the entry leaves the history, so that git never drops a
  commit a revert needs after its tag moved or was deleted. `revertable` also requires the commit
  to be present.
- A moved tag (`tag_moved`) is never redeployed automatically. By hand, deploying that tag builds
  its new commit.
- `deployed`, history and the running operation carry the tag.

## Webhooks, poll, backups and restore

- **Webhooks**: no server change. GitHub, Gitea and Forgejo send `push` for a tag created or
  deleted, GitLab sends `Tag Push Hook`; all of them already start a check, and the body is never
  read. The dashboard's GitLab guide adds "Tag push events".
- **Poll**: every five minutes, as today.
- **Backups**: the manifest's git origin gains `follow`, `tag`, `tag_pattern` and `prereleases`,
  all omitted when empty, so a branch-mode backup is byte-for-byte what it is today.
- **Restore on a box without the app**: the app comes back in the same mode and pattern. The
  repository is cloned at the backup's tag; if the tag still points at the backed-up commit, that
  is it. If the tag moved or is gone, the restore fetches the commit itself
  (`git fetch -- <url> <commit>`); if the forge refuses (some do not serve a commit by hash), the
  restore fails with "the tag v1.4.2 no longer points at the backed-up commit, and the forge does
  not give that commit", never falling back to something else. The webhook comes back off, as
  today.
- **Restore on a box that has the app**: unchanged; the data comes back, the definition stays.
- An older AppManagement restoring a tag-mode backup sees an empty branch and fails with its
  usual access message: accepted, since it only concerns a new backup restored on an old version.
- **Message bus**: no new event; the six `app:git-*` events cover it.

## Dashboard

- **Registration**: a choice "Follow a branch / Follow tags". Branch: the current branch field.
  Tags: an optional pattern ("For example `v2.*` to stay on version 2; empty for every version
  tag") and a checkbox "Include pre-releases (-rc, -beta)".
- **Repository tab**:
  - status line: "Newest tag v1.4.2 · deployed v1.4.1", or "Up to date (v1.4.2)";
  - a moved tag: "The tag v1.4.2 now points at another commit: it is not redeployed
    automatically", with a "Redeploy v1.4.2" button;
  - no eligible tag: the check's message, which names the pattern and the pre-release setting;
  - mode, pattern and checkbox are edited where the branch is today; on a change of mode, the note
    "The first deployment in this mode is manual; automatic deployment resumes after it";
  - "Deploy a tag…" opens the list from `GET …/tags` (name, short commit, and a label: deployed,
    newest, in history); deploying a tag lower than the deployed one asks for confirmation
    ("This is an older version than the one deployed");
  - history shows "v1.4.1 (abc1234)" instead of the commit alone.
- **App grid**: the update badge follows the check, which compares tags in semver; it never lights
  for a lower or a moved tag.
- **Webhook guide**: GitLab lists "Push events" and "Tag push events".
- Every string in the language files, English and French, following GitRepoTab.

## Core (CasaOS)

Anonymous statistics log each successful send at Info (`telemetry: sent`, with the event name), so
that `journalctl -u casaos | grep telemetry` shows them; nothing else changes.

## Tests

### AppManagement (Go, on Linux in CI)

- Tag listing: annotated and lightweight tags, the peeled line giving the commit, an invalid name
  ignored, an empty listing.
- Choice: pattern, pre-releases excluded then included, non-semver tags ignored, `v1.2.3` against
  `1.2.3`, no eligible tag and its message.
- Automatic: deploys a strictly higher tag; nothing for a deleted tag, a narrowed pattern or a
  moved tag (`tag_moved`); nothing while `deployed.tag` is empty after a change of mode.
- Manual: an older tag builds; a history commit reverts; a tag that moved since the check is
  refused.
- History: the `kept` refs are made and removed; a revert works after the tag was deleted on the
  remote.
- API: the new fields, `GET …/tags`, `deploy {tag}` (and `tag` with `commit` is a 400); the view
  contract test; the enum-constant count stays 34.
- Backups and restore: a tag-mode manifest; restore at the tag, then by commit when the tag moved;
  a branch-mode manifest unchanged byte for byte.

### Dashboard (vitest)

The mode choice and its fields at registration and in the tab; the tag list and its labels; the
confirmation of an older tag; the moved-tag warning; history with tags; the GitLab guide.

### Core (Go)

A successful send logs `telemetry: sent` with the event name.

### Install check

A new step "An app that follows tags", on the test repository the git step already uses: tag
`v1.0.0`, register the app in tag mode, deploy by hand; turn automatic deployment on, tag `v1.1.0`
and post a signed push to the webhook: `v1.1.0` is deployed; tag `v1.2.0-rc.1`: nothing happens;
delete `v1.1.0`: no return to `v1.0.0`; move `v1.1.0` to another commit: `tag_moved`, no
deployment.

## Components and release

- **CasaOS-AppManagement**: the model, the check, the deployment, the routes, the OpenAPI schema
  and its codegen, backups and restore.
- **CasaOS-UI**: registration, the Repository tab, the tag list, the grid badge, the guide.
- **CasaOS**: the `telemetry: sent` log line.
- **CasaOS-Install**: the install-check step.
- Shipped together in one distribution release.

## Out of scope

- Following several branches, or a branch and tags at once.
- Tags that are not semver (dates, codenames), and floating tags (`stable`, `v2`) redeployed when
  they move.
- Repositories without a compose file (the next design).
