# Git apps: installer, install check and release — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put `git` on every CasaOS box, prove apps deployed from git work end to end on a fresh install, and ship the feature as a distribution release.

**Architecture:** The installer gains one dependency. The install check gains one step that plays the whole feature against the published bundle through the API: create an app from a bare repository on the runner, deploy it, rebuild it automatically while polling its port, and roll a broken commit back. The release follows the distribution's established sequence: component tags, installer pins, installer tag, install check.

**Tech Stack:** bash, GitHub Actions (`.github/workflows/install-check.yml`), `curl` + `jq` against AppManagement's v2 API, busybox `httpd` as the test app.

**Spec:** `docs/superpowers/specs/2026-09-16-git-apps-design.md` (sections "Installer" and "Testing"). Companion plans: `2026-09-16-git-apps-appmanagement.md`, `2026-09-16-git-apps-dashboard.md`. Tasks 1 and 2 can be done before those plans are executed; Task 3 needs both executed, reviewed and merged.

## Global Constraints

- Repository: `D:/clients/casaos/get-digest`, a worktree of CasaOS-Install on branch `fix/bundle-local-tar`, which is the remote `main`. Push with `git push inkly HEAD:main` (`fork/inkly` is checked out in `D:/clients/casaos/get`, so `branch -f` fails there).
- `git` is added to `CASA_DEPANDS_PACKAGE` and `CASA_DEPANDS_COMMAND` in `install.sh`; the same package name works for apk, apt-get, dnf, zypper, yum and pacman.
- API contract, from the AppManagement plan: routes under `/v2/app_management/git`; `POST /git` body `{"name","url","branch"?,"access","token"?}`; `GET /git/{app}` answers `{"data": GitApp}`; `PUT /git/{app}` body `{"branch"?,"auto_deploy"?,"access"?,"token"?}`; `POST /git/{app}/check` answers 202 and runs in the background (poll `GET` until `operation` is null); `POST /git/{app}/deploy` body `{"commit"?,"env"?}` answers 202; `GitApp.state` is one of `idle | building | deploying | build_failed | rolled_back | failed | unreachable | interrupted`; `GitApp.cloned`, `GitApp.operation` (null when none runs), `GitApp.deployed.commit`, `GitApp.history[].outcome`, `GitApp.build_log`; the app grid item carries `git: {"new_commits", "state", "deployed"}`.
- Under `set -o pipefail`, never pipe into a reader that stops early: `sed -n 1p` instead of `head -n 1`, `grep X >/dev/null` instead of `grep -q X`.
- A step that uses `CASA_URL` or `CASA_TOKEN` goes after "First user, then a token".
- Commits: `git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "..."`. No Co-Authored-By and no tool attribution anywhere.
- Never `git push --tags` (CasaOS-AppManagement holds 131 local-only IceWhale tags): push each tag by name.
- Never dispatch `release.yml` by hand on CasaOS-Install. A tag run that dies before publishing is re-run with `gh run rerun <id> --failed`; an install check alone is dispatched with `gh workflow run install-check.yml --ref main -f tag=vX`.
- Pinned versions stay in `release/components.env` as TAG plus full COMMIT sha.

---

### Task 1: `git` on every box

**Files:**
- Modify: `install.sh:52-53` (the two `readonly CASA_DEPANDS_*` arrays)

**Interfaces:**
- Consumes: nothing.
- Produces: the `git` binary on every box the installer installs or upgrades, which AppManagement's `pkg/git` runs.

- [ ] **Step 1: Check the current arrays**

Run: `cd D:/clients/casaos/get-digest && sed -n 52,53p install.sh`
Expected:
```
readonly CASA_DEPANDS_PACKAGE=('wget' 'curl' 'smartmontools' 'parted' 'ntfs-3g' 'net-tools' 'udevil' 'samba' 'cifs-utils' 'mergerfs' 'unzip')
readonly CASA_DEPANDS_COMMAND=('wget' 'curl' 'smartctl' 'parted' 'ntfs-3g' 'netstat' 'udevil' 'smbd' 'mount.cifs' 'mount.mergerfs' 'unzip')
```

- [ ] **Step 2: Add `git` to both arrays**

Replace the two lines with:
```bash
readonly CASA_DEPANDS_PACKAGE=('wget' 'curl' 'smartmontools' 'parted' 'ntfs-3g' 'net-tools' 'udevil' 'samba' 'cifs-utils' 'mergerfs' 'unzip' 'git')
readonly CASA_DEPANDS_COMMAND=('wget' 'curl' 'smartctl' 'parted' 'ntfs-3g' 'netstat' 'udevil' 'smbd' 'mount.cifs' 'mount.mergerfs' 'unzip' 'git')
```

