# Git App Webhooks: Install Check and Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove on a freshly installed box that a signed push posted to a git app's webhook, through the gateway and without a token, makes AppManagement check the app at once, then ship the feature as one distribution release.

**Architecture:** The install check gains one step, right after "An app deployed from its git repository", that plays the forge with `curl` and `openssl` against the `gitcheck` app that step left behind. It enables the webhook with a PUT, reads the secret from the view, pushes a commit to the bare repository, posts a GitHub `push` signed with `X-Hub-Signature-256` and polls the view until the check has seen the commit, then covers `ping`, a wrong signature, a GET without a token, the 404 of a disabled webhook, and the absence of the secret and signatures from AppManagement's log. The release pins the AppManagement and CasaOS-UI tags published before it, and the install check of that release proves the chain.

**Tech Stack:** bash under `set -euo pipefail`, GitHub Actions (`.github/workflows/install-check.yml`), `curl` + `jq` + `openssl` on the Ubuntu 22.04 runners, AppManagement's v2 API through the gateway, Python 3 + PyYAML for local validation.

**Spec:** D:/clients/casaos/CasaOS-AppManagement/docs/superpowers/specs/2026-09-23-git-webhooks-design.md

## Global Constraints

- Repository: `D:/clients/casaos/get-digest` (GitHub `ReCasaOS/CasaOS-Install`, remote `inkly`). Work happens on branch `feat/git-webhooks`, created from `inkly/main` at execution time with `git -C D:/clients/casaos/get-digest fetch inkly` and then `git -C D:/clients/casaos/get-digest switch -c feat/git-webhooks inkly/main`. Every path below is relative to that repository.
- `feat/git-webhooks` reaches `inkly/main` only in Task 2, after AppManagement and CasaOS-UI have published the feature. Until then, any release cut from `main` would run the new step against an AppManagement without webhooks and go red.
- Route: `POST /v2/app_management/git/{app}/webhook`. Only that method and path skip the JWT (`^/v2/app_management/git/[^/]+/webhook$`), so a GET on the same path without a token answers 401.
- Answers: 202 `{"message":"checking"|"coalesced"|"queued"}` for a push; 200 `{"message":"pong"}` for `X-GitHub-Event: ping`; 202 `{"message":"ignored"}` for any other event; 401 when the signature is bad or missing; 404 when the webhook is disabled or the app is unknown, with the same answer in both cases; 413 when the body is over 5 MiB.
- Signature used here: header `X-Hub-Signature-256: sha256=<hex>`, where `<hex>` is the HMAC-SHA256 of the raw body. The key is the 64-character secret string exactly as pasted into a forge, not its 32 decoded bytes. It is computed with `openssl dgst -sha256 -hmac "<secret>" <body | awk '{print $NF}'`. The event header is `X-GitHub-Event: push`, or `ping` for the ping.
- App view (`GET /v2/app_management/git/{app}`, and the answer to every PUT, under `.data`): `webhook` is `{"enabled": bool, "path": "/v2/app_management/git/<app>/webhook", "secret": "<64 hex>" (only while enabled), "last_delivery": {"at", "forge", "event", "result"} or null}`. The answer `checking` is recorded as `result` `checked`; `queued` is recorded as `queued`.
- `last_delivery` is recorded before the 202 (or the 200 of a ping) is sent, and it survives the operation that holds the app at that moment: AppManagement writes it on the same state that check saves at its end, or re-reads it under the hold before that save. It is still there once `operation` is null again. The step reads it only after the check is over, so an AppManagement whose check erases it fails the step, and that is fixed in AppManagement.
- Existing view fields used: `.data.check` is `{"at": RFC 3339 UTC, "remote_commit", "error"}`, `.data.operation` is null when nothing runs, and `.data.auto_deploy`. A check is an operation: `operation` is set while it runs, and `check.at` and `check.remote_commit` are saved when it ends.
- `PUT /v2/app_management/git/{app}` takes the optional fields `auto_deploy`, `webhook_enabled` and `regenerate_webhook_secret`, in any combination.
- AppManagement's log is `/var/log/casaos/app-management.log` (root-owned, `LogPath`/`LogSaveName`/`LogFileExt` of `/etc/casaos/app-management.conf`). It never holds a request body, a header value, a signature or the secret.
- What the previous step leaves behind: app `gitcheck` created from `file:///opt/gitcheck.git`; work clone `/opt/gitcheck-work` owned by root, on the `broken` commit, with an untracked `grid.json` that step wrote after its `cd /opt/gitcheck-work`; port 18080 answering `v2`; `auto_deploy` on; state `rolled_back`. Every git command in the clone runs as `sudo git -C /opt/gitcheck-work`, and only `index.html` is staged there.
- Each step starts in the runner's workspace, so the new step's own files (`push.json`, `ping.json`, `hook.json`, `disabled.json`, `signatures.txt`) are written there, not in the clone.
- `CASA_URL` is `http://127.0.0.1:<gateway HttpPort>` (written by "The dashboard answers"). `CASA_TOKEN` is the `smoke` user's access token, already masked (written by "First user, then a token").
- The webhook secret never reaches the CI log: `::add-mask::` is emitted as soon as it has been validated, every print of the view drops `.webhook.secret`, and signatures are written to a file, never printed.
- Under `set -o pipefail`, never pipe into a reader that stops early: use `sed -n 1p`, not `head -n 1`, and never `grep -q` at the end of a pipe. Under `set -e`, a check that must fail the step is never written `! cmd` (a negated command never stops the shell): use `if cmd; then ...; exit 1; fi`.
- Local host: Windows with Git Bash and forward slashes. Python 3.12 with PyYAML is available and `jq` is not. `bash -n` is called through `shutil.which('bash')`, because a bare `bash` started from Python can resolve to WSL's launcher in System32.
- Inline Bash commands lose their backslashes, so the workflow is edited with the Edit tool only (never `sed -i` or `echo >>`), and the commands in this plan contain no backslashes.
- Bash tool state does not persist between calls: a command that needs a value computed earlier (a run id) computes it again.
- `install.sh` does not change: the box needs nothing new for webhooks, and `openssl` is only used on the runner.
- Commits: `git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "..."`. Never add a Co-Authored-By trailer or any AI or tool attribution.
- Never `git push --tags`: push each tag by name. Never dispatch `release.yml` by hand. A tag run that dies before publishing is re-run with `gh run rerun <id> -R ReCasaOS/CasaOS-Install --failed`.
- This plan never pushes, tags or releases in CasaOS-AppManagement or CasaOS-UI; the controller does (Task 2 precondition).
- Pinned versions stay in `release/components.env` as a TAG plus the full COMMIT sha.

