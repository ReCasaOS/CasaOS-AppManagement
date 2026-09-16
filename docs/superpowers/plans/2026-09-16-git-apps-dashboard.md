# Git apps: the dashboard (CasaOS-UI) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In CasaOS-UI, give the owner "From a git repository…" in the apps "+" menu (register, clone and review, deploy with the live build log), the Repository tab of a git or adoptable app, the notices that keep Settings, Compose and a tracked `.env` from editing tracked files, the git badges of the app card, and a card and a panel for a git app no deployment has given a container.

**Architecture:** A hand-written client, `src/service/gitApps.js`, registered as `$api.gitApps`, speaks AppManagement's six git routes. Every rule and every word that can be checked without mounting a component lives in `src/components/Apps/gitApps.js`: badge, outcome and sensitive-option labels, the last-check line, revert, adoption, the grid item of a git app with no container, the default app name, the live log, and `checkEnded`, which reads an app until the check it started has ended (a check answers 202 and nothing else says when it ends). Two new components, `GitAppModal.vue` and `GitRepoTab.vue`, follow the `app:git-*` message-bus events through the existing `sockets: { … }` component option (`src/plugins/socket.js`). `AppPanel.vue` asks the git route once when it opens an installed app and adds the tab and the notices, or shows the tab alone; `AppCard.vue` reads the grid item's `git`; `AppSection.vue` opens the dialog, and opens the panel of a git app with no container without asking for a compose app it does not have.

**Tech Stack:** Vue 3.5 (options API), Buefy 3.1, Bulma 1, vue-i18n 9 in legacy mode, CodeMirror 5 through `src/components/basicComponents/CodeMirror.vue`, `clipboard-copy`, vitest 1.6 with happy-dom and @vue/test-utils 2.

**Spec:** `D:/clients/casaos/CasaOS-AppManagement/docs/superpowers/specs/2026-09-16-git-apps-design.md`, sections "What the owner sees", "Dashboard" and "Clarifications made while planning". AppManagement's backend and the installer have their own plans beside this one; this plan consumes the API contract below, as the clarifications amend it, and every call is mocked in its specs, so it does not wait on them.

## Global Constraints

- Repository `D:/clients/casaos/CasaOS-UI`, branch `feat/git-apps` created from `inkly/main` (`6f2d4ef`, `chore(release): 0.4.65`). Every command below runs in Git Bash from `D:/clients/casaos/CasaOS-UI`.
- Base: CasaOS-UI v0.4.65, AppManagement v0.4.56, distribution v0.4.91 (from the spec).
- The client is hand-written like `src/service/updates.js` and registered as `$api.gitApps`. Do not regenerate `src/openapi/app_management` and do not touch `src/service/index.spec.js`: "The generated client lags the spec, and regenerating it rewrites thousands of lines."
- New strings go to `src/assets/lang/en_US.json` and `src/assets/lang/fr_FR.json` only. In `en_US.json` the value is the key. `fr_FR.json` gets real French (a space before `:`, `…` for an ellipsis). A key never contains `|` (vue-i18n plural separator); a key may end with a period; no message contains `@` (vue-i18n linked-message syntax).
- Card badge labels, exactly: new_commits "New commits"; building "Building"; deploying "Deploying"; build_failed "Build failed"; rolled_back "Rolled back"; failed "Deployment failed"; unreachable "Repository unreachable"; interrupted "Interrupted".
- The automatic rebuild switch shows the warning that it runs whatever is pushed to the branch.
- Code style is `eslint.config.mjs` (@antfu/eslint-config): tabs, single quotes, no semicolons, `<template>` then `<script>` then `<style>`. Gate before any push: `npx eslint . --quiet` exits 0. Never run a linter or formatter with `--fix`.
- Tests: `npx vitest run <spec paths>`. A component spec mounts with `mount(Component, { global: { plugins: [Buefy, i18n], mocks } })` and mocks `@/assets/lang` as `{ default: { en_us: {} } }`, so `$t` returns its key and leaves `{placeholders}` uninterpolated. `shallowMount` does not render a stubbed component's slot; Buefy tooltips and dropdown menus render with append-to-body, so a spec that looks for them queries `document.body` and clears `document.body.innerHTML` in `afterEach` (see `src/components/Apps/AppCard.spec.js`). A spec that waits on a timer fakes only `setTimeout` and `clearTimeout` (`vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })`): `flushPromises` needs the real `setImmediate`. The full vitest run can fail a few suites at load time under parallel load (a smoke test timing out after 5000 ms, "Failed to load url") and pass on a re-run: re-run once before diagnosing.
- The working tree is checked out with CRLF line endings (`core.autocrlf=true`). Change an existing file with the Edit tool, replacing exactly the block the step shows; never rewrite an existing file whole, never convert its line endings.
- Commits: stage files by name (`git add -A` and `git add .` sweep in `.cache/`), then `git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "..."`. No `Co-Authored-By` line and no tool attribution in any message. Never push.
- Forbidden: `git push`, `git reset --hard`, `git checkout -- <path>`, `git restore`, `git stash`, `rm -rf`, `pnpm install`, `npm run lint -- --fix`, `npx eslint --fix`, editing anything under `src/openapi/`, editing a file this plan does not list.

### API contract, as first given

Routes under `/v2/app_management/git`, owner token like every v2 route, errors as `{"message": string}` bodies:

1. `POST /v2/app_management/git`, body `{"name": string, "url": string, "branch": string (optional), "access": "none"|"key"|"token", "token": string (required when access is token)}`: 200 `{"data": GitApp}` with origin `created`, `cloned` false, `public_key` when access is key. 400 invalid name, URL or access. 409 when the name is used by an app, a compose project or a registered git app.
2. `GET /v2/app_management/git/{app}`: 200 `{"data": GitApp}`. 404 when the app is neither registered nor adoptable.
3. `PUT /v2/app_management/git/{app}`, body `{"branch": string, "auto_deploy": boolean, "access": "none"|"key"|"token", "token": string}`, every field optional: 200 `{"data": GitApp}`. Adopts an adoptable app. 400 invalid. 409 while an operation runs.
4. `POST /v2/app_management/git/{app}/check`: 200 `{"data": GitApp}`, synchronous: ls-remote; for a registered app not cloned yet, clone and validate the compose files; adopts an adoptable app. 400 `{"message": string, "data": {"example": string}}` when the clone has no compose file. 409 while an operation runs.
5. `POST /v2/app_management/git/{app}/deploy`, body `{"commit": string (optional, 40 hex), "env": string (optional)}`: 202 `{"data": GitApp}`, the deployment runs in the background. No commit: the remote's latest. A commit from the history: a revert. `env` only before the first deployment and never when `.env` is tracked. 400 when a precondition fails. 409 while an operation runs.
6. `DELETE /v2/app_management/git/{app}`: 200. 409 when the app has been deployed or an operation runs.

GitApp:

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
  "env_template": "KEY=value\n",
  "build_log": "the last 64 KiB of the build log"
}
```

`head` null until cloned, `deployed` null before the first deployment, `check` null before the first check, `operation` null when none runs, `compose` null until cloned, `env_template` null without a template.

- Grid: `WebAppGridItem` gains `git: {"new_commits": boolean, "state": <the same enum>}`, absent for other apps.
- Events: `app:git-build-begin`, `app:git-build-progress`, `app:git-build-end`, `app:git-build-error`, `app:git-deploy-end`, `app:git-deploy-error`; all carry `app:name`; progress and both error events also carry `message`. The dashboard receives them as `res.Properties['app:name']` and `res.Properties.message` in a `sockets:` handler, like every other event (`src/components/CoreService.vue:541-727`).

### Clarifications (binding; they supersede the contract above where they differ)

From the spec's "Clarifications made while planning" and the coordinator's decisions of 2026-09-16:

1. **Deploy adopts.** `POST /git/{app}/deploy` adopts an adoptable app before deploying, like `PUT` and `check`; "Fetch and rebuild" is offered to an adoptable app.
2. **Detached HEAD, no remote.** An adoptable folder in detached HEAD answers `"branch": ""`, one without a remote answers `"remote": ""`, and `PUT`, `check` and `deploy` on it answer 400 with the reason.
3. **A git app with no container keeps a card.** A registered git app never deployed, or left without a container by a failed first deployment, appears in the grid as `{"app_type": "v2app", "name", "title": {"en_us": name}, "status": "", "git": {"new_commits", "state", "deployed": false}}`; the grid's `git` gains `"deployed": boolean`. Its card shows the git badge and offers only opening the panel and deleting the app; its panel shows only the Repository tab.
4. **DELETE** `/git/{app}` is allowed while `GitApp.deployed` is null, whatever the history. It removes leftover containers and networks, the clone, the state and the secrets, and answers 409 once a deployment has succeeded or while an operation runs.
5. **Sensitive options.** `compose.services[].sensitive` holds only `"privileged"`, `"network_mode_host"`, `"pid_host"`, `"cap_add"`, `"devices"` and `"docker_socket"`, translated in `en_US` and `fr_FR`.
6. **Check is asynchronous.** `POST /git/{app}/check` answers 202 `{"data": GitApp}` with `operation.kind` `"check"`, and the client polls `GET` until the check has ended; no request waits for a clone. A clone without a compose file ends with the clone removed, `check.error` holding the message, and a new GitApp field `"compose_example": string` holding a sample compose file, null otherwise. The 400 with `data.example` is gone. An automatic deployment a check triggers starts once the check's operation has ended.
7. **Adoptable apps get the notices too.** For an adoptable app as for a git app, the Settings and Compose tabs show the repository notice, and the `.env` tab is read-only when `env_tracked` is true.
8. **Minor rules.** `new_commits` is false while `deployed` is null. `"token": ""` is invalid in `PUT`; switching `access` to `none` or `key` forgets a saved token. `env` in deploy is accepted while `deployed` is null, and never when `env_tracked` is true.

### Where the clarified contract still leaves room, this plan decides

1. **One badge on a card**, in this order: the NEW marker, a git `state` other than `idle`, `new_commits`, then "Update available". A failed build's commit is still new; the failure is the news.
2. **A git app with no container** is recognised by `git.deployed === false` together with an empty `status`. A compose app's grid status is never empty (`composeAppStatus` in AppManagement's `route/v2/compose_app.go:790-807` answers `"unknown"` at worst), so a git app whose failed first deployment left containers keeps the ordinary card and panel. Clicking the icon of a card without a container opens its panel, since there is nothing to start.
3. **`checkEnded` reads the app every 2 seconds** while `operation.kind` is `"check"`, and stops when the component closes. It stops at a deployment the check started (clarification 6): that one reports through its events.
4. **The dialog closes only through its own buttons** (`canCancel: []`): its Cancel deletes an app that was registered and never deployed. After a failed first deployment it offers "Deploy again" (with the same `.env`) and "Delete this app".
5. **A live build log keeps the last 64 KiB**, as the server does, so a long build cannot grow the page without bound.
6. **`ComposeConfig` stays mounted, hidden, for a git or adoptable app.** It is what names the app in the panel title, the service the terminal opens on and the file the export saves (`AppPanel.vue:477-481`, `1576`, `1529-1538`). In the panel of a git app with no container nothing needs it, and it is not mounted.
7. **The token is write-only.** The access form sends `token` only when one was typed; an empty field keeps the saved token (clarification 8).

## Files

| File | Responsibility |
|---|---|
| `src/service/gitApps.js` (new) | The six routes, nothing else |
| `src/service/gitApps.spec.js` (new) | Method, URL and body of each call |
| `src/service/api.js` | Registers `gitApps` |
| `src/components/Apps/gitApps.js` (new) | Pure rules and words of git apps, and `checkEnded` |
| `src/components/Apps/gitApps.spec.js` (new) | Their specs |
| `src/components/Apps/AppCard.vue`, `AppCard.spec.js` | The git badge; the menu of a git app with no container |
| `src/components/Apps/GitAppModal.vue`, `GitAppModal.spec.js` (new) | The three-step dialog and the live log |
| `src/components/Apps/GitRepoTab.vue`, `GitRepoTab.spec.js` (new) | The Repository tab |
| `src/components/Apps/EnvEditor.vue`, `EnvEditor.spec.js` | A read-only mode for a tracked `.env` |
| `src/components/Apps/AppPanel.vue`, `AppPanel.spec.js` (new) | The tab, the notices, the read-only `.env`, the Repository tab alone |
| `src/components/Apps/AppSection.vue`, `AppSection.spec.js` | "From a git repository…"; the panel of a git app with no container |
| `src/assets/lang/en_US.json`, `src/assets/lang/fr_FR.json` | The words, each task adding its own |

The tasks run in order: each string step appends after the entry the previous task added.

---

### Task 1: The git apps client, `$api.gitApps`

**Files:**
- Create: `src/service/gitApps.js` (the pattern of `src/service/updates.js:1-24`)
- Create: `src/service/gitApps.spec.js` (the fake-adapter pattern of `src/service/container.spec.js:1-16`)
- Modify: `src/service/api.js:4` (imports) and `src/service/api.js:25` (the Apps group)

**Interfaces:**
- Consumes: `api` from `src/service/service.js:147-181`: `get(url)`, `post(url, data, config)`, `put(url, data)`, `delete(url)`; the shared axios instance times out after 60 s (`service.js:9-16`), which every call here fits in now that `check` answers as it starts.
- Produces, on `this.$api.gitApps` (every call returns the axios promise, the GitApp at `res.data.data`):
  - `create(body: { name: string, url: string, branch?: string, access: 'none'|'key'|'token', token?: string })` → POST `/v2/app_management/git`
  - `get(app: string)` → GET `/v2/app_management/git/{app}`
  - `update(app: string, body: { branch?, auto_deploy?, access?, token? })` → PUT `/v2/app_management/git/{app}`
  - `check(app: string)` → POST `/v2/app_management/git/{app}/check`, no body; answers 202 as the check starts
  - `deploy(app: string, body: { commit?: string, env?: string } = {})` → POST `/v2/app_management/git/{app}/deploy`; answers 202
  - `remove(app: string)` → DELETE `/v2/app_management/git/{app}`

- [ ] **Step 1: Create the branch**

Run:

```bash
cd D:/clients/casaos/CasaOS-UI && git status --short
```

Expected: no output. If anything is listed, stop and report it; do not stash, reset or check out anything.

Run:

```bash
git -C D:/clients/casaos/CasaOS-UI fetch inkly && git -C D:/clients/casaos/CasaOS-UI switch -c feat/git-apps inkly/main
```

Expected: ends with `Switched to a new branch 'feat/git-apps'`.

- [ ] **Step 2: Write the failing spec**

Create `src/service/gitApps.spec.js`:

```js
// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import gitApps from './gitApps.js'
import { instance } from './service.js'

// service.js wires its 401 interceptor to the router and the store; neither
// is exercised here.
vi.mock('@/router', () => ({ default: { replace() {} } }))
vi.mock('@/store', () => ({ default: { commit() {} } }))

// A fake adapter sees the request as XHR would, after transformRequest.
const adapter = vi.fn(config => Promise.resolve({ data: { data: {} }, status: 200, statusText: 'OK', headers: {}, config }))
instance.defaults.adapter = adapter

const sent = () => adapter.mock.calls[0][0]

const COMMIT = 'a'.repeat(40)

describe('gitApps client', () => {
	beforeEach(() => adapter.mockClear())

	it.each([
		[() => gitApps.create({ name: 'jarvis', url: 'https://example.com/jarvis.git', access: 'none' }), 'post', '/v2/app_management/git'],
		[() => gitApps.get('jarvis'), 'get', '/v2/app_management/git/jarvis'],
		[() => gitApps.update('jarvis', { auto_deploy: true }), 'put', '/v2/app_management/git/jarvis'],
		[() => gitApps.check('jarvis'), 'post', '/v2/app_management/git/jarvis/check'],
		[() => gitApps.deploy('jarvis'), 'post', '/v2/app_management/git/jarvis/deploy'],
		[() => gitApps.remove('jarvis'), 'delete', '/v2/app_management/git/jarvis'],
	])('%# sends the method and the URL of the contract', async (call, method, url) => {
		await call()
		expect(sent().method).toBe(method)
		expect(sent().url).toBe(url)
	})

	it('sends the registration as JSON, leaving out what was not given', async () => {
		await gitApps.create({ name: 'jarvis', url: 'https://example.com/jarvis.git', branch: undefined, access: 'key', token: undefined })
		expect(JSON.parse(sent().data)).toEqual({ name: 'jarvis', url: 'https://example.com/jarvis.git', access: 'key' })
	})

	it('sends a revert as the commit to deploy', async () => {
		await gitApps.deploy('jarvis', { commit: COMMIT })
		expect(JSON.parse(sent().data)).toEqual({ commit: COMMIT })
	})

	it('starts a check with no body, under the shared timeout: it answers as it starts', async () => {
		await gitApps.check('jarvis')
		expect(sent().data).toBeUndefined()
		expect(sent().timeout).toBe(60000)
	})

	it('escapes the app name in the path', async () => {
		await gitApps.get('a b')
		expect(sent().url).toBe('/v2/app_management/git/a%20b')
	})
})
```

- [ ] **Step 3: Run it to see it fail**

Run: `npx vitest run src/service/gitApps.spec.js`

Expected: FAIL, `Error: Failed to resolve import "./gitApps.js" from "src/service/gitApps.spec.js". Does the file exist?`

- [ ] **Step 4: Write the client**

Create `src/service/gitApps.js`:

```js
import { api } from './service.js'

const PREFIX = '/v2/app_management/git'

// Apps deployed from their own git repository (AppManagement, design of
// 2026-09-16). Hand-written like updates.js: the generated client lags the spec,
// and regenerating it rewrites thousands of lines. Every answer that carries an
// app is `{ data: GitApp }`; every error is `{ message }`.
const gitApps = {
	// `{ name, url, branch?, access, token? }`. Registers the app without cloning
	// it; in key mode the answer carries the public key to add to the repository.
	create(body) {
		return api.post(PREFIX, body)
	},

	// 404 when the app is neither registered nor adoptable.
	get(app) {
		return api.get(`${PREFIX}/${encodeURIComponent(app)}`)
	},

	// Any of `{ branch, auto_deploy, access, token }`. Adopts an adoptable app.
	update(app, body) {
		return api.put(`${PREFIX}/${encodeURIComponent(app)}`, body)
	},

	// Answers 202 as the check starts, with `operation.kind` "check"; `get` says
	// when it has ended (checkEnded in components/Apps/gitApps.js). Asks the remote,
	// clones an app registered and not cloned yet, adopts an adoptable one.
	check(app) {
		return api.post(`${PREFIX}/${encodeURIComponent(app)}/check`)
	},

	// Answers 202 as the deployment starts, and adopts an adoptable app.
	// `{ commit?, env? }`: no commit is the remote's latest, a commit from the
	// history is a revert; `env` only while no deployment has succeeded.
	deploy(app, body = {}) {
		return api.post(`${PREFIX}/${encodeURIComponent(app)}/deploy`, body)
	},

	// Removes an app no deployment has succeeded for, with whatever a failed one
	// left; 409 once one has.
	remove(app) {
		return api.delete(`${PREFIX}/${encodeURIComponent(app)}`)
	},
}