- [ ] **Step 3: Check the script still parses and the arrays pair up**

Run:
```bash
cd D:/clients/casaos/get-digest && bash -n install.sh && bash -c 'source <(sed -n 52,53p install.sh); echo "${#CASA_DEPANDS_PACKAGE[@]} ${#CASA_DEPANDS_COMMAND[@]} ${CASA_DEPANDS_PACKAGE[11]} ${CASA_DEPANDS_COMMAND[11]}"'
```
Expected: `12 12 git git`

- [ ] **Step 4: Commit**

```bash
cd D:/clients/casaos/get-digest && git add install.sh && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(install): git is a dependency, apps are deployed from their repository"
```

---

### Task 2: The install check deploys an app from git

**Files:**
- Modify: `.github/workflows/install-check.yml`, inserting a step immediately before the line `      - name: A RAID array is one storage`

**Interfaces:**
- Consumes: the API contract in Global Constraints; `CASA_URL` and `CASA_TOKEN` from earlier steps; `git` from Task 1.
- Produces: on every leg, evidence that a git app is created, deployed, rebuilt automatically with its old version answering during the whole build, and rolled back from a broken commit.

- [ ] **Step 1: Write the step**

Insert this block, with its indentation, immediately before `      - name: A RAID array is one storage`:

```yaml
      # An app deployed from its git repository, the way an owner would run their own
      # project: a bare repository on this machine stands in for GitHub. The app is
      # created and deployed through the API, then a second commit is rebuilt
      # automatically while its port is polled: every request made while the new
      # version builds must be answered by the old one. A third commit whose container
      # exits must be rolled back, with the second version still answering.
      - name: An app deployed from its git repository
        run: |
          set -euo pipefail
          api() { curl -fsS "$@" -H "Authorization: ${CASA_TOKEN}"; }
          state() { api "${CASA_URL}/v2/app_management/git/gitcheck" | jq -r '.data.state'; }
          trap 'api "${CASA_URL}/v2/app_management/git/gitcheck" | jq ".data | {state, operation, deployed, check, history, build_log}" || true' EXIT
          git --version

          sudo git init -q --bare --initial-branch=main /opt/gitcheck.git
          sudo git clone -q /opt/gitcheck.git /opt/gitcheck-work
          cd /opt/gitcheck-work
          # an empty clone's first branch depends on git's version and configuration
          sudo git symbolic-ref HEAD refs/heads/main
          sudo tee Dockerfile >/dev/null <<'EOF'
          FROM busybox
          COPY index.html /www/index.html
          CMD ["httpd", "-f", "-p", "80", "-h", "/www"]
          EOF
          sudo tee compose.yaml >/dev/null <<'EOF'
          services:
            web:
              build: .
              ports:
                - "18080:80"
              env_file: .env
          EOF
          echo 'GREETING=hello' | sudo tee .env.example >/dev/null
          echo '.env' | sudo tee .gitignore >/dev/null
          echo v1 | sudo tee index.html >/dev/null
          commit() { sudo git add -A && sudo git -c user.name=check -c user.email=check@example.invalid commit -q -m "$1" && sudo git push -q origin main; }
          commit v1

          api -X POST "${CASA_URL}/v2/app_management/git" -H 'content-type: application/json' \
            -d '{"name":"gitcheck","url":"file:///opt/gitcheck.git","access":"none"}' | jq .
          idle_operation() {
            for _ in $(seq 1 300); do
              [ "$(api "${CASA_URL}/v2/app_management/git/gitcheck" | jq -r '.data.operation')" = null ] && return 0
              sleep 2
            done
            return 1
          }
          api -X POST "${CASA_URL}/v2/app_management/git/gitcheck/check" >/dev/null
          idle_operation
          api "${CASA_URL}/v2/app_management/git/gitcheck" | jq '.data | {cloned, check, compose, env_template}'
          test "$(api "${CASA_URL}/v2/app_management/git/gitcheck" | jq -r '.data.cloned')" = true
          api -X POST "${CASA_URL}/v2/app_management/git/gitcheck/deploy" -H 'content-type: application/json' \
            -d '{"env":"GREETING=hello\n"}' | jq .data.state
          for _ in $(seq 1 120); do
            [ "$(state)" = idle ] && [ "$(curl -fsS --max-time 2 http://127.0.0.1:18080/ || true)" = v1 ] && break
            sleep 2
          done
          test "$(curl -fsS http://127.0.0.1:18080/)" = v1

          api -X PUT "${CASA_URL}/v2/app_management/git/gitcheck" -H 'content-type: application/json' \
            -d '{"auto_deploy":true}' | jq .data.auto_deploy

          # the second version builds for at least 15 seconds, so the poll sees the build
          sudo sed -i 's#^COPY index.html#RUN sleep 15\nCOPY index.html#' Dockerfile
          echo v2 | sudo tee index.html >/dev/null
          commit v2
          api -X POST "${CASA_URL}/v2/app_management/git/gitcheck/check" >/dev/null
          seen_building=false
          not_v1_while_building=0
          for _ in $(seq 1 900); do
            st="$(state)"
            body="$(curl -fsS --max-time 2 http://127.0.0.1:18080/ || true)"
            if [ "${st}" = building ]; then
              seen_building=true
              [ "${body}" = v1 ] || not_v1_while_building=$((not_v1_while_building + 1))
            fi
            [ "${st}" = idle ] && [ "${body}" = v2 ] && break
            sleep 0.2
          done
          echo "the build was seen: ${seen_building}; requests not answered by v1 during it: ${not_v1_while_building}"
          test "${seen_building}" = true
          test "${not_v1_while_building}" = 0
          test "$(curl -fsS http://127.0.0.1:18080/)" = v2

          sudo sed -i 's#^CMD .*#CMD ["sh", "-c", "exit 1"]#' Dockerfile
          commit broken
          api -X POST "${CASA_URL}/v2/app_management/git/gitcheck/check" >/dev/null
          for _ in $(seq 1 150); do
            st="$(state)"
            case "${st}" in rolled_back|failed) break ;; esac
            sleep 2
          done
          echo "state after the broken commit: ${st}"
          test "${st}" = rolled_back
          test "$(curl -fsS http://127.0.0.1:18080/)" = v2
          test "$(api "${CASA_URL}/v2/app_management/git/gitcheck" | jq -r '.data.history[0].outcome')" = rolled_back
          api "${CASA_URL}/v2/app_management/web/appgrid" > grid.json
          jq '.data[] | select(.name == "gitcheck") | .git' grid.json
          jq -e '.data[] | select(.name == "gitcheck") | .git.state == "rolled_back" and .git.new_commits == true' grid.json >/dev/null
```