---

## File structure

| File | Change | Responsibility |
|---|---|---|
| `.github/workflows/install-check.yml` | Modify: insert a step between line 826 (last line of "An app deployed from its git repository") and line 828 (`- name: A RAID array is one storage`) | The step "A push reaches a git app through its webhook": the end-to-end proof, through the gateway, of the route, the signature, the check it starts, the ping, the refusals, the disabled 404 and a log free of the secret and signatures |
| `release/components.env` | Modify: `CASAOS_RELEASE_TAG`, `CASAOS_APP_MANAGEMENT_TAG`/`_COMMIT`, `CASAOS_UI_TAG`/`_COMMIT` | The component versions the distribution installs |
| `CHANGELOG.md` | Modify: a new section above the newest `## [` section | What the release changes, for owners |
| `README.md` | Modify: a new `## What is in` section above the newest one, plus the CasaOS-UI and CasaOS-AppManagement rows of the components table | The release summary and the component table |

---

### Task 1: The install check posts a signed push to the git app's webhook

**Files:**
- Modify: `.github/workflows/install-check.yml:826-828` (the new step goes after the blank line 827, before `      - name: A RAID array is one storage`)
- Test: the local structural checks below. The real test is the install check of the release (Task 2, Step 6).

**Interfaces:**
- Consumes: the route, answers, signature, view and PUT fields listed under Global Constraints, including the survival of `last_delivery` across the check's final save; the env `CASA_URL` and `CASA_TOKEN`; the `gitcheck` app, `/opt/gitcheck.git`, `/opt/gitcheck-work` and port 18080 as the previous step leaves them; `/var/log/casaos/app-management.log`.
- Produces: a workflow step named exactly `A push reaches a git app through its webhook`, which prints these lines, in this order, on a green run:
  - `GET without a token: 401`
  - `signed push: 202 {"message":"checking"}` (or `"queued"` when the five-minute poll held the app at that moment)
  - `after the push: {"operation":null,"check":{"at":"…","remote_commit":"<pushed sha>","error":""}}`
  - `last delivery: {"at":"…","forge":"github","event":"push","result":"checked"}` (or `"queued"`)
  - `ping: 200 {"message":"pong"}`
  - `push signed with another secret: 401`
  - `push to a disabled webhook: 404`
  - `push to an app that does not exist: 404`
  - `secret or signature in the AppManagement log: none`