export default gitApps
```

- [ ] **Step 5: Register it as `$api.gitApps`**

In `src/service/api.js`:

Replace:

```js
import updates from './updates.js'
```

with:

```js
import updates from './updates.js'
import gitApps from './gitApps.js'
```

and:

Replace:

```js
	updates,
```

with:

```js
	updates,
	gitApps,
```

- [ ] **Step 6: Run the spec to see it pass**

Run: `npx vitest run src/service/gitApps.spec.js`

Expected: PASS, `✓ src/service/gitApps.spec.js  (10 tests)` and `Tests  10 passed (10)`.

- [ ] **Step 7: Commit**

```bash
git add src/service/gitApps.js src/service/gitApps.spec.js src/service/api.js
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): a client for AppManagement's git app routes"
```

---

### Task 2: The rules and the words of git apps

**Files:**
- Create: `src/components/Apps/gitApps.js` (the pure-helper style of `src/components/Apps/updateAll.js:1-36`)
- Create: `src/components/Apps/gitApps.spec.js` (the style of `src/components/Apps/updateAll.spec.js:1-38`)
- Modify: `src/assets/lang/en_US.json:715` and `src/assets/lang/fr_FR.json:715` (the last entry of each)

**Interfaces:**
- Consumes: nothing.
- Produces, as named exports of `src/components/Apps/gitApps.js`:
  - `outcomeTag(value: string): { label: string, type: 'is-success'|'is-danger' }`: a state or a history outcome in words; `adopted`, `deployed`, `building`, `deploying` are `is-success`, anything else `is-danger`; an unknown value is its own label.
  - `gitBadge(git?: { state: string, new_commits: boolean }): { label, type } | null`: the card's badge.
  - `withoutContainer(item: WebAppGridItem): boolean`: the grid item of a git app no deployment has given a container (`git.deployed === false` and an empty `status`).
  - `sensitiveLabel(option: string): string`: the words of one of the six sensitive options; an unknown one is its own label.
  - `sensitiveServices(compose: GitApp['compose']): Service[]`: the services whose `sensitive` is not empty; `[]` for a null summary.
  - `shortCommit(commit: string): string`: the first seven characters, `''` for null.
  - `checkSummary(app: GitApp): { message: string, params: object }`: the last check, to pass to `$t`.
  - `canRevert(app: GitApp, entry: HistoryEntry): boolean`: `revertable`, and not the deployed commit.
  - `canFollow(app: GitApp): boolean`: false only for an adoptable app with an empty `remote` or `branch`.
  - `checkEnded(app: GitApp, read: () => Promise<GitApp>, { stop?: () => boolean, every?: number = 2000 }): Promise<GitApp>`: from the app a check answered with, reads the app every `every` ms while `operation.kind` is `'check'` and `stop()` is false; resolves to the last app read.
  - `projectName(url: string): string`: the default app name.
  - `appendLog(log: string, lines: string): string`: appends with one trailing newline, keeps the last 64 KiB.

- [ ] **Step 1: Write the failing spec**

Create `src/components/Apps/gitApps.spec.js`:

```js
import { describe, expect, it, vi } from 'vitest'
import { appendLog, canFollow, canRevert, checkEnded, checkSummary, gitBadge, outcomeTag, projectName, sensitiveLabel, sensitiveServices, shortCommit, withoutContainer } from './gitApps'

const A = 'a'.repeat(40)
const B = 'b'.repeat(40)

describe('the badge of a git app on its card', () => {
	it.each([
		['building', 'Building', 'is-success'],
		['deploying', 'Deploying', 'is-success'],
		['build_failed', 'Build failed', 'is-danger'],
		['rolled_back', 'Rolled back', 'is-danger'],
		['failed', 'Deployment failed', 'is-danger'],
		['unreachable', 'Repository unreachable', 'is-danger'],
		['interrupted', 'Interrupted', 'is-danger'],
	])('names the state %s', (state, label, type) => {
		expect(gitBadge({ state, new_commits: false })).toEqual({ label, type })
	})

	it('says new commits wait only when nothing else is going on', () => {
		expect(gitBadge({ state: 'idle', new_commits: true })).toEqual({ label: 'New commits', type: 'is-success' })
		// the commit of a failed build is still new: the failure is the news
		expect(gitBadge({ state: 'build_failed', new_commits: true }).label).toBe('Build failed')
	})

	it('says nothing of an idle app with nothing new, nor of an app that is not a git app', () => {
		expect(gitBadge({ state: 'idle', new_commits: false })).toBeNull()
		expect(gitBadge(undefined)).toBeNull()
	})
})

describe('a git app no deployment has given a container', () => {
	const git = { new_commits: false, state: 'build_failed', deployed: false }

	it('is the grid item with no status and a git app never deployed', () => {
		expect(withoutContainer({ app_type: 'v2app', name: 'jarvis', status: '', git })).toBe(true)
	})

	it('is not an app whose first deployment left containers, nor a deployed one, nor any other app', () => {
		expect(withoutContainer({ app_type: 'v2app', name: 'jarvis', status: 'exited', git })).toBe(false)
		expect(withoutContainer({ app_type: 'v2app', name: 'jarvis', status: '', git: { ...git, deployed: true } })).toBe(false)
		expect(withoutContainer({ app_type: 'v2app', name: 'syncthing', status: '' })).toBe(false)
	})
})

describe('the outcome of a deployment', () => {
	it('tells what went right from what did not', () => {
		expect(outcomeTag('adopted')).toEqual({ label: 'Adopted', type: 'is-success' })
		expect(outcomeTag('deployed')).toEqual({ label: 'Deployed', type: 'is-success' })
		expect(outcomeTag('rolled_back')).toEqual({ label: 'Rolled back', type: 'is-danger' })
		expect(outcomeTag('interrupted')).toEqual({ label: 'Interrupted', type: 'is-danger' })
	})

	it('shows an outcome it does not know as it came', () => {
		expect(outcomeTag('exploded').label).toBe('exploded')
	})
})

describe('the review of a compose summary', () => {
	it('keeps the services with a sensitive option, and survives no summary', () => {
		const compose = {
			files: ['compose.yaml'],
			services: [
				{ name: 'web', build: true, sensitive: ['privileged', 'network_mode: host'] },
				{ name: 'db', build: false, sensitive: [] },
				{ name: 'cache', build: false, sensitive: null },
			],
		}
		expect(sensitiveServices(compose).map(s => s.name)).toEqual(['web'])
		expect(sensitiveServices(null)).toEqual([])
	})

	it.each([
		['privileged', 'Privileged mode'],
		['network_mode_host', 'Host network'],
		['pid_host', 'Host process namespace'],
		['cap_add', 'Added capabilities'],
		['devices', 'Host devices'],
		['docker_socket', 'Docker socket'],
	])('words the sensitive option %s', (option, label) => {
		expect(sensitiveLabel(option)).toBe(label)
	})

	it('shows a sensitive option it does not know as it came', () => {
		expect(sensitiveLabel('ipc_host')).toBe('ipc_host')
	})
})

describe('a check, which answers as it starts', () => {
	const checking = { app: 'jarvis', operation: { kind: 'check', commit: '', started_at: '2026-09-16T10:05:00Z' } }
	const done = { app: 'jarvis', operation: null, cloned: true }

	it('reads nothing when no check runs', async () => {
		const read = vi.fn()
		expect(await checkEnded(done, read, { every: 0 })).toBe(done)
		expect(read).not.toHaveBeenCalled()
	})

	it('reads the app until the check has ended', async () => {
		const read = vi.fn().mockResolvedValueOnce(checking).mockResolvedValueOnce(done)
		expect(await checkEnded(checking, read, { every: 0 })).toBe(done)
		expect(read).toHaveBeenCalledTimes(2)
	})

	it('stops at a deployment the check started, which has events of its own', async () => {
		const deploying = { app: 'jarvis', operation: { kind: 'deploy', commit: B, started_at: '2026-09-16T10:05:09Z' } }
		const read = vi.fn().mockResolvedValueOnce(deploying)
		expect(await checkEnded(checking, read, { every: 0 })).toBe(deploying)
	})

	it('stops when told to, with the last app read', async () => {
		const read = vi.fn().mockResolvedValue(checking)
		expect(await checkEnded(checking, read, { every: 0, stop: () => read.mock.calls.length === 2 })).toBe(checking)
		expect(read).toHaveBeenCalledTimes(2)
	})
})

describe('the last check, in words', () => {
	const at = '2026-09-16T10:05:00Z'

	it('says when nothing was checked yet', () => {
		expect(checkSummary({ check: null }).message).toBe('Not checked yet.')
	})

	it('gives the error of a failed check', () => {
		expect(checkSummary({ check: { at, remote_commit: '', error: 'Permission denied (publickey)' } }))
			.toEqual({ message: 'The check failed: {error}', params: { error: 'Permission denied (publickey)' } })
	})

	it('names the new commit, and only when there is one', () => {
		expect(checkSummary({ check: { at, remote_commit: B, error: '' }, new_commits: true }))
			.toEqual({ message: 'Commit {commit} is new on the branch.', params: { commit: 'bbbbbbb' } })
		expect(checkSummary({ check: { at, remote_commit: A, error: '' }, new_commits: false }))
			.toEqual({ message: 'The branch is at {commit}: up to date.', params: { commit: 'aaaaaaa' } })
	})
})

describe('what the Repository tab offers', () => {
	it('offers a revert to a kept version that is not the deployed one', () => {
		const app = { deployed: { commit: A } }
		expect(canRevert(app, { commit: B, revertable: true })).toBe(true)
		expect(canRevert(app, { commit: A, revertable: true })).toBe(false)
		expect(canRevert(app, { commit: B, revertable: false })).toBe(false)
	})

	it('follows a registered app, and an adoptable folder only on a branch with a remote', () => {
		expect(canFollow({ origin: 'created', remote: '', branch: '' })).toBe(true)
		expect(canFollow({ origin: 'adoptable', remote: 'https://example.com/a.git', branch: 'main' })).toBe(true)
		expect(canFollow({ origin: 'adoptable', remote: 'https://example.com/a.git', branch: '' })).toBe(false)
		expect(canFollow({ origin: 'adoptable', remote: '', branch: 'main' })).toBe(false)
	})

	it('shortens a commit to seven digits', () => {
		expect(shortCommit(A)).toBe('aaaaaaa')
		expect(shortCommit(null)).toBe('')
	})
})

describe('the name an app takes from its repository', () => {
	it.each([
		['https://github.com/owner/jarvis.git', 'jarvis'],
		['git@github.com:owner/Jarvis.git', 'jarvis'],
		['https://gitlab.com/group/my.app/', 'my-app'],
		['ssh://git@host:2222/srv/_private.git', 'private'],
		['', ''],
	])('%s gives %s', (url, name) => {
		expect(projectName(url)).toBe(name)
	})
})