Note on the `sed` of the second version: GNU sed turns the `\n` of the replacement into a newline, so the Dockerfile gains a `RUN sleep 15` line before `COPY`. The line is written in a YAML block, not typed in a shell tool, so its backslash reaches the runner as written.

- [ ] **Step 2: Check the workflow is valid YAML and the step sits before the RAID step**

Run:
```bash
cd D:/clients/casaos/get-digest && python -c "import yaml,sys; d=yaml.safe_load(open('.github/workflows/install-check.yml', encoding='utf-8')); steps=[s.get('name') for j in d['jobs'].values() for s in j.get('steps', [])]; i=steps.index('An app deployed from its git repository'); print(steps[i+1])"
```
Expected: `A RAID array is one storage`

- [ ] **Step 3: Check the step's shell parses**

Run:
```bash
cd D:/clients/casaos/get-digest && python -c "import yaml; d=yaml.safe_load(open('.github/workflows/install-check.yml', encoding='utf-8')); s=[s for j in d['jobs'].values() for s in j.get('steps', []) if s.get('name')=='An app deployed from its git repository'][0]; open('gitcheck-step.sh','w', newline='\n').write(s['run'])" && bash -n gitcheck-step.sh && echo parses && rm gitcheck-step.sh
```
Expected: `parses`

- [ ] **Step 4: Commit**

```bash
cd D:/clients/casaos/get-digest && git add .github/workflows/install-check.yml && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "ci(install-check): an app deployed from its git repository, rebuilt without a gap and rolled back"
```

---

### Task 3: Release

Precondition: the AppManagement and dashboard plans are executed, reviewed, and their branches `feat/git-apps` are ready to merge in both repositories.

**Files:**
- Modify: `release/components.env` (AppManagement and CasaOS-UI TAG and COMMIT, `CASAOS_RELEASE_TAG`)
- Modify: `CHANGELOG.md` (new section above the newest one)
- Modify: `README.md` (new "What is in" section above the newest one; the components table rows for CasaOS-UI and CasaOS-AppManagement)

