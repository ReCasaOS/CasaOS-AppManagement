# Git Apps That Follow Tags: Install Check and Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove on a freshly installed box that a git app registered in tag mode deploys its highest release, upgrades automatically when a higher version is tagged and a signed push reaches its webhook, and never deploys a pre-release, an older tag left after a deletion, or a moved tag; then commit the distribution release that ships the feature, which the controller publishes.

**Architecture:** The install check gains one step, "An app that follows tags", right after "A push reaches a git app through its webhook". It tags the test repository the git step created (`/opt/gitcheck.git`), registers a second app, `gittags`, from it in tag mode on port 18081, deploys `v1.0.0` by hand, turns automatic deployment and the webhook on, and plays the forge with `git`, `openssl` and `curl` for four tag events: `v1.1.0` (deployed), `v1.2.0-rc.1` (nothing), `v1.1.0` deleted (nothing, no return to `v1.0.0`), `v1.1.0` moved to another commit (`tag_moved`, nothing). The release pins the CasaOS-AppManagement, CasaOS-UI and CasaOS tags published before it, and the install check of that release proves the chain.

**Tech Stack:** bash under `set -euo pipefail`, GitHub Actions (`.github/workflows/install-check.yml`), `git` + `curl` + `jq` + `openssl` on the Ubuntu 22.04 runners, AppManagement's v2 API through the gateway, Python 3 + PyYAML for local validation.

**Spec:** D:/clients/casaos/CasaOS-AppManagement/docs/superpowers/specs/2026-09-23-git-tags-design.md

## Global Constraints