- [ ] **Step 1: Write the failing structural check**

The check asserts that the step following the git step is the webhook step:
```bash
cd D:/clients/casaos/get-digest && python -c "import yaml; steps=yaml.safe_load(open('.github/workflows/install-check.yml', encoding='utf-8'))['jobs']['install-check']['steps']; names=[s.get('name') for s in steps]; i=names.index('An app deployed from its git repository'); assert names[i+1] == 'A push reaches a git app through its webhook', names[i+1]; print(names[i+1], '|', names[i+2])"
```

- [ ] **Step 2: Run it and see it fail**

Run the command of Step 1.
Expected: it ends with `AssertionError: A RAID array is one storage`.

- [ ] **Step 3: Check the anchor**

Run: `cd D:/clients/casaos/get-digest && sed -n 826,828p .github/workflows/install-check.yml`
Expected:
```
          jq -e '.data[] | select(.name == "gitcheck") | .git.state == "rolled_back" and .git.new_commits == true' grid.json >/dev/null

      - name: A RAID array is one storage
```
If the lines have moved (because `inkly/main` gained a commit since this plan was written), keep the same anchor text: the new step goes right after the git step's last line, before the RAID step.

- [ ] **Step 4: Insert the step**

With the Edit tool on `.github/workflows/install-check.yml`, replace `      - name: A RAID array is one storage` (unique in the file) with the block below. The block ends with that same line, preceded by one blank line.
```yaml
      # A push to the repository makes the box check the app at once, instead of at the
      # next five-minute poll. curl plays the forge: the webhook is enabled through the
      # API and its secret read from the app's view, a commit is pushed to the bare
      # repository, and a GitHub push event signed with that secret is posted through the
      # gateway without a token. Automatic deployment is turned off first, so what is
      # proved is the check: it runs, sees the new commit, and deploys nothing. GitHub's
      # ping answers pong; a body signed with another secret is refused and written
      # nowhere; the path takes no GET without a token; a disabled webhook answers
      # exactly like an app that does not exist; and AppManagement's log holds neither
      # the secret nor any signature it was sent.
      - name: A push reaches a git app through its webhook
        run: |
          set -euo pipefail
          api() { curl -fsS "$@" -H "Authorization: ${CASA_TOKEN}"; }
          app_view() { api "${CASA_URL}/v2/app_management/git/gitcheck"; }
          trap 'app_view | jq ".data | {state, operation, check, webhook: (.webhook | del(.secret))}" || true' EXIT
          # hook <path> <body file> <secret> <event>: posts the body signed the way GitHub
          # signs it, keeps the signature in signatures.txt, prints the status code, and
          # leaves the answer in hook.json
          hook() {
            local signature
            signature="$(openssl dgst -sha256 -hmac "$3" <"$2" | awk '{print $NF}')"
            echo "${signature}" >> signatures.txt
            curl -sS -o hook.json -w '%{http_code}' -X POST "${CASA_URL}$1" \
              -H 'content-type: application/json' -H "X-GitHub-Event: $4" \
              -H "X-Hub-Signature-256: sha256=${signature}" --data-binary @"$2"
          }

          answer="$(api -X PUT "${CASA_URL}/v2/app_management/git/gitcheck" -H 'content-type: application/json' \
            -d '{"auto_deploy":false,"webhook_enabled":true}')"
          secret="$(jq -r '.data.webhook.secret' <<<"${answer}")"
          [[ "${secret}" =~ ^[0-9a-f]{64}$ ]] || { echo "the view holds no 64-hex secret"; exit 1; }
          echo "::add-mask::${secret}"
          jq '.data | {auto_deploy, webhook: (.webhook | del(.secret))}' <<<"${answer}"
          jq -e '.data.auto_deploy == false and .data.webhook.enabled == true and .data.webhook.last_delivery == null' <<<"${answer}" >/dev/null
          path="$(jq -r '.data.webhook.path' <<<"${answer}")"
          test "${path}" = /v2/app_management/git/gitcheck/webhook
          # the path is open to POST alone: a GET still wants a token
          code="$(curl -s -o /dev/null -w '%{http_code}' "${CASA_URL}${path}")"
          echo "GET without a token: ${code}"
          test "${code}" = 401

          echo v3 | sudo tee /opt/gitcheck-work/index.html >/dev/null
          sudo git -C /opt/gitcheck-work add index.html
          sudo git -C /opt/gitcheck-work -c user.name=check -c user.email=check@example.invalid commit -q -m v3
          sudo git -C /opt/gitcheck-work push -q origin main
          pushed="$(sudo git -C /opt/gitcheck-work rev-parse HEAD)"
          printf '{"ref":"refs/heads/main","after":"%s"}' "${pushed}" > push.json
          before="$(app_view | jq -r '.data.check.at')"
          code="$(hook "${path}" push.json "${secret}" push)"
          echo "signed push: ${code} $(cat hook.json)"
          test "${code}" = 202
          message="$(jq -r '.message' hook.json)"
          case "${message}" in checking|queued) ;; *) echo "a first push is not ${message}"; exit 1 ;; esac
          seen=""
          for _ in $(seq 1 60); do
            seen="$(app_view | jq -c '.data | {operation, check}')"
            # wait for the check that saw the push, not for whichever check ends first
            jq -e --arg b "${before}" --arg c "${pushed}" '.operation == null and .check.at != $b and .check.remote_commit == $c' <<<"${seen}" >/dev/null && break
            sleep 2
          done
          echo "after the push: ${seen}"
          jq -e --arg b "${before}" --arg c "${pushed}" '.operation == null and .check.at != $b and .check.remote_commit == $c and .check.error == ""' <<<"${seen}" >/dev/null
          delivery="$(app_view | jq -c '.data.webhook.last_delivery')"
          echo "last delivery: ${delivery}"
          jq -e --arg m "${message}" '.forge == "github" and .event == "push" and .result == (if $m == "checking" then "checked" else $m end)' <<<"${delivery}" >/dev/null
          # automatic deployment is off: the check deployed nothing
          test "$(curl -fsS http://127.0.0.1:18080/)" = v2

          printf '{"zen":"install check","hook_id":1}' > ping.json
          code="$(hook "${path}" ping.json "${secret}" ping)"
          echo "ping: ${code} $(cat hook.json)"
          test "${code}" = 200
          test "$(jq -r '.message' hook.json)" = pong
          delivery="$(app_view | jq -c '.data.webhook.last_delivery')"
          jq -e '.forge == "github" and .event == "ping" and .result == "pong"' <<<"${delivery}" >/dev/null

          code="$(hook "${path}" push.json "$(openssl rand -hex 32)" push)"
          echo "push signed with another secret: ${code}"
          test "${code}" = 401
          test "$(app_view | jq -c '.data.webhook.last_delivery')" = "${delivery}"

          answer="$(api -X PUT "${CASA_URL}/v2/app_management/git/gitcheck" -H 'content-type: application/json' \
            -d '{"webhook_enabled":false}')"
          jq -e '.data.webhook.enabled == false and (.data.webhook | has("secret") | not)' <<<"${answer}" >/dev/null
          code="$(hook "${path}" push.json "${secret}" push)"
          echo "push to a disabled webhook: ${code}"
          test "${code}" = 404
          cp hook.json disabled.json
          code="$(hook /v2/app_management/git/nosuchapp/webhook push.json "${secret}" push)"
          echo "push to an app that does not exist: ${code}"
          test "${code}" = 404
          cmp disabled.json hook.json

          # AppManagement logged neither the secret nor any signature it was sent; the log
          # must exist, or the search below would prove nothing
          sudo test -s /var/log/casaos/app-management.log
          if sudo grep -qF -e "${secret}" -f signatures.txt /var/log/casaos/app-management.log; then
            echo "the AppManagement log holds the secret or a signature"; exit 1
          fi
          echo "secret or signature in the AppManagement log: none"

      - name: A RAID array is one storage
```