**Interfaces:**
- Consumes: both component branches, merged; Tasks 1 and 2 committed.
- Produces: the distribution release, with a green install check on its three legs.

- [ ] **Step 1: Choose the versions**

Another session can release from the same clones, so read the state first:
```bash
cd D:/clients/casaos && for r in CasaOS-AppManagement CasaOS-UI get-digest; do git -C $r fetch inkly --tags --quiet; echo "$r newest: $(git -C $r tag --sort=-v:refname | grep -E '^v0\.4\.' | sed -n 1p)"; done
```
Expected, if nobody released since v0.4.91: `CasaOS-AppManagement newest: v0.4.56`, `CasaOS-UI newest: v0.4.65`, `get-digest newest: v0.4.91`. The release is then AppManagement v0.4.57, CasaOS-UI v0.4.66 and the distribution v0.4.92; otherwise take the next free number of each.

- [ ] **Step 2: Release CasaOS-UI**

```bash
mkdir -p D:/clients/casaos/_work && cd D:/clients/casaos/CasaOS-UI && npx vitest run > D:/clients/casaos/_work/ui-vitest.log 2>&1; tail -5 D:/clients/casaos/_work/ui-vitest.log
```
Expected: every test file passes (a suite failing at load time under parallel load passes on a re-run).
```bash
cd D:/clients/casaos/CasaOS-UI && npx eslint . --quiet && echo lint-ok
```
Expected: `lint-ok`. Then, one command per call:
```bash
git -C D:/clients/casaos/CasaOS-UI push inkly feat/git-apps:main
```
Wait for the `ci.yml` run of that commit to succeed (`gh run list -R ReCasaOS/CasaOS-UI --workflow ci.yml --limit 1`), then:
```bash
git -C D:/clients/casaos/CasaOS-UI -c user.name=Gary -c user.email=contact@ixelia.fr tag -a v0.4.66 -m v0.4.66 feat/git-apps
```
```bash
git -C D:/clients/casaos/CasaOS-UI push inkly v0.4.66
```
Wait for the release run, then:
```bash
gh release view v0.4.66 -R ReCasaOS/CasaOS-UI --json isDraft,assets -q '"draft=\(.isDraft) \([.assets[].name] | join(","))"'
```
Expected: `draft=false linux-all-casaos-v0.4.66.tar.gz`

- [ ] **Step 3: Release CasaOS-AppManagement**

Push, wait for `codecov.yml` on the commit to succeed, tag and push the tag, one command per call:
```bash
git -C D:/clients/casaos/CasaOS-AppManagement push inkly feat/git-apps:main
```
```bash
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr tag -a v0.4.57 -m v0.4.57 feat/git-apps
```
```bash
git -C D:/clients/casaos/CasaOS-AppManagement push inkly v0.4.57
```
When the release run ends, check there is no stray draft (GoReleaser can leave one after a GitHub 500; publish it with `gh release edit <tag> --draft=false` rather than re-tagging), then verify the published binary:
```bash
gh api repos/ReCasaOS/CasaOS-AppManagement/releases --jq '.[] | select(.draft) | .tag_name'
```
Expected: no output.
```bash
mkdir -p D:/clients/casaos/_work/am057 && cd D:/clients/casaos/_work/am057 && gh release download v0.4.57 -R ReCasaOS/CasaOS-AppManagement --clobber -p checksums.txt -p 'linux-amd64-casaos-app-management-v0.4.57.tar.gz' && sha256sum -c --ignore-missing checksums.txt && tar -xzf linux-amd64-casaos-app-management-v0.4.57.tar.gz
```
Expected: `linux-amd64-casaos-app-management-v0.4.57.tar.gz: OK`
```powershell
docker run --rm -v D:\clients\casaos\_work\am057:/r golang:1.26 sh -c "go version -m /r/build/sysroot/usr/bin/casaos-app-management | grep vcs.revision"
```
Expected: the line `build	vcs.revision=` followed by the sha that this command prints:
```bash
git -C D:/clients/casaos/CasaOS-AppManagement rev-parse v0.4.57^{commit}
```

- [ ] **Step 4: Pin both releases and write the notes**