- Repository: `D:/clients/casaos/get-digest` (GitHub `ReCasaOS/CasaOS-Install`, remote `inkly`). The remote `origin` is IceWhale's: never fetch from it, never push to it. Work happens on branch `feat/git-tags`, created from `inkly/main` in Task 1, Step 1. Every relative path below is relative to that repository.
- `feat/git-tags` reaches `inkly/main` only when the controller releases it, after Task 2, once CasaOS-AppManagement, CasaOS-UI and CasaOS have published the feature. Until then, any release cut from `main` would run the new step against an AppManagement that does not follow tags, and go red.
- No step of this plan pushes, tags, merges, pulls, rebases, resets, cleans or stashes, or runs a linter or formatter with a fix option. Both tasks end with a local commit. Publishing the release (bringing `main` forward, the distribution tag, watching the release and its install check) is the controller's: the hand-off at the end of Task 2 says what it releases and how the install check proves it. Never dispatch `release.yml` by hand. `install-check.yml` alone may be dispatched by hand against a published tag: `gh -R ReCasaOS/CasaOS-Install workflow run install-check.yml -f tag=vX`.
- The `git commit`, `git tag` and `git push` inside the new workflow step run on the CI runner, against the throwaway repository `/opt/gitcheck.git` that the git step created there. They are not steps of this plan.
- AppManagement contract the step relies on (produced by this feature's AppManagement plan; the spec's names):
  - `POST /v2/app_management/git` accepts `follow` (the plain string `"branch"` or `"tags"`), with the optional `tag_pattern` (string) and `prereleases` (bool); the step sends `{"name":"gittags","url":"file:///opt/gitcheck.git","access":"none","follow":"tags"}`. Two apps may be registered from the same URL.
  - The app's view is `.data` of `GET /v2/app_management/git/{app}` and of the answers to POST, `check`, `deploy` and PUT. Keys read: `follow`; `branch`, which is `""` in tag mode, before and after a check; `cloned`; `head.commit`; `state` (`idle` when nothing runs and the last deployment succeeded); `operation`, null or `{kind, commit, tag, started_at}` with `kind` one of `check`, `build`, `deploy`, `revert`; `deployed`, `{commit, subject, at, tag}`; `history`, newest first, entries `{commit, subject, at, outcome, reason, revertable, tag}`; `check`, `{at, remote_commit, remote_tag, tag_moved, error}`, where an absent `tag_moved` counts as false; `auto_deploy`; `webhook`, `{enabled, path, secret (while enabled), last_delivery}`. `tag_pattern` and `prereleases` are printed, not asserted.
  - `POST /v2/app_management/git/{app}/check` answers 202. In tag mode it lists the remote's tags, records the highest eligible one in `check.remote_tag` and its commit in `check.remote_commit` (for an annotated tag, the peeled commit, never the tag object), and never writes the remote's default branch into `branch`. The first check of a registered app clones it at that tag, so `head.commit` is the tag's commit.
  - `POST /v2/app_management/git/{app}/deploy` accepts `{"tag": "<name>", "env": "<.env text>"}` and answers 202 with `.data.operation.commit` set to the tag's commit and `.data.operation.tag` to its name.
  - `GET /v2/app_management/git/{app}/tags` answers 200 with `.data` an array of `{name, commit}`: the eligible tags of the remote, highest first, each with its peeled commit.
  - `PUT /v2/app_management/git/{app}` accepts `auto_deploy` and `webhook_enabled` as today, and keeps `follow` when the body does not name it.
  - Automatic deployment in tag mode takes only a `check.remote_tag` strictly higher in semver than a non-empty `deployed.tag`. A pre-release while `prereleases` is false, a lower tag left after the newest was deleted, and a moved tag (`check.remote_tag == deployed.tag` with another commit, reported as `check.tag_moved: true`) start nothing. The gate stops before any attempt: no operation, no history entry, no `attempted` commit. The semver rule therefore belongs to the automatic gate (`gitMayDeployAutomatically` in `service/git_deploy.go`, which `deployGitAppAutomatically` calls before it hands the check's hold to a deployment), not to `gitDeployPreconditions`: an automatic deployment refused by a precondition records a `failed` history entry, adds its commit to `attempted` and turns `state` to `failed`, which `nothing_deployed` reports as a change. The AppManagement plan of this feature puts it in the gate (its Global Constraints, "Automatic gate").
  - Unchanged from the webhook release: `POST /v2/app_management/git/{app}/webhook` signed with `X-Hub-Signature-256: sha256=<hex HMAC-SHA256 of the raw body, keyed with the 64-character secret string>` and `X-GitHub-Event: push` answers 202 `{"message":"checking"|"coalesced"|"queued"}` and never reads the body. `coalesced` (a push less than 10 seconds after the last check a webhook started) still gets one check when the window closes, and `queued` (a busy app) one check when the app is free. A call on an app another operation holds answers 409.
- What the previous steps leave behind: the bare repository `/opt/gitcheck.git` (branch `main`); the work clone `/opt/gitcheck-work`, owned by root, on `main` at the webhook step's `v3` commit, whose `Dockerfile` still carries the git step's `RUN sleep 15` and a `CMD` that exits at once, with an untracked `grid.json` beside the tracked files; the app `gitcheck`, branch mode, running `v2` on port 18080, with automatic deployment and its webhook both off. Every git command in the clone runs as `sudo git -C /opt/gitcheck-work`.
- Each step starts in the runner's workspace, so the new step's own files (`answer.json`, `push.json`, `hook.json`) are written there, not in the clone.
- `CASA_URL` is `http://127.0.0.1:<gateway HttpPort>` (written by "The dashboard answers"). `CASA_TOKEN` is the `smoke` user's access token, already masked (written by "First user, then a token").
- The webhook secret never reaches the CI log: `::add-mask::` is emitted as soon as it has been validated, and no print of the view includes `.webhook.secret`.
- Under `set -o pipefail`, never pipe into a reader that stops early (`grep -q`, `head`): capture first, or use `sed -n 1p`. Under `set -e`, a check that must fail the step is never a bare `! cmd` (a negated command never stops the shell): use `if ! cmd; then ...; exit 1; fi` or `test`.
- Local host: Windows with Git Bash and forward slashes. Python 3.12 with PyYAML is available and `jq` is not. `bash -n` is called through `shutil.which('bash')`, because a bare `bash` started from Python can resolve to WSL's launcher in System32.
- Inline Bash commands lose their backslashes, so the workflow is edited with the Edit tool only (never `sed -i` or `echo >>`), and the commands in this plan contain no backslashes. The workflow has CRLF line endings (`core.autocrlf=true`); the edit keeps them.
- Bash tool state does not persist between calls: a command that needs a value computed earlier (a version, a run id) computes it again.
- `install.sh` does not change: the box needs nothing new to follow tags.
- Commits: `git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "..."`, conventional subjects in plain English. Never add a Co-Authored-By trailer or any AI or tool attribution.
- Pinned versions stay in `release/components.env` as a TAG plus the full COMMIT sha.

---

## File structure

| File | Change | Responsibility |
|---|---|---|
| `.github/workflows/install-check.yml` | Modify: insert a step between line 967 (last line of "A push reaches a git app through its webhook") and line 969 (`      - name: A RAID array is one storage`) | The step "An app that follows tags": the end-to-end proof, through the gateway, of registration in tag mode, the first clone at a tag, a deployment by hand of a tag, the automatic upgrade a signed push starts, and the three tag events that must deploy nothing |
| `release/components.env` | Modify: `CASAOS_RELEASE_TAG`, `CASAOS_TAG`/`_COMMIT`, `CASAOS_APP_MANAGEMENT_TAG`/`_COMMIT`, `CASAOS_UI_TAG`/`_COMMIT` | The component versions the distribution installs |
| `CHANGELOG.md` | Modify: a new section above the newest `## [` section | What the release changes, for owners |
| `README.md` | Modify: a new `## What is in` section above the newest one; one sentence in "Anonymous statistics"; the CasaOS, CasaOS-UI and CasaOS-AppManagement rows of the components table | The release summary, how to see statistics sends, the component table |

---

### Task 1: The install check follows the releases of a git app

**Files:**
- Modify: `.github/workflows/install-check.yml:967-969` (the new step goes after the blank line 968, before `      - name: A RAID array is one storage`)
- Test: the local structural checks below. The real test is the install check of the release the controller publishes after Task 2 (see the hand-off at the end of Task 2).

**Interfaces:**
- Consumes: the AppManagement contract and the webhook answers listed under Global Constraints (`POST /v2/app_management/git` with `follow`, `check`, `deploy` with `tag`, `GET …/tags`, `PUT`, the webhook; the view keys `follow`, `branch`, `cloned`, `head.commit`, `state`, `operation.{kind,commit,tag}`, `deployed.{commit,tag}`, `history[].{tag,outcome}`, `check.{at,remote_commit,remote_tag,tag_moved,error}`, `auto_deploy`, `webhook.{enabled,path,secret}`); the env `CASA_URL` and `CASA_TOKEN`; `/opt/gitcheck.git` and `/opt/gitcheck-work` as the previous steps leave them.
- Produces: a workflow step named exactly `An app that follows tags`, which leaves the app `gittags` (tag mode, automatic deployment and webhook on, `v1.1.0` running on port 18081) and these tags on `/opt/gitcheck.git`: `v1.0.0` (annotated), `v1.2.0-rc.1` (lightweight), `v1.1.0` (annotated, on the commit `v1.1.0-moved`). On a green run it prints these lines, in this order:
  - `registered: {"follow":"tags",…,"branch":""}`
  - `first check: {…"cloned":true,"head":"<v1.0.0 commit>",…"remote_tag":"v1.0.0"…}`
  - `deployment asked: {"kind":"build","commit":"<v1.0.0 commit>","tag":"v1.0.0"}`
  - `v1.0.0 deployed by hand: {…"deployed":{…"tag":"v1.0.0"}…}`
  - `automatic deployment and webhook: {"follow":"tags","auto_deploy":true,"webhook":{"enabled":true,"path":"/v2/app_management/git/gittags/webhook",…}}`
  - `webhook for v1.1.0: 202 {"message":"checking"}` (or `coalesced` or `queued`, here and below)
  - `v1.1.0 deployed automatically: {…"deployed":{…"tag":"v1.1.0"}…}`
  - `webhook for v1.2.0-rc.1: 202 {"message":"checking"}`
  - `after v1.2.0-rc.1: {…"remote_tag":"v1.1.0"…}`
  - `v1.2.0-rc.1: nothing deployed, v1.1.0 still answers`
  - `eligible tags: [{"name":"v1.1.0","commit":"…"},{"name":"v1.0.0","commit":"…"}]`
  - `webhook for the deletion of v1.1.0: 202 {"message":"coalesced"}`
  - `after v1.1.0 was deleted: {…"remote_tag":"v1.0.0"…}`
  - `v1.1.0 deleted: nothing deployed, v1.1.0 still answers`
  - `webhook for v1.1.0 moved: 202 {"message":"checking"}`
  - `after v1.1.0 moved: {…"remote_tag":"v1.1.0",…"tag_moved":true…}`
  - `v1.1.0 moved: nothing deployed, v1.1.0 still answers`

- [ ] **Step 1: Create the branch**

Run: `git -C D:/clients/casaos/get-digest status --porcelain`
Expected: no output (a clean tree). If it lists files, stop: another session is working in this clone.

Then run:
```bash
git -C D:/clients/casaos/get-digest fetch inkly && git -C D:/clients/casaos/get-digest switch --no-track -c feat/git-tags inkly/main
```
Expected: `Switched to a new branch 'feat/git-tags'`, and no `set up to track` line: like `feat/git-webhooks`, the branch has no upstream. If the branch already exists, stop: another session has started this task.

- [ ] **Step 2: Write the failing structural check**

The check asserts that the step following the webhook step is the new one:
```bash
cd D:/clients/casaos/get-digest && python -c "import yaml; steps=yaml.safe_load(open('.github/workflows/install-check.yml', encoding='utf-8'))['jobs']['install-check']['steps']; names=[s.get('name') for s in steps]; i=names.index('A push reaches a git app through its webhook'); assert names[i+1] == 'An app that follows tags', names[i+1]; print(names[i+1], '|', names[i+2])"
```

- [ ] **Step 3: Run it and see it fail**

Run the command of Step 2.
Expected: it ends with `AssertionError: A RAID array is one storage`.

- [ ] **Step 4: Check the anchor**

Run: `cd D:/clients/casaos/get-digest && sed -n 967,969p .github/workflows/install-check.yml`
Expected:
```
          echo "secret or signature in the AppManagement log: none"

      - name: A RAID array is one storage
```
If the lines have moved (because `inkly/main` gained a commit since this plan was written), keep the same anchor text: the new step goes right after the webhook step's last line, before the RAID step.

- [ ] **Step 5: Insert the step**

With the Edit tool on `.github/workflows/install-check.yml`, replace `      - name: A RAID array is one storage` (unique in the file) with the block below. The block ends with that same line, preceded by one blank line.
```yaml
      # An app that follows the releases of its repository instead of a branch. The test
      # repository gets version tags, and a second app, gittags, is registered from it in
      # tag mode, on port 18081 (gitcheck keeps 18080). v1.0.0 is deployed by hand; with
      # automatic deployment and the webhook on, v1.1.0 is tagged and a signed push posted,
      # and it is deployed. Then nothing may deploy: a pre-release tag, v1.1.0 deleted (the
      # app does not go back to v1.0.0), and v1.1.0 tagged again on another commit, which
      # the check reports as moved. v1.0.0 and the second v1.1.0 are annotated tags, so the
      # commit the box reads for them must be the peeled one, not the tag object.
      - name: An app that follows tags
        run: |
          set -euo pipefail
          app_view() { curl -fsS "${CASA_URL}/v2/app_management/git/gittags" -H "Authorization: ${CASA_TOKEN}"; }
          trap 'app_view | jq ".data | {state, operation, follow, branch, head, deployed, check, history, build_log}" || true' EXIT
          # g: git in the work clone the previous steps pushed from, as the owner of that clone
          g() { sudo git -C /opt/gitcheck-work -c user.name=check -c user.email=check@example.invalid "$@"; }
          # publish <text>: commits index.html saying <text> on main and pushes main
          publish() {
            echo "$1" | sudo tee /opt/gitcheck-work/index.html >/dev/null
            g add Dockerfile compose.yaml index.html
            g commit -q -m "$1"
            g push -q origin main
          }
          # send <method> <path after /v2/app_management/git> [<json body>]: an API call, asked
          # again while another operation holds the app (409), such as the five-minute check;
          # prints the answer, or the refusal and its message
          send() {
            local code body=()
            if [[ $# -ge 3 ]]; then body=(-H 'content-type: application/json' -d "$3"); fi
            for _ in $(seq 1 30); do
              rm -f answer.json
              code="$(curl -sS -o answer.json -w '%{http_code}' -X "$1" "${CASA_URL}/v2/app_management/git$2" \
                -H "Authorization: ${CASA_TOKEN}" "${body[@]}")"
              [[ "${code}" == 409 ]] || break
              sleep 2
            done
            [[ "${code}" == 2* ]] || { echo "$1 $2: ${code} $(cat answer.json 2>/dev/null)" >&2; return 1; }
            cat answer.json
          }
          # wait_for <label> <jq condition> [jq options]: polls the app, up to five minutes,
          # until no operation is recorded and the condition holds on the summary below;
          # prints the last summary seen
          wait_for() {
            local label="$1" condition="$2" seen=""
            shift 2
            for _ in $(seq 1 150); do
              seen="$(app_view | jq -c '.data | {state, operation, branch, cloned, head: .head.commit, deployed, check}')"
              if jq -e "$@" ".operation == null and (${condition})" <<<"${seen}" >/dev/null; then
                echo "${label}: ${seen}"
                return 0
              fi
              sleep 2
            done
            echo "${label}, never reached: ${seen}"
            return 1
          }
          # answers <text>: the app's port answers <text>
          answers() {
            local body
            body="$(curl -fsS --max-time 5 http://127.0.0.1:18081/)"
            [[ "${body}" == "$1" ]] || { echo "18081 answers ${body}, not $1"; return 1; }
          }
          # announce <label> <tag> <commit>: posts what GitHub sends when refs/tags/<tag>
          # changes, signed with the app's secret (the box reads the signature, never the body)
          announce() {
            local signature code
            printf '{"ref":"refs/tags/%s","after":"%s"}' "$2" "$3" > push.json
            signature="$(openssl dgst -sha256 -hmac "${secret}" <push.json | awk '{print $NF}')"
            code="$(curl -sS -o hook.json -w '%{http_code}' -X POST "${CASA_URL}${path}" \
              -H 'content-type: application/json' -H 'X-GitHub-Event: push' \
              -H "X-Hub-Signature-256: sha256=${signature}" --data-binary @push.json)"
            echo "webhook for $1: ${code} $(cat hook.json)"
            test "${code}" = 202
            case "$(jq -r '.message' hook.json)" in checking|coalesced|queued) ;; *) return 1 ;; esac
          }
          # nothing_deployed <label>: five seconds after the check ended, no deployment runs,
          # and the folder, the deployed version and the history are what they were once
          # v1.1.0 was deployed: nothing was deployed, and nothing was tried and refused
          nothing_deployed() {
            local now
            sleep 5
            now="$(app_view | jq -c '.data | {operation, kept: {head: .head.commit, deployed, history}}')"
            if ! jq -e --argjson kept "${kept}" '(.operation == null or .operation.kind == "check") and .kept == $kept' <<<"${now}" >/dev/null; then
              echo "$1: the app changed: ${now}"
              return 1
            fi
            answers v1.1.0
            echo "$1: nothing deployed, v1.1.0 still answers"
          }

          # the previous steps left main's Dockerfile exiting at once: the first release
          # starts again, on a port of its own
          sudo tee /opt/gitcheck-work/Dockerfile >/dev/null <<'EOF'
          FROM busybox
          COPY index.html /www/index.html
          CMD ["httpd", "-f", "-p", "80", "-h", "/www"]
          EOF
          sudo tee /opt/gitcheck-work/compose.yaml >/dev/null <<'EOF'
          services:
            web:
              build: .
              ports:
                - "18081:80"
              env_file: .env
          EOF
          publish v1.0.0
          c1="$(g rev-parse HEAD)"
          g tag -a v1.0.0 -m v1.0.0
          g push -q origin refs/tags/v1.0.0

          answer="$(send POST "" '{"name":"gittags","url":"file:///opt/gitcheck.git","access":"none","follow":"tags"}')"
          echo "registered: $(jq -c '.data | {follow, tag_pattern, prereleases, branch}' <<<"${answer}")"
          jq -e '.data.follow == "tags" and .data.branch == ""' <<<"${answer}" >/dev/null
          send POST /gittags/check >/dev/null
          # cloned at the tag, and still no branch: the check never writes the remote's default
          wait_for "first check" '.cloned and .head == $c and .branch == "" and .check.remote_tag == "v1.0.0" and .check.remote_commit == $c and .check.error == ""' --arg c "${c1}"

          answer="$(send POST /gittags/deploy '{"tag":"v1.0.0","env":"GREETING=hello\n"}')"
          echo "deployment asked: $(jq -c '.data.operation | {kind, commit, tag}' <<<"${answer}")"
          jq -e --arg c "${c1}" '.data.operation.commit == $c and .data.operation.tag == "v1.0.0"' <<<"${answer}" >/dev/null
          wait_for "v1.0.0 deployed by hand" '.state == "idle" and .head == $c and .deployed.tag == "v1.0.0" and .deployed.commit == $c' --arg c "${c1}"
          answers v1.0.0

          answer="$(send PUT /gittags '{"auto_deploy":true,"webhook_enabled":true}')"
          secret="$(jq -r '.data.webhook.secret' <<<"${answer}")"
          [[ "${secret}" =~ ^[0-9a-f]{64}$ ]] || { echo "the view holds no 64-hex secret"; exit 1; }
          echo "::add-mask::${secret}"
          echo "automatic deployment and webhook: $(jq -c '.data | {follow, auto_deploy, webhook: (.webhook | del(.secret))}' <<<"${answer}")"
          jq -e '.data.follow == "tags" and .data.auto_deploy == true and .data.webhook.enabled == true' <<<"${answer}" >/dev/null
          path="$(jq -r '.data.webhook.path' <<<"${answer}")"
          test "${path}" = /v2/app_management/git/gittags/webhook

          publish v1.1.0
          c2="$(g rev-parse HEAD)"
          g tag v1.1.0
          g push -q origin refs/tags/v1.1.0
          announce v1.1.0 v1.1.0 "${c2}"
          wait_for "v1.1.0 deployed automatically" '.state == "idle" and .head == $c and .deployed.tag == "v1.1.0" and .deployed.commit == $c and .check.remote_tag == "v1.1.0"' --arg c "${c2}"
          answers v1.1.0
          kept="$(app_view | jq -c '.data | {head: .head.commit, deployed, history}')"
          jq -e '.history[0].tag == "v1.1.0" and .history[0].outcome == "deployed" and .history[1].tag == "v1.0.0"' <<<"${kept}" >/dev/null

          # a pre-release is not a release while the app leaves pre-releases out
          publish v1.2.0-rc.1
          c3="$(g rev-parse HEAD)"
          g tag v1.2.0-rc.1
          g push -q origin refs/tags/v1.2.0-rc.1
          before="$(app_view | jq -r '.data.check.at')"
          announce v1.2.0-rc.1 v1.2.0-rc.1 "${c3}"
          wait_for "after v1.2.0-rc.1" '.check.at != $b and .check.remote_tag == "v1.1.0" and .check.remote_commit == $c' --arg b "${before}" --arg c "${c2}"
          nothing_deployed v1.2.0-rc.1
          tags="$(send GET /gittags/tags | jq -c '[.data[] | {name, commit}]')"
          echo "eligible tags: ${tags}"
          jq -e --arg c1 "${c1}" --arg c2 "${c2}" '. == [{name: "v1.1.0", commit: $c2}, {name: "v1.0.0", commit: $c1}]' <<<"${tags}" >/dev/null

          # the newest release deleted: v1.0.0 is the newest left, and older than what runs
          g tag -d v1.1.0 >/dev/null
          g push -q origin :refs/tags/v1.1.0
          announce "the deletion of v1.1.0" v1.1.0 0000000000000000000000000000000000000000
          wait_for "after v1.1.0 was deleted" '.check.remote_tag == "v1.0.0" and .check.remote_commit == $c and (.check.tag_moved | not)' --arg c "${c1}"
          nothing_deployed "v1.1.0 deleted"

          # v1.1.0 again, on another commit: the version that runs, moved
          publish v1.1.0-moved
          c4="$(g rev-parse HEAD)"
          g tag -a v1.1.0 -m v1.1.0
          g push -q origin refs/tags/v1.1.0
          announce "v1.1.0 moved" v1.1.0 "${c4}"
          wait_for "after v1.1.0 moved" '.check.remote_tag == "v1.1.0" and .check.remote_commit == $c and .check.tag_moved == true' --arg c "${c4}"
          nothing_deployed "v1.1.0 moved"

      - name: A RAID array is one storage
```

Why the step reads this way:
- **A second app, not `gitcheck` switched to tags.** The spec's install check registers the app in tag mode, which is the path that sends `follow` at registration and clones a repository at a tag (a detached HEAD). Switching `gitcheck` would run neither: it is already cloned on `main`. It would also inherit everything the two previous steps leave in it (a rolled-back deployment in its history, the broken commit in its `attempted` list, automatic deployment and webhook off), and it would depend on how AppManagement moves a folder that is on a branch into tag mode, which the spec leaves to AppManagement's unit tests. The price of a second app is a port: the commits this step makes publish 18081, while `gitcheck` keeps running `v2` on 18080. Its automatic deployment is off, so the commits pushed to `main` here never reach it; its five-minute check only records them.
- **The Dockerfile and the compose file are written again.** `main`'s `Dockerfile` still carries the git step's `RUN sleep 15` and the `CMD` of its broken commit, and its compose file publishes 18080. Only `Dockerfile`, `compose.yaml` and `index.html` are staged: the git step left `grid.json` untracked in the clone.
- **Annotated and lightweight tags.** `v1.0.0` and the second `v1.1.0` are annotated, so the remote lists a tag object and a peeled `^{}` line for each. `check.remote_commit`, `head.commit` after the first clone and the `commit` of `GET …/tags` must be the commit itself, which the step knows from `git rev-parse HEAD`. `v1.1.0` (the first) and `v1.2.0-rc.1` are lightweight.
- **`send`** waits out a 409 from the five-minute check the way the webhook step's `put_app` does, and prints a refusal with its message, which the `curl -f` of the other steps would hide. It removes `answer.json` before each call: a call that fails before any answer (code `000`) must not print the previous answer, such as the PUT's view, as its own.
- **Every wait targets what the check must show.** A push less than 10 seconds after the last webhook check answers `coalesced`, and its check runs when the window closes; a busy app answers `queued`. So the step accepts all three answers and waits for the view that only a check made after the push can produce: the deployed `v1.1.0`, `remote_tag` `v1.0.0` after the deletion, `tag_moved` after the move.
- **The pre-release wait can only use `check.at`**, since a check that ignores `v1.2.0-rc.1` records what the previous one did. If it stops at a five-minute check that listed the remote just before the push, the proof still holds twice: `GET …/tags` lists the remote's eligible tags after the push, and the check after the deletion sees `v1.2.0-rc.1` still on the remote and must name `v1.0.0`.
- **`nothing_deployed` waits five seconds** after the check has ended, because the automatic deployment starts right after the check saves its result. A deployment started by mistake shows as an operation other than a check, or as a change in the folder, the deployed version or the history; even a refused automatic attempt writes a history entry. A five-minute check running at that moment is tolerated.
- `tag_moved` false is written `(.check.tag_moved | not)` so that an omitted false passes. `tag_pattern` and `prereleases` are printed, not asserted: their defaults are a serialization detail, and the unit tests pin them.
- The step leaves `gittags` running on 18081; no later step uses it or that port.

- [ ] **Step 6: Run the structural check and see it pass**

Run the command of Step 2.
Expected: `An app that follows tags | A RAID array is one storage`

- [ ] **Step 7: Check that every run block of the workflow parses**

Run:
```bash
cd D:/clients/casaos/get-digest && python -c "import yaml, shutil, subprocess; steps=yaml.safe_load(open('.github/workflows/install-check.yml', encoding='utf-8'))['jobs']['install-check']['steps']; print('bash -n failures:', [s['name'] for s in steps if 'run' in s and subprocess.run([shutil.which('bash'), '-n'], input=s['run'].encode()).returncode])"
```
Expected: `bash -n failures: []`

- [ ] **Step 8: Check the diff touches only the new step, with the file's line endings**

Run:
```bash
cd D:/clients/casaos/get-digest && git diff --stat && git diff --check && python -c "import sys; [print(p, 'bare LF:', open(p,'rb').read().replace(bytes([13,10]),bytes()).count(bytes([10]))) for p in sys.argv[1:]]" .github/workflows/install-check.yml
```
Expected: `1 file changed, 170 insertions(+)` for `.github/workflows/install-check.yml` (the step's 169 lines, comment included, and the blank line after it), no deletions, no output from `--check`, and `.github/workflows/install-check.yml bare LF: 0`. (`file` is not used here: it reads only the first 64 KiB of a file, and the step ends past that offset.)
If the count is not 0, the edit wrote LF lines: make every line end in CRLF with
```bash
cd D:/clients/casaos/get-digest && python -c "p='.github/workflows/install-check.yml'; crlf=bytes([13,10]); lf=bytes([10]); d=open(p,'rb').read().replace(crlf,lf).replace(lf,crlf); open(p,'wb').write(d)"
```
then run this step again.

- [ ] **Step 9: Commit (no push)**

```bash
cd D:/clients/casaos/get-digest && git add .github/workflows/install-check.yml && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "ci(install-check): a git app that follows tags upgrades, and never goes back"
```

---

### Task 2: Prepare the distribution release that follows tags

This task pins the published components and writes the release notes in one local commit. It never pushes or tags: the release itself is the controller's (see "Controller hand-off" at the end of this task).

Precondition: the other plans of this feature (`2026-09-23-git-tags-appmanagement.md` and `2026-09-23-git-tags-dashboard.md`, in the same folder as this plan) are executed and reviewed, and none of them pushes or tags; the core's log line is the pull request ReCasaOS/CasaOS#6 (branch `feat/telemetry-sent-log`). Between those plans and this task, the controller brings each reviewed branch onto `inkly/main` in CasaOS-AppManagement, CasaOS-UI and CasaOS, pushes the tag of each by name (CasaOS-AppManagement `v0.4.62`, CasaOS-UI `v0.4.73`, CasaOS `v0.4.61`, or the next free patch of each), and waits until the three releases are published and not drafts. Step 1 reads what the controller published and stops when something is missing.

**Files:**
- Modify: `release/components.env` (7 lines: `CASAOS_RELEASE_TAG`, `CASAOS_TAG`, `CASAOS_COMMIT`, `CASAOS_APP_MANAGEMENT_TAG`, `CASAOS_APP_MANAGEMENT_COMMIT`, `CASAOS_UI_TAG`, `CASAOS_UI_COMMIT`)
- Modify: `CHANGELOG.md` (a new section above the newest `## [` section)
- Modify: `README.md` (a new `## What is in` section above the newest one; one sentence in "Anonymous statistics"; the table rows `| [CasaOS](…) |`, `| [CasaOS-UI](…) |` and `| [CasaOS-AppManagement](…) |`)
- Test: the checks of Steps 1 to 5; the install check runs on the release the controller publishes from this commit

**Interfaces:**
- Consumes: the tags `$am` (CasaOS-AppManagement), `$ui` (CasaOS-UI) and `$core` (CasaOS), published by the controller, carrying respectively `tag_pattern` in `api/app_management/openapi.yaml`, a dashboard whose non-test sources name `tag_pattern`, and the log line `telemetry: sent` in non-test Go code; the Task 1 commit on `feat/git-tags`.
- Produces: the local commit `chore(release): 0.5.4` on `feat/git-tags`, directly on the Task 1 commit, which pins the three tags in `release/components.env` with `CASAOS_RELEASE_TAG=v0.5.4` (the next patch after the `last` of Step 1) and adds the 0.5.4 notes to `CHANGELOG.md` and `README.md`. Nothing is pushed or tagged.

- [ ] **Step 1: Read the versions and make sure the three components carry the feature**

Another session can release from the same clones, so read the state instead of assuming it. Run the whole block in one Bash call:
```bash
cd D:/clients/casaos && for r in CasaOS-AppManagement CasaOS-UI CasaOS get-digest; do git -C $r fetch inkly --tags --quiet; done
am=$(git -C CasaOS-AppManagement tag --sort=-v:refname | grep -E '^v0[.]4[.][0-9]+$' | sed -n 1p)
ui=$(git -C CasaOS-UI tag --sort=-v:refname | grep -E '^v0[.]4[.][0-9]+$' | sed -n 1p)
core=$(git -C CasaOS tag --sort=-v:refname | grep -E '^v0[.]4[.][0-9]+$' | sed -n 1p)
last=$(git -C get-digest tag --sort=-v:refname | grep -E '^v[0-9]+[.][0-9]+[.][0-9]+$' | sed -n 1p)
echo "am=$am ui=$ui core=$core last=$last"
echo "openapi naming tag_pattern: $(git -C CasaOS-AppManagement grep -l tag_pattern "$am" -- api/app_management/openapi.yaml | wc -l)"
echo "dashboard files sending tag_pattern: $(git -C CasaOS-UI grep -l tag_pattern "$ui" -- src ':!*.spec.js' | wc -l)"
echo "core files logging telemetry sends: $(git -C CasaOS grep -l 'telemetry: sent' "$core" -- '*.go' ':!*_test.go' | wc -l)"
echo "common: $(git -C CasaOS-AppManagement grep -h 'ReCasaOS/CasaOS-Common v' "$am" -- go.mod) / $(git -C CasaOS grep -h 'ReCasaOS/CasaOS-Common v' "$core" -- go.mod)"
echo "am draft: $(gh release view "$am" -R ReCasaOS/CasaOS-AppManagement --json isDraft -q .isDraft)"
echo "ui draft: $(gh release view "$ui" -R ReCasaOS/CasaOS-UI --json isDraft -q .isDraft)"
echo "core draft: $(gh release view "$core" -R ReCasaOS/CasaOS --json isDraft -q .isDraft)"
```
Expected, if nobody has released anything else since v0.5.3: `am=v0.4.62 ui=v0.4.73 core=v0.4.61 last=v0.5.3`; `openapi naming tag_pattern: 1`; at least 1 dashboard file and at least 1 core file (the `:!` pathspecs leave out the `*.spec.js` and `*_test.go` files, so a count comes from production code only); `common:` followed twice by `github.com/ReCasaOS/CasaOS-Common v0.4.27`; and `false` for the three drafts.

The distribution then becomes `v0.5.4`, the next patch after `last`. If other numbers print, use them in place of `v0.4.62`, `v0.4.73`, `v0.4.61`, `v0.5.3` and `v0.5.4` everywhere below. If `common:` prints a version other than `v0.4.27`, Common has changed too: in Step 4, write it with its new version in the `Components:` part of the CHANGELOG line and remove it from the `Unchanged from` part (`release/components.env` has no Common line, so nothing else changes). If a count is 0, a command prints nothing, or a release is a draft, stop: the controller has not released that component yet.

- [ ] **Step 2: Make sure the branch holds the current `main`**

```bash
git -C D:/clients/casaos/get-digest switch feat/git-tags && git -C D:/clients/casaos/get-digest fetch inkly && git -C D:/clients/casaos/get-digest merge-base --is-ancestor inkly/main feat/git-tags && echo "feat/git-tags holds inkly/main" && git -C D:/clients/casaos/get-digest log --oneline -2
```
Expected: `feat/git-tags holds inkly/main`, then the Task 1 commit (`ci(install-check): a git app that follows tags upgrades, and never goes back`) on top of the newest commit of `inkly/main`. If that line does not print, `main` has moved since Task 1: stop. This plan does not rebase; bringing the branch onto the new `main`, with the tags step kept directly after the webhook step and Task 1 Steps 6 to 8 run again, is the controller's call before it comes back here.

- [ ] **Step 3: Pin the three components**

Print the three commits to pin:
```bash
git -C D:/clients/casaos/CasaOS-AppManagement rev-parse "v0.4.62^{commit}"; git -C D:/clients/casaos/CasaOS-UI rev-parse "v0.4.73^{commit}"; git -C D:/clients/casaos/CasaOS rev-parse "v0.4.61^{commit}"
```
With the Edit tool, set these values in `release/components.env`:
- `CASAOS_RELEASE_TAG=v0.5.4`
- `CASAOS_TAG=v0.4.61`
- `CASAOS_COMMIT=` followed by the third sha
- `CASAOS_APP_MANAGEMENT_TAG=v0.4.62`
- `CASAOS_APP_MANAGEMENT_COMMIT=` followed by the first sha
- `CASAOS_UI_TAG=v0.4.73`
- `CASAOS_UI_COMMIT=` followed by the second sha

Then run: `cd D:/clients/casaos/get-digest && git diff --stat release/components.env`
Expected: `1 file changed, 7 insertions(+), 7 deletions(-)`

- [ ] **Step 4: Write the release notes**

In `CHANGELOG.md`, add this section above the newest `## [` section (`## [0.5.3] - 2026-09-23`); the block ends with the blank line that separates it from `## [0.5.3]`. Replace `YYYY-MM-DD` with the output of `date +%F`. Take the "Unchanged from" versions from the Components line of the section it goes above, keeping only the components this release does not change (Common moves to `Components:` when Step 1 printed another version):
```markdown
## [0.5.4] - YYYY-MM-DD

Components: CasaOS `v0.4.61`, CasaOS-AppManagement `v0.4.62`, CasaOS-UI `v0.4.73`. Unchanged from v0.5.3: CasaOS-UserService `v0.4.30`, CasaOS-MessageBus `v0.4.28`, CasaOS-LocalStorage `v0.4.44`, CasaOS-Gateway `v0.4.30`, Common `v0.4.27`, rclone `v1.75.1`.

### Added

- **Git apps can follow releases instead of a branch.** When adding an app from its repository, choose *Follow tags*: the app deploys the highest version tag (`v1.4.2`; with automatic deployment on, `v1.5.0` as soon as it is tagged), never a commit pushed between two releases. Pre-releases (`-rc`, `-beta`) are left out unless the app includes them, and an optional pattern such as `v2.*` keeps it on one line of releases. Automatic deployment only goes up: a deleted tag or a narrowed pattern never takes the app back, and a tag moved to another commit is reported in the Repository tab, never redeployed by itself. By hand, *Deploy a tag…* deploys any eligible tag, an older one after a confirmation. The mode, the pattern and the pre-release choice can be changed later in the Repository tab; after a change of mode, the first deployment is made by hand. Webhooks need nothing new on GitHub, Gitea and Forgejo; on GitLab, tick *Tag push events* as well. A backup records the mode and the tag, and a restore on a new box clones the repository at that tag, or fetches the backed-up commit when the tag has moved since; when the forge does not serve a commit by its hash, that restore stops and says so.

### Changed

- **The core logs each statistics send.** `journalctl -u casaos | grep 'telemetry: sent'` lists every event it sent, by name.
- **The install check follows tags.** It registers a second app from its test repository in tag mode and deploys `v1.0.0` by hand. Through signed webhook pushes, it then checks that `v1.1.0` is deployed automatically, and that a pre-release, the deletion of `v1.1.0` and `v1.1.0` moved to another commit deploy nothing.

### Upgrade notes

- Existing git apps keep following their branch. A backup of an app that follows tags cannot be restored by an earlier release.

```

In `README.md`, add above the newest `## What is in` section (`## What is in v0.5.3`):
```markdown
## What is in v0.5.4

**Git apps can follow releases.** Choose *Follow tags* when adding an app from its repository: it deploys the newest version tag (pre-releases left out unless you include them, and a pattern such as `v2.*` if you want one line of releases); with automatic deployment on, it upgrades by itself when a higher version is tagged, and it never goes back on its own. A tag moved to another commit is reported, not redeployed.

```
In the "Anonymous statistics" section, with the Edit tool, replace `A failed send waits for the next hourly check; nothing is queued.` (unique in the file, at the end of the paragraph that starts with `**When.**`) with:
```markdown
A failed send waits for the next hourly check; nothing is queued. The core logs each event it sends: `journalctl -u casaos | grep 'telemetry: sent'` lists them.
```

In the components table, set the three rows to `| [CasaOS](https://github.com/ReCasaOS/CasaOS) | v0.4.61 |`, `| [CasaOS-UI](https://github.com/ReCasaOS/CasaOS-UI) | v0.4.73 |` and `| [CasaOS-AppManagement](https://github.com/ReCasaOS/CasaOS-AppManagement) | v0.4.62 |`.

Then run:
```bash
cd D:/clients/casaos/get-digest && git diff --stat && git diff --check && python -c "import sys; [print(p, 'bare LF:', open(p,'rb').read().replace(bytes([13,10]),bytes()).count(bytes([10]))) for p in sys.argv[1:]]" README.md CHANGELOG.md release/components.env
```
Expected: 3 files changed (`CHANGELOG.md`, `README.md`, `release/components.env`), no output from `--check`, and `bare LF: 0` for each of the three files, which are CRLF. (`file` would not do: it reads only the first 64 KiB, and the README's components table lies past that offset.)
If a count is not 0, an edit wrote LF lines: make every line of the three files end in CRLF (a file already in CRLF is left as it is) with
```bash
cd D:/clients/casaos/get-digest && python -c "import sys; crlf=bytes([13,10]); lf=bytes([10]); ds={p: open(p,'rb').read().replace(crlf,lf).replace(lf,crlf) for p in sys.argv[1:]}; [open(p,'wb').write(d) for p, d in ds.items()]" README.md CHANGELOG.md release/components.env
```
then run this check again.

- [ ] **Step 5: Commit (no push, no tag)**

```bash
cd D:/clients/casaos/get-digest && git add release/components.env CHANGELOG.md README.md && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "chore(release): 0.5.4" && git log --oneline -3
```
Expected: `chore(release): 0.5.4`, then the Task 1 commit (`ci(install-check): a git app that follows tags upgrades, and never goes back`), then the newest commit of `inkly/main`.

**Controller hand-off (not a step of this plan).** The controller releases this commit: it fast-forwards `main` of `ReCasaOS/CasaOS-Install` to `feat/git-tags` (Step 2 made sure the branch holds `inkly/main`; if `main` has moved since, the branch is first brought onto it, as Step 2 says, and Steps 4 and 5 are checked again), creates the annotated distribution tag `v0.5.4` on that commit and pushes that one tag by name. `release.yml` then publishes the installer and calls the install check. The release is done when `v0.5.4` is the latest release of `ReCasaOS/CasaOS-Install`, not a draft, with `success` on the job `Assemble and publish the installer` and on the three install-check legs (amd64 preinstalled, arm64 preinstalled, amd64 current). In each leg's log, the step `An app that follows tags` prints these 10 lines, in this order (GitHub also echoes the step's script on lines marked `[36;1m`, which are not output): `v1.0.0 deployed by hand:` with `"tag":"v1.0.0"` in `deployed`; `webhook for v1.1.0: 202` with `checking`, `coalesced` or `queued`; `v1.1.0 deployed automatically:` with `"tag":"v1.1.0"` in `deployed`; `webhook for v1.2.0-rc.1: 202`; `v1.2.0-rc.1: nothing deployed, v1.1.0 still answers`; `eligible tags: [{"name":"v1.1.0",…},{"name":"v1.0.0",…}]`; `webhook for the deletion of v1.1.0: 202`; `v1.1.0 deleted: nothing deployed, v1.1.0 still answers`; `webhook for v1.1.0 moved: 202`; `v1.1.0 moved: nothing deployed, v1.1.0 still answers`. The webhook secret appears nowhere in the log (GitHub prints `***` where a masked value was). A red tags step means the published AppManagement does not match the contract under Global Constraints: it is fixed in AppManagement with a new tag, not by loosening the step.

---

## Spec coverage

| Spec requirement | Where |
|---|---|
| Install check: a new step "An app that follows tags", on the test repository the git step already uses | Task 1, Step 5 (`gittags` registered from `file:///opt/gitcheck.git`, inserted right after the webhook step) |
| Install check: tag `v1.0.0`, register the app in tag mode, deploy by hand | Task 1, Step 5 (annotated `v1.0.0`; `POST /git` with `follow: "tags"`; `check`; `deploy {"tag":"v1.0.0"}`; `deployed.tag` and port 18081 answering `v1.0.0`) |
| Install check: automatic deployment on, tag `v1.1.0`, a signed push to the webhook: `v1.1.0` is deployed | Task 1, Step 5 (PUT `auto_deploy` and `webhook_enabled`; `announce` signs a GitHub `push` for `refs/tags/v1.1.0`; `deployed.tag` `v1.1.0`, 18081 answering `v1.1.0`) |
| Install check: tag `v1.2.0-rc.1`: nothing happens | Task 1, Step 5 (`remote_tag` stays `v1.1.0`, `nothing_deployed`, `GET …/tags` without the pre-release) |
| Install check: delete `v1.1.0`: no return to `v1.0.0` | Task 1, Step 5 (`remote_tag` `v1.0.0`, `tag_moved` false, `nothing_deployed`) |
| Install check: move `v1.1.0` to another commit: `tag_moved`, no deployment | Task 1, Step 5 (annotated `v1.1.0` on a new commit, `remote_commit` the peeled commit, `tag_moved` true, `nothing_deployed`) |
| In tag mode `branch` stays empty, and the check never resolves the remote's default branch | Task 1, Step 5 (`branch == ""` in the registration answer and after the first check) |
| The first clone in tag mode clones at the chosen tag | Task 1, Step 5 (`cloned` and `head.commit` the `v1.0.0` commit after the first check) |
| Annotated tags: the peeled line gives the commit | Task 1, Step 5 (`check.remote_commit` for `v1.0.0` and the moved `v1.1.0`, `commit` of `v1.0.0` in `GET …/tags`) |
| `deployed.tag`, `history[].tag`, `operation.tag` | Task 1, Step 5 (the deploy answer's `operation`, both waits on `deployed`, `history[0]` and `history[1]` after `v1.1.0`) |
| `GET …/tags`: eligible tags, highest first, `{name, commit}` | Task 1, Step 5 (exact list after the pre-release is pushed) |
| `deploy` accepts `tag` | Task 1, Step 5 |
| Automatic: strictly higher only; nothing for a deleted or moved tag, or a pre-release | Task 1, Step 5 (`nothing_deployed`: no operation, folder, deployed version and history unchanged, the port still answering `v1.1.0`) |
| Webhooks: no server change, a tag push starts a check | Task 1, Step 5 (every tag event goes through the webhook, answers `checking`, `coalesced` or `queued` accepted) |
| Core: `telemetry: sent` logged for each send | Task 2, Step 1 (the pinned CasaOS tag carries the line); Step 4 (CHANGELOG and README say how to see it); the Go test is in ReCasaOS/CasaOS#6 |
| Components and release: shipped together in one distribution release | Task 2 precondition (the controller releases the three components); Task 2 (three tags checked and pinned, release notes, one release commit); the controller hand-off after Task 2 (one distribution tag, install check green on three legs) |
| Pattern, pre-releases included, a manual older tag, a revert after a deletion, the `kept` refs, a change of mode, the 400s of `GET …/tags` in branch mode and of `deploy` with `tag` and `commit`, backups and restore, the grid badge | Not in this plan: the AppManagement plan's unit tests |
| Dashboard: registration choice, Repository tab, tag list, GitLab guide | Not in this plan: the dashboard plan |