Why the step reads this way:
- `before` is read after the `git push` and just before the POST. A poll check that ran between the two has therefore already moved `check.at`, and only a later check can satisfy the loop.
- The loop also waits for `check.remote_commit` to be the pushed sha. When the webhook answers `queued`, or a five-minute poll check that listed the remote before the push is still running when `before` is read, that other check ends first and leaves `operation` null for a moment before the pending check claims the app; stopping there would fail the final assertion although AppManagement is right.
- `auto_deploy` goes off in the same PUT that enables the webhook. After the previous step, automatic deployment is on and the tree holds the broken `CMD`, so a webhook check would otherwise rebuild and roll back again. With it off, the `v2` still answering on 18080 proves the spec's "a webhook only checks".
- Only `index.html` is staged: the previous step left `grid.json` untracked in the clone, and `add -A` would commit it.
- The ping comes after the push, so the push can never be coalesced by a debounce, whichever way the ping is counted.
- The log check is an `if`, not `! sudo grep`: a negated command never stops a `set -e` shell, so `!` would let a leaked secret pass. `grep -F -f signatures.txt` searches for every signature the step sent, the one made with a random secret included, and `sudo test -s` makes a missing or empty log fail instead of passing silently.
- The step leaves `/opt/gitcheck*` and the `gitcheck` app in place; no later step uses them.