describe('a build log as it arrives', () => {
	it('ends every piece with a newline, once', () => {
		expect(appendLog('', 'step 1/3')).toBe('step 1/3\n')
		expect(appendLog('step 1/3\n', 'step 2/3\n')).toBe('step 1/3\nstep 2/3\n')
		expect(appendLog('kept\n', '')).toBe('kept\n')
	})

	it('keeps the last 64 KiB, as the server does', () => {
		const log = appendLog('x'.repeat(64 * 1024), 'last')
		expect(log).toHaveLength(64 * 1024)
		expect(log.endsWith('xlast\n')).toBe(true)
	})
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `npx vitest run src/components/Apps/gitApps.spec.js`

Expected: FAIL, `Error: Failed to load url ./gitApps (resolved id: ./gitApps) in …/src/components/Apps/gitApps.spec.js. Does the file exist?`

- [ ] **Step 3: Write the helpers**

Create `src/components/Apps/gitApps.js`:

```js
// The rules and the words of git apps, kept out of the components so they can be
// checked without mounting one.

// A state or an outcome, as it is shown. The same words serve the card's badge,
// the Repository tab's state and the history of deployments.
const LABELS = {
	adopted: 'Adopted',
	deployed: 'Deployed',
	building: 'Building',
	deploying: 'Deploying',
	build_failed: 'Build failed',
	rolled_back: 'Rolled back',
	failed: 'Deployment failed',
	unreachable: 'Repository unreachable',
	interrupted: 'Interrupted',
}

// What went, or is going, the way it should. Anything else is a failure.
const GOOD = ['adopted', 'deployed', 'building', 'deploying']

// `type` is a Bulma colour: the card's tooltip draws is-success green and
// is-danger red, and draws nothing else.
export function outcomeTag(value) {
	return { label: LABELS[value] || String(value), type: GOOD.includes(value) ? 'is-success' : 'is-danger' }
}

// The badge for a grid item's `git`, or null. One at most, since badges sit on
// top of the icon; a state says more than new commits do: the commit of a
// failed build is still new, and the failure is what the owner has to see.
export function gitBadge(git) {
	if (!git)
		return null
	if (git.state && git.state !== 'idle')
		return outcomeTag(git.state)
	return git.new_commits ? { label: 'New commits', type: 'is-success' } : null
}

// A registered git app no deployment has given a container. The grid lists it
// anyway, with no status, so that its Repository tab and its deletion stay in
// reach; a compose app always has one, "unknown" at worst.
export function withoutContainer(item) {
	return Boolean(item.git) && item.git.deployed === false && !item.status
}

// The only options the server calls sensitive, in words.
const SENSITIVE = {
	privileged: 'Privileged mode',
	network_mode_host: 'Host network',
	pid_host: 'Host process namespace',
	cap_add: 'Added capabilities',
	devices: 'Host devices',
	docker_socket: 'Docker socket',
}

export function sensitiveLabel(option) {
	return SENSITIVE[option] || String(option)
}

// The services whose options reach beyond their own container, for the review
// step to call out before anything runs.
export function sensitiveServices(compose) {
	return ((compose && compose.services) || []).filter(service => service.sensitive && service.sensitive.length > 0)
}

export function shortCommit(commit) {
	return String(commit || '').slice(0, 7)
}

// The last check, in words. `new_commits` is the server's: the branch has a
// commit that is not the deployed one.
export function checkSummary(app) {
	if (!app.check)
		return { message: 'Not checked yet.', params: {} }
	if (app.check.error)
		return { message: 'The check failed: {error}', params: { error: app.check.error } }
	if (app.new_commits)
		return { message: 'Commit {commit} is new on the branch.', params: { commit: shortCommit(app.check.remote_commit) } }
	return { message: 'The branch is at {commit}: up to date.', params: { commit: shortCommit(app.check.remote_commit) } }
}

// A kept version can be switched back to, except the one already deployed.
export function canRevert(app, entry) {
	return Boolean(entry.revertable) && entry.commit !== (app.deployed && app.deployed.commit)
}

// Whether CasaOS can follow this repository: a registered app, or an adoptable
// folder on a branch with a remote. Detached HEAD answers an empty branch, no
// remote an empty remote, and the server refuses to adopt either.
export function canFollow(app) {
	return app.origin !== 'adoptable' || Boolean(app.remote && app.branch)
}

// A check answers as it starts, and says it has ended only by the operation
// leaving its app. `app` is that first answer and `read` resolves to the app as
// it is now; the reading stops once no check runs -- a deployment the check
// starts is followed by its own events -- or once `stop` says so, and resolves to
// the last app read.
export async function checkEnded(app, read, { stop = () => false, every = 2000 } = {}) {
	let current = app
	while (current.operation && current.operation.kind === 'check' && !stop()) {
		await new Promise(resolve => setTimeout(resolve, every))
		current = await read()
	}
	return current
}

// The name a compose project would take from the repository: the last segment
// of its URL without `.git`, lowercased, with anything but a letter, a digit, `_`
// or `-` turned into `-`, and starting with a letter or a digit.
export function projectName(url) {
	const segment = String(url || '').trim().replace(/\/+$/, '').split(/[/:]/).pop()
	return segment.replace(/\.git$/i, '').toLowerCase().replace(/[^\w-]+/g, '-').replace(/^[^a-z0-9]+/, '')
}

// The server keeps the last 64 KiB of a build log; a live one keeps as much.
const LOG_LIMIT = 64 * 1024

// Build output as the progress events bring it: lines, which may or may not end
// with a newline.
export function appendLog(log, lines) {
	if (!lines)
		return log
	const next = log + (lines.endsWith('\n') ? lines : `${lines}\n`)
	return next.length > LOG_LIMIT ? next.slice(-LOG_LIMIT) : next
}
```

- [ ] **Step 4: Run the spec to see it pass**

Run: `npx vitest run src/components/Apps/gitApps.spec.js`

Expected: PASS, `✓ src/components/Apps/gitApps.spec.js  (38 tests)`.

- [ ] **Step 5: Add the words the helpers return**

In `src/assets/lang/en_US.json`, replace the last entry:

```json
  "Container removed. These volumes were kept: {names}": "Container removed. These volumes were kept: {names}"
```

with:

```json
  "Container removed. These volumes were kept: {names}": "Container removed. These volumes were kept: {names}",
  "Adopted": "Adopted",
  "Deployed": "Deployed",
  "Building": "Building",
  "Deploying": "Deploying",
  "Build failed": "Build failed",
  "Rolled back": "Rolled back",
  "Deployment failed": "Deployment failed",
  "Repository unreachable": "Repository unreachable",
  "Interrupted": "Interrupted",
  "New commits": "New commits",
  "Not checked yet.": "Not checked yet.",
  "The check failed: {error}": "The check failed: {error}",
  "Commit {commit} is new on the branch.": "Commit {commit} is new on the branch.",
  "The branch is at {commit}: up to date.": "The branch is at {commit}: up to date.",
  "Privileged mode": "Privileged mode",
  "Host network": "Host network",
  "Host process namespace": "Host process namespace",
  "Added capabilities": "Added capabilities",
  "Host devices": "Host devices",
  "Docker socket": "Docker socket"
```

In `src/assets/lang/fr_FR.json`, replace the last entry:

```json
  "Container removed. These volumes were kept: {names}": "Conteneur supprimé. Ces volumes ont été conservés : {names}"
```

with:

```json
  "Container removed. These volumes were kept: {names}": "Conteneur supprimé. Ces volumes ont été conservés : {names}",
  "Adopted": "Adopté",
  "Deployed": "Déployé",
  "Building": "Construction",
  "Deploying": "Déploiement",
  "Build failed": "Construction échouée",
  "Rolled back": "Retour arrière",
  "Deployment failed": "Déploiement échoué",
  "Repository unreachable": "Dépôt injoignable",
  "Interrupted": "Interrompu",
  "New commits": "Nouveaux commits",
  "Not checked yet.": "Pas encore vérifié.",
  "The check failed: {error}": "La vérification a échoué : {error}",
  "Commit {commit} is new on the branch.": "Le commit {commit} est nouveau sur la branche.",
  "The branch is at {commit}: up to date.": "La branche est à {commit} : à jour.",
  "Privileged mode": "Mode privilégié",
  "Host network": "Réseau de l'hôte",
  "Host process namespace": "Espace de processus de l'hôte",
  "Added capabilities": "Capacités ajoutées",
  "Host devices": "Périphériques de l'hôte",
  "Docker socket": "Socket Docker"
```

- [ ] **Step 6: Check both files still parse**

Run:

```bash
node -e 'for (const f of ["en_US", "fr_FR"]) JSON.parse(require("node:fs").readFileSync("src/assets/lang/" + f + ".json", "utf8")); console.log("both parse")'
```

Expected: `both parse`.

- [ ] **Step 7: Commit**

```bash
git add src/components/Apps/gitApps.js src/components/Apps/gitApps.spec.js src/assets/lang/en_US.json src/assets/lang/fr_FR.json
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): the rules and the words of git apps"
```

---

### Task 3: The app card: the git badge, and the menu of a git app with no container

**Files:**
- Modify: `src/components/Apps/AppCard.vue:4-5` (the action menu), `:128-132` (the badges), `:174` (imports), `:252-254` (`tooltipLabel`), `:288-290` (computed), `:365-369` (`openApp`), `:505-509` (methods, before `uninstallApp`)
- Modify: `src/components/Apps/AppCard.spec.js:88` (a new `describe` before `app card update button`, reusing the `card()` and `badges()` helpers at `:25-55`)
- Modify: `src/assets/lang/en_US.json` and `src/assets/lang/fr_FR.json` (append after Task 2's last entry)

**Interfaces:**
- Consumes: `gitBadge(git)` and `withoutContainer(item)` (Task 2); `this.$api.gitApps.remove(app)` (Task 1); the grid item's `git: { new_commits, state, deployed }`, absent for an app that is not a git app (clarification 3); `CTooltip` (`src/components/basicComponents/tooltip/tooltip.vue`), whose `content` it translates and whose `modal` draws `is-success` green and `is-danger` red.
- Produces: `AppCard` computed `repoBadge: { label, type } | null` and `isGitAppWithoutContainer: boolean`; methods `deleteGitAppConfirm()` and `deleteGitApp()`. A git app with no container keeps emitting `configApp(item, true)` to open its panel, which Task 7 opens on the Repository tab alone, and emits `updateState` once deleted.

How the card of a git app with no container behaves, which the spec pins down: its menu holds "Setting" (the panel) and "Delete" and nothing else; clicking its icon opens the panel, since there is nothing to start; "Delete" asks first, then calls `remove` and has the section read the grid again. A git app whose failed first deployment left containers has a status, and keeps the whole menu.

- [ ] **Step 1: Write the failing tests**

In `src/components/Apps/AppCard.spec.js`:

Replace:

```js
describe('app card update button', () => {
```

with:

```js
describe('app card git badge', () => {
	// the grid's item for a git app no deployment has given a container
	const withoutContainer = { name: 'jarvis', app_type: 'v2app', status: '', title: { en_us: 'jarvis' }, git: { new_commits: false, state: 'build_failed', deployed: false } }

	it('shows the state of a git app, in red when it failed', async () => {
		const wrapper = await card({ git: { state: 'build_failed', new_commits: true } })
		expect(badges(wrapper)).toEqual(['Build failed'])
		expect(wrapper.findComponent(cTooltip).props('modal')).toBe('is-danger')
		wrapper.unmount()
	})

	it('says new commits wait on an idle git app', async () => {
		const wrapper = await card({ git: { state: 'idle', new_commits: true } })
		expect(badges(wrapper)).toEqual(['New commits'])
		expect(wrapper.findComponent(cTooltip).props('modal')).toBe('is-success')
		wrapper.unmount()
	})

	it('says nothing of an idle git app with nothing new', async () => {
		const wrapper = await card({ git: { state: 'idle', new_commits: false } })
		expect(badges(wrapper)).toEqual([])
		wrapper.unmount()
	})

	it('offers a git app with no container only its panel and its deletion', async () => {
		const wrapper = await card(withoutContainer)
		expect([...document.body.querySelectorAll('button')].map(b => b.textContent.trim())).toEqual(['Setting', 'Delete'])
		wrapper.unmount()
	})

	it('opens the panel of a git app with no container, which has nothing to start', async () => {
		const wrapper = await card(withoutContainer, { $messageBus: () => {} })
		wrapper.vm.openApp(wrapper.props('item'))
		expect(wrapper.emitted('configApp')).toEqual([[wrapper.props('item'), true]])
		wrapper.unmount()
	})

	it('deletes a git app with no container through its own route, once confirmed', async () => {
		const confirm = vi.fn()
		const remove = vi.fn().mockResolvedValue({ data: {} })
		const wrapper = await card(withoutContainer, { $buefy: { dialog: { confirm } }, $api: { gitApps: { remove } } })
		document.body.querySelectorAll('button').forEach((b) => {
			if (b.textContent.trim() === 'Delete')
				b.click()
		})
		expect(remove).not.toHaveBeenCalled()

		await confirm.mock.calls[0][0].onConfirm()
		expect(remove).toHaveBeenCalledWith('jarvis')
		expect(wrapper.emitted('updateState')).toHaveLength(1)
		wrapper.unmount()
	})

	it('keeps the whole menu of a git app whose first deployment left containers', async () => {
		const wrapper = await card({ ...withoutContainer, status: 'exited' })
		const labels = [...document.body.querySelectorAll('button')].map(b => b.textContent.trim())
		expect(labels).toContain('Uninstall')
		expect(labels).not.toContain('Delete')
		wrapper.unmount()
	})

	it('puts a git state before the image badge, and the NEW marker before both', async () => {
		const building = await card({ git: { state: 'building', new_commits: false }, update_available: true })
		expect(badges(building)).toEqual(['Building'])
		building.unmount()

		sessionStorage.setItem('newAppTag', JSON.stringify(['syncthing']))
		const fresh = await card({ git: { state: 'building', new_commits: false } })
		expect(badges(fresh)).toEqual(['NEW'])
		sessionStorage.removeItem('newAppTag')
		fresh.unmount()
	})
})

describe('app card update button', () => {
```

- [ ] **Step 2: Run them to see them fail**

Run: `npx vitest run src/components/Apps/AppCard.spec.js`

Expected: FAIL, `Tests  6 failed | 30 passed (36)`, among them `expected [] to deeply equal [ 'Build failed' ]`, `expected [ 'Tips', 'Setting', …(5) ] to deeply equal [ 'Setting', 'Delete' ]` and `expected [ 'Update available' ] to deeply equal [ 'Building' ]`.

- [ ] **Step 3: Give a git app with no container its own menu**

In `src/components/Apps/AppCard.vue`, the action menu:

Replace:

```vue
		<!-- Action Button Start -->
		<div v-if="item.app_type !== 'system' && !isUninstalling && hasActions" class="action-btn">
```

with:

```vue
		<!-- Action Button Start -->
		<!-- A git app no deployment has given a container: its panel, which holds the
			Repository tab, and deleting it are all there is to do with it. -->
		<div v-if="isGitAppWithoutContainer && !isUninstalling" class="action-btn">
			<b-dropdown ref="dro" :mobile-modal="false" :triggers="['contextmenu', 'click']" animation="fade1"
				append-to-body aria-role="list" class="app-card-drop" :position="dropdownPosition"
				@active-change="setDropState">
				<template #trigger>
					<p role="button" @click="handleDorpdownPosition">
						<b-icon class="is-clickable" icon="dots-vertical-outline" pack="casa" />
					</p>
				</template>

				<b-dropdown-item :focusable="false" aria-role="menu-item" custom>
					<b-button expanded type="is-text" @click="configApp()">
						{{ $t('Setting') }}
					</b-button>
					<b-button class="has-text-red" expanded type="is-text" @click="deleteGitAppConfirm">
						{{ $t('Delete') }}
					</b-button>
				</b-dropdown-item>
			</b-dropdown>
		</div>
		<div v-else-if="item.app_type !== 'system' && !isUninstalling && hasActions" class="action-btn">
```

- [ ] **Step 4: Show the badge**

In `src/components/Apps/AppCard.vue`, the badge:

Replace:

```vue
							<CTooltip v-else-if="item.update_available" class="__position __position-wide" content="Update available" />
```

with:

```vue
							<!-- A git app's state, or its new commits: gitApps.js says which wins. Before
								the image badge, since a failed build matters more than a newer image. -->
							<CTooltip v-else-if="repoBadge" :content="repoBadge.label" :modal="repoBadge.type" class="__position __position-wide" />
							<CTooltip v-else-if="item.update_available" class="__position __position-wide" content="Update available" />
```

- [ ] **Step 5: The script**

The import:

Replace:

```js
import { ageKey, containerFacts } from './legacyApps'
```

with:

```js
import { ageKey, containerFacts } from './legacyApps'
import { gitBadge, withoutContainer } from './gitApps'
```

the tooltip, which has nothing to launch:

Replace:

```js
			} else if (this.isCheckThenUpdate) {
				return this.$t('CheckThenUpdate')
			} else if (this.item.status === 'running') {
```

with:

```js
			} else if (this.isCheckThenUpdate) {
				return this.$t('CheckThenUpdate')
			} else if (this.isGitAppWithoutContainer) {
				// nothing to launch: a click opens its panel
				return this.$t('Setting')
			} else if (this.item.status === 'running') {
```

the computed:

Replace:

```js
		isContainerApp() {
			return this.item.app_type === 'container'
		},
```

with:

```js
		isContainerApp() {
			return this.item.app_type === 'container'
		},
		// from the grid item's `git`, which only a git app carries
		repoBadge() {
			return gitBadge(this.item.git)
		},
		isGitAppWithoutContainer() {
			return withoutContainer(this.item)
		},
```

the icon's click:

Replace:

```js
		openApp(item) {
			if (this.isContainerApp) {
				this.$emit('importApp', item, false)
				return false
			}
```

with:

```js
		openApp(item) {
			if (this.isContainerApp) {
				this.$emit('importApp', item, false)
				return false
			}
			if (this.isGitAppWithoutContainer) {
				this.configApp()
				return false
			}
```

and the deletion:

Replace:

```js
		/**
		 * @description: Uninstall app
		 * @return {*} void
		 */
		uninstallApp(checkDelConfig) {
```

with:

```js
		// A git app with no container goes through its own route, which takes what
		// CasaOS cloned, built or left behind for it: the compose route knows no such app.
		deleteGitAppConfirm() {
			this.$refs.dro.isActive = false
			this.$buefy.dialog.confirm({
				title: this.$t('Attention'),
				message: this.$t('Delete {name}? What CasaOS cloned, built or started for it goes with it; the repository itself is not touched.', { name: this.containerName }),
				type: 'is-dark',
				confirmText: this.$t('Delete'),
				cancelText: this.$t('Cancel'),
				onConfirm: () => this.deleteGitApp(),
			})
		},

		async deleteGitApp() {
			this.isUninstalling = true
			try {
				await this.$api.gitApps.remove(this.item.name)
				this.removeIdFromSessionStorage(this.item.name)
				// the section reads the grid again, and this card is gone from it
				this.$emit('updateState')
			} catch (err) {
				this.isUninstalling = false
				this.$buefy.toast.open({
					message: err.response?.data?.message || err.message,
					type: 'is-danger',
					position: 'is-top',
					duration: 5000,
				})
			}
		},

		/**
		 * @description: Uninstall app
		 * @return {*} void
		 */
		uninstallApp(checkDelConfig) {
```

- [ ] **Step 6: Run the card spec and the smoke spec to see them pass**

Run: `npx vitest run src/components/Apps/AppCard.spec.js src/__tests__/mount.spec.js`

Expected: PASS, `✓ src/components/Apps/AppCard.spec.js  (36 tests)` and `✓ src/__tests__/mount.spec.js  (22 tests)`. The smoke spec mounts `AppCard` with an item that has no `git`, and fails on any Vue warning.

- [ ] **Step 7: Add the card's words**

In `src/assets/lang/en_US.json`, replace the last entry:

```json
  "Docker socket": "Docker socket"
```

with:

```json
  "Docker socket": "Docker socket",
  "Delete {name}? What CasaOS cloned, built or started for it goes with it; the repository itself is not touched.": "Delete {name}? What CasaOS cloned, built or started for it goes with it; the repository itself is not touched."
```

In `src/assets/lang/fr_FR.json`, replace the last entry:

```json
  "Docker socket": "Socket Docker"
```

with:

```json
  "Docker socket": "Socket Docker",
  "Delete {name}? What CasaOS cloned, built or started for it goes with it; the repository itself is not touched.": "Supprimer {name} ? Ce que CasaOS a cloné, construit ou démarré pour elle part avec elle ; le dépôt lui-même n'est pas touché."
```

- [ ] **Step 8: Check both files still parse**

Run:

```bash
node -e 'for (const f of ["en_US", "fr_FR"]) JSON.parse(require("node:fs").readFileSync("src/assets/lang/" + f + ".json", "utf8")); console.log("both parse")'
```

Expected: `both parse`.

- [ ] **Step 9: Commit**

```bash
git add src/components/Apps/AppCard.vue src/components/Apps/AppCard.spec.js src/assets/lang/en_US.json src/assets/lang/fr_FR.json
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): a git app's state on its card, and the card of one with no container"
```

---

### Task 4: The dialog, `GitAppModal.vue`

**Files:**
- Create: `src/components/Apps/GitAppModal.vue` (the modal-card layout of `src/components/Apps/UpdateAllModal.vue:1-62`; the CodeMirror options of `src/components/Apps/EnvEditor.vue:62-68`)
- Create: `src/components/Apps/GitAppModal.spec.js` (the mount pattern of `src/components/Apps/EnvEditor.spec.js:1-28`)
- Modify: `src/assets/lang/en_US.json` and `src/assets/lang/fr_FR.json` (append after Task 3's entry)

**Interfaces:**
- Consumes: `this.$api.gitApps.create`, `.check`, `.get`, `.deploy`, `.remove` (Task 1); `projectName`, `sensitiveServices`, `sensitiveLabel`, `checkEnded`, `appendLog` (Task 2); `events.RELOAD_APP_LIST` (`src/events/events.js:17`) on `this.$EventBus`; `this.$buefy.toast`; the `app:git-*` events through `sockets:` (`src/plugins/socket.js`).
- Produces: component `GitAppModal` (no props, emits `close`). Steps held in `data.step`: `'repository'`, `'review'`, `'deploy'`. Opened by Task 7 with `this.$buefy.modal.open({ component: GitAppModal, hasModalCard: true, trapFocus: true, canCancel: [] })`.

How it behaves, which the spec pins down:
- Repository step: URL, branch, name, access. The name follows the URL (`projectName`) until one is typed. "Next" registers (`create`, `branch` and `token` left out when empty or not in token mode). Without a key it clones at once; with a key it shows the public key with a Copy button and clones on "Continue". Cloning is `check`, which answers 202, then `checkEnded` reading `get` until the check has ended; Cancel stays disabled meanwhile, since `DELETE` answers 409 while an operation runs. A clone that ended without `cloned` and `compose` shows `check.error` and, for a repository without a compose file, `compose_example`; the button becomes "Retry".
- Review step: every service, built or pulled, with its published ports and volumes; the services with sensitive options under a warning, each option in words (`sensitiveLabel`); the `.env` editor prefilled from `env_template`, read-only with an explanation when `env_tracked`.
- Deploy step: `deploy` with `{ env }`, or `{}` when `.env` is tracked. Outcome, reason and "built" are cleared before the request, because the events of a build that fails at once can arrive before the 202. The log follows `app:git-build-progress` of this app only. `app:git-deploy-end` says "Deployed"; `app:git-build-error` and `app:git-deploy-error` show the reason and offer "Deploy again" (the `.env` again: accepted while `deployed` is null) and "Delete this app" (`remove`, allowed while `deployed` is null). Each outcome reloads the grid.
- Cancel, on the first two steps, deletes an app that was registered (`remove`) before closing.

- [ ] **Step 1: Write the failing spec**

Create `src/components/Apps/GitAppModal.spec.js`:

```js
// @vitest-environment happy-dom
import Buefy from 'buefy'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import GitAppModal from './GitAppModal.vue'
import i18n from '@/plugins/i18n'

// require.context has no Vite equivalent; an empty table makes $t return its key.
vi.mock('@/assets/lang', () => ({ default: { en_us: {} } }))

const COMMIT = 'c'.repeat(40)
const REPO_URL = 'https://example.com/owner/Jarvis.git'

function gitApp(fields = {}) {
	return {
		app: 'jarvis',
		origin: 'created',
		dir: '/DATA/AppData/jarvis',
		remote: REPO_URL,
		branch: 'main',
		access: 'none',
		token_set: false,
		auto_deploy: false,
		auto_paused: false,
		blocked: false,
		env_tracked: false,
		cloned: false,
		head: null,
		deployed: null,
		check: null,
		new_commits: false,
		state: 'idle',
		operation: null,
		history: [],
		compose: null,
		env_template: null,
		compose_example: null,
		build_log: '',
		...fields,
	}
}

function cloned(fields = {}) {
	return gitApp({
		cloned: true,
		head: { commit: COMMIT, subject: 'first', tracked_files_clean: true },
		compose: {
			files: ['compose.yaml'],
			services: [
				{ name: 'web', build: true, image: '', ports: ['8080:8080'], volumes: ['./data:/data'], sensitive: ['privileged'] },
				{ name: 'db', build: false, image: 'postgres:16', ports: [], volumes: [], sensitive: [] },
			],
		},
		env_template: 'TOKEN=\n',
		...fields,
	})
}

function setup(api = {}) {
	const gitApps = {
		create: vi.fn().mockResolvedValue({ data: { data: gitApp() } }),
		check: vi.fn().mockResolvedValue({ data: { data: cloned() } }),
		deploy: vi.fn().mockResolvedValue({ data: { data: cloned({ state: 'building' }) } }),
		remove: vi.fn().mockResolvedValue({ data: {} }),
		get: vi.fn().mockResolvedValue({ data: { data: cloned() } }),
		...api,
	}
	const reload = vi.fn()
	const wrapper = mount(GitAppModal, {
		global: {
			plugins: [Buefy, i18n],
			mocks: { $api: { gitApps }, $EventBus: { $emit: reload } },
		},
	})
	const button = label => wrapper.findAll('button').find(b => b.text() === label)
	const click = async (label) => {
		await button(label).trigger('click')
		await flushPromises()
	}
	const fire = (event, Properties) => wrapper.vm.$options.sockets[event].call(wrapper.vm, { Properties })
	const inputs = () => wrapper.findAll('input')
	return { wrapper, gitApps, reload, button, click, fire, inputs }
}

describe('gitAppModal repository step', () => {
	it('names the app after the repository until a name is typed', async () => {
		const { wrapper, inputs } = setup()
		await inputs()[0].setValue(REPO_URL)
		expect(inputs()[2].element.value).toBe('jarvis')

		await inputs()[2].setValue('mine')
		await inputs()[0].setValue('https://example.com/owner/other.git')
		expect(inputs()[2].element.value).toBe('mine')
		wrapper.unmount()
	})

	it('registers, clones and reviews a public repository in one go', async () => {
		const { wrapper, gitApps, click, inputs } = setup()
		await inputs()[0].setValue(REPO_URL)
		await click('Next')

		expect(gitApps.create).toHaveBeenCalledWith({ name: 'jarvis', url: REPO_URL, branch: undefined, access: 'none', token: undefined })
		expect(gitApps.check).toHaveBeenCalledWith('jarvis')
		expect(wrapper.text()).toContain('Read from {files}.')
		wrapper.unmount()
	})

	it('sends a token only in token mode', async () => {
		const { wrapper, gitApps, click, inputs } = setup()
		await inputs()[0].setValue(REPO_URL)
		await inputs()[1].setValue('dev')
		await wrapper.find('select').setValue('token')
		await inputs()[3].setValue('s3cret')
		await click('Next')

		expect(gitApps.create).toHaveBeenCalledWith({ name: 'jarvis', url: REPO_URL, branch: 'dev', access: 'token', token: 's3cret' })
		wrapper.unmount()
	})

	it('shows the deploy key and clones nothing until the owner continues', async () => {
		const { wrapper, gitApps, click, inputs } = setup({
			create: vi.fn().mockResolvedValue({ data: { data: gitApp({ access: 'key', public_key: 'ssh-ed25519 AAAA casaos-jarvis' }) } }),
		})
		await inputs()[0].setValue(REPO_URL)
		await wrapper.find('select').setValue('key')
		await click('Next')

		expect(wrapper.text()).toContain('ssh-ed25519 AAAA casaos-jarvis')
		expect(gitApps.check).not.toHaveBeenCalled()

		await click('Continue')
		expect(gitApps.check).toHaveBeenCalledWith('jarvis')
		wrapper.unmount()
	})

	it('shows what to add when the repository has no compose file, and retries', async () => {
		const example = 'services:\n  app:\n    build: .\n'
		const noCompose = gitApp({ check: { at: '2026-09-16T10:05:00Z', remote_commit: COMMIT, error: 'no compose file at the root' }, compose_example: example })
		const check = vi.fn()
			.mockResolvedValueOnce({ data: { data: noCompose } })
			.mockResolvedValueOnce({ data: { data: cloned() } })
		const { wrapper, click, inputs } = setup({ check })
		await inputs()[0].setValue(REPO_URL)
		await click('Next')

		expect(wrapper.text()).toContain('no compose file at the root')
		expect(wrapper.find('pre').text()).toBe(example.trim())

		await click('Retry')
		expect(check).toHaveBeenCalledTimes(2)
		expect(wrapper.text()).toContain('Read from {files}.')
		wrapper.unmount()
	})

	describe('while the clone runs', () => {
		afterEach(() => vi.useRealTimers())

		it('reads the app until the check has ended, then reviews it', async () => {
			vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
			const checking = gitApp({ operation: { kind: 'check', commit: '', started_at: '2026-09-16T10:05:00Z' } })
			const get = vi.fn().mockResolvedValueOnce({ data: { data: checking } }).mockResolvedValueOnce({ data: { data: cloned() } })
			const { wrapper, gitApps, click, inputs, button } = setup({
				check: vi.fn().mockResolvedValue({ data: { data: checking } }),
				get,
			})
			await inputs()[0].setValue(REPO_URL)
			await click('Next')
			expect(gitApps.check).toHaveBeenCalledWith('jarvis')
			expect(button('Cancel').attributes('disabled')).toBeDefined()

			await vi.advanceTimersByTimeAsync(2000)
			expect(get).toHaveBeenCalledTimes(1)
			expect(wrapper.text()).not.toContain('Read from {files}.')

			await vi.advanceTimersByTimeAsync(2000)
			await flushPromises()
			expect(get).toHaveBeenCalledWith('jarvis')
			expect(wrapper.text()).toContain('Read from {files}.')
			wrapper.unmount()
		})
	})

	it('deletes an app it registered when the owner cancels, and nothing before that', async () => {
		const first = setup()
		await first.click('Cancel')
		expect(first.gitApps.remove).not.toHaveBeenCalled()
		expect(first.wrapper.emitted('close')).toHaveLength(1)
		first.wrapper.unmount()

		const second = setup({
			create: vi.fn().mockResolvedValue({ data: { data: gitApp({ access: 'key', public_key: 'ssh-ed25519 AAAA' }) } }),
		})
		await second.inputs()[0].setValue(REPO_URL)
		await second.wrapper.find('select').setValue('key')
		await second.click('Next')
		await second.click('Cancel')
		expect(second.gitApps.remove).toHaveBeenCalledWith('jarvis')
		expect(second.wrapper.emitted('close')).toHaveLength(1)
		second.wrapper.unmount()
	})
})

describe('gitAppModal review step', () => {
	async function reviewed(app = cloned()) {
		const tools = setup({ check: vi.fn().mockResolvedValue({ data: { data: app } }) })
		await tools.inputs()[0].setValue(REPO_URL)
		await tools.click('Next')
		return tools
	}

	it('lists every service, built or pulled, and calls out the sensitive ones', async () => {
		const { wrapper } = await reviewed()
		const services = wrapper.findAll('.git-app__service').map(s => s.text())
		expect(services[0]).toContain('web')
		expect(services[0]).toContain('built from the repository')
		expect(services[0]).toContain('Published ports: {ports}')
		expect(services[1]).toContain('postgres:16')
		expect(wrapper.text()).toContain('web: Privileged mode')
		wrapper.unmount()
	})

	it('prefills .env from the template and deploys with it', async () => {
		const { wrapper, gitApps, click } = await reviewed()
		const editor = wrapper.findComponent({ name: 'CodeMirrorEditor' }).vm.codemirror
		expect(editor.getValue()).toBe('TOKEN=\n')
		expect(editor.getOption('readOnly')).toBe(false)

		editor.setValue('TOKEN=abc\n')
		await click('Deploy')
		expect(gitApps.deploy).toHaveBeenCalledWith('jarvis', { env: 'TOKEN=abc\n' })
		wrapper.unmount()
	})

	it('keeps a tracked .env read-only and does not send it', async () => {
		const { wrapper, gitApps, click } = await reviewed(cloned({ env_tracked: true, env_template: null }))
		expect(wrapper.findComponent({ name: 'CodeMirrorEditor' }).vm.codemirror.getOption('readOnly')).toBe(true)
		expect(wrapper.text()).toContain('The repository tracks its .env file')

		await click('Deploy')
		expect(gitApps.deploy).toHaveBeenCalledWith('jarvis', {})
		wrapper.unmount()
	})
})

describe('gitAppModal deploy step', () => {
	async function deploying(api = {}) {
		const tools = setup(api)
		await tools.inputs()[0].setValue(REPO_URL)
		await tools.click('Next')
		await tools.click('Deploy')
		return tools
	}

	it('follows the build of its own app and says when it is deployed', async () => {
		const { wrapper, fire, reload } = await deploying()
		expect(wrapper.text()).toContain('Building…')

		fire('app:git-build-begin', { 'app:name': 'jarvis' })
		fire('app:git-build-progress', { 'app:name': 'jarvis', 'message': '#1 [web] FROM node:20' })
		fire('app:git-build-progress', { 'app:name': 'other', 'message': 'not ours' })
		fire('app:git-build-end', { 'app:name': 'jarvis' })
		await flushPromises()
		expect(wrapper.find('pre').text()).toBe('#1 [web] FROM node:20')
		expect(wrapper.text()).toContain('Built. Starting the app…')

		fire('app:git-deploy-end', { 'app:name': 'jarvis' })
		await flushPromises()
		expect(wrapper.find('.has-text-success').text()).toBe('Deployed')
		expect(reload).toHaveBeenCalledWith('reloadAppList')
		wrapper.unmount()
	})

	it('keeps a failure that arrives before the answer, and offers to retry or delete', async () => {
		let modal
		const deploy = vi.fn(() => {
			// the build failed at once: its event lands before the 202
			modal.vm.$options.sockets['app:git-build-error'].call(modal.vm, { Properties: { 'app:name': 'jarvis', 'message': 'no Dockerfile' } })
			return Promise.resolve({ data: { data: cloned({ state: 'build_failed' }) } })
		})
		const tools = setup({ deploy })
		modal = tools.wrapper
		await tools.inputs()[0].setValue(REPO_URL)
		await tools.click('Next')
		await tools.click('Deploy')

		expect(modal.text()).toContain('The build failed: {reason}')
		expect(tools.button('Deploy again')).toBeTruthy()

		await tools.click('Delete this app')
		expect(tools.gitApps.remove).toHaveBeenCalledWith('jarvis')
		expect(modal.emitted('close')).toHaveLength(1)
		modal.unmount()
	})
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `npx vitest run src/components/Apps/GitAppModal.spec.js`

Expected: FAIL, `Error: Failed to resolve import "./GitAppModal.vue" from "src/components/Apps/GitAppModal.spec.js". Does the file exist?`

- [ ] **Step 3: Write the dialog**

Create `src/components/Apps/GitAppModal.vue`:

```vue
<template>
	<div class="modal-card git-app">
		<header class="modal-card-head b-line">
			<h3 class="title is-5 has-text-black">{{ $t('An app from a git repository') }}</h3>
		</header>

		<section class="modal-card-body">
			<b-message v-if="error" class="mb-3" size="is-small" type="is-danger">
				<p>{{ error }}</p>
				<!-- a repository without a compose file: what to add, as the server wrote it -->
				<pre v-if="example" class="git-app__text mt-2">{{ example }}</pre>
			</b-message>

			<!-- 1. the repository, and how to reach it -->
			<template v-if="step === 'repository'">
				<b-field :label="$t('Repository URL')">
					<b-input v-model="url" :disabled="!!app" placeholder="https://github.com/owner/app.git"></b-input>
				</b-field>
				<b-field :label="$t('Branch')" :message="$t('Empty: the branch the repository names as its default.')">
					<b-input v-model="branch" :disabled="!!app"></b-input>
				</b-field>
				<b-field :label="$t('App name')">
					<b-input v-model="name" :disabled="!!app"></b-input>
				</b-field>
				<b-field :label="$t('Access')">
					<b-select v-model="access" :disabled="!!app" expanded>
						<option value="none">{{ $t('Public repository, nothing needed') }}</option>
						<option value="key">{{ $t('Deploy key') }}</option>
						<option value="token">{{ $t('Access token') }}</option>
					</b-select>
				</b-field>
				<b-field v-if="access === 'token'" :label="$t('Token')">
					<b-input v-model="token" :disabled="!!app" autocomplete="off" password-reveal type="password"></b-input>
				</b-field>

				<!-- The key is made as the app is registered, and nothing is cloned until
					the owner has added it to the repository. -->
				<template v-if="app && app.public_key">
					<p class="is-size-7 mb-2">{{ $t('Add this key to the repository as a read-only deploy key, then continue.') }}</p>
					<div class="is-flex is-align-items-flex-start">
						<pre class="git-app__text is-flex-grow-1 mr-2">{{ app.public_key }}</pre>
						<b-button :label="$t('Copy')" rounded size="is-small" @click="copyKey"></b-button>
					</div>
				</template>
			</template>

			<!-- 2. what the repository will run, before anything runs -->
			<template v-else-if="step === 'review'">
				<p class="is-size-7 has-text-full-03 mb-3">{{ $t('Read from {files}.', { files: app.compose.files.join(', ') }) }}</p>
				<div v-for="service in app.compose.services" :key="service.name" class="mb-3 git-app__service">
					<p class="is-size-7">
						<span class="has-text-weight-bold">{{ service.name }}</span>
						<span class="has-text-full-03"> · {{ service.build ? $t('built from the repository') : service.image }}</span>
					</p>
					<p v-if="service.ports && service.ports.length" class="is-size-7 has-text-full-03">
						{{ $t('Published ports: {ports}', { ports: service.ports.join(', ') }) }}
					</p>
					<p v-if="service.volumes && service.volumes.length" class="is-size-7 has-text-full-03">
						{{ $t('Volumes: {volumes}', { volumes: service.volumes.join(', ') }) }}
					</p>
				</div>

				<b-message v-if="sensitive.length" size="is-small" type="is-warning">
					<p>{{ $t('These services reach beyond their own container. Deploy them only from a repository you trust.') }}</p>
					<p v-for="service in sensitive" :key="service.name">
						{{ service.name }}: {{ service.sensitive.map(option => $t(sensitiveLabel(option))).join(', ') }}
					</p>
				</b-message>

				<p class="has-text-weight-bold is-size-7 mt-4 mb-2">.env</p>
				<p v-if="app.env_tracked" class="is-size-7 has-text-full-03 mb-2">
					{{ $t('The repository tracks its .env file, so it cannot be edited here: an edit would modify a tracked file and block every later deployment.') }}
				</p>
				<Codemirror :options="envOptions" :value="env" class="git-app__env" @input="env = $event"></Codemirror>
			</template>

			<!-- 3. the deployment, and the build as it goes -->
			<template v-else>
				<p :class="statusClass" class="is-size-7 mb-2">{{ status }}</p>
				<pre ref="log" class="git-app__text git-app__log">{{ log }}</pre>
			</template>
		</section>

		<footer class="modal-card-foot is-flex is-justify-content-flex-end">
			<b-button v-if="step !== 'deploy'" :disabled="busy" class="mr-2" rounded @click="cancel">{{ $t('Cancel') }}</b-button>
			<b-button v-if="step === 'repository' && !app" :disabled="!canRegister" :loading="busy" rounded type="is-primary" @click="register">
				{{ $t('Next') }}
			</b-button>
			<b-button v-else-if="step === 'repository'" :loading="busy" rounded type="is-primary" @click="clone">
				{{ error ? $t('Retry') : $t('Continue') }}
			</b-button>
			<b-button v-else-if="step === 'review'" :loading="busy" rounded type="is-primary" @click="deploy">
				{{ $t('Deploy') }}
			</b-button>
			<template v-else>
				<!-- a first deployment that failed leaves an app on no card: it is retried
					or deleted here, or it holds its name for nothing -->
				<template v-if="outcome && outcome !== 'deployed'">
					<b-button :disabled="busy" class="mr-2" rounded type="is-danger" @click="cancel">{{ $t('Delete this app') }}</b-button>
					<b-button :loading="busy" class="mr-2" rounded @click="deploy">{{ $t('Deploy again') }}</b-button>
				</template>
				<b-button rounded type="is-primary" @click="$emit('close')">
					{{ outcome ? $t('Close') : $t('Continue in background') }}
				</b-button>
			</template>
		</footer>
	</div>
</template>

<script>
import copy from 'clipboard-copy'
import { appendLog, checkEnded, projectName, sensitiveLabel, sensitiveServices } from './gitApps'
import Codemirror from '@/components/basicComponents/CodeMirror.vue'
import events from '@/events/events'
import 'codemirror/lib/codemirror.css'
import 'codemirror/theme/monokai.css'
import 'codemirror/mode/shell/shell.js'

// An app from a git repository, in three steps: register it (a deploy key is
// made there), clone it and review what it runs, then deploy it and follow the
// build. The dialog closes only through its own buttons, because Cancel deletes
// an app registered and never deployed.
export default {
	name: 'GitAppModal',
	components: { Codemirror },
	emits: ['close'],
	data() {
		return {
			step: 'repository',
			url: '',
			branch: '',
			name: '',
			access: 'none',
			token: '',
			// the GitApp as the server last answered, once registered
			app: null,
			env: '',
			log: '',
			built: false,
			// '' while it runs, then deployed, build_failed or failed
			outcome: '',
			reason: '',
			busy: false,
			error: '',
			example: '',
			// set as the dialog goes, so a check it waits for stops being read
			closed: false,
		}
	},
	computed: {
		canRegister() {
			return Boolean(this.url.trim() && this.name.trim() && (this.access !== 'token' || this.token))
		},
		sensitive() {
			return sensitiveServices(this.app && this.app.compose)
		},
		// read once, as the editor mounts on the review step
		envOptions() {
			return { mode: 'text/x-sh', theme: 'monokai', lineNumbers: true, lineWrapping: true, readOnly: Boolean(this.app && this.app.env_tracked) }
		},
		status() {
			if (this.outcome === 'deployed')
				return this.$t('Deployed')
			if (this.outcome === 'build_failed')
				return this.$t('The build failed: {reason}', { reason: this.reason })
			if (this.outcome === 'failed')
				return this.$t('The deployment failed: {reason}', { reason: this.reason })
			return this.built ? this.$t('Built. Starting the app…') : this.$t('Building…')
		},
		statusClass() {
			return { 'has-text-success': this.outcome === 'deployed', 'has-text-danger': Boolean(this.outcome) && this.outcome !== 'deployed' }
		},
	},
	watch: {
		// The name follows the URL until somebody types one.
		url(next, previous) {
			if (this.name === projectName(previous))
				this.name = projectName(next)
		},
		log() {
			this.$nextTick(() => {
				if (this.$refs.log)
					this.$refs.log.scrollTop = this.$refs.log.scrollHeight
			})
		},
	},
	beforeUnmount() {
		this.closed = true
	},
	methods: {
		sensitiveLabel,

		async register() {
			this.busy = true
			this.error = ''
			try {
				const res = await this.$api.gitApps.create({
					name: this.name.trim(),
					url: this.url.trim(),
					branch: this.branch.trim() || undefined,
					access: this.access,
					token: this.access === 'token' ? this.token : undefined,
				})
				this.app = res.data.data
			} catch (error) {
				this.fail(error)
				return
			} finally {
				this.busy = false
			}
			// with a key, the owner adds it to the repository before anything is cloned
			if (this.access !== 'key')
				await this.clone()
		},

		// The check answers as it starts; the clone has ended once no check runs.
		// A clone that failed -- no access, no compose file -- says why in
		// check.error, and a missing compose file comes with an example to add.
		async clone() {
			this.busy = true
			this.error = ''
			this.example = ''
			try {
				const name = this.app.app
				const started = await this.$api.gitApps.check(name)
				const app = await checkEnded(started.data.data, () => this.$api.gitApps.get(name).then(res => res.data.data), { stop: () => this.closed })
				if (this.closed)
					return
				this.app = app
				if (!app.cloned || !app.compose) {
					this.error = (app.check && app.check.error) || this.$t('CasaOS could not clone the repository.')
					this.example = app.compose_example || ''
					return
				}
				this.env = app.env_template || ''
				this.step = 'review'
			} catch (error) {
				this.fail(error)
			} finally {
				this.busy = false
			}
		},

		async deploy() {
			const before = { outcome: this.outcome, reason: this.reason }
			// Cleared before the request: the events of a build that fails at once can
			// arrive ahead of its answer.
			this.outcome = ''
			this.reason = ''
			this.built = false
			this.busy = true
			this.error = ''
			try {
				// a repository that tracks .env deploys its own
				await this.$api.gitApps.deploy(this.app.app, this.app.env_tracked ? {} : { env: this.env })
				this.step = 'deploy'
			} catch (error) {
				// nothing started, so what the last attempt said still stands
				this.outcome = before.outcome
				this.reason = before.reason
				this.fail(error)
			} finally {
				this.busy = false
			}
		},

		// Registered and never deployed, the app is deleted rather than left holding
		// its name and its clone.
		async cancel() {
			if (!this.app) {
				this.$emit('close')
				return
			}
			this.busy = true
			this.error = ''
			try {
				await this.$api.gitApps.remove(this.app.app)
				this.$emit('close')
			} catch (error) {
				this.fail(error)
			} finally {
				this.busy = false
			}
		},

		copyKey() {
			copy(this.app.public_key)
			this.$buefy.toast.open({ message: this.$t('Copied to clipboard'), type: 'is-success' })
		},

		fail(error) {
			const data = error && error.response && error.response.data
			this.error = (data && data.message) || (error && error.message) || String(error)
		},

		finish(outcome, reason) {
			this.outcome = outcome
			this.reason = reason || ''
			// the app is on the grid once its containers exist, whatever became of them
			this.$EventBus.$emit(events.RELOAD_APP_LIST)
		},

		isMine(res) {
			return Boolean(this.app) && res.Properties['app:name'] === this.app.app
		},
	},
	sockets: {
		'app:git-build-begin': function (res) {
			if (this.isMine(res)) {
				this.log = ''
				this.built = false
			}
		},
		'app:git-build-progress': function (res) {
			if (this.isMine(res))
				this.log = appendLog(this.log, res.Properties.message)
		},
		'app:git-build-end': function (res) {
			if (this.isMine(res))
				this.built = true
		},
		'app:git-build-error': function (res) {
			if (this.isMine(res))
				this.finish('build_failed', res.Properties.message)
		},
		'app:git-deploy-error': function (res) {
			if (this.isMine(res))
				this.finish('failed', res.Properties.message)
		},
		'app:git-deploy-end': function (res) {
			if (this.isMine(res))
				this.finish('deployed', '')
		},
	},
}
</script>

<style lang="scss" scoped>
.modal-card {
	width: 40rem;
}

.git-app__text {
	white-space: pre-wrap;
	word-break: break-all;
	font-size: 0.75rem;
}

.git-app__log {
	height: 20rem;
	overflow-y: auto;
}

.git-app__env :deep(.CodeMirror) {
	height: 12rem;
	border-radius: 0.5rem;
	font-size: 0.8125rem;
}
</style>
```

- [ ] **Step 4: Run the spec to see it pass**

Run: `npx vitest run src/components/Apps/GitAppModal.spec.js`

Expected: PASS, `✓ src/components/Apps/GitAppModal.spec.js  (12 tests)`.

- [ ] **Step 5: Add the dialog's words**

In `src/assets/lang/en_US.json`, replace the last entry:

```json
  "Delete {name}? What CasaOS cloned, built or started for it goes with it; the repository itself is not touched.": "Delete {name}? What CasaOS cloned, built or started for it goes with it; the repository itself is not touched."
```

with:

```json
  "Delete {name}? What CasaOS cloned, built or started for it goes with it; the repository itself is not touched.": "Delete {name}? What CasaOS cloned, built or started for it goes with it; the repository itself is not touched.",
  "An app from a git repository": "An app from a git repository",
  "Repository URL": "Repository URL",
  "Branch": "Branch",
  "Empty: the branch the repository names as its default.": "Empty: the branch the repository names as its default.",
  "Access": "Access",
  "Public repository, nothing needed": "Public repository, nothing needed",
  "Deploy key": "Deploy key",
  "Access token": "Access token",
  "Token": "Token",
  "Add this key to the repository as a read-only deploy key, then continue.": "Add this key to the repository as a read-only deploy key, then continue.",
  "Read from {files}.": "Read from {files}.",
  "built from the repository": "built from the repository",
  "Published ports: {ports}": "Published ports: {ports}",
  "Volumes: {volumes}": "Volumes: {volumes}",
  "These services reach beyond their own container. Deploy them only from a repository you trust.": "These services reach beyond their own container. Deploy them only from a repository you trust.",
  "The repository tracks its .env file, so it cannot be edited here: an edit would modify a tracked file and block every later deployment.": "The repository tracks its .env file, so it cannot be edited here: an edit would modify a tracked file and block every later deployment.",
  "Deploy": "Deploy",
  "Delete this app": "Delete this app",
  "Deploy again": "Deploy again",
  "The build failed: {reason}": "The build failed: {reason}",
  "The deployment failed: {reason}": "The deployment failed: {reason}",
  "Built. Starting the app…": "Built. Starting the app…",
  "Building…": "Building…",
  "CasaOS could not clone the repository.": "CasaOS could not clone the repository."
```

In `src/assets/lang/fr_FR.json`, replace the last entry:

```json
  "Delete {name}? What CasaOS cloned, built or started for it goes with it; the repository itself is not touched.": "Supprimer {name} ? Ce que CasaOS a cloné, construit ou démarré pour elle part avec elle ; le dépôt lui-même n'est pas touché."
```

with:

```json
  "Delete {name}? What CasaOS cloned, built or started for it goes with it; the repository itself is not touched.": "Supprimer {name} ? Ce que CasaOS a cloné, construit ou démarré pour elle part avec elle ; le dépôt lui-même n'est pas touché.",
  "An app from a git repository": "Une application depuis un dépôt git",
  "Repository URL": "URL du dépôt",
  "Branch": "Branche",
  "Empty: the branch the repository names as its default.": "Vide : la branche que le dépôt désigne par défaut.",
  "Access": "Accès",
  "Public repository, nothing needed": "Dépôt public, rien à fournir",
  "Deploy key": "Clé de déploiement",
  "Access token": "Jeton d'accès",
  "Token": "Jeton",
  "Add this key to the repository as a read-only deploy key, then continue.": "Ajoutez cette clé au dépôt comme clé de déploiement en lecture seule, puis continuez.",
  "Read from {files}.": "Lu depuis {files}.",
  "built from the repository": "construit depuis le dépôt",
  "Published ports: {ports}": "Ports publiés : {ports}",
  "Volumes: {volumes}": "Volumes : {volumes}",
  "These services reach beyond their own container. Deploy them only from a repository you trust.": "Ces services vont au-delà de leur propre conteneur. Ne les déployez que depuis un dépôt de confiance.",
  "The repository tracks its .env file, so it cannot be edited here: an edit would modify a tracked file and block every later deployment.": "Le dépôt suit son fichier .env, il ne peut donc pas être modifié ici : une modification toucherait un fichier suivi et bloquerait tous les déploiements suivants.",
  "Deploy": "Déployer",
  "Delete this app": "Supprimer cette application",
  "Deploy again": "Redéployer",
  "The build failed: {reason}": "La construction a échoué : {reason}",
  "The deployment failed: {reason}": "Le déploiement a échoué : {reason}",
  "Built. Starting the app…": "Construction terminée. Démarrage de l'application…",
  "Building…": "Construction en cours…",
  "CasaOS could not clone the repository.": "CasaOS n'a pas pu cloner le dépôt."
```

- [ ] **Step 6: Check both files still parse**

Run:

```bash
node -e 'for (const f of ["en_US", "fr_FR"]) JSON.parse(require("node:fs").readFileSync("src/assets/lang/" + f + ".json", "utf8")); console.log("both parse")'
```

Expected: `both parse`.

- [ ] **Step 7: Commit**

```bash
git add src/components/Apps/GitAppModal.vue src/components/Apps/GitAppModal.spec.js src/assets/lang/en_US.json src/assets/lang/fr_FR.json
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): create an app from a git repository"
```

---

### Task 5: The Repository tab, `GitRepoTab.vue`

**Files:**
- Create: `src/components/Apps/GitRepoTab.vue` (a `modal-card-body` section like `src/components/Apps/ContainersTab.vue:1-15`; the copy-and-toast of `src/components/account/TwoFactorPanel.vue:59` and `:159-165`)
- Create: `src/components/Apps/GitRepoTab.spec.js` (the mount pattern of `src/components/Apps/ContainersTab.spec.js:42-60`)
- Modify: `src/assets/lang/en_US.json` and `src/assets/lang/fr_FR.json` (append after Task 4's last entry)

**Interfaces:**
- Consumes: `this.$api.gitApps.get`, `.update`, `.check`, `.deploy` (Task 1); `appendLog`, `canFollow`, `canRevert`, `checkEnded`, `checkSummary`, `gitBadge`, `outcomeTag`, `shortCommit` (Task 2); `this.$buefy.dialog.confirm`, `this.$buefy.toast`; the `app:git-*` events through `sockets:`.
- Produces: component `GitRepoTab`, props `appId: String` (required) and `gitApp: Object` (required, the GitApp), emits `change(gitApp)` with every GitApp the server answers. The parent owns the GitApp and passes the new one back down (Task 6).

How it behaves, which the spec pins down:
- Shows the remote, the branch, the deployed commit (short hash, subject, date), the commit in the folder with a warning when tracked files were modified, the last check (`checkSummary`) and a state other than idle.
- Access: mode, token field (write-only; the placeholder says when one is saved; `token` is sent only when typed), Save (`update`), Test access, and the public key with Copy in key mode.
- The automatic rebuild switch (`update({ auto_deploy })`), put back when the server refuses, with its warning always shown.
- Check now and Test access run `check`: the 202's app goes up at once, then `checkEnded` reads `get` until the check has ended and that app goes up; the button spins meanwhile. Fetch and rebuild (`deploy` with no body) is offered to an adoptable app too: deploying adopts it (clarification 1).
- The last build log: `build_log`, then the live lines of this app's build once `app:git-build-begin` arrives. Each `app:git-*` event of this app but progress reads the app again (`get`).
- The last deployments with their outcome and reason, and "Revert to this version", confirmed first, for a kept version other than the deployed one (`deploy({ commit })`).
- Every action waits while `operation` is not null; an adoptable folder with an empty branch or remote allows none (clarification 2). A failed rollback (`blocked`) is said before a paused rebuild (`auto_paused`).

- [ ] **Step 1: Write the failing spec**

Create `src/components/Apps/GitRepoTab.spec.js`:

```js
// @vitest-environment happy-dom
import Buefy from 'buefy'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import GitRepoTab from './GitRepoTab.vue'
import i18n from '@/plugins/i18n'

// require.context has no Vite equivalent; an empty table makes $t return its key.
vi.mock('@/assets/lang', () => ({ default: { en_us: {} } }))

const A = 'a'.repeat(40)
const B = 'b'.repeat(40)
const C = 'c'.repeat(40)
const AT = '2026-09-16T10:00:00Z'

function gitApp(fields = {}) {
	return {
		app: 'jarvis',
		origin: 'created',
		dir: '/DATA/AppData/jarvis',
		remote: 'https://example.com/owner/jarvis.git',
		branch: 'main',
		access: 'none',
		token_set: false,
		auto_deploy: false,
		auto_paused: false,
		blocked: false,
		env_tracked: false,
		cloned: true,
		head: { commit: A, subject: 'feat: voice wake word', tracked_files_clean: true },
		deployed: { commit: A, subject: 'feat: voice wake word', at: AT },
		check: { at: AT, remote_commit: A, error: '' },
		new_commits: false,
		state: 'idle',
		operation: null,
		history: [{ commit: A, subject: 'feat: voice wake word', at: AT, outcome: 'deployed', reason: '', revertable: true }],
		compose: null,
		env_template: null,
		compose_example: null,
		build_log: 'old build\n',
		...fields,
	}
}

function setup(fields = {}, api = {}) {
	const answer = gitApp(fields)
	const gitApps = {
		get: vi.fn().mockResolvedValue({ data: { data: answer } }),
		update: vi.fn().mockResolvedValue({ data: { data: answer } }),
		check: vi.fn().mockResolvedValue({ data: { data: answer } }),
		deploy: vi.fn().mockResolvedValue({ data: { data: { ...answer, state: 'building' } } }),
		...api,
	}
	const confirm = vi.fn()
	const wrapper = mount(GitRepoTab, {
		props: { appId: 'jarvis', gitApp: answer },
		global: {
			plugins: [Buefy, i18n],
			mocks: { $api: { gitApps }, $buefy: { dialog: { confirm }, toast: { open: vi.fn() } } },
		},
	})
	const button = label => wrapper.findAll('button').find(b => b.text() === label)
	const click = async (label) => {
		await button(label).trigger('click')
		await flushPromises()
	}
	const fire = (event, Properties) => wrapper.vm.$options.sockets[event].call(wrapper.vm, { Properties })
	return { wrapper, gitApps, confirm, button, click, fire }
}

describe('gitRepoTab', () => {
	it('shows the remote, the branch, the deployed commit and the last check', () => {
		const { wrapper } = setup()
		const text = wrapper.text()
		expect(text).toContain('https://example.com/owner/jarvis.git')
		expect(text).toContain('main')
		expect(text).toContain('aaaaaaa')
		expect(text).toContain('feat: voice wake word')
		expect(text).toContain('The branch is at {commit}: up to date.')
		expect(text).not.toContain('Tracked files were modified')
		wrapper.unmount()
	})

	it('says when tracked files were modified in the folder', () => {
		const { wrapper } = setup({ head: { commit: A, subject: 'x', tracked_files_clean: false } })
		expect(wrapper.text()).toContain('Tracked files were modified in the folder.')
		wrapper.unmount()
	})

	it('saves the access mode, with a token only when one is typed', async () => {
		const { wrapper, gitApps, click } = setup({ token_set: true })
		await wrapper.find('select').setValue('token')
		await wrapper.find('input[type="password"]').setValue('s3cret')
		await click('Save')
		expect(gitApps.update).toHaveBeenLastCalledWith('jarvis', { access: 'token', token: 's3cret' })
		expect(wrapper.emitted('change')).toHaveLength(1)
		expect(wrapper.find('input[type="password"]').element.value).toBe('')

		await click('Save')
		expect(gitApps.update).toHaveBeenLastCalledWith('jarvis', { access: 'token' })
		wrapper.unmount()
	})

	it('turns automatic rebuild on, and puts the switch back when the server refuses', async () => {
		const update = vi.fn().mockRejectedValue({ response: { status: 409, data: { message: 'deploy is running' } } })
		const { wrapper } = setup({}, { update })
		await wrapper.find('input[type="checkbox"]').setValue(true)
		await flushPromises()

		expect(update).toHaveBeenCalledWith('jarvis', { auto_deploy: true })
		expect(wrapper.vm.autoDeploy).toBe(false)
		expect(wrapper.text()).toContain('deploy is running')
		expect(wrapper.text()).toContain('It runs whatever is pushed to the branch.')
		wrapper.unmount()
	})

	describe('while a check runs', () => {
		afterEach(() => vi.useRealTimers())

		it('hands up the check as it starts, and the app once the check has ended', async () => {
			vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
			const checking = gitApp({ operation: { kind: 'check', commit: '', started_at: AT } })
			const checked = gitApp({ check: { at: AT, remote_commit: B, error: '' }, new_commits: true })
			const get = vi.fn().mockResolvedValue({ data: { data: checked } })
			const { wrapper, button } = setup({}, { check: vi.fn().mockResolvedValue({ data: { data: checking } }), get })

			await button('Test access').trigger('click')
			await flushPromises()
			expect(wrapper.emitted('change')).toEqual([[checking]])
			expect(button('Test access').classes()).toContain('is-loading')

			await vi.advanceTimersByTimeAsync(2000)
			await flushPromises()
			expect(get).toHaveBeenCalledWith('jarvis')
			expect(wrapper.emitted('change').at(-1)).toEqual([checked])
			expect(button('Test access').classes()).not.toContain('is-loading')
			wrapper.unmount()
		})
	})

	it('checks, and deploys the latest commit of the branch', async () => {
		const { wrapper, gitApps, click } = setup({ new_commits: true, check: { at: AT, remote_commit: B, error: '' } })
		expect(wrapper.text()).toContain('Commit {commit} is new on the branch.')

		await click('Check now')
		expect(gitApps.check).toHaveBeenCalledWith('jarvis')

		await click('Fetch and rebuild')
		expect(gitApps.deploy).toHaveBeenCalledWith('jarvis')
		expect(wrapper.emitted('change').at(-1)[0].state).toBe('building')
		wrapper.unmount()
	})

	it('offers a revert to a kept version other than the deployed one, once confirmed', async () => {
		const history = [
			{ commit: A, subject: 'now', at: AT, outcome: 'deployed', reason: '', revertable: true },
			{ commit: B, subject: 'before', at: AT, outcome: 'deployed', reason: '', revertable: true },
			{ commit: C, subject: 'long ago', at: AT, outcome: 'rolled_back', reason: 'web exited with code 1', revertable: false },
		]
		const { wrapper, gitApps, confirm } = setup({ history })
		const reverts = wrapper.findAll('.git-repo-tab__deployment').map(row => row.text().includes('Revert to this version'))
		expect(reverts).toEqual([false, true, false])
		expect(wrapper.text()).toContain('web exited with code 1')

		await wrapper.findAll('button').find(b => b.text() === 'Revert to this version').trigger('click')
		expect(gitApps.deploy).not.toHaveBeenCalled()
		confirm.mock.calls[0][0].onConfirm()
		await flushPromises()
		expect(gitApps.deploy).toHaveBeenCalledWith('jarvis', { commit: B })
		wrapper.unmount()
	})

	it('follows a build of its own app live, and reads the app again as it moves on', async () => {
		const { wrapper, gitApps, fire } = setup()
		expect(wrapper.find('pre').text()).toBe('old build')

		fire('app:git-build-begin', { 'app:name': 'jarvis' })
		fire('app:git-build-progress', { 'app:name': 'jarvis', 'message': '#1 [web] FROM node:20' })
		fire('app:git-build-progress', { 'app:name': 'other', 'message': 'not ours' })
		await flushPromises()
		expect(wrapper.find('pre').text()).toBe('#1 [web] FROM node:20')

		fire('app:git-deploy-end', { 'app:name': 'other' })
		fire('app:git-deploy-end', { 'app:name': 'jarvis' })
		await flushPromises()
		// once for the begin, once for the end of its own deployment
		expect(gitApps.get).toHaveBeenCalledTimes(2)
		wrapper.unmount()
	})

	it('waits while an operation runs', () => {
		const { wrapper, button } = setup({ operation: { kind: 'build', commit: B, started_at: AT }, state: 'building' })
		expect(button('Check now').attributes('disabled')).toBeDefined()
		expect(wrapper.text()).toContain('Building')
		wrapper.unmount()
	})

	it('changes nothing for an adoptable folder until the owner acts, and cannot adopt one with no remote', async () => {
		const ready = setup({ origin: 'adoptable', deployed: null, history: [] })
		expect(ready.wrapper.text()).toContain('Nothing changes until you act here')
		expect(ready.button('Check now').attributes('disabled')).toBeUndefined()
		// deploying adopts it too
		await ready.click('Fetch and rebuild')
		expect(ready.gitApps.deploy).toHaveBeenCalledWith('jarvis')
		ready.wrapper.unmount()

		const detached = setup({ origin: 'adoptable', remote: '', branch: '', deployed: null, history: [] })
		expect(detached.wrapper.text()).toContain('not on a branch with a remote')
		expect(detached.button('Check now').attributes('disabled')).toBeDefined()
		detached.wrapper.unmount()
	})

	it('shows a stopped automatic rebuild before a paused one', () => {
		const { wrapper } = setup({ blocked: true, auto_paused: true })
		expect(wrapper.text()).toContain('A rollback failed')
		expect(wrapper.text()).not.toContain('Automatic rebuild is paused')
		wrapper.unmount()
	})
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `npx vitest run src/components/Apps/GitRepoTab.spec.js`

Expected: FAIL, `Error: Failed to resolve import "./GitRepoTab.vue" from "src/components/Apps/GitRepoTab.spec.js". Does the file exist?`

- [ ] **Step 3: Write the tab**

Create `src/components/Apps/GitRepoTab.vue`:

```vue
<template>
	<section class="modal-card-body git-repo-tab">
		<b-message v-if="error" size="is-small" type="is-danger">{{ error }}</b-message>

		<b-message v-if="gitApp.origin === 'adoptable'" size="is-small" type="is-info">
			{{ followable
				? $t('This app runs from a git work tree. Nothing changes until you act here: the first action makes CasaOS follow its repository.')
				: $t('This folder is not on a branch with a remote, so CasaOS cannot follow its repository yet.') }}
		</b-message>
		<b-message v-if="gitApp.blocked" size="is-small" type="is-danger">
			{{ $t('A rollback failed, so automatic rebuilds are stopped. Deploy or revert by hand to resume them.') }}
		</b-message>
		<b-message v-else-if="gitApp.auto_paused" size="is-small" type="is-warning">
			{{ $t('Automatic rebuild is paused since a revert. Turn it on again, or deploy by hand, to resume it.') }}
		</b-message>

		<table class="table is-narrow is-fullwidth is-size-7">
			<tbody>
				<tr>
					<th>{{ $t('Remote') }}</th>
					<td class="git-repo-tab__mono">{{ gitApp.remote || '-' }}</td>
				</tr>
				<tr>
					<th>{{ $t('Branch') }}</th>
					<td>{{ gitApp.branch || '-' }}</td>
				</tr>
				<tr>
					<th>{{ $t('Deployed') }}</th>
					<td>
						<template v-if="gitApp.deployed">
							<span class="git-repo-tab__mono">{{ short(gitApp.deployed.commit) }}</span>
							{{ gitApp.deployed.subject }} · {{ when(gitApp.deployed.at) }}
						</template>
						<span v-else class="has-text-full-03">{{ $t('Not deployed yet.') }}</span>
					</td>
				</tr>
				<tr v-if="gitApp.head">
					<th>{{ $t('In the folder') }}</th>
					<td>
						<span class="git-repo-tab__mono">{{ short(gitApp.head.commit) }}</span>
						{{ gitApp.head.subject }}
						<p v-if="!gitApp.head.tracked_files_clean" class="has-text-danger">
							{{ $t('Tracked files were modified in the folder. Every deployment is refused until they are restored.') }}
						</p>
					</td>
				</tr>
				<tr>
					<th>{{ $t('Last check') }}</th>
					<td>
						<span v-if="gitApp.check">{{ when(gitApp.check.at) }} · </span>{{ $t(checkLine.message, checkLine.params) }}
					</td>
				</tr>
				<tr v-if="stateTag">
					<th>{{ $t('State') }}</th>
					<td><b-tag :type="stateTag.type">{{ $t(stateTag.label) }}</b-tag></td>
				</tr>
			</tbody>
		</table>

		<p class="has-text-weight-bold is-size-7 mb-2">{{ $t('Access') }}</p>
		<div class="is-flex is-align-items-center is-flex-wrap-wrap">
			<b-select v-model="access" :disabled="!canAct" class="mr-2 mb-2" size="is-small">
				<option value="none">{{ $t('Public repository, nothing needed') }}</option>
				<option value="key">{{ $t('Deploy key') }}</option>
				<option value="token">{{ $t('Access token') }}</option>
			</b-select>
			<!-- write-only: a saved token never comes back -->
			<b-input v-if="access === 'token'" v-model="token" :disabled="!canAct"
				:placeholder="gitApp.token_set ? $t('A token is saved. Type one to replace it.') : $t('Token')"
				autocomplete="off" class="mr-2 mb-2" password-reveal size="is-small" type="password"></b-input>
			<b-button :disabled="!canAct" :loading="busy === 'access'" class="mr-2 mb-2" rounded size="is-small" @click="saveAccess">
				{{ $t('Save') }}
			</b-button>
			<b-button :disabled="!canAct" :loading="busy === 'test'" class="mb-2" rounded size="is-small" @click="check('test')">
				{{ $t('Test access') }}
			</b-button>
		</div>
		<div v-if="gitApp.access === 'key' && gitApp.public_key" class="is-flex is-align-items-flex-start mb-2">
			<pre class="git-repo-tab__text is-flex-grow-1 mr-2">{{ gitApp.public_key }}</pre>
			<b-button :label="$t('Copy')" rounded size="is-small" @click="copyKey"></b-button>
		</div>

		<p class="has-text-weight-bold is-size-7 mt-3 mb-2">{{ $t('Automatic rebuild') }}</p>
		<b-switch :disabled="!canAct" :model-value="autoDeploy" size="is-small" @update:model-value="setAutoDeploy">
			{{ $t('Deploy a new commit as soon as a check finds it') }}
		</b-switch>
		<p class="is-size-7 has-text-danger mt-1">{{ $t('It runs whatever is pushed to the branch.') }}</p>

		<div class="is-flex mt-4">
			<b-button :disabled="!canAct" :loading="busy === 'check'" class="mr-2" rounded size="is-small" @click="check('check')">
				{{ $t('Check now') }}
			</b-button>
			<b-button :disabled="!canAct" :loading="busy === 'deploy'" rounded size="is-small" type="is-primary" @click="deploy">
				{{ $t('Fetch and rebuild') }}
			</b-button>
		</div>

		<p class="has-text-weight-bold is-size-7 mt-4 mb-2">{{ $t('Last build') }}</p>
		<pre ref="log" class="git-repo-tab__text git-repo-tab__log">{{ log || $t('No build yet.') }}</pre>

		<p class="has-text-weight-bold is-size-7 mt-4 mb-2">{{ $t('Last deployments') }}</p>
		<p v-if="!history.length" class="is-size-7 has-text-full-03">{{ $t('No deployment yet.') }}</p>
		<table v-else class="table is-narrow is-fullwidth is-size-7">
			<tbody>
				<tr v-for="entry in history" :key="`${entry.commit}-${entry.at}`" class="git-repo-tab__deployment">
					<td>
						<span class="git-repo-tab__mono">{{ short(entry.commit) }}</span> {{ entry.subject }}
						<p class="has-text-full-03">{{ when(entry.at) }}</p>
					</td>
					<td>
						<b-tag :type="outcomeTag(entry.outcome).type">{{ $t(outcomeTag(entry.outcome).label) }}</b-tag>
						<p v-if="entry.reason" class="has-text-full-03">{{ entry.reason }}</p>
					</td>
					<td class="has-text-right">
						<b-button v-if="canRevert(gitApp, entry)" :disabled="!canAct" :loading="busy === entry.commit" rounded
							size="is-small" @click="confirmRevert(entry)">
							{{ $t('Revert to this version') }}
						</b-button>
					</td>
				</tr>
			</tbody>
		</table>
	</section>
</template>

<script>
import copy from 'clipboard-copy'
import { appendLog, canFollow, canRevert, checkEnded, checkSummary, gitBadge, outcomeTag, shortCommit } from './gitApps'

const appOf = res => res.data.data

// A build or a deployment of this app moved on: read the app again.
function reloadMine(res) {
	if (this.isMine(res))
		this.reload()
}

// The Repository tab: a git app, or a compose app whose folder is a git work
// tree. The panel owns the app; every answer carries the whole of it, and goes
// back up as `change`.
export default {
	name: 'GitRepoTab',
	props: {
		appId: { type: String, required: true },
		gitApp: { type: Object, required: true },
	},
	emits: ['change'],
	data() {
		return {
			access: this.gitApp.access,
			token: '',
			autoDeploy: this.gitApp.auto_deploy,
			// '' or what runs: access, auto, test, check, deploy, or the commit of a revert
			busy: '',
			error: '',
			// null until a build is followed live, then the log as its events bring it
			liveLog: null,
			// set as the panel closes, so a check it waits for stops being read
			closed: false,
		}
	},
	computed: {
		followable() {
			return canFollow(this.gitApp)
		},
		// nothing is sent while something runs: the server would answer 409
		canAct() {
			return !this.busy && !this.gitApp.operation && this.followable
		},
		checkLine() {
			return checkSummary(this.gitApp)
		},
		stateTag() {
			return gitBadge({ state: this.gitApp.state })
		},
		history() {
			return this.gitApp.history || []
		},
		log() {
			return this.liveLog === null ? (this.gitApp.build_log || '') : this.liveLog
		},
	},
	watch: {
		'gitApp.access': function (value) {
			this.access = value
		},
		'gitApp.auto_deploy': function (value) {
			this.autoDeploy = value
		},
		log() {
			this.$nextTick(() => {
				if (this.$refs.log)
					this.$refs.log.scrollTop = this.$refs.log.scrollHeight
			})
		},
	},
	beforeUnmount() {
		this.closed = true
	},
	methods: {
		canRevert,
		outcomeTag,
		short: shortCommit,

		when(at) {
			return new Date(at).toLocaleString()
		},

		messageOf(error) {
			const data = error && error.response && error.response.data
			return (data && data.message) || (error && error.message) || String(error)
		},

		// One action at a time; `request` resolves to the app the server answered.
		// True when the server took the action.
		async run(kind, request) {
			this.busy = kind
			this.error = ''
			try {
				this.$emit('change', await request())
				return true
			} catch (error) {
				this.error = this.messageOf(error)
				return false
			} finally {
				this.busy = ''
			}
		},

		// "Check now" and "Test access" run the same check; `kind` spins the right
		// button. The check answers as it starts, and the button spins until it ends.
		check(kind) {
			return this.run(kind, async () => {
				const started = appOf(await this.$api.gitApps.check(this.appId))
				this.$emit('change', started)
				return checkEnded(started, () => this.$api.gitApps.get(this.appId).then(appOf), { stop: () => this.closed })
			})
		},

		deploy() {
			return this.run('deploy', () => this.$api.gitApps.deploy(this.appId).then(appOf))
		},

		async saveAccess() {
			// an empty token field keeps the token that is saved
			const body = this.access === 'token' && this.token ? { access: this.access, token: this.token } : { access: this.access }
			if (await this.run('access', () => this.$api.gitApps.update(this.appId, body).then(appOf)))
				this.token = ''
		},

		async setAutoDeploy(value) {
			this.autoDeploy = value
			if (!await this.run('auto', () => this.$api.gitApps.update(this.appId, { auto_deploy: value }).then(appOf)))
				this.autoDeploy = this.gitApp.auto_deploy
		},

		confirmRevert(entry) {
			this.$buefy.dialog.confirm({
				title: this.$t('Revert to this version'),
				message: this.$t('The app goes back to {commit}, and automatic rebuild is paused until you turn it on again or deploy by hand.', { commit: shortCommit(entry.commit) }),
				confirmText: this.$t('Revert to this version'),
				cancelText: this.$t('Cancel'),
				type: 'is-warning',
				onConfirm: () => this.run(entry.commit, () => this.$api.gitApps.deploy(this.appId, { commit: entry.commit }).then(appOf)),
			})
		},

		copyKey() {
			copy(this.gitApp.public_key)
			this.$buefy.toast.open({ message: this.$t('Copied to clipboard'), type: 'is-success' })
		},

		async reload() {
			try {
				this.$emit('change', appOf(await this.$api.gitApps.get(this.appId)))
			} catch (error) {
				this.error = this.messageOf(error)
			}
		},

		isMine(res) {
			return res.Properties['app:name'] === this.appId
		},
	},
	sockets: {
		'app:git-build-begin': function (res) {
			if (!this.isMine(res))
				return
			this.liveLog = ''
			this.reload()
		},
		'app:git-build-progress': function (res) {
			if (this.isMine(res))
				this.liveLog = appendLog(this.log, res.Properties.message)
		},
		'app:git-build-end': reloadMine,
		'app:git-build-error': reloadMine,
		'app:git-deploy-end': reloadMine,
		'app:git-deploy-error': reloadMine,
	},
}
</script>

<style lang="scss" scoped>
.git-repo-tab__mono {
	font-family: monospace;
	word-break: break-all;
}

.git-repo-tab__text {
	white-space: pre-wrap;
	word-break: break-all;
	font-size: 0.75rem;
}

.git-repo-tab__log {
	max-height: 16rem;
	overflow-y: auto;
}
</style>
```

- [ ] **Step 4: Run the spec to see it pass**

Run: `npx vitest run src/components/Apps/GitRepoTab.spec.js`

Expected: PASS, `✓ src/components/Apps/GitRepoTab.spec.js  (11 tests)`.

- [ ] **Step 5: Add the tab's words**

In `src/assets/lang/en_US.json`, replace the last entry:

```json
  "CasaOS could not clone the repository.": "CasaOS could not clone the repository."
```

with:

```json
  "CasaOS could not clone the repository.": "CasaOS could not clone the repository.",
  "This app runs from a git work tree. Nothing changes until you act here: the first action makes CasaOS follow its repository.": "This app runs from a git work tree. Nothing changes until you act here: the first action makes CasaOS follow its repository.",
  "This folder is not on a branch with a remote, so CasaOS cannot follow its repository yet.": "This folder is not on a branch with a remote, so CasaOS cannot follow its repository yet.",
  "A rollback failed, so automatic rebuilds are stopped. Deploy or revert by hand to resume them.": "A rollback failed, so automatic rebuilds are stopped. Deploy or revert by hand to resume them.",
  "Automatic rebuild is paused since a revert. Turn it on again, or deploy by hand, to resume it.": "Automatic rebuild is paused since a revert. Turn it on again, or deploy by hand, to resume it.",
  "Remote": "Remote",
  "Not deployed yet.": "Not deployed yet.",
  "In the folder": "In the folder",
  "Tracked files were modified in the folder. Every deployment is refused until they are restored.": "Tracked files were modified in the folder. Every deployment is refused until they are restored.",
  "Last check": "Last check",
  "A token is saved. Type one to replace it.": "A token is saved. Type one to replace it.",
  "Test access": "Test access",
  "Automatic rebuild": "Automatic rebuild",
  "Deploy a new commit as soon as a check finds it": "Deploy a new commit as soon as a check finds it",
  "It runs whatever is pushed to the branch.": "It runs whatever is pushed to the branch.",
  "Check now": "Check now",
  "Fetch and rebuild": "Fetch and rebuild",
  "Last build": "Last build",
  "No build yet.": "No build yet.",
  "Last deployments": "Last deployments",
  "No deployment yet.": "No deployment yet.",
  "Revert to this version": "Revert to this version",
  "The app goes back to {commit}, and automatic rebuild is paused until you turn it on again or deploy by hand.": "The app goes back to {commit}, and automatic rebuild is paused until you turn it on again or deploy by hand."
```

In `src/assets/lang/fr_FR.json`, replace the last entry:

```json
  "CasaOS could not clone the repository.": "CasaOS n'a pas pu cloner le dépôt."
```

with:

```json
  "CasaOS could not clone the repository.": "CasaOS n'a pas pu cloner le dépôt.",
  "This app runs from a git work tree. Nothing changes until you act here: the first action makes CasaOS follow its repository.": "Cette application tourne depuis un arbre de travail git. Rien ne change tant que vous n'agissez pas ici : la première action fait suivre son dépôt par CasaOS.",
  "This folder is not on a branch with a remote, so CasaOS cannot follow its repository yet.": "Ce dossier n'est pas sur une branche avec un dépôt distant, CasaOS ne peut donc pas encore suivre son dépôt.",
  "A rollback failed, so automatic rebuilds are stopped. Deploy or revert by hand to resume them.": "Un retour arrière a échoué, les reconstructions automatiques sont donc arrêtées. Déployez ou revenez à une version à la main pour les reprendre.",
  "Automatic rebuild is paused since a revert. Turn it on again, or deploy by hand, to resume it.": "La reconstruction automatique est en pause depuis un retour à une version. Réactivez-la, ou déployez à la main, pour la reprendre.",
  "Remote": "Dépôt distant",
  "Not deployed yet.": "Pas encore déployé.",
  "In the folder": "Dans le dossier",
  "Tracked files were modified in the folder. Every deployment is refused until they are restored.": "Des fichiers suivis ont été modifiés dans le dossier. Tout déploiement est refusé tant qu'ils ne sont pas restaurés.",
  "Last check": "Dernière vérification",
  "A token is saved. Type one to replace it.": "Un jeton est enregistré. Saisissez-en un pour le remplacer.",
  "Test access": "Tester l'accès",
  "Automatic rebuild": "Reconstruction automatique",
  "Deploy a new commit as soon as a check finds it": "Déployer un nouveau commit dès qu'une vérification le trouve",
  "It runs whatever is pushed to the branch.": "Elle exécute tout ce qui est poussé sur la branche.",
  "Check now": "Vérifier maintenant",
  "Fetch and rebuild": "Récupérer et reconstruire",
  "Last build": "Dernière construction",
  "No build yet.": "Aucune construction pour l'instant.",
  "Last deployments": "Derniers déploiements",
  "No deployment yet.": "Aucun déploiement pour l'instant.",
  "Revert to this version": "Revenir à cette version",
  "The app goes back to {commit}, and automatic rebuild is paused until you turn it on again or deploy by hand.": "L'application revient à {commit}, et la reconstruction automatique est mise en pause jusqu'à ce que vous la réactiviez ou déployiez à la main."
```

- [ ] **Step 6: Check both files still parse**

Run:

```bash
node -e 'for (const f of ["en_US", "fr_FR"]) JSON.parse(require("node:fs").readFileSync("src/assets/lang/" + f + ".json", "utf8")); console.log("both parse")'
```

Expected: `both parse`.

- [ ] **Step 7: Commit**

```bash
git add src/components/Apps/GitRepoTab.vue src/components/Apps/GitRepoTab.spec.js src/assets/lang/en_US.json src/assets/lang/fr_FR.json
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): the Repository tab"
```

---

### Task 6: The app panel: Repository tab, notices, read-only `.env`, the tab alone

**Files:**
- Modify: `src/components/Apps/EnvEditor.vue:2-14` (intro), `:53-55` (props), `:66-68` (CodeMirror options), `:90` (`canApply`)
- Modify: `src/components/Apps/EnvEditor.spec.js:11-14` (`setup` takes props) and `:76` (a new test)
- Modify: `src/components/Apps/AppPanel.vue:414` (tab bar), `:436-442` (tab buttons), `:448` (Compose editor), `:456-459` (`.env` editor), `:471-472` (Settings form), `:578` (Save), `:584` (Apply), `:637` (imports), `:685` (components), `:710-714` (props), `:745-747` (data), `:858-860` (`showExportButton`), `:910-913` (computed), `:992-993` (`created`), `:1738-1740` (methods)
- Create: `src/components/Apps/AppPanel.spec.js`
- Modify: `src/assets/lang/en_US.json` and `src/assets/lang/fr_FR.json` (append after Task 5's last entry)

**Interfaces:**
- Consumes: `GitRepoTab` (Task 5: props `appId`, `gitApp`; event `change`); `this.$api.gitApps.get` (Task 1). `AppSection.showConfigPanel` (`AppSection.vue:454-501`) opens the panel with `state: 'update'` and `isCasa` from `AppCard.configApp()` (`AppCard.vue:667-671`), true for a v2 app; the tab bar only shows then (`AppPanel.vue:414`).
- Produces:
  - `EnvEditor` prop `readonly: Boolean` (default false): CodeMirror `readOnly`, no intro, `canApply` always false.
  - `AppPanel` prop `repositoryOnly: Boolean` (default false): no tab bar, no export button, `editorTab` starts at `'repository'`, the title names the app from `id`. Task 7 passes it, with `settingComposeData: ''`, for a git app with no container.
  - `AppPanel` data `gitApp` (the GitApp of a git or adoptable app, or null); computed `envTracked` (a GitApp with `env_tracked`), `gitNotice` (a GitApp, on the Settings or Compose tab, or on the `.env` tab when `envTracked`); method `loadGitApp()`; `editorTab` value `'repository'`; the notice's wrapper class `git-notice`.

- [ ] **Step 1: Write the failing `EnvEditor` test**

In `src/components/Apps/EnvEditor.spec.js`, let `setup` take props:

Replace:

```js
function setup(value = '') {
	const applyComposeEnv = vi.fn().mockResolvedValue({ data: { message: 'ok' } })
	const wrapper = mount(EnvEditor, {
		props: { appId: 'jellyfin', value },
```

with:

```js
function setup(value = '', props = {}) {
	const applyComposeEnv = vi.fn().mockResolvedValue({ data: { message: 'ok' } })
	const wrapper = mount(EnvEditor, {
		props: { appId: 'jellyfin', value, ...props },
```

and add the test:

Replace:

```js
	it('accepts an empty draft, which deletes the file', async () => {
```

with:

```js
	it('stays read-only and never applies when the repository tracks the file', async () => {
		const { wrapper, applyComposeEnv, type, lastState } = setup('A=1\n', { readonly: true })
		expect(wrapper.findComponent({ name: 'CodeMirrorEditor' }).vm.codemirror.getOption('readOnly')).toBe(true)
		expect(wrapper.text()).not.toContain('Applying an empty file deletes it.')

		type('A=2\n')
		await wrapper.vm.$nextTick()
		expect(lastState().canApply).toBe(false)
		await wrapper.vm.apply()
		expect(applyComposeEnv).not.toHaveBeenCalled()
		wrapper.unmount()
	})

	it('accepts an empty draft, which deletes the file', async () => {
```

- [ ] **Step 2: Run it to see it fail**

Run: `npx vitest run src/components/Apps/EnvEditor.spec.js`

Expected: FAIL, `Tests  1 failed | 4 passed (5)`, with `AssertionError: expected false to be true // Object.is equality`.

- [ ] **Step 3: Give `EnvEditor` its read-only mode**

In `src/components/Apps/EnvEditor.vue`, the intro:

Replace:

```vue
	<section class="modal-card-body env-editor">
		<p class="has-text-full-03 is-size-7 mb-2">
			{{ $t('Edit the .env file of this app. Applying an empty file deletes it.') }}
		</p>

		<b-message class="mb-3" size="is-small" type="is-info">
			<ul>
				<li>{{ $t('One KEY=VALUE per line; a line starting with # is a comment.') }}</li>
				<li>{{ $t('Quote values as in a Docker Compose .env file.') }}</li>
				<li>{{ $t('Reference a key from the Compose file as {ref}.', { ref: '${KEY}' }) }}</li>
				<li>{{ $t('Keys set by CasaOS (TZ, PUID, PGID) cannot be overridden here.') }}</li>
			</ul>
		</b-message>
```

with:

```vue
	<section class="modal-card-body env-editor">
		<!-- read-only, the panel above says why and there is nothing to explain -->
		<template v-if="!readonly">
			<p class="has-text-full-03 is-size-7 mb-2">
				{{ $t('Edit the .env file of this app. Applying an empty file deletes it.') }}
			</p>

			<b-message class="mb-3" size="is-small" type="is-info">
				<ul>
					<li>{{ $t('One KEY=VALUE per line; a line starting with # is a comment.') }}</li>
					<li>{{ $t('Quote values as in a Docker Compose .env file.') }}</li>
					<li>{{ $t('Reference a key from the Compose file as {ref}.', { ref: '${KEY}' }) }}</li>
					<li>{{ $t('Keys set by CasaOS (TZ, PUID, PGID) cannot be overridden here.') }}</li>
				</ul>
			</b-message>
		</template>
```

the prop:

Replace:

```js
		appId: { type: String, required: true },
		value: { type: String, default: '' },
	},
```

with:

```js
		appId: { type: String, required: true },
		value: { type: String, default: '' },
		// A git app whose repository tracks .env: an edit would modify a tracked file.
		readonly: { type: Boolean, default: false },
	},
```

the editor option:

Replace:

```js
				lineWrapping: true,
				styleActiveLine: true,
			},
```

with:

```js
				lineWrapping: true,
				styleActiveLine: true,
				readOnly: this.readonly,
			},
```

and `canApply`:

Replace:

```js
			return !this.localError && this.isDirty && !this.isApplying
```

with:

```js
			return !this.readonly && !this.localError && this.isDirty && !this.isApplying
```

- [ ] **Step 4: Run it to see it pass**

Run: `npx vitest run src/components/Apps/EnvEditor.spec.js`

Expected: PASS, `✓ src/components/Apps/EnvEditor.spec.js  (5 tests)`.

- [ ] **Step 5: Write the failing `AppPanel` spec**

Create `src/components/Apps/AppPanel.spec.js`:

```js
// @vitest-environment happy-dom
import Buefy from 'buefy'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AppPanel from './AppPanel.vue'
import ComposeConfig from './ComposeConfig.vue'
import ComposeEditor from './ComposeEditor.vue'
import EnvEditor from './EnvEditor.vue'
import GitRepoTab from './GitRepoTab.vue'

// lottie-web paints into a canvas the moment it is imported and happy-dom has no
// 2d context, so the import itself throws.
vi.mock('lottie-web-vue', () => ({ default: { name: 'lottie-animation', template: '<div/>' } }))

const COMPOSE = 'name: jarvis\nservices:\n  web:\n    image: nginx\n'

// The settings panel of an installed app, as AppSection opens it. Every other
// request it makes on its way up never settles: they are not what is under test.
async function panel(answer, props = {}) {
	const pending = () => new Promise(() => {})
	const get = answer instanceof Error ? vi.fn().mockRejectedValue(answer) : vi.fn().mockResolvedValue({ data: { data: answer } })
	const wrapper = shallowMount(AppPanel, {
		props: {
			id: 'jarvis',
			state: 'update',
			isCasa: true,
			runningStatus: 'running',
			configData: { memory: { total: 4 * 1024 * 1024 * 1024 }, networks: [] },
			settingComposeData: COMPOSE,
			...props,
		},
		global: {
			plugins: [Buefy],
			// the install progress is rendered through it, and nothing here installs
			directives: { 'dompurify-html': {} },
			mocks: {
				$t: key => key,
				$messageBus: () => {},
				$store: { state: { isMobile: false } },
				$api: { gitApps: { get }, sys: { hardwareInfo: pending }, container: { getComposeEnv: () => Promise.resolve({ data: 'A=1\n' }) } },
				$openAPI: { appManagement: { appStore: { composeAppStoreInfoList: pending } } },
			},
		},
	})
	await flushPromises()
	return { wrapper, get }
}

// v-show, read off the element: isVisible() needs a wrapper attached to a document
function shown(component) {
	return component.element.style.display !== 'none'
}

function gitApp(fields = {}) {
	return { app: 'jarvis', origin: 'created', remote: 'https://example.com/jarvis.git', branch: 'main', env_tracked: false, history: [], ...fields }
}

describe('appPanel of a git app', () => {
	it('asks once whether the app is a git app, and adds no tab when it is not', async () => {
		const { wrapper, get } = await panel(Object.assign(new Error('404'), { response: { status: 404 } }))
		expect(get).toHaveBeenCalledWith('jarvis')
		expect(wrapper.vm.gitApp).toBeNull()
		expect(wrapper.find('.git-notice').exists()).toBe(false)
		expect(shown(wrapper.findComponent(ComposeConfig))).toBe(true)
		wrapper.unmount()
	})

	it('points Settings and Compose of a git app to its repository', async () => {
		const { wrapper } = await panel(gitApp())
		expect(wrapper.find('.git-notice').exists()).toBe(true)
		// kept mounted and hidden: it names the service the terminal opens on
		expect(wrapper.findComponent(ComposeConfig).exists()).toBe(true)
		expect(shown(wrapper.findComponent(ComposeConfig))).toBe(false)

		await wrapper.setData({ editorTab: 'compose' })
		expect(wrapper.find('.git-notice').exists()).toBe(true)
		expect(wrapper.findComponent(ComposeEditor).exists()).toBe(false)

		await wrapper.setData({ editorTab: 'repository' })
		expect(wrapper.findComponent(GitRepoTab).props('gitApp').app).toBe('jarvis')
		wrapper.unmount()
	})

	it('takes the app the Repository tab hands back', async () => {
		const { wrapper } = await panel(gitApp({ origin: 'adoptable' }))
		await wrapper.setData({ editorTab: 'repository' })
		wrapper.findComponent(GitRepoTab).vm.$emit('change', gitApp({ origin: 'adopted', env_tracked: true }))
		await flushPromises()
		expect(wrapper.findComponent(GitRepoTab).props('gitApp').origin).toBe('adopted')
		expect(wrapper.vm.envTracked).toBe(true)
		wrapper.unmount()
	})

	it('keeps .env editable unless the repository tracks it', async () => {
		const editable = await panel(gitApp())
		await editable.wrapper.setData({ editorTab: 'env', envLoaded: true })
		expect(editable.wrapper.find('.git-notice').exists()).toBe(false)
		expect(editable.wrapper.findComponent(EnvEditor).props('readonly')).toBe(false)
		editable.wrapper.unmount()

		const tracked = await panel(gitApp({ env_tracked: true }))
		await tracked.wrapper.setData({ editorTab: 'env', envLoaded: true })
		expect(tracked.wrapper.find('.git-notice').exists()).toBe(true)
		expect(tracked.wrapper.findComponent(EnvEditor).props('readonly')).toBe(true)
		tracked.wrapper.unmount()
	})

	it('points an adoptable app to its repository too', async () => {
		const { wrapper } = await panel(gitApp({ origin: 'adoptable', env_tracked: true }))
		expect(wrapper.find('.git-notice').exists()).toBe(true)
		expect(shown(wrapper.findComponent(ComposeConfig))).toBe(false)

		await wrapper.setData({ editorTab: 'env', envLoaded: true })
		expect(wrapper.find('.git-notice').exists()).toBe(true)
		expect(wrapper.findComponent(EnvEditor).props('readonly')).toBe(true)
		wrapper.unmount()
	})

	it('is the Repository tab alone for a git app with no container', async () => {
		const { wrapper, get } = await panel(gitApp({ state: 'build_failed' }), { settingComposeData: '', runningStatus: '', repositoryOnly: true })
		expect(get).toHaveBeenCalledWith('jarvis')
		expect(wrapper.find('.compose-mode-switch').exists()).toBe(false)
		expect(wrapper.find('.git-notice').exists()).toBe(false)
		expect(wrapper.findComponent(ComposeConfig).exists()).toBe(false)
		expect(wrapper.findComponent(GitRepoTab).exists()).toBe(true)
		expect(wrapper.vm.panelTitle).toBe('jarvis Setting')
		expect(wrapper.vm.showExportButton).toBe(false)
		wrapper.unmount()
	})
})
```

- [ ] **Step 6: Run it to see it fail**

Run: `npx vitest run src/components/Apps/AppPanel.spec.js`

Expected: FAIL, `Tests  6 failed (6)`, among them `expected "spy" to be called with arguments: [ 'jarvis' ]` and `Cannot call vm on an empty VueWrapper.`

- [ ] **Step 7: Add the tab, the notice, the read-only `.env` and the tab alone to the template**

In `src/components/Apps/AppPanel.vue`, the tab bar, which a git app with no container does without:

Replace:

```vue
				<div v-if="isCasa && state == 'update'" class="is-flex px-4 pt-3 compose-mode-switch">
```

with:

```vue
				<div v-if="isCasa && state == 'update' && !repositoryOnly" class="is-flex px-4 pt-3 compose-mode-switch">
```

the Repository button, the notice and the tab:

Replace:

```vue
					<b-button :type="editorTab === 'containers' ? 'is-primary' : 'is-text'"
						rounded
						size="is-small"
						@click="setEditorTab('containers')">
						{{ $t('Containers') }}
					</b-button>
				</div>
```

with:

```vue
					<b-button :type="editorTab === 'containers' ? 'is-primary' : 'is-text'"
						rounded
						size="is-small"
						@click="setEditorTab('containers')">
						{{ $t('Containers') }}
					</b-button>
					<!-- a git app, or a compose app whose folder is a git work tree -->
					<b-button v-if="gitApp"
						:type="editorTab === 'repository' ? 'is-primary' : 'is-text'"
						class="ml-2"
						rounded
						size="is-small"
						@click="setEditorTab('repository')">
						{{ $t('Repository') }}
					</b-button>
				</div>

				<!-- A git or adoptable app is its repository: an edit here would modify
					tracked files, and modified tracked files refuse every later deployment. -->
				<div v-if="gitNotice" class="px-4 pt-3 git-notice">
					<b-message class="mb-0" size="is-small" type="is-info">
						{{ $t('This app is defined by its git repository: change it there. An edit here would modify tracked files and block every later deployment.') }}
						<a @click="setEditorTab('repository')">{{ $t('Open the Repository tab') }}</a>
					</b-message>
				</div>

				<GitRepoTab v-if="gitApp && editorTab === 'repository'"
					:app-id="id"
					:git-app="gitApp"
					@change="gitApp = $event" />
```

the Compose editor:

Replace:

```vue
				<ComposeEditor v-if="isCasa && state == 'update' && editorTab === 'compose'"
```

with:

```vue
				<ComposeEditor v-if="isCasa && state == 'update' && editorTab === 'compose' && !gitApp"
```

the `.env` editor:

Replace:

```vue
					<EnvEditor v-if="envLoaded"
						ref="envEditor"
						:app-id="id"
						:value="envText"
```

with:

```vue
					<EnvEditor v-if="envLoaded"
						ref="envEditor"
						:app-id="id"
						:readonly="envTracked"
						:value="envText"
```

the Settings form, kept mounted and hidden (decision 6):

Replace:

```vue
				<ComposeConfig v-if="isCasa && editorTab === 'settings'"
					ref="ComposeConfig"
```

with:

```vue
				<!-- still mounted for a git or adoptable app, hidden: it names the service
					the terminal opens on and the app the title shows -->
				<ComposeConfig v-if="isCasa && editorTab === 'settings'"
					v-show="!gitApp"
					ref="ComposeConfig"
```

the footer's Save:

Replace:

```vue
				<b-button v-if="isCasa && currentSlide == 1 && state == 'update' && editorTab === 'settings'"
```

with:

```vue
				<b-button v-if="isCasa && currentSlide == 1 && state == 'update' && editorTab === 'settings' && !gitApp"
```

and the footer's Apply:

Replace:

```vue
				<b-button v-if="isCasa && currentSlide == 1 && state == 'update' && (editorTab === 'compose' || editorTab === 'env')"
```

with:

```vue
				<b-button v-if="isCasa && currentSlide == 1 && state == 'update' && ((editorTab === 'compose' && !gitApp) || (editorTab === 'env' && !envTracked))"
```

- [ ] **Step 8: Load the GitApp in the script**

In `src/components/Apps/AppPanel.vue`, the import:

Replace:

```js
import ContainersTab from '@/components/Apps/ContainersTab.vue'
```

with:

```js
import ContainersTab from '@/components/Apps/ContainersTab.vue'
import GitRepoTab from '@/components/Apps/GitRepoTab.vue'
```

the component:

Replace:

```js
		ContainersTab,
		VeeField,
```

with:

```js
		ContainersTab,
		GitRepoTab,
		VeeField,
```

the prop:

Replace:

```js
		// for compose app.
		settingComposeData: {
			type: String,
		},
	},
```

with:

```js
		// for compose app.
		settingComposeData: {
			type: String,
		},
		// A git app no deployment has given a container: there is no compose app to
		// set, and the panel is its Repository tab alone.
		repositoryOnly: {
			type: Boolean,
			default: false,
		},
	},
```

the data:

Replace:

```js
			// 'settings' | 'compose' | 'env'; the last two mount an editor whose
			// state the footer Apply button reads.
			editorTab: 'settings',
```

with:

```js
			// 'settings' | 'compose' | 'env' | 'containers' | 'repository'; compose
			// and env mount an editor whose state the footer Apply button reads.
			editorTab: this.repositoryOnly ? 'repository' : 'settings',
			// the GitApp of a git or adoptable app, null for any other app
			gitApp: null,
```

the export button, which has no compose file to save for a git app with no container:

Replace:

```js
		showExportButton() {
			return this.currentSlide == 1 && this.state == 'update'
```

with:

```js
		showExportButton() {
			return this.currentSlide == 1 && this.state == 'update' && !this.repositoryOnly
```

the computed:

Replace:

```js
		isMobile() {
			return this.$store.state.isMobile
		},
	},
```

with:

```js
		isMobile() {
			return this.$store.state.isMobile
		},
		// A git app or an adoptable one alike: its repository defines it.
		envTracked() {
			return Boolean(this.gitApp && this.gitApp.env_tracked)
		},
		gitNotice() {
			return Boolean(this.gitApp) && (this.editorTab === 'settings' || this.editorTab === 'compose' || (this.editorTab === 'env' && this.envTracked))
		},
	},
```

the calls in `created`:

Replace:

```js
		// If StoreId is not 0
		if (this.storeId != 0) {
```

with:

```js
		if (this.isCasa && this.state === 'update') {
			this.loadGitApp()
		}
		// no Settings form here to name the app in the title
		if (this.repositoryOnly) {
			this.currentInstallId = this.id
		}

		// If StoreId is not 0
		if (this.storeId != 0) {
```

and the method:

Replace:

```js
		onEditorState(state) {
			this.editorState = state
		},
```

with:

```js
		onEditorState(state) {
			this.editorState = state
		},

		// Any failure -- a 404, or a service without these routes -- is an app that
		// is neither a git app nor adoptable, and the panel stays as it was.
		async loadGitApp() {
			try {
				const res = await this.$api.gitApps.get(this.id)
				this.gitApp = res.data.data
			} catch {
				this.gitApp = null
			}
		},
```

- [ ] **Step 9: Run the panel's specs to see them pass**

`AppSection.spec.js` imports `AppPanel.vue`, so it runs too.

Run: `npx vitest run src/components/Apps/AppPanel.spec.js src/components/Apps/EnvEditor.spec.js src/components/Apps/AppSection.spec.js`

Expected: PASS, `Test Files  3 passed (3)` and `Tests  14 passed (14)`.

- [ ] **Step 10: Add the panel's words**

In `src/assets/lang/en_US.json`, replace the last entry:

```json
  "The app goes back to {commit}, and automatic rebuild is paused until you turn it on again or deploy by hand.": "The app goes back to {commit}, and automatic rebuild is paused until you turn it on again or deploy by hand."
```

with:

```json
  "The app goes back to {commit}, and automatic rebuild is paused until you turn it on again or deploy by hand.": "The app goes back to {commit}, and automatic rebuild is paused until you turn it on again or deploy by hand.",
  "Repository": "Repository",
  "This app is defined by its git repository: change it there. An edit here would modify tracked files and block every later deployment.": "This app is defined by its git repository: change it there. An edit here would modify tracked files and block every later deployment.",
  "Open the Repository tab": "Open the Repository tab"
```

In `src/assets/lang/fr_FR.json`, replace the last entry:

```json
  "The app goes back to {commit}, and automatic rebuild is paused until you turn it on again or deploy by hand.": "L'application revient à {commit}, et la reconstruction automatique est mise en pause jusqu'à ce que vous la réactiviez ou déployiez à la main."
```

with:

```json
  "The app goes back to {commit}, and automatic rebuild is paused until you turn it on again or deploy by hand.": "L'application revient à {commit}, et la reconstruction automatique est mise en pause jusqu'à ce que vous la réactiviez ou déployiez à la main.",
  "Repository": "Dépôt",
  "This app is defined by its git repository: change it there. An edit here would modify tracked files and block every later deployment.": "Cette application est définie par son dépôt git : modifiez-la là-bas. Une modification ici toucherait des fichiers suivis et bloquerait tous les déploiements suivants.",
  "Open the Repository tab": "Ouvrir l'onglet Dépôt"
```

- [ ] **Step 11: Check both files still parse**

Run:

```bash
node -e 'for (const f of ["en_US", "fr_FR"]) JSON.parse(require("node:fs").readFileSync("src/assets/lang/" + f + ".json", "utf8")); console.log("both parse")'
```

Expected: `both parse`.

- [ ] **Step 12: Commit**

```bash
git add src/components/Apps/EnvEditor.vue src/components/Apps/EnvEditor.spec.js src/components/Apps/AppPanel.vue src/components/Apps/AppPanel.spec.js src/assets/lang/en_US.json src/assets/lang/fr_FR.json
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): the Repository tab in the app panel, and what it keeps from editing"
```

---

### Task 7: The apps section: "From a git repository…", and the panel of a git app with no container

**Files:**
- Modify: `src/components/Apps/AppSection.vue:18-20` (the "+" menu), `:103` (imports), `:242` (a method before `checkImageUpdates`, beside `showUpdateAll` at `:231-240`), `:468-473` and `:495` (`showConfigPanel`, whose whole body is `:454-501`)
- Modify: `src/components/Apps/AppSection.spec.js:3` (imports) and `:14` (two new `describe`s, calling the methods on a fake `this` like the spec's `section()` helper)
- Modify: `src/assets/lang/en_US.json` and `src/assets/lang/fr_FR.json` (append after Task 6's last entry)

**Interfaces:**
- Consumes: `GitAppModal` (Task 4); `withoutContainer(item)` (Task 2); `AppPanel`'s `repositoryOnly` prop (Task 6); `AppCard`'s `configApp(item, true)` for a git app with no container (Task 3).
- Produces: `AppSection` method `showGitApp()`, bound to the new menu item; `showConfigPanel(item, isCasa)` opens a git app with no container with `settingComposeData: ''` and `repositoryOnly: true`, without calling `myComposeApp`, and every other app with `repositoryOnly: false`, as before.

- [ ] **Step 1: Write the failing tests**

In `src/components/Apps/AppSection.spec.js`, the import:

Replace:

```js
import AppSection from '@/components/Apps/AppSection.vue'
```

with:

```js
import AppSection from '@/components/Apps/AppSection.vue'
import GitAppModal from '@/components/Apps/GitAppModal.vue'
```

and the tests:

Replace:

```js
describe('app section update outcome', () => {
```

with:

```js
describe('app section git entry', () => {
	it('opens the git dialog, which only its own buttons close', () => {
		const open = vi.fn()
		AppSection.methods.showGitApp.call({ $buefy: { modal: { open } } })

		expect(open).toHaveBeenCalledTimes(1)
		expect(open.mock.calls[0][0].component).toBe(GitAppModal)
		// its Cancel deletes an app registered and never deployed; escape would not
		expect(open.mock.calls[0][0].canCancel).toEqual([])
	})
})

describe('app section opening a git app', () => {
	function section(myComposeApp) {
		const open = vi.fn()
		const vm = {
			$messageBus: vi.fn(),
			$api: { container: { getNetworks: () => Promise.resolve({ data: { data: [] } }) } },
			$store: { state: { hardwareInfo: { mem: { total: 1 } } } },
			$openAPI: { appManagement: { compose: { myComposeApp } } },
			$buefy: { modal: { open } },
		}
		return { vm, open }
	}

	it('opens a git app with no container on its Repository tab alone, reading no compose app', async () => {
		const myComposeApp = vi.fn()
		const { vm, open } = section(myComposeApp)
		const item = { name: 'jarvis', app_type: 'v2app', status: '', title: { en_us: 'jarvis' }, git: { new_commits: false, state: 'build_failed', deployed: false } }

		await AppSection.methods.showConfigPanel.call(vm, item, true)

		expect(myComposeApp).not.toHaveBeenCalled()
		expect(open.mock.calls[0][0].props).toMatchObject({ id: 'jarvis', state: 'update', isCasa: true, settingComposeData: '', repositoryOnly: true })
	})

	it('reads the compose app of an app that has containers, as before', async () => {
		const myComposeApp = vi.fn().mockResolvedValue({ data: 'name: jarvis\n' })
		const { vm, open } = section(myComposeApp)
		const item = { name: 'jarvis', app_type: 'v2app', status: 'running', title: { en_us: 'jarvis' }, git: { new_commits: false, state: 'idle', deployed: true } }

		await AppSection.methods.showConfigPanel.call(vm, item, true)

		expect(myComposeApp).toHaveBeenCalledTimes(1)
		expect(open.mock.calls[0][0].props).toMatchObject({ settingComposeData: 'name: jarvis\n', repositoryOnly: false })
	})
})

describe('app section update outcome', () => {
```

- [ ] **Step 2: Run them to see them fail**

Run: `npx vitest run src/components/Apps/AppSection.spec.js`

Expected: FAIL, `Tests  3 failed | 3 passed (6)`, with `TypeError: Cannot read properties of undefined (reading 'call')`, `expected "spy" to not be called at all, but actually been called 1 times` and `expected { id: 'jarvis', state: 'update', …(4) } to match object { …(2) }`. A `TypeError: Cannot read properties of undefined (reading 'data')` logged by `showConfigPanel`'s own `catch` is part of that run.

- [ ] **Step 3: Add the menu item, and open the dialog**

In `src/components/Apps/AppSection.vue`, the menu:

Replace:

```vue
				<b-dropdown-item aria-role="menuitem" @click="showInstall(0, 'custom')">
					{{ $t('Custom Install APP') }}
				</b-dropdown-item>
```

with:

```vue
				<b-dropdown-item aria-role="menuitem" @click="showInstall(0, 'custom')">
					{{ $t('Custom Install APP') }}
				</b-dropdown-item>
				<b-dropdown-item aria-role="menuitem" @click="showGitApp">
					{{ $t('From a git repository…') }}
				</b-dropdown-item>
```

the imports:

Replace:

```js
import ExternalLinkPanel from '@/components/Apps/ExternalLinkPanel'
```

with:

```js
import ExternalLinkPanel from '@/components/Apps/ExternalLinkPanel'
import GitAppModal from '@/components/Apps/GitAppModal.vue'
import { withoutContainer } from '@/components/Apps/gitApps'
```

and the method:

Replace:

```js
		async checkImageUpdates() {
```

with:

```js
		// An app built from its own repository. Only the dialog's buttons close it:
		// its Cancel deletes an app registered and never deployed, which escape or a
		// click outside would leave behind, holding its name.
		showGitApp() {
			this.$buefy.modal.open({
				component: GitAppModal,
				hasModalCard: true,
				trapFocus: true,
				canCancel: [],
				scroll: 'keep',
				animation: 'zoom-in',
			})
		},

		async checkImageUpdates() {
```

- [ ] **Step 4: Open a git app with no container on its Repository tab alone**

In `src/components/Apps/AppSection.vue`, in `showConfigPanel`, the compose app it reads:

Replace:

```js
				const ret = await this.$openAPI.appManagement.compose.myComposeApp(name, {
					headers: {
						'content-type': 'application/yaml',
						'accept': 'application/yaml',
					},
				})
				this.$buefy.modal.open({
```

with:

```js
				// A git app no deployment has given a container has no compose app to
				// read: its panel is its Repository tab alone.
				const repositoryOnly = withoutContainer(item)
				const ret = repositoryOnly
					? { data: '' }
					: await this.$openAPI.appManagement.compose.myComposeApp(name, {
						headers: {
							'content-type': 'application/yaml',
							'accept': 'application/yaml',
						},
					})
				this.$buefy.modal.open({
```

and what it opens the panel with:

Replace:

```js
						// settingData: ret.data,
						settingComposeData: ret.data,
					},
```

with:

```js
						// settingData: ret.data,
						settingComposeData: ret.data,
						repositoryOnly,
					},
```

- [ ] **Step 5: Run the spec to see it pass**

Run: `npx vitest run src/components/Apps/AppSection.spec.js`

Expected: PASS, `✓ src/components/Apps/AppSection.spec.js  (6 tests)`.

- [ ] **Step 6: Add the menu's words**

In `src/assets/lang/en_US.json`, replace the last entry:

```json
  "Open the Repository tab": "Open the Repository tab"
```

with:

```json
  "Open the Repository tab": "Open the Repository tab",
  "From a git repository…": "From a git repository…"
```

In `src/assets/lang/fr_FR.json`, replace the last entry:

```json
  "Open the Repository tab": "Ouvrir l'onglet Dépôt"
```

with:

```json
  "Open the Repository tab": "Ouvrir l'onglet Dépôt",
  "From a git repository…": "Depuis un dépôt git…"
```

- [ ] **Step 7: Check both files still parse**

Run:

```bash
node -e 'for (const f of ["en_US", "fr_FR"]) JSON.parse(require("node:fs").readFileSync("src/assets/lang/" + f + ".json", "utf8")); console.log("both parse")'
```

Expected: `both parse`.

- [ ] **Step 8: Commit**

```bash
git add src/components/Apps/AppSection.vue src/components/Apps/AppSection.spec.js src/assets/lang/en_US.json src/assets/lang/fr_FR.json
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): an app from a git repository in the apps menu, and the panel of one with no container"
```

---

### Task 8: The branch as a whole

**Files:**
- None changed. If a step fails, fix the file the failure names, in the task that created it, re-run that task's spec, and commit the fix on its own.

**Interfaces:**
- Consumes: everything above.
- Produces: a branch `feat/git-apps` of seven commits on `inkly/main`, green on the tests and the lint gate, not pushed.

- [ ] **Step 1: Every word the new code shows is in both language files**

Run:

```bash
node - <<'EOF'
const fs = require('node:fs')
const read = name => JSON.parse(fs.readFileSync(`src/assets/lang/${name}.json`, 'utf8'))
const en = read('en_US')
const fr = read('fr_FR')
// the words AppCard.vue, AppPanel.vue and AppSection.vue gained, and every word the new files use
const keys = new Set([
	'Delete {name}? What CasaOS cloned, built or started for it goes with it; the repository itself is not touched.',
	'Repository',
	'This app is defined by its git repository: change it there. An edit here would modify tracked files and block every later deployment.',
	'Open the Repository tab',
	'From a git repository…',
])
for (const file of ['src/components/Apps/GitAppModal.vue', 'src/components/Apps/GitRepoTab.vue']) {
	for (const match of fs.readFileSync(file, 'utf8').matchAll(/\$t\('([^']+)'/g))
		keys.add(match[1])
}
for (const match of fs.readFileSync('src/components/Apps/gitApps.js', 'utf8').matchAll(/(?:label|message): '([^']+)'|^\t\w+: '([^']+)',\r?$/gm))
	keys.add(match[1] || match[2])
const wrong = [...keys].filter(key => en[key] !== key || !fr[key] || key.includes('|'))
console.log(`${keys.size} keys checked, wrong: ${JSON.stringify(wrong)}`)
process.exit(wrong.length ? 1 : 0)
EOF
```

Expected: `82 keys checked, wrong: []`, then exit 0. The script only reads files, and reads them the same with LF or CRLF endings.

- [ ] **Step 2: The whole test suite, once**

Run:

```bash
npx vitest run > "${TMPDIR:-/tmp}/git-apps-vitest.log" 2>&1; echo "exit=$?"; grep -E "Test Files|Tests  " "${TMPDIR:-/tmp}/git-apps-vitest.log"
```

Expected: `exit=0`, `Test Files  53 passed (53)` and `Tests  501 passed (501)`. If a few suites fail while loading ("Failed to load url", a smoke test timing out after 5000 ms) and none fails an assertion, run the same command once more before looking into it; read the log file for the details instead of running the suite again.

- [ ] **Step 3: The lint gate**

Run: `npx eslint . --quiet; echo "exit=$?"`

Expected: no report, then `exit=0`.

- [ ] **Step 4: The branch holds what this plan made, and nothing else**

Run:

```bash
git status --short; git log --oneline inkly/main..HEAD; git diff --stat inkly/main..HEAD | tail -1
```

Expected: `git status --short` prints nothing; the log lists seven commits, newest first: `feat(apps): an app from a git repository in the apps menu, and the panel of one with no container`, `feat(apps): the Repository tab in the app panel, and what it keeps from editing`, `feat(apps): the Repository tab`, `feat(apps): create an app from a git repository`, `feat(apps): a git app's state on its card, and the card of one with no container`, `feat(apps): the rules and the words of git apps`, `feat(apps): a client for AppManagement's git app routes`; the stat line reads `19 files changed`.

The branch stays local. Trying it against a box waits for AppManagement's git routes (the backend plan beside this one): there, create an app from a repository, follow its build, check the card's badge, the tab and the notices with the language set to French, and delete an app whose first build failed from its card.

---

## Spec coverage

| Spec ("What the owner sees", "Dashboard", "Clarifications made while planning") | Task |
|---|---|
| `src/service/gitApps.js`, hand-written, `$api.gitApps` | 1 |
| `Apps/gitApps.js`: state to badge, outcome labels, compose summary flags, with specs | 2 |
| Card badges, following "Update available" | 3 |
| Clarification 3: a git app with no container keeps a card, which offers its panel and deletion only; its panel is the Repository tab alone | 3, 6, 7 |
| Clarification 4: `DELETE` while `deployed` is null, leftovers included (card, dialog) | 3, 4 |
| "+" menu: "From a git repository…" | 7 |
| Dialog step 1: URL, optional branch, name defaulting to the repository name, access none/key/token, the key's public half with a copy button, a used name refused | 4 (the refusal is the server's 409 message) |
| Dialog step 2 and clarification 6: the clone (a check, followed until it ends), every service built or pulled with ports and volumes, sensitive options called out in words (clarification 5), `.env` prefilled from the template, read-only when tracked, `compose_example` when there is no compose file | 2, 4 |
| Dialog step 3: deploy, the build log live; `env` again after a failed first deployment (clarification 8) | 4 |
| Adopting: the tab by itself, remote, branch, commit, tracked files modified; detached HEAD or no remote shown and not adoptable (clarification 2); deploying adopts (clarification 1) | 5, 6 |
| Tab: remote, branch, deployed commit, last check; access mode, public key, write-only token, "Test access"; automatic rebuild switch with its warning; "Check now", "Fetch and rebuild"; last build log; last three deployments with outcome and "Revert to this version" | 5 |
| Settings and Compose tabs show a notice for a git app and for an adoptable one (clarification 7); `.env` read-only with the notice when tracked; containers, logs, terminal, backups unchanged | 6 |
| New strings in `en_US.json` and `fr_FR.json` only | 2, 3, 4, 5, 6, 7, 8 |
| Testing: vitest specs for the dialog steps, the tab states, the badges, the client's methods and URLs | 1–7 |