Print the two commits to pin:
```bash
git -C D:/clients/casaos/CasaOS-AppManagement rev-parse v0.4.57^{commit}; git -C D:/clients/casaos/CasaOS-UI rev-parse v0.4.66^{commit}
```
In `release/components.env` set `CASAOS_RELEASE_TAG=v0.4.92`, `CASAOS_APP_MANAGEMENT_TAG=v0.4.57`, `CASAOS_APP_MANAGEMENT_COMMIT=` the first sha, `CASAOS_UI_TAG=v0.4.66` and `CASAOS_UI_COMMIT=` the second sha. The file mixes line endings: edit it with a script that matches each line with whichever ending it has, and check the result with `git diff release/components.env`, which must show exactly five changed lines.

Add above the newest section of `CHANGELOG.md`, with `YYYY-MM-DD` replaced by the output of `date +%F`:
```markdown
## [0.4.92] - YYYY-MM-DD

Components: CasaOS-AppManagement `v0.4.57`, CasaOS-UI `v0.4.66`. Unchanged from v0.4.91: CasaOS `v0.4.54`, Gateway `v0.4.29`, UserService `v0.4.28`, MessageBus `v0.4.26`, LocalStorage `v0.4.40`, Common `v0.4.25`, rclone `v1.75.1`.

### Added

- **An app deployed from its git repository.** "From a git repository…" in the apps menu clones a repository, shows what its compose file will run, lets the `.env` be filled in, then builds and starts it. A stack started by hand whose folder is a git repository can be adopted from its new Repository tab. CasaOS checks the branch every 5 minutes and shows new commits on the card; a switch per app rebuilds them automatically. The running version keeps serving while the new one builds, a failed build changes nothing, and a new version that does not start correctly is rolled back to the previous one. Private repositories are reached with a deploy key CasaOS generates, or with a token. The last three versions can be reverted to in one click.

### Changed

- **`git` is installed with CasaOS.**
- **One operation at a time per app.** A build, an update, a settings or `.env` save, and a backup of the same app no longer overlap: the second one is refused and says which operation is running.
- **The install check deploys an app from a bare repository**, rebuilds it while polling its port, and rolls a broken commit back.
```

Add above the newest "What is in" section of `README.md`:
```markdown
## What is in v0.4.92

**Your own projects are apps.** An app can be deployed from its git repository, or adopted from a folder that already is one: CasaOS builds it, follows its branch, rebuilds new commits without stopping the running version, and rolls back a version that does not start.
```
and set the components table rows to `| [CasaOS-UI](https://github.com/ReCasaOS/CasaOS-UI) | v0.4.66 |` and `| [CasaOS-AppManagement](https://github.com/ReCasaOS/CasaOS-AppManagement) | v0.4.57 |`.

- [ ] **Step 5: Commit, push, tag**

```bash
cd D:/clients/casaos/get-digest && git add release/components.env CHANGELOG.md README.md && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "chore(release): 0.4.92"
```
One command per call:
```bash
git -C D:/clients/casaos/get-digest push inkly HEAD:main
```
```bash
git -C D:/clients/casaos/get-digest -c user.name=Gary -c user.email=contact@ixelia.fr tag -a v0.4.92 -m v0.4.92 HEAD
```
```bash
git -C D:/clients/casaos/get-digest push inkly v0.4.92
```

- [ ] **Step 6: Watch the release and its install check**

```bash
id=$(gh run list -R ReCasaOS/CasaOS-Install --workflow release.yml --limit 5 --json databaseId,headBranch -q '.[] | select(.headBranch == "v0.4.92") | .databaseId' | sed -n 1p); gh run watch "$id" -R ReCasaOS/CasaOS-Install --exit-status --interval 30; echo "exit=$?"
```
Expected: `exit=0`, and:
```bash
gh run view "$id" -R ReCasaOS/CasaOS-Install --json jobs -q '.jobs[] | "\(.conclusion) \(.name)"'
```
Expected: `success` on "Assemble and publish the installer" and on the three install-check legs. In each leg's log the step "An app deployed from its git repository" prints `the build was seen: true; requests not answered by v1 during it: 0` and `state after the broken commit: rolled_back`.

If the bundle job died on a network error while fetching an asset (for example `curl: (35)` and "No digest for …") and no release was created, run `gh run rerun "$id" -R ReCasaOS/CasaOS-Install --failed` and watch again. Any other red leg: read that job's log before changing anything.

- [ ] **Step 7: Confirm what is published**

```bash
gh release view -R ReCasaOS/CasaOS-Install --json tagName,isDraft -q '"latest=\(.tagName) draft=\(.isDraft)"'
```
Expected: `latest=v0.4.92 draft=false`