- [ ] **Step 5: Run the structural check and see it pass**

Run the command of Step 1.
Expected: `A push reaches a git app through its webhook | A RAID array is one storage`

- [ ] **Step 6: Check that every run block of the workflow parses**

Run:
```bash
cd D:/clients/casaos/get-digest && python -c "import yaml, shutil, subprocess; steps=yaml.safe_load(open('.github/workflows/install-check.yml', encoding='utf-8'))['jobs']['install-check']['steps']; print('bash -n failures:', [s['name'] for s in steps if 'run' in s and subprocess.run([shutil.which('bash'), '-n'], input=s['run'].encode()).returncode])"
```
Expected: `bash -n failures: []`

- [ ] **Step 7: Check the signature pipeline the step uses against a known answer**

This is the HMAC-SHA256 test vector for key `key` and the pangram:
```bash
printf 'The quick brown fox jumps over the lazy dog' | openssl dgst -sha256 -hmac key | awk '{print $NF}'
```
Expected: `f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8`

- [ ] **Step 8: Check the diff touches only the new step**

Run: `cd D:/clients/casaos/get-digest && git diff --stat && git diff --check`
Expected: `1 file changed, 102 insertions(+)` for `.github/workflows/install-check.yml` (the step's 101 lines, comment included, and the blank line after it), no deletions, and no output from `--check`.

- [ ] **Step 9: Commit (no push)**

```bash
cd D:/clients/casaos/get-digest && git add .github/workflows/install-check.yml && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "ci(install-check): a signed push reaches a git app through its webhook"
```

---

### Task 2: Release the distribution that carries the webhooks

Precondition: the AppManagement and dashboard plans (`2026-09-23-git-webhooks-appmanagement.md` and `2026-09-23-git-webhooks-dashboard.md`, in the same folder as this plan) are executed and reviewed. Neither of them pushes or tags. The controller releases both components between those plans and this task: it brings each reviewed `feat/git-webhooks` onto `inkly/main` in CasaOS-AppManagement and in CasaOS-UI, pushes the tag of each by name (CasaOS-AppManagement `v0.4.61` and CasaOS-UI `v0.4.70`, or the next free patch of each), and waits until both releases are published and not drafts. This plan does not push, tag or release in those two repositories; Step 1 reads what the controller published and stops when it is missing.

**Files:**
- Modify: `release/components.env` (5 lines: `CASAOS_RELEASE_TAG`, `CASAOS_APP_MANAGEMENT_TAG`, `CASAOS_APP_MANAGEMENT_COMMIT`, `CASAOS_UI_TAG`, `CASAOS_UI_COMMIT`)
- Modify: `CHANGELOG.md` (a new section above the newest `## [` section)
- Modify: `README.md` (a new `## What is in` section above the newest one; the table rows `| [CasaOS-UI](…) |` and `| [CasaOS-AppManagement](…) |`)
- Test: the release's install check, on its three legs

**Interfaces:**
- Consumes: the AppManagement tag `$am` and the CasaOS-UI tag `$ui`, both published by the controller and both carrying the webhook; the Task 1 commit on `feat/git-webhooks`.
- Produces: the distribution tag `$dist`, published as the latest release of `ReCasaOS/CasaOS-Install`, with a green install check on all three legs.

- [ ] **Step 1: Read the versions and make sure both components carry the feature**

Another session can release from the same clones (`get-digest` also has a `feat/telemetry` branch), so read the state instead of assuming it. Run the whole block in one Bash call:
```bash
cd D:/clients/casaos && for r in CasaOS-AppManagement CasaOS-UI get-digest; do git -C $r fetch inkly --tags --quiet; done
am=$(git -C CasaOS-AppManagement tag --sort=-v:refname | grep -E '^v0[.]4[.][0-9]+$' | sed -n 1p)
ui=$(git -C CasaOS-UI tag --sort=-v:refname | grep -E '^v0[.]4[.][0-9]+$' | sed -n 1p)
last=$(git -C get-digest tag --sort=-v:refname | grep -E '^v[0-9]+[.][0-9]+[.][0-9]+$' | sed -n 1p)
echo "am=$am ui=$ui last=$last"
echo "openapi mentions of regenerate_webhook_secret: $(git -C CasaOS-AppManagement grep -c regenerate_webhook_secret "$am" -- api/app_management/openapi.yaml)"
echo "dashboard files sending webhook_enabled: $(git -C CasaOS-UI grep -l webhook_enabled "$ui" -- src | wc -l)"
echo "common: $(git -C CasaOS-AppManagement grep -h 'ReCasaOS/CasaOS-Common v' "$am" -- go.mod)"
echo "am draft: $(gh release view "$am" -R ReCasaOS/CasaOS-AppManagement --json isDraft -q .isDraft)"
echo "ui draft: $(gh release view "$ui" -R ReCasaOS/CasaOS-UI --json isDraft -q .isDraft)"
```
Expected, if nobody has released anything else since v0.5.0: `am=v0.4.61 ui=v0.4.70 last=v0.5.0`, a non-zero count of `regenerate_webhook_secret` mentions, at least 1 dashboard file, `common:` followed by `github.com/ReCasaOS/CasaOS-Common v0.4.27`, `am draft: false` and `ui draft: false`.

The distribution then becomes `v0.5.1`, the next patch after `last`. If other numbers print, use them in place of `v0.4.61`, `v0.4.70`, `v0.5.0` and `v0.5.1` everywhere below. If `common:` prints a version other than `v0.4.27`, Common has changed too: in Step 4, write it with its new version in the `Components:` part of the CHANGELOG line and remove it from the `Unchanged from` part (`release/components.env` has no Common line, so nothing else changes). If the count is 0, a command prints nothing, or a release is a draft, stop: the controller has not released that component yet.

- [ ] **Step 2: Bring the branch onto the current `main`**

```bash
cd D:/clients/casaos/get-digest && git switch feat/git-webhooks && git fetch inkly && git rebase inkly/main && git log --oneline -3
```
Expected: the rebase succeeds and the newest commit is `ci(install-check): a signed push reaches a git app through its webhook`. If `install-check.yml` conflicts, keep both sides, with the webhook step directly after "An app deployed from its git repository". Then run Task 1 Steps 5 and 6 again.

- [ ] **Step 3: Pin both components**

Print the two commits to pin:
```bash
git -C D:/clients/casaos/CasaOS-AppManagement rev-parse "v0.4.61^{commit}"; git -C D:/clients/casaos/CasaOS-UI rev-parse "v0.4.70^{commit}"
```
With the Edit tool, set these values in `release/components.env`:
- `CASAOS_RELEASE_TAG=v0.5.1`
- `CASAOS_APP_MANAGEMENT_TAG=v0.4.61`
- `CASAOS_APP_MANAGEMENT_COMMIT=` followed by the first sha
- `CASAOS_UI_TAG=v0.4.70`
- `CASAOS_UI_COMMIT=` followed by the second sha

Then run: `cd D:/clients/casaos/get-digest && git diff --stat release/components.env`
Expected: `1 file changed, 5 insertions(+), 5 deletions(-)`

- [ ] **Step 4: Write the release notes**

In `CHANGELOG.md`, add this section above the newest `## [` section. Replace `YYYY-MM-DD` with the output of `date +%F`. Take the "Unchanged from" versions from the Components line of the section it goes above, keeping only the components this release does not change (Common moves to `Components:` when Step 1 printed another version):
```markdown
## [0.5.1] - YYYY-MM-DD

Components: CasaOS-AppManagement `v0.4.61`, CasaOS-UI `v0.4.70`. Unchanged from v0.5.0: CasaOS `v0.4.59`, CasaOS-UserService `v0.4.30`, CasaOS-MessageBus `v0.4.28`, CasaOS-Gateway `v0.4.30`, CasaOS-LocalStorage `v0.4.44`, Common `v0.4.27`, rclone `v1.75.1`.

### Added

- **A push checks a git app at once.** A git app's Repository tab has a "Check on every push" switch. When it is on, the tab shows a URL and a secret to paste into the forge's webhook settings, with instructions for GitHub, Gitea/Forgejo and GitLab (Gogs is accepted too). From then on, a push makes the box check the app immediately instead of at the next five-minute poll, which keeps running. A webhook only checks: it deploys when the app's automatic deployment would, and otherwise the new commits show on the card. The route needs no sign-in, only a request signed with the app's secret. Anything else is refused, and a disabled webhook answers exactly like an app that does not exist. The tab shows the last delivery. The secret can be regenerated, and it is not kept in backups, so a restored app comes back with its webhook off.

### Changed

- **The install check plays a forge.** It enables the webhook of the git app it deployed, pushes a commit, and posts a signed GitHub push event through the gateway. It then checks that the new commit is seen and nothing is deployed, that GitHub's ping is answered, that a wrong signature is refused, that a disabled webhook answers 404, and that AppManagement's log holds neither the secret nor a signature.

### Upgrade notes

- Nothing changes until a webhook is turned on in an app's Repository tab. A box behind a reverse proxy or a tunnel must let the forge reach `POST /v2/app_management/git/<app>/webhook`.
```

In `README.md`, add above the newest `## What is in` section:
```markdown
## What is in v0.5.1

**A push checks a git app at once.** Turn on a git app's webhook and paste its URL and secret into GitHub, GitLab, Gitea or Forgejo. Every push then makes the box look at the repository immediately, and deploy the new commit when automatic deployment is on.

```
In the components table, set the two rows to `| [CasaOS-UI](https://github.com/ReCasaOS/CasaOS-UI) | v0.4.70 |` and `| [CasaOS-AppManagement](https://github.com/ReCasaOS/CasaOS-AppManagement) | v0.4.61 |`.

Then run: `cd D:/clients/casaos/get-digest && git diff --stat && git diff --check`
Expected: 3 files changed (`CHANGELOG.md`, `README.md`, `release/components.env`), and no output from `--check`.

- [ ] **Step 5: Commit, bring `main` forward, tag**

```bash
cd D:/clients/casaos/get-digest && git add release/components.env CHANGELOG.md README.md && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "chore(release): 0.5.1"
```
Run each of these in its own call:
```bash
git -C D:/clients/casaos/get-digest push inkly feat/git-webhooks:main
```
Expected: a fast-forward of `main` (the branch was rebased onto it in Step 2). If the push is rejected, `main` has moved: go back to Step 2.
```bash
git -C D:/clients/casaos/get-digest -c user.name=Gary -c user.email=contact@ixelia.fr tag -a v0.5.1 -m v0.5.1 feat/git-webhooks
```
```bash
git -C D:/clients/casaos/get-digest push inkly v0.5.1
```

- [ ] **Step 6: Watch the release and its install check**

The run appears a few seconds after the tag push, so the first command retries every 5 seconds for up to a minute. The watch outlasts one Bash call: start this block with `run_in_background` and read its output when it exits.
```bash
id=""; for _ in $(seq 1 12); do id=$(gh run list -R ReCasaOS/CasaOS-Install --workflow release.yml --limit 5 --json databaseId,headBranch -q '.[] | select(.headBranch == "v0.5.1") | .databaseId' | sed -n 1p); [ -n "$id" ] && break; sleep 5; done; echo "run=$id"; test -n "$id" && gh run watch "$id" -R ReCasaOS/CasaOS-Install --exit-status --interval 30; echo "exit=$?"
```
Expected: `run=` followed by a number, then `exit=0`. An empty `run=` means no release run started for the tag: check `gh run list -R ReCasaOS/CasaOS-Install --limit 5` before anything else. Then:
```bash
id=$(gh run list -R ReCasaOS/CasaOS-Install --workflow release.yml --limit 5 --json databaseId,headBranch -q '.[] | select(.headBranch == "v0.5.1") | .databaseId' | sed -n 1p); gh run view "$id" -R ReCasaOS/CasaOS-Install --json jobs -q '.jobs[] | .conclusion + " " + .name'
```
Expected: `success` on the publish job and on the three install-check legs (amd64 preinstalled, arm64 preinstalled, amd64 current). Also check the legs' logs for the new step's output lines. GitHub echoes each step's script into the log on lines marked `[36;1m`, which the second `grep` drops:
```bash
id=$(gh run list -R ReCasaOS/CasaOS-Install --workflow release.yml --limit 5 --json databaseId,headBranch -q '.[] | select(.headBranch == "v0.5.1") | .databaseId' | sed -n 1p); gh run view "$id" -R ReCasaOS/CasaOS-Install --log | grep -E 'GET without a token:|signed push:|last delivery:|ping: |another secret:|disabled webhook:|app that does not exist:|in the AppManagement log:' | grep -vF '[36;1m' | sed -n 1,30p
```
Expected, on each of the three legs (27 lines): `GET without a token: 401`, `signed push: 202 {"message":"checking"}` (or `queued`), `last delivery:` with `"forge":"github","event":"push","result":"checked"` (or `queued`), `ping: 200 {"message":"pong"}`, `push signed with another secret: 401`, `push to a disabled webhook: 404`, `push to an app that does not exist: 404`, `secret or signature in the AppManagement log: none`. The secret must not appear anywhere in the log: GitHub prints `***` where a masked value would have been.

If the publish job died on a network error while fetching an asset (for example `curl: (35)` and "No digest for …") and no release was created, run `gh run rerun <id> -R ReCasaOS/CasaOS-Install --failed` with the run number Step 6 printed, and watch again. For any other red leg, read that job's log before changing anything. A red webhook step means the AppManagement release does not match the contract under Global Constraints, which is fixed in AppManagement with a new tag, not by loosening the step.

- [ ] **Step 7: Confirm what is published**

```bash
gh release view -R ReCasaOS/CasaOS-Install --json tagName,isDraft -q '"latest=" + .tagName + " draft=" + (.isDraft | tostring)'
```
Expected: `latest=v0.5.1 draft=false`

---

## Spec coverage

| Spec requirement | Where |
|---|---|
| Install check: enable the webhook through the API and read the secret | Task 1, Step 4 (PUT `webhook_enabled: true`, `.data.webhook.secret` validated as 64 hex, then masked) |
| Install check: push a commit to the test repository | Task 1, Step 4 (`v3` pushed to `/opt/gitcheck.git`, only `index.html` staged) |
| Install check: POST a signed `push` computed with `openssl` | Task 1, Step 4 (`hook`: `openssl dgst -sha256 -hmac`, `X-Hub-Signature-256: sha256=<hex>`, `X-GitHub-Event: push`, through `CASA_URL` without a token); Step 7 pins the formula |
| Install check: 202, and a new check (`check.at` moves, the new commit is seen) | Task 1, Step 4 (202 `checking`/`queued`; poll until `operation` is null, `check.at` moved and `remote_commit` is the pushed sha; `error` is empty) |
| Install check: a badly signed body gives 401 | Task 1, Step 4 (the same body signed with a random secret) |
| Install check: after disabling, 404 | Task 1, Step 4 (PUT `webhook_enabled: false`, then 404) |
| Events: `ping` answers 200 `{"message":"pong"}` | Task 1, Step 4 |
| Guards: disabled and unknown app give the same 404 | Task 1, Step 4 (`cmp` of the two answers) |
| Route exempt from the JWT for POST only | Task 1, Step 4 (GET on the path without a token answers 401) |
| A webhook only checks, and deploys only when automatic deployment would | Task 1, Step 4 (automatic deployment off, and 18080 still answers `v2` after the check) |
| Last delivery `{at, forge, event, result}`, null until the first, stored in the app state; refused requests never written | Task 1, Step 4 (null after enabling; `github`/`push`/`checked` read once the check is over, then `ping`/`pong`; unchanged after the 401); Global Constraints (the delivery survives the check's final save) |
| `secret` present only while enabled | Task 1, Step 4 (`has("secret")` is false after disabling) |
| AppManagement's log never holds the secret or a signature | Task 1, Step 4 (`sudo grep -F` of the secret and of every signature sent in `/var/log/casaos/app-management.log`) |
| CI log never shows the secret | Task 1, Step 4 (`::add-mask::`, view printed without `.webhook.secret`, signatures kept in a file); Task 2, Step 6 |
| Components and release: AppManagement and the dashboard released first under their own tags, then one distribution release | Task 2 precondition (the controller tags and releases both components); Task 2 (both tags checked and pinned, one distribution tag, install check green on three legs) |
| Signatures of the other forges, 413, debounce, queued check, regenerate, backups, request body and header values absent from the log | Not in this plan: AppManagement unit tests (`2026-09-23-git-webhooks-appmanagement.md`) |
| Dashboard Webhook section | Not in this plan: `2026-09-23-git-webhooks-dashboard.md` |
