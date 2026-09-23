# Git apps that follow tags: the dashboard (CasaOS-UI) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In CasaOS-UI, let the owner register a git app that follows the newest release tag instead of a branch, change that choice later in the Repository tab, read the tag-mode status line, be warned of a tag that moved and redeploy it by hand, deploy any listed tag (confirming an older one), see history entries named by their tags, and tell GitLab to send tag pushes, with every string in English and French.

**Architecture:** Every decision stays on the server: which tag is eligible, which is newest, whether it moved, whether the card lights. The dashboard only words what the view says, and orders nothing: the tag list is shown in the order the server sends it. The one comparison it makes, "is this tag older than the deployed one", guards a confirmation dialog and lives in `src/components/Apps/gitApps.js` with the other rules that can be checked without a mount. A view without `follow` (an AppManagement older than this feature) shows exactly today's tab: the tag UI is keyed on `gitApp.follow`.

**Tech Stack:** Vue 3.5 (options API), Buefy 3.1.0 (`b-select`, `b-input`, `b-checkbox`, `b-tag`, `b-button`, `b-message`, `$buefy.dialog.confirm`), vue-i18n 9 in legacy mode, vitest 4.1 with happy-dom and @vue/test-utils 2.

**Spec:** D:/clients/casaos/CasaOS-AppManagement/docs/superpowers/specs/2026-09-23-git-tags-design.md

## Global Constraints

- Repository `D:/clients/casaos/CasaOS-UI`. Branch `feat/git-tags`, created in Task 1 from a freshly fetched `inkly/main` (`0453168` when this was written). Every command runs in Git Bash from `D:/clients/casaos/CasaOS-UI`.
- The contract with AppManagement (its plan, `D:/clients/casaos/CasaOS-AppManagement/docs/superpowers/plans/2026-09-23-git-tags-appmanagement.md`, implements it; this plan never calls anything else):
  - Every answer that carries an app is `{ "data": GitApp }`. A server that knows tags adds to `GitApp`: `follow` (`"branch"` or `"tags"`, a plain string), `tag_pattern` (string, `""` for every version tag), `prereleases` (bool); `check.remote_tag` (string: the highest eligible tag the last check found, `""` in branch mode) and `check.tag_moved` (bool, computed with the view: tag mode, `remote_tag` not empty, equal to `deployed.tag`, and `remote_commit` different from `deployed.commit`); `deployed.tag` and `history[].tag` (string, `""` for a branch deployment). `branch` is `""` in tag mode. `operation.tag` exists but the dashboard does not read it.
  - `follow` is never `""` on a server that knows tags, for an app whose state file predates the field too (an empty `follow` in a state file reads `"branch"`). The tab takes a falsy `follow` for an older server.
  - With no eligible tag, `check.error` is the server's sentence naming the filter, such as ``no tag matches (pattern `v2.*`, pre-releases excluded)``, and the check keeps the previous `remote_commit` and `remote_tag` (`""` when no check ever found a tag). `check.tag_moved`, computed from them, can then still be true: the dashboard reads neither `remote_tag` nor `tag_moved` while `check.error` is set (Task 5).
  - With no eligible tag, `state` is what it would be without that error (`"idle"` for an app at rest), not `"unreachable"`: the remote was reached. The AppManagement plan does it (its Task 4); nothing in this plan depends on it.
  - `new_commits` (in the view and in the grid item's `git`) is, in tag mode, true when `remote_commit` differs from `deployed.commit` and either `deployed.tag` is `""` (no deployment yet in this mode) or `remote_tag` is strictly higher in semver than `deployed.tag`. The card badge reads it as today.
  - A revert (`deploy { commit }` of a history entry) in tag mode records that entry's `tag` as `deployed.tag`, so the first-deployment note (Task 4) does not come back after a revert.
  - A change of mode on a cloned app puts its folder in the shape the new mode deploys from (detached HEAD for tags, the branch checked out for a branch), so the first deployment by hand that the tab asks for is accepted.
  - An older tag deployed by hand pauses automatic deployment, as a revert does (Task 5's confirmation says so beforehand).
  - `POST /v2/app_management/git` accepts `follow`, `tag_pattern`, `prereleases`. The dashboard sends them only for tags: `{ name, url, follow: "tags", tag_pattern?, prereleases, access, token? }`; a branch app is registered with exactly today's body. A server that knows tags answers `follow: "tags"` for it.
  - `PUT /v2/app_management/git/{app}` accepts `{ follow: "tags", tag_pattern: string, prereleases: bool }` (in tag mode or to switch to it; `tag_pattern: ""` clears the pattern) and `{ follow: "branch", branch?: string }` (to switch back; no `branch` means the remote's default). The switch to a branch must be accepted on a cloned app.
  - `POST /v2/app_management/git/{app}/deploy` accepts `{ tag: "<name>" }`; `{}` in tag mode deploys the highest eligible tag at the time of the request.
  - `GET /v2/app_management/git/{app}/tags` answers `{ "data": [{ "name": string, "commit": string }] }`, eligible tags, highest first, at most 50; 400 with `{ message }` for an app that follows a branch.
- A server older than this feature sends no `follow`: the Repository tab then behaves exactly as today (Branch row, branch wording, no tag UI). The registration dialog cannot know the server before registering: on such a server a tag registration comes back without `follow: "tags"`, and the dialog deletes that app and says so (Task 3).
- The dashboard never sorts or filters tags. `isOlderTag` (Task 2) only decides whether to ask for confirmation.
- New strings go to `src/assets/lang/en_US.json` and `src/assets/lang/fr_FR.json` only; other languages fall back to `en_us`. In `en_US.json` the value is the key. `fr_FR.json` gets real French with a space before `:` and `;`. No key contains `|` or `@`.
- Code style is `eslint.config.mjs` (@antfu/eslint-config): tabs, single quotes, no semicolons. Lint gate: `npx eslint . --quiet` exits 0. Never run a linter or formatter with `--fix`, never `npm run lint` or `pnpm lint`.
- Tests: `npx vitest run <spec paths>`. `node_modules` is installed: do not run `pnpm install`. Build: `NODE_OPTIONS=--max-old-space-size=4096 npx -y pnpm@9.0.6 build`.
- A component spec mounts with `mount(Component, { global: { plugins: [Buefy, i18n], mocks } })` and mocks `@/assets/lang`, so `$t` returns its key uninterpolated unless the mock table holds that key.
- Line endings differ per file in this working tree (`core.autocrlf=true`: some files are CRLF, some LF). Change an existing file only with the Edit tool, replacing exactly the block a step shows, so each file keeps its own endings. Never rewrite an existing file whole.
- Commits: stage files by name, then `git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "..."`. Conventional subject in plain English. No `Co-Authored-By` line and no tool or AI attribution anywhere.
- Forbidden: `git push`, `git tag`, `git merge`, `git pull`, `git rebase`, `git reset --hard`, `git checkout -- <path>`, `git restore`, `git clean`, `git stash`, `rm -rf` on a directory, `pnpm install`, any linter or formatter with `--fix`, editing anything under `src/openapi/`, editing a file this plan does not list. Releases are the controller's.

---

## File structure

| File | Change | Responsibility |
|---|---|---|
| `src/service/gitApps.js` | modify (comments lines 10-11, 21-23, 35-40; new method after `deploy`) | `tags(app)`; documents the new request fields |
| `src/service/gitApps.spec.js` | modify (line 27) | the method and URL of `tags` |
| `src/components/Apps/gitApps.js` | modify (lines 69-76, before line 81) | the tag-mode status line in `checkSummary`; `versionName`, `isOlderTag`, `tagLabels` |
| `src/components/Apps/gitApps.spec.js` | modify (line 2, append at the end) | those four, without a mount |
| `src/components/Apps/GitAppModal.vue` | modify (lines 19-21, 133-135, 197-209) | the mode choice at registration, its fields, the older-server guard |
| `src/components/Apps/GitAppModal.spec.js` | modify (three `find('select')`, after line 134) | tag registration and the guard |
| `src/components/Apps/GitRepoTab.vue` | modify (Task 4 then Task 5) | the Mode row, wording, history names, GitLab step; then the moved-tag warning, "Deploy a tag…", the confirmations |
| `src/components/Apps/GitRepoTab.spec.js` | modify (lines 8-10, end of file) | the tab's tag specs |
| `src/assets/lang/en_US.json` | modify (line 823, end of file) | 23 new strings, the GitLab step replaced |
| `src/assets/lang/fr_FR.json` | modify (line 823, end of file) | the same in French |

`src/service/index.spec.js` covers the generated OpenAPI client only; git apps go through the hand-written `src/service/gitApps.js`, whose `gitApps.spec.js` asserts the method and URL of every operation. It is not touched.

---

### Task 1: The client: list the tags, and the new request fields

**Files:**
- Modify: `src/service/gitApps.js` (comments at lines 10-11, 21-23, 35-40; new method after `deploy`)
- Test: `src/service/gitApps.spec.js` (the `it.each` table, line 27)

**Interfaces:**
- Consumes: `GET /v2/app_management/git/{app}/tags` as in the Global Constraints.
- Produces: `gitApps.tags(app: string): Promise<{ data: { data: Array<{ name: string, commit: string }> } }>`, reached as `this.$api.gitApps.tags(appId)` (the client is already registered in `src/service/api.js`). `gitApps.deploy(app, { tag })` and `gitApps.create`/`update` with the new fields need no code: they pass any body through.

- [ ] **Step 1: Create the branch**

Run:

```bash
git fetch inkly && test -z "$(git status --short --untracked-files=no)" && git switch -c feat/git-tags inkly/main && git log --oneline -1
```

Expected: `Switched to a new branch 'feat/git-tags'`, then the head commit of the freshly fetched `inkly/main` (`0453168 fix(theme): a switch that is on is the bright one in the dark theme too` when this was written). If the command stops before the switch (the fetch fails, or a tracked file is modified or staged), stop and report: nothing has been switched, and the tree belongs to another piece of work.

- [ ] **Step 2: Write the failing test**

In `src/service/gitApps.spec.js`, replace:

```js
		[() => gitApps.deploy('jarvis'), 'post', '/v2/app_management/git/jarvis/deploy'],
		[() => gitApps.remove('jarvis'), 'delete', '/v2/app_management/git/jarvis'],
```

with:

```js
		[() => gitApps.deploy('jarvis'), 'post', '/v2/app_management/git/jarvis/deploy'],
		[() => gitApps.tags('jarvis'), 'get', '/v2/app_management/git/jarvis/tags'],
		[() => gitApps.remove('jarvis'), 'delete', '/v2/app_management/git/jarvis'],
```

- [ ] **Step 3: Run it and see it fail**

Run: `npx vitest run src/service/gitApps.spec.js`

Expected: FAIL, `1 failed | 10 passed (11)`, the new row failing with `TypeError: ... tags is not a function`.

- [ ] **Step 4: Implement**

In `src/service/gitApps.js`, replace:

```js
	// `{ name, url, branch?, access, token? }`. Registers the app without cloning
	// it; in key mode the answer carries the public key to add to the repository.
	create(body) {
```

with:

```js
	// `{ name, url, branch?, follow?, tag_pattern?, prereleases?, access, token? }`.
	// Registers the app without cloning it; in key mode the answer carries the
	// public key to add to the repository. `follow` is "branch" (absent) or "tags";
	// an AppManagement older than tags ignores the three tag fields.
	create(body) {
```

Replace:

```js
	// Any of `{ branch, auto_deploy, access, token, webhook_enabled,
	// regenerate_webhook_secret }`. Adopts an adoptable app. Regenerating the
	// secret of a webhook that is off answers 400.
	update(app, body) {
```

with:

```js
	// Any of `{ branch, follow, tag_pattern, prereleases, auto_deploy, access,
	// token, webhook_enabled, regenerate_webhook_secret }`. Adopts an adoptable
	// app. Regenerating the secret of a webhook that is off answers 400.
	update(app, body) {
```

Replace:

```js
	// Answers 202 as the deployment starts, and adopts an adoptable app.
	// `{ commit?, env? }`: no commit is the remote's latest, a commit from the
	// history is a revert; `env` only while no deployment has succeeded.
	deploy(app, body = {}) {
		return api.post(`${PREFIX}/${encodeURIComponent(app)}/deploy`, body)
	},
```

with:

```js
	// Answers 202 as the deployment starts, and adopts an adoptable app.
	// `{ commit?, tag?, env? }`: neither commit nor tag is the remote's latest (the
	// highest eligible tag for an app that follows tags), a commit from the history
	// is a revert, a tag is that tag, lower ones included; both at once is a 400.
	// `env` only while no deployment has succeeded.
	deploy(app, body = {}) {
		return api.post(`${PREFIX}/${encodeURIComponent(app)}/deploy`, body)
	},

	// `{ data: [{ name, commit }] }`: the eligible tags of the remote, highest
	// first, at most 50. Asks the remote like a check does; 400 for an app that
	// follows a branch.
	tags(app) {
		return api.get(`${PREFIX}/${encodeURIComponent(app)}/tags`)
	},
```

- [ ] **Step 5: Run it and see it pass**

Run: `npx vitest run src/service/gitApps.spec.js`

Expected: PASS, `11 passed (11)`.

Then run: `npx eslint src/service/gitApps.js src/service/gitApps.spec.js --quiet; echo "exit=$?"`

Expected: no report, `exit=0`.

- [ ] **Step 6: Commit**

```bash
git add src/service/gitApps.js src/service/gitApps.spec.js
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): list the tags of a git app, and deploy one by name"
```

---

### Task 2: The rules of an app that follows tags, without a mount

**Files:**
- Modify: `src/components/Apps/gitApps.js` (`checkSummary`, lines 69-76; new functions before line 81, `// A kept version can be switched back to…`)
- Test: `src/components/Apps/gitApps.spec.js` (import on line 2, append after the last `describe`)

**Interfaces:**
- Consumes: the view keys of the Global Constraints (`follow`, `check.remote_tag`, `check.tag_moved`, `check.error`, `deployed.tag`, `deployed.commit`, `history[].commit`, `history[].outcome`).
- Produces, all exported from `src/components/Apps/gitApps.js`:
  - `checkSummary(app): { message: string, params: object }`, unchanged for an app without `follow === 'tags'`. For one with it, after the existing `Not checked yet.` and `The check failed: {error}` cases (so a stale `remote_tag` kept beside an error is never read): `{ message: 'Not checked yet.', params: {} }` when `check.remote_tag` is empty; `{ message: 'The tag {tag} moved.', params: { tag } }` when `check.tag_moved`; `{ message: 'Up to date ({tag})', params: { tag } }` when `deployed.tag === check.remote_tag`; otherwise `{ message: 'Newest tag {tag} · deployed {deployed}', params: { tag, deployed } }` where `deployed` is `deployed.tag`, else the 7-digit commit, else `'-'`.
  - `versionName(version: { commit: string, tag?: string }): string`: `'v1.4.1 (aaaaaaa)'`, or `'aaaaaaa'` when `tag` is empty or absent.
  - `isOlderTag(tag: string, than: string): boolean`: true when `tag` is strictly lower than `than` by semver precedence (one leading `v` allowed, build metadata ignored); false when either is not a strict semver tag, `''`, `null` or `undefined`.
  - `tagLabels(app, tag: { name, commit }, index: number): string[]`: a subset, in this order, of `'deployed'` (same name and same commit as `app.deployed`), `'newest'` (`index === 0`), `'in history'` (a `app.history` entry on that commit ran, its `outcome` being `deployed` or `adopted`, and the tag is not the deployed one). The words are language keys.

- [ ] **Step 1: Write the failing tests**

In `src/components/Apps/gitApps.spec.js`, replace line 2:

```js
import { appendLog, canFollow, canRevert, checkEnded, checkSummary, gitBadge, outcomeTag, projectName, sensitiveLabel, sensitiveServices, shortCommit, timeAgo, withoutContainer } from './gitApps'
```

with:

```js
import { appendLog, canFollow, canRevert, checkEnded, checkSummary, gitBadge, isOlderTag, outcomeTag, projectName, sensitiveLabel, sensitiveServices, shortCommit, tagLabels, timeAgo, versionName, withoutContainer } from './gitApps'
```

Then replace the end of the file:

```js
	it('reads a time in the future as now: the clocks disagree, nothing came later', () => {
		expect(timeAgo(before(-30), 'en_us', now)).toBe('now')
	})
})
```

with:

```js
	it('reads a time in the future as now: the clocks disagree, nothing came later', () => {
		expect(timeAgo(before(-30), 'en_us', now)).toBe('now')
	})
})

describe('an app that follows tags', () => {
	const at = '2026-09-23T10:05:00Z'
	const tagApp = (check, deployed = { commit: A, tag: 'v1.4.1' }) => ({ follow: 'tags', check: { at, error: '', tag_moved: false, ...check }, deployed })

	it('names the newest tag and the deployed one, or says it is up to date', () => {
		expect(checkSummary(tagApp({ remote_tag: 'v1.4.2', remote_commit: B })))
			.toEqual({ message: 'Newest tag {tag} · deployed {deployed}', params: { tag: 'v1.4.2', deployed: 'v1.4.1' } })
		expect(checkSummary(tagApp({ remote_tag: 'v1.4.1', remote_commit: A })))
			.toEqual({ message: 'Up to date ({tag})', params: { tag: 'v1.4.1' } })
	})

	it('says a tag moved rather than up to date, and names a version deployed from a branch by its commit', () => {
		expect(checkSummary(tagApp({ remote_tag: 'v1.4.1', remote_commit: B, tag_moved: true })))
			.toEqual({ message: 'The tag {tag} moved.', params: { tag: 'v1.4.1' } })
		expect(checkSummary(tagApp({ remote_tag: 'v1.4.2', remote_commit: B }, { commit: A, tag: '' })).params.deployed).toBe('aaaaaaa')
		expect(checkSummary(tagApp({ remote_tag: 'v1.4.2', remote_commit: B }, null)).params.deployed).toBe('-')
	})

	it('gives the check\'s own words when no tag is eligible, and waits for a check made in tags', () => {
		const error = 'no tag matches (pattern `v2.*`, pre-releases excluded)'
		// the check keeps the tag it found before, moved or not: the error wins
		expect(checkSummary(tagApp({ remote_tag: 'v1.4.1', remote_commit: B, tag_moved: true, error })))
			.toEqual({ message: 'The check failed: {error}', params: { error } })
		expect(checkSummary(tagApp({ remote_tag: '', remote_commit: B })).message).toBe('Not checked yet.')
	})

	it('names a version by its tag and its commit, or by its commit alone', () => {
		expect(versionName({ commit: A, tag: 'v1.4.1' })).toBe('v1.4.1 (aaaaaaa)')
		expect(versionName({ commit: A, tag: '' })).toBe('aaaaaaa')
		expect(versionName({ commit: A })).toBe('aaaaaaa')
	})

	it.each([
		['v1.4.1', 'v1.4.2', true],
		['v1.4.2', 'v1.4.1', false],
		['v1.9.0', 'v1.10.0', true],
		['1.2.3', 'v1.2.3', false],
		['v2.0.0-rc.1', 'v2.0.0', true],
		['v2.0.0', 'v2.0.0-rc.1', false],
		['v2.0.0-rc.2', 'v2.0.0-rc.10', true],
		['v2.0.0-alpha', 'v2.0.0-alpha.1', true],
		['v2.0.0-1', 'v2.0.0-alpha', true],
		['v2.0.0-beta', 'v2.0.0-alpha', false],
		['v1.2.3+build.9', 'v1.2.3', false],
		['latest', 'v1.0.0', false],
		['v1.0.0', '', false],
	])('%s is older than %s: %s', (tag, than, older) => {
		expect(isOlderTag(tag, than)).toBe(older)
	})

	it('labels the deployed tag, the newest one and the ones the history keeps', () => {
		const C = 'c'.repeat(40)
		const D = 'd'.repeat(40)
		const app = {
			deployed: { commit: A, tag: 'v1.4.1' },
			history: [
				{ commit: A, tag: 'v1.4.1', outcome: 'deployed' },
				{ commit: D, tag: 'v1.3.9', outcome: 'build_failed' },
				{ commit: C, tag: 'v1.4.0', outcome: 'adopted' },
			],
		}
		expect(tagLabels(app, { name: 'v1.4.2', commit: B }, 0)).toEqual(['newest'])
		expect(tagLabels(app, { name: 'v1.4.1', commit: A }, 1)).toEqual(['deployed'])
		expect(tagLabels(app, { name: 'v1.4.0', commit: C }, 2)).toEqual(['in history'])
		// a build that failed never ran: nothing of it is kept
		expect(tagLabels(app, { name: 'v1.3.9', commit: D }, 3)).toEqual([])
		// the deployed tag, moved to another commit, is that commit
		expect(tagLabels(app, { name: 'v1.4.1', commit: B }, 0)).toEqual(['newest'])
		expect(tagLabels({ deployed: null }, { name: 'v1.0.0', commit: A }, 0)).toEqual(['newest'])
	})
})
```

- [ ] **Step 2: Run them and see them fail**

Run: `npx vitest run src/components/Apps/gitApps.spec.js`

Expected: FAIL, `18 failed | 45 passed (63)`: `TypeError: versionName is not a function`, `isOlderTag is not a function`, `tagLabels is not a function`, and AssertionErrors on the three `checkSummary` tests (for example `expected 'The branch is at {commit}: up to date.' to be 'Not checked yet.'`). The 45 tests of `inkly/main` pass.

- [ ] **Step 3: Implement**

In `src/components/Apps/gitApps.js`, replace:

```js
// The last check, in words. `new_commits` is the server's: the branch has a
// commit that is not the deployed one.
export function checkSummary(app) {
	if (!app.check)
		return { message: 'Not checked yet.', params: {} }
	if (app.check.error)
		return { message: 'The check failed: {error}', params: { error: app.check.error } }
	if (app.new_commits)
```

with:

```js
// The last check, in words. `new_commits` is the server's: the branch has a
// commit that is not the deployed one. An app that follows tags is worded by the
// tag the server chose; the dashboard compares none. With no eligible tag, the
// check's error says so, naming the filter, and the tag it keeps from an earlier
// check is not read.
export function checkSummary(app) {
	if (!app.check)
		return { message: 'Not checked yet.', params: {} }
	if (app.check.error)
		return { message: 'The check failed: {error}', params: { error: app.check.error } }
	if (app.follow === 'tags') {
		const tag = app.check.remote_tag
		// a check made before the app followed tags
		if (!tag)
			return { message: 'Not checked yet.', params: {} }
		// the deployed tag on another commit: the tab's warning says the rest
		if (app.check.tag_moved)
			return { message: 'The tag {tag} moved.', params: { tag } }
		if (app.deployed && app.deployed.tag === tag)
			return { message: 'Up to date ({tag})', params: { tag } }
		const deployed = app.deployed ? (app.deployed.tag || shortCommit(app.deployed.commit)) : '-'
		return { message: 'Newest tag {tag} · deployed {deployed}', params: { tag, deployed } }
	}
	if (app.new_commits)
```

Then replace:

```js
// A kept version can be switched back to, except the one already deployed.
```

with:

```js
// A deployed version as the owner knows it: `v1.4.1 (abc1234)`, or the commit
// alone when it was deployed from a branch.
export function versionName(version) {
	return version.tag ? `${version.tag} (${shortCommit(version.commit)})` : shortCommit(version.commit)
}

// A version tag as the server reads one: strict semver after one leading `v`.
const VERSION = /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/

// Semver precedence of two pre-release identifiers: numbers below words,
// numbers by value, words in ASCII order.
function compareIdentifiers(a, b) {
	const numeric = [/^\d+$/.test(a), /^\d+$/.test(b)]
	if (numeric[0] && numeric[1])
		return Number(a) - Number(b)
	if (numeric[0] !== numeric[1])
		return numeric[0] ? -1 : 1
	if (a === b)
		return 0
	return a < b ? -1 : 1
}

// Whether tag `tag` is a lower version than tag `than`, by semver precedence;
// false when either is not a version. Only the confirmation of an older tag asks:
// the order of the tags and the choice of the newest are the server's.
export function isOlderTag(tag, than) {
	const [a, b] = [VERSION.exec(tag || ''), VERSION.exec(than || '')]
	if (!a || !b)
		return false
	for (const part of [1, 2, 3]) {
		if (a[part] !== b[part])
			return Number(a[part]) < Number(b[part])
	}
	// a pre-release comes before its release
	if (!a[4] || !b[4])
		return Boolean(a[4]) && !b[4]
	const [x, y] = [a[4].split('.'), b[4].split('.')]
	for (let i = 0; i < Math.min(x.length, y.length); i++) {
		const order = compareIdentifiers(x[i], y[i])
		if (order)
			return order < 0
	}
	return x.length < y.length
}

// What a tag of the list is, the list coming highest first: the version that
// runs (the same name on the same commit), the newest, a version the history
// keeps (one that ran: the server keeps no failed build). The words are
// language keys.
export function tagLabels(app, tag, index) {
	const deployed = Boolean(app.deployed) && app.deployed.tag === tag.name && app.deployed.commit === tag.commit
	const kept = (app.history || []).some(entry => entry.commit === tag.commit && ['deployed', 'adopted'].includes(entry.outcome))
	return [deployed && 'deployed', index === 0 && 'newest', !deployed && kept && 'in history'].filter(Boolean)
}

// A kept version can be switched back to, except the one already deployed.
```

- [ ] **Step 4: Run them and see them pass**

Run: `npx vitest run src/components/Apps/gitApps.spec.js`

Expected: PASS, `63 passed (63)`.

Then run: `npx eslint src/components/Apps/gitApps.js src/components/Apps/gitApps.spec.js --quiet; echo "exit=$?"`

Expected: no report, `exit=0`.

- [ ] **Step 5: Commit**

```bash
git add src/components/Apps/gitApps.js src/components/Apps/gitApps.spec.js
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): word the check and the versions of an app that follows tags"
```

---

### Task 3: Registration: follow a branch, or tags

**Files:**
- Modify: `src/components/Apps/GitAppModal.vue` (template lines 19-21; `data` lines 133-135; `register` lines 197-209)
- Test: `src/components/Apps/GitAppModal.spec.js` (the three `find('select')`, lines 112, 125, 196; two tests after the one ending at line 134)

**Interfaces:**
- Consumes: `this.$api.gitApps.create(body)`, `this.$api.gitApps.remove(app)` (unchanged client); the create answer's `follow` (Global Constraints).
- Produces, in `GitAppModal`:
  - data `follow: 'branch'|'tags'` (`'branch'`), `tagPattern: string` (`''`), `prereleases: boolean` (`false`);
  - DOM on the repository step: the `Mode` field's `b-select` is the first `select` of the dialog (Access becomes the second); options `branch` (`Follow a branch`) and `tags` (`Follow tags`), the spec's words. In branch mode the inputs are unchanged: URL `inputs()[0]`, Branch `[1]`, App name `[2]`, Token `[3]`. In tag mode: URL `[0]`, Tag pattern `[1]`, the pre-release checkbox `[2]`, App name `[3]`;
  - request: a branch app sends today's body `{ name, url, branch?, access, token? }`; a tag app sends `{ name, url, follow: 'tags', tag_pattern?, prereleases, access, token? }` (`tag_pattern` left out when empty, `branch` never sent);
  - an answer without `follow === 'tags'` to a tag registration: `remove(app)`, `app` back to `null`, the error `This CasaOS cannot follow tags yet: update it, or follow a branch.`, nothing cloned.

- [ ] **Step 1: Point the existing specs at the Access select**

In `src/components/Apps/GitAppModal.spec.js`, replace every `.find('select')` with `.findAll('select')[1]` (use the Edit tool with `replace_all`: the file has exactly three, on lines 112, 125 and 196, all of them the Access select, which becomes the second select of the dialog).

- [ ] **Step 2: Write the failing tests**

In the same file, replace:

```js
		await click('Continue')
		expect(gitApps.check).toHaveBeenCalledWith('jarvis')
		wrapper.unmount()
	})
```

with:

```js
		await click('Continue')
		expect(gitApps.check).toHaveBeenCalledWith('jarvis')
		wrapper.unmount()
	})

	it('registers an app that follows tags, with its pattern and its pre-releases', async () => {
		const { wrapper, gitApps, click, inputs } = setup({
			create: vi.fn().mockResolvedValue({ data: { data: gitApp({ branch: '', follow: 'tags', tag_pattern: 'v2.*', prereleases: true }) } }),
		})
		await inputs()[0].setValue(REPO_URL)
		await inputs()[1].setValue('dev')
		await wrapper.findAll('select')[0].setValue('tags')
		expect(wrapper.text()).toContain('For example v2.* to stay on version 2; empty for every version tag.')
		expect(wrapper.text()).not.toContain('Empty: the branch the repository names as its default.')

		// the branch field is gone: the pattern comes first, then the checkbox
		await inputs()[1].setValue('v2.*')
		await wrapper.find('input[type="checkbox"]').setValue(true)
		await click('Next')
		expect(gitApps.create).toHaveBeenCalledWith({ name: 'jarvis', url: REPO_URL, follow: 'tags', tag_pattern: 'v2.*', prereleases: true, access: 'none' })
		expect(gitApps.check).toHaveBeenCalledWith('jarvis')
		wrapper.unmount()
	})

	it('takes back an app a server older than tags registered on a branch, and says why', async () => {
		const { wrapper, gitApps, click, inputs } = setup()
		await inputs()[0].setValue(REPO_URL)
		await wrapper.findAll('select')[0].setValue('tags')
		await click('Next')

		expect(gitApps.remove).toHaveBeenCalledWith('jarvis')
		expect(gitApps.check).not.toHaveBeenCalled()
		expect(wrapper.text()).toContain('This CasaOS cannot follow tags yet: update it, or follow a branch.')
		// nothing is registered: the form can be changed and sent again
		expect(inputs()[0].attributes('disabled')).toBeUndefined()
		wrapper.unmount()
	})
```

(`toHaveBeenCalledWith` treats a property whose value is `undefined` as absent, so `branch: undefined` and `token: undefined` in the real call match the expected object.)

- [ ] **Step 3: Run them and see them fail**

Run: `npx vitest run src/components/Apps/GitAppModal.spec.js`

Expected: FAIL, `5 failed | 11 passed (16)`: the three tests that pick the Access select fail with `TypeError: Cannot read properties of undefined (reading 'setValue')` (the dialog has one select so far), the tag registration with `expected '…' to contain 'For example v2.* to stay on version 2…'`, the older-server test with `expected "vi.fn()" to be called with arguments: [ 'jarvis' ]`.

- [ ] **Step 4: The template**

In `src/components/Apps/GitAppModal.vue`, replace:

```html
				<b-field :label="$t('Branch')" :message="$t('Empty: the branch the repository names as its default.')">
					<b-input v-model="branch" :disabled="!!app"></b-input>
				</b-field>
```

with:

```html
				<b-field :label="$t('Mode')">
					<b-select v-model="follow" :disabled="!!app" expanded>
						<option value="branch">{{ $t('Follow a branch') }}</option>
						<option value="tags">{{ $t('Follow tags') }}</option>
					</b-select>
				</b-field>
				<b-field v-if="follow === 'branch'" :label="$t('Branch')" :message="$t('Empty: the branch the repository names as its default.')">
					<b-input v-model="branch" :disabled="!!app"></b-input>
				</b-field>
				<template v-else>
					<b-field :label="$t('Tag pattern')" :message="$t('For example v2.* to stay on version 2; empty for every version tag.')">
						<b-input v-model="tagPattern" :disabled="!!app"></b-input>
					</b-field>
					<b-field>
						<b-checkbox v-model="prereleases" :disabled="!!app">{{ $t('Include pre-releases (-rc, -beta)') }}</b-checkbox>
					</b-field>
				</template>
```

- [ ] **Step 5: The script**

Replace:

```js
			url: '',
			branch: '',
			name: '',
```

with:

```js
			url: '',
			// "branch" or "tags"
			follow: 'branch',
			branch: '',
			tagPattern: '',
			prereleases: false,
			name: '',
```

Replace:

```js
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
```

with:

```js
		async register() {
			const tags = this.follow === 'tags'
			this.busy = true
			this.error = ''
			try {
				// a branch app is registered with the body it always had
				const res = await this.$api.gitApps.create({
					name: this.name.trim(),
					url: this.url.trim(),
					branch: tags ? undefined : this.branch.trim() || undefined,
					follow: tags ? 'tags' : undefined,
					tag_pattern: tags ? this.tagPattern.trim() || undefined : undefined,
					prereleases: tags ? this.prereleases : undefined,
					access: this.access,
					token: this.access === 'token' ? this.token : undefined,
				})
				this.app = res.data.data
				// An AppManagement older than tags ignores them and registers a branch
				// app, which would deploy every push: take it back.
				if (tags && this.app.follow !== 'tags') {
					await this.$api.gitApps.remove(this.app.app)
					this.app = null
					this.error = this.$t('This CasaOS cannot follow tags yet: update it, or follow a branch.')
					return
				}
			} catch (error) {
```

(If `remove` itself fails, the existing `catch` shows the server's message and `app` stays set, so Cancel still deletes it.)

- [ ] **Step 6: Run them and see them pass**

Run: `npx vitest run src/components/Apps/GitAppModal.spec.js`

Expected: PASS, `16 passed (16)`.

Then run: `npx eslint src/components/Apps/GitAppModal.vue src/components/Apps/GitAppModal.spec.js --quiet; echo "exit=$?"`

Expected: no report, `exit=0`.

- [ ] **Step 7: Commit**

```bash
git add src/components/Apps/GitAppModal.vue src/components/Apps/GitAppModal.spec.js
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): register a git app that follows tags"
```

---

### Task 4: The Repository tab: what the app follows

**Files:**
- Modify: `src/components/Apps/GitRepoTab.vue` (template lines 23-31, 84-87, 151; script lines 172, 208, 233-239, 255-257, 292-294, 316-318, 366-372)
- Test: `src/components/Apps/GitRepoTab.spec.js` (mock lines 8-10; end of the file, lines 374-381)

**Interfaces:**
- Consumes: `checkSummary` and `versionName` from Task 2 (`checkLine` already renders `checkSummary`); `this.$api.gitApps.update(app, body)` (unchanged client); the view keys `follow`, `tag_pattern`, `prereleases`, `branch`, `deployed.tag`, `history[].tag`; the tab's existing `run(kind, request)`, `busy`, `canAct`, `error`, `appOf`.
- Produces, in `GitRepoTab`:
  - data `follow: 'branch'|'tags'` (`gitApp.follow || 'branch'`), `branch: string` (`''`, the branch typed when switching back), `tagPattern: string` (`gitApp.tag_pattern || ''`), `prereleases: boolean` (`Boolean(gitApp.prereleases)`), each reset by a watcher when the server's value changes;
  - computed `tagMode: boolean` (`gitApp.follow === 'tags'`, the server's mode), `followChanged: boolean`, `firstInMode: boolean`;
  - methods `version(version)` (= `versionName`), `saveFollow(): Promise<void>`;
  - `busy` value `'follow'`;
  - DOM: without `gitApp.follow`, the Branch row as today and no other change; with it, `tr.git-repo-tab__follow` (headed `Mode`) holds the mode `select` (options `branch` `Follow a branch`, `tags` `Follow tags`), in tag mode an `input.input` (pattern) and an `input[type="checkbox"]` (pre-releases), when switching to a branch an `input.input` (branch), and a `Save` button only while `followChanged`;
  - requests: `update(appId, { follow: 'tags', tag_pattern: <trimmed>, prereleases })` or `update(appId, { follow: 'branch', branch: <trimmed> || undefined })`;
  - the HOW_TO GitLab step `'Trigger: Push events and Tag push events.'` replaces `'Trigger: Push events.'`.

- [ ] **Step 1: Write the failing tests**

In `src/components/Apps/GitRepoTab.spec.js`, replace lines 8-10:

```js
// require.context has no Vite equivalent; a table this small makes $t return its
// key, except for the one message the last delivery needs filled in.
vi.mock('@/assets/lang', () => ({ default: { en_us: { 'Received {when}': 'Received {when}' } } }))
```

with (the last two messages serve Task 5):

```js
// require.context has no Vite equivalent; a table this small makes $t return its
// key, except for the messages below, which the specs need filled in.
vi.mock('@/assets/lang', () => ({
	default: {
		en_us: Object.fromEntries([
			'Received {when}',
			'The check failed: {error}',
			'Newest tag {tag} · deployed {deployed}',
			'Up to date ({tag})',
			'The tag {tag} now points at another commit: it is not redeployed automatically.',
			'Redeploy {tag}',
		].map(key => [key, key])),
	},
}))
```

Then replace the end of the file:

```js
		expect(wrapper.findAll('.tabs li').map(li => li.text())).toEqual(['GitHub', 'Gitea/Forgejo', 'GitLab'])
		expect(wrapper.text()).toContain('Which events: “Just the push event”.')
		wrapper.unmount()
	})
})
```

with:

```js
		expect(wrapper.findAll('.tabs li').map(li => li.text())).toEqual(['GitHub', 'Gitea/Forgejo', 'GitLab'])
		expect(wrapper.text()).toContain('Which events: “Just the push event”.')
		// GitLab sends a tag apart from a push, and only when asked
		expect(wrapper.text()).toContain('Trigger: Push events and Tag push events.')
		wrapper.unmount()
	})
})

describe('a git app that follows tags', () => {
	const release = (commit, tag, fields = {}) => ({ commit, tag, subject: `release ${tag}`, at: AT, outcome: 'deployed', reason: '', revertable: true, ...fields })
	const tagApp = (fields = {}) => ({
		follow: 'tags',
		branch: '',
		tag_pattern: '',
		prereleases: false,
		deployed: { commit: A, tag: 'v1.4.1', subject: 'release v1.4.1', at: AT },
		check: { at: AT, remote_commit: B, remote_tag: 'v1.4.2', tag_moved: false, error: '' },
		new_commits: true,
		history: [release(A, 'v1.4.1')],
		...fields,
	})
	const followRow = wrapper => wrapper.find('.git-repo-tab__follow')
	const saveFollow = wrapper => followRow(wrapper).findAll('button').find(b => b.text() === 'Save')

	it('says which tag is newest and which one runs, and names versions by their tags', () => {
		const behind = setup(tagApp())
		const text = behind.wrapper.text()
		expect(text).toContain('Newest tag v1.4.2 · deployed v1.4.1')
		expect(behind.wrapper.find('.git-repo-tab__deployment').text()).toContain('v1.4.1 (aaaaaaa) release v1.4.1')
		expect(text).toContain('Deploy a newer tag as soon as a check finds it')
		expect(text).toContain('It runs whatever is tagged with a higher version.')
		expect(text).not.toContain('It runs whatever is pushed to the branch.')
		behind.wrapper.unmount()

		const current = setup(tagApp({ check: { at: AT, remote_commit: A, remote_tag: 'v1.4.1', tag_moved: false, error: '' }, new_commits: false }))
		expect(current.wrapper.text()).toContain('Up to date (v1.4.1)')
		current.wrapper.unmount()
	})

	it('gives the check\'s words when no tag is eligible, and nothing of the tag it found before', () => {
		const error = 'no tag matches (pattern `v2.*`, pre-releases excluded)'
		// as the server answers it: the check keeps the tag and the commit it found
		// last, the view still compares them, and the app is at rest (Global Constraints)
		const stale = { at: AT, remote_commit: B, remote_tag: 'v1.4.1', tag_moved: true, error }
		const { wrapper } = setup(tagApp({ tag_pattern: 'v2.*', check: stale, new_commits: false, state: 'idle' }))
		expect(wrapper.text()).toContain(`The check failed: ${error}`)
		expect(wrapper.text()).not.toContain('Newest tag')
		expect(wrapper.text()).not.toContain('Up to date')
		wrapper.unmount()
	})

	it('shows the branch, and nothing of tags, to a server that knows none', () => {
		// the fixture of the specs above has no `follow`, as an older AppManagement answers
		const { wrapper } = setup()
		expect(followRow(wrapper).exists()).toBe(false)
		expect(wrapper.text()).toContain('Branch')
		expect(wrapper.text()).not.toContain('Deploy a tag…')
		expect(wrapper.findAll('select')).toHaveLength(1)
		wrapper.unmount()
	})

	it('switches a branch app to tags with the filter it kept, and says the first deployment is by hand', async () => {
		const { wrapper, gitApps } = setup({ follow: 'branch', tag_pattern: 'v2.*', prereleases: false })
		expect(followRow(wrapper).text()).toContain('main')
		expect(saveFollow(wrapper)).toBeUndefined()
		expect(wrapper.text()).not.toContain('The first deployment in this mode is manual')

		await followRow(wrapper).find('select').setValue('tags')
		expect(followRow(wrapper).find('input.input').element.value).toBe('v2.*')
		await followRow(wrapper).find('input[type="checkbox"]').setValue(true)
		expect(wrapper.text()).toContain('The first deployment in this mode is manual; automatic deployment resumes after it.')

		await saveFollow(wrapper).trigger('click')
		await flushPromises()
		expect(gitApps.update).toHaveBeenCalledWith('jarvis', { follow: 'tags', tag_pattern: 'v2.*', prereleases: true })
		wrapper.unmount()
	})

	it('changes the pattern, and says so until a tag is deployed by hand', async () => {
		const { wrapper, gitApps } = setup(tagApp({ deployed: { commit: A, tag: '', subject: 'feat: voice wake word', at: AT } }))
		expect(wrapper.text()).toContain('The first deployment in this mode is manual')

		await followRow(wrapper).find('input.input').setValue(' v2.* ')
		await saveFollow(wrapper).trigger('click')
		await flushPromises()
		expect(gitApps.update).toHaveBeenCalledWith('jarvis', { follow: 'tags', tag_pattern: 'v2.*', prereleases: false })
		wrapper.unmount()
	})

	it('goes back to a branch, the one typed or the default', async () => {
		const { wrapper, gitApps } = setup(tagApp())
		await followRow(wrapper).find('select').setValue('branch')
		await followRow(wrapper).find('input.input').setValue('dev')
		await saveFollow(wrapper).trigger('click')
		await flushPromises()
		expect(gitApps.update).toHaveBeenLastCalledWith('jarvis', { follow: 'branch', branch: 'dev' })

		await followRow(wrapper).find('select').setValue('branch')
		await saveFollow(wrapper).trigger('click')
		await flushPromises()
		// JSON leaves out a branch left empty: the server takes the remote's default
		expect(JSON.stringify(gitApps.update.mock.lastCall[1])).toBe('{"follow":"branch"}')
		wrapper.unmount()
	})
})
```

- [ ] **Step 2: Run them and see them fail**

Run: `npx vitest run src/components/Apps/GitRepoTab.spec.js`

Expected: FAIL, `5 failed | 22 passed (27)`. The 21 tests of `inkly/main` still pass, except the GitLab guide (`expected '…' to contain 'Trigger: Push events and Tag push eve…'`). `gives the check's words when no tag is eligible, and nothing of the tag it found before` passes already (Task 2's `checkSummary`) and so does `shows the branch, and nothing of tags, to a server that knows none`, which guards today's tab. The four others fail with AssertionErrors (for example `expected 'aaaaaaa release v1.4.1 …' to contain 'v1.4.1 (aaaaaaa) release v1.4.1'`, or `… to contain 'The first deployment in this mode is …'`) or with `Error: Cannot call text on an empty DOMWrapper.` / `Cannot call find on an empty DOMWrapper.` (no Mode row yet).

- [ ] **Step 3: The template**

In `src/components/Apps/GitRepoTab.vue`, replace (lines 23-31):

```html
				<tr>
					<th>{{ $t('Branch') }}</th>
					<td>{{ gitApp.branch || '-' }}</td>
				</tr>
				<tr>
					<th>{{ $t('Deployed') }}</th>
					<td>
						<template v-if="gitApp.deployed">
							<span class="git-repo-tab__mono">{{ short(gitApp.deployed.commit) }}</span>
```

with:

```html
				<!-- an AppManagement older than tags sends no `follow`: the branch, as it was -->
				<tr v-if="!gitApp.follow">
					<th>{{ $t('Branch') }}</th>
					<td>{{ gitApp.branch || '-' }}</td>
				</tr>
				<tr v-else class="git-repo-tab__follow">
					<th>{{ $t('Mode') }}</th>
					<td>
						<div class="is-flex is-align-items-center is-flex-wrap-wrap">
							<b-select v-model="follow" :disabled="!canAct" class="mr-2 mb-1" size="is-small">
								<option value="branch">{{ $t('Follow a branch') }}</option>
								<option value="tags">{{ $t('Follow tags') }}</option>
							</b-select>
							<template v-if="follow === 'tags'">
								<b-input v-model="tagPattern" :disabled="!canAct" :placeholder="$t('Every version tag')" class="mr-2 mb-1" size="is-small"></b-input>
								<b-checkbox v-model="prereleases" :disabled="!canAct" class="mr-2 mb-1" size="is-small">
									{{ $t('Include pre-releases (-rc, -beta)') }}
								</b-checkbox>
							</template>
							<!-- the branch of a cloned app is the one its folder is on -->
							<span v-else-if="gitApp.follow === 'branch'" class="mr-2 mb-1">{{ gitApp.branch || '-' }}</span>
							<b-input v-else v-model="branch" :disabled="!canAct" :placeholder="$t('Default branch')" class="mr-2 mb-1" size="is-small"></b-input>
							<b-button v-if="followChanged" :disabled="!canAct" :loading="busy === 'follow'" class="mb-1" rounded size="is-small" @click="saveFollow">
								{{ $t('Save') }}
							</b-button>
						</div>
						<p v-if="firstInMode" class="has-text-full-03">{{ $t('The first deployment in this mode is manual; automatic deployment resumes after it.') }}</p>
					</td>
				</tr>
				<tr>
					<th>{{ $t('Deployed') }}</th>
					<td>
						<template v-if="gitApp.deployed">
							<span class="git-repo-tab__mono">{{ version(gitApp.deployed) }}</span>
```

Replace (lines 84-87):

```html
		<b-switch :disabled="!canAct" :model-value="autoDeploy" size="is-small" @update:model-value="setAutoDeploy">
			{{ $t('Deploy a new commit as soon as a check finds it') }}
		</b-switch>
		<p class="is-size-7 has-text-danger mt-1">{{ $t('It runs whatever is pushed to the branch.') }}</p>
```

with:

```html
		<b-switch :disabled="!canAct" :model-value="autoDeploy" size="is-small" @update:model-value="setAutoDeploy">
			{{ tagMode ? $t('Deploy a newer tag as soon as a check finds it') : $t('Deploy a new commit as soon as a check finds it') }}
		</b-switch>
		<p class="is-size-7 has-text-danger mt-1">
			{{ tagMode ? $t('It runs whatever is tagged with a higher version.') : $t('It runs whatever is pushed to the branch.') }}
		</p>
```

Replace (line 151, the history row):

```html
						<span class="git-repo-tab__mono">{{ short(entry.commit) }}</span> {{ entry.subject }}
```

with:

```html
						<span class="git-repo-tab__mono">{{ version(entry) }}</span> {{ entry.subject }}
```

- [ ] **Step 4: The script**

Replace line 172:

```js
import { appendLog, canFollow, canRevert, checkEnded, checkSummary, gitBadge, outcomeTag, shortCommit, timeAgo } from './gitApps'
```

with:

```js
import { appendLog, canFollow, canRevert, checkEnded, checkSummary, gitBadge, outcomeTag, shortCommit, timeAgo, versionName } from './gitApps'
```

Replace line 208, the last GitLab step of `HOW_TO`:

```js
			'Trigger: Push events.',
```

with:

```js
			'Trigger: Push events and Tag push events.',
```

Replace (lines 233-239):

```js
			autoDeploy: this.gitApp.auto_deploy,
			webhookOn: Boolean(this.gitApp.webhook && this.gitApp.webhook.enabled),
			showSecret: false,
			howTo: HOW_TO[0].forge,
			howTos: HOW_TO,
			// '' or what runs: access, auto, webhook, secret, test, check, deploy, or the commit of a revert
			busy: '',
```

with:

```js
			autoDeploy: this.gitApp.auto_deploy,
			// what the Mode row shows until it is saved: the mode, the branch to
			// switch to, the tag filter
			follow: this.gitApp.follow || 'branch',
			branch: '',
			tagPattern: this.gitApp.tag_pattern || '',
			prereleases: Boolean(this.gitApp.prereleases),
			webhookOn: Boolean(this.gitApp.webhook && this.gitApp.webhook.enabled),
			showSecret: false,
			howTo: HOW_TO[0].forge,
			howTos: HOW_TO,
			// '' or what runs: access, auto, follow, webhook, secret, test, check, deploy, or the commit of a revert
			busy: '',
```

Replace (lines 255-257):

```js
		checkLine() {
			return checkSummary(this.gitApp)
		},
```

with:

```js
		checkLine() {
			return checkSummary(this.gitApp)
		},
		// the mode the server has, not the one being chosen
		tagMode() {
			return this.gitApp.follow === 'tags'
		},
		followChanged() {
			if (this.follow !== this.gitApp.follow)
				return true
			return this.follow === 'tags' && (this.tagPattern.trim() !== (this.gitApp.tag_pattern || '') || this.prereleases !== Boolean(this.gitApp.prereleases))
		},
		// A mode chosen, or saved and not deployed in yet: the server deploys
		// automatically in tags only after a tag was deployed, in a branch only after
		// a commit was deployed from it, so the first one is by hand.
		firstInMode() {
			const deployed = this.gitApp.deployed
			return this.follow !== this.gitApp.follow || Boolean(deployed && this.tagMode !== Boolean(deployed.tag))
		},
```

Replace (lines 292-294):

```js
		'gitApp.auto_deploy': function (value) {
			this.autoDeploy = value
		},
```

with:

```js
		'gitApp.auto_deploy': function (value) {
			this.autoDeploy = value
		},
		'gitApp.follow': function (value) {
			this.follow = value || 'branch'
		},
		'gitApp.tag_pattern': function (value) {
			this.tagPattern = value || ''
		},
		'gitApp.prereleases': function (value) {
			this.prereleases = Boolean(value)
		},
```

Replace (lines 316-318):

```js
		canRevert,
		outcomeTag,
		short: shortCommit,
```

with:

```js
		canRevert,
		outcomeTag,
		short: shortCommit,
		version: versionName,
```

Replace (lines 366-372):

```js
		async setAutoDeploy(value) {
			this.autoDeploy = value
			if (!await this.run('auto', () => this.$api.gitApps.update(this.appId, { auto_deploy: value }).then(appOf)))
				this.autoDeploy = this.gitApp.auto_deploy
		},

		confirmRevert(entry) {
```

with:

```js
		async setAutoDeploy(value) {
			this.autoDeploy = value
			if (!await this.run('auto', () => this.$api.gitApps.update(this.appId, { auto_deploy: value }).then(appOf)))
				this.autoDeploy = this.gitApp.auto_deploy
		},

		// A pattern and pre-releases for tags; a branch left empty is the remote's
		// default. The server keeps the tag filter while the app follows a branch.
		async saveFollow() {
			const body = this.follow === 'tags'
				? { follow: 'tags', tag_pattern: this.tagPattern.trim(), prereleases: this.prereleases }
				: { follow: 'branch', branch: this.branch.trim() || undefined }
			if (await this.run('follow', () => this.$api.gitApps.update(this.appId, body).then(appOf)))
				this.branch = ''
		},

		confirmRevert(entry) {
```

(The revert dialog keeps its commit alone: Buefy renders a dialog's `message` as HTML, and a tag is text from the remote.)

- [ ] **Step 5: Run them and see them pass**

Run: `npx vitest run src/components/Apps/GitRepoTab.spec.js`

Expected: PASS, `27 passed (27)`.

Then run: `npx vitest run src/components/Apps/AppPanel.spec.js src/components/Apps/AppCard.spec.js`

Expected: PASS, `42 passed (42)` (their fixtures have no `follow`: the tab shows today's Branch row).

Then run: `npx eslint src/components/Apps/GitRepoTab.vue src/components/Apps/GitRepoTab.spec.js --quiet; echo "exit=$?"`

Expected: no report, `exit=0`.

- [ ] **Step 6: Commit**

```bash
git add src/components/Apps/GitRepoTab.vue src/components/Apps/GitRepoTab.spec.js
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): choose what a git app follows in its Repository tab"
```

---

### Task 5: The Repository tab: deploying tags by hand

**Files:**
- Modify: `src/components/Apps/GitRepoTab.vue` (the blocks shown below, as Task 4 left them: the `auto_paused` message, lines 13-15; the button row ending with "Fetch and rebuild", about line 164; the `import` line, about line 199; `data`, about lines 266-272; the `tagMode` computed, about line 292; `version: versionName`, about line 377; `deploy()`, about line 414)
- Test: `src/components/Apps/GitRepoTab.spec.js` (the end of `describe('a git app that follows tags')`, the last lines of the file)

**Interfaces:**
- Consumes: `isOlderTag(tag, than)` and `tagLabels(app, tag, index)` from Task 2; `this.$api.gitApps.tags(app)` from Task 1; `this.$api.gitApps.deploy(app, body)` (sends `{ tag }` or nothing); the view keys `check.remote_tag`, `check.tag_moved`, `deployed.tag`; from Task 4, `tagMode`, `version`, and the mock table that already holds `The tag {tag} now points at another commit: it is not redeployed automatically.` and `Redeploy {tag}`.
- Produces, in `GitRepoTab`:
  - data `tags: null | Array<{ name, commit }>` (null until "Deploy a tag…" answers);
  - computed `tagMoved: boolean` (`tagMode`, no `check.error`, and `check.tag_moved`: a check that found no eligible tag keeps an older `remote_tag`, which says nothing now);
  - methods `labelsOf(tag, index): string[]`, `confirmOlder(tag: string, action: () => any)`, `listTags(): Promise<void>`, `pickTag(tag: { name, commit })`, `deployTag(tag: string, kind: string): Promise<void>`; `deploy()` now goes through `confirmOlder(check.remote_tag, …)` in tag mode when the check has no error, `confirmOlder('', …)` (no question) otherwise;
  - `busy` values `'tags'`, `'redeploy'`, `` `tag:${name}` ``;
  - DOM: a warning `b-message` with the button `Redeploy <tag>` while `tagMoved`; the button `Deploy a tag…` next to "Fetch and rebuild" in tag mode only; then either `No tag to deploy.` or one `tr.git-repo-tab__tag` per tag: name, 7-digit commit, a `.tag` per label, and a `Deploy` button except on the deployed one;
  - requests: `tags(appId)` on "Deploy a tag…"; `deploy(appId, { tag })` from a row (after the confirmation `This is an older version than the one deployed; automatic rebuild is paused until you turn it on again or deploy by hand.` when `isOlderTag(tag, deployed.tag)`, the dialog's title being the tag) and from Redeploy; `deploy(appId)` from "Fetch and rebuild", with the same confirmation first when the last check's newest tag is older than the deployed one. That guard is best-effort: the server deploys the highest eligible tag at the time of the request, which the last check may not have seen (a tag deleted since), and a lower tag deployed by hand is allowed anyway.

- [ ] **Step 1: Write the failing tests**

In `src/components/Apps/GitRepoTab.spec.js`, replace the end of the file, as Task 4 left it:

```js
		// JSON leaves out a branch left empty: the server takes the remote's default
		expect(JSON.stringify(gitApps.update.mock.lastCall[1])).toBe('{"follow":"branch"}')
		wrapper.unmount()
	})
})
```

with:

```js
		// JSON leaves out a branch left empty: the server takes the remote's default
		expect(JSON.stringify(gitApps.update.mock.lastCall[1])).toBe('{"follow":"branch"}')
		wrapper.unmount()
	})

	it('warns of a tag that moved, and redeploys it only by hand', async () => {
		const moved = { at: AT, remote_commit: B, remote_tag: 'v1.4.1', tag_moved: true, error: '' }
		const { wrapper, gitApps, click } = setup(tagApp({ check: moved, new_commits: false }))
		expect(wrapper.text()).toContain('The tag v1.4.1 now points at another commit: it is not redeployed automatically.')
		expect(wrapper.text()).not.toContain('Up to date')
		expect(gitApps.deploy).not.toHaveBeenCalled()

		await click('Redeploy v1.4.1')
		expect(gitApps.deploy).toHaveBeenCalledWith('jarvis', { tag: 'v1.4.1' })
		wrapper.unmount()
	})

	it('neither warns nor asks on the tag a check kept from before it found none eligible', async () => {
		const error = 'no tag matches (pattern `v2.*`, pre-releases excluded)'
		// the view still compares the tag the check kept: it moved, or it is older
		const moved = setup(tagApp({ check: { at: AT, remote_commit: B, remote_tag: 'v1.4.1', tag_moved: true, error }, new_commits: false }))
		expect(moved.wrapper.text()).not.toContain('now points at another commit')
		expect(moved.button('Redeploy v1.4.1')).toBeUndefined()
		moved.wrapper.unmount()

		const older = setup(tagApp({ check: { at: AT, remote_commit: C, remote_tag: 'v1.4.0', tag_moved: false, error }, new_commits: false }))
		await older.click('Fetch and rebuild')
		expect(older.confirm).not.toHaveBeenCalled()
		expect(older.gitApps.deploy).toHaveBeenCalledWith('jarvis')
		older.wrapper.unmount()
	})

	describe('deploy a tag', () => {
		const LIST = [
			{ name: 'v1.4.2', commit: B },
			{ name: 'v1.4.1', commit: A },
			{ name: 'v1.4.0', commit: C },
		]
		const history = [release(A, 'v1.4.1'), release(C, 'v1.4.0')]
		const listed = () => {
			const tags = vi.fn().mockResolvedValue({ data: { data: LIST } })
			return { tags, ...setup(tagApp({ history }), { tags }) }
		}
		const rows = wrapper => wrapper.findAll('.git-repo-tab__tag')

		it('lists the tags the remote has, with what each one is', async () => {
			const { wrapper, tags, click } = listed()
			await click('Deploy a tag…')
			expect(tags).toHaveBeenCalledWith('jarvis')
			expect(rows(wrapper).map(row => row.findAll('.tag').map(tag => tag.text()))).toEqual([['newest'], ['deployed'], ['in history']])
			expect(rows(wrapper)[0].text()).toContain('v1.4.2 bbbbbbb')
			// nothing to deploy where it already runs
			expect(rows(wrapper).map(row => row.findAll('button').length)).toEqual([1, 0, 1])
			wrapper.unmount()
		})

		it('deploys a newer tag at once, and an older one once confirmed', async () => {
			const { wrapper, gitApps, confirm, click } = listed()
			await click('Deploy a tag…')
			await rows(wrapper)[0].find('button').trigger('click')
			await flushPromises()
			expect(confirm).not.toHaveBeenCalled()
			expect(gitApps.deploy).toHaveBeenLastCalledWith('jarvis', { tag: 'v1.4.2' })
			// the server took it: the list is done with
			expect(rows(wrapper)).toHaveLength(0)

			await click('Deploy a tag…')
			await rows(wrapper)[2].find('button').trigger('click')
			expect(gitApps.deploy).toHaveBeenCalledTimes(1)
			// the server pauses automatic deployment after an older tag: the owner is told first
			expect(confirm.mock.calls[0][0].title).toBe('v1.4.0')
			expect(confirm.mock.calls[0][0].message).toBe('This is an older version than the one deployed; automatic rebuild is paused until you turn it on again or deploy by hand.')
			confirm.mock.calls[0][0].onConfirm()
			await flushPromises()
			expect(gitApps.deploy).toHaveBeenLastCalledWith('jarvis', { tag: 'v1.4.0' })
			wrapper.unmount()
		})

		it('says when there is no tag to deploy, and what the server said when it could not list them', async () => {
			const empty = setup(tagApp(), { tags: vi.fn().mockResolvedValue({ data: { data: [] } }) })
			await empty.click('Deploy a tag…')
			expect(empty.wrapper.text()).toContain('No tag to deploy.')
			empty.wrapper.unmount()

			const refused = setup(tagApp(), { tags: vi.fn().mockRejectedValue({ response: { status: 400, data: { message: 'the app follows a branch' } } }) })
			await refused.click('Deploy a tag…')
			expect(refused.wrapper.text()).toContain('the app follows a branch')
			refused.wrapper.unmount()
		})

		it('asks before Fetch and rebuild goes back to the older newest tag of the last check', async () => {
			const gone = { at: AT, remote_commit: C, remote_tag: 'v1.4.0', tag_moved: false, error: '' }
			const { wrapper, gitApps, confirm, click } = setup(tagApp({ check: gone, new_commits: false }))
			await click('Fetch and rebuild')
			expect(gitApps.deploy).not.toHaveBeenCalled()
			confirm.mock.calls[0][0].onConfirm()
			await flushPromises()
			expect(gitApps.deploy).toHaveBeenCalledWith('jarvis')
			wrapper.unmount()
		})
	})
})
```

- [ ] **Step 2: Run them and see them fail**

Run: `npx vitest run src/components/Apps/GitRepoTab.spec.js`

Expected: FAIL, `5 failed | 28 passed (33)`: the moved tag with `expected '…' to contain 'The tag v1.4.1 now points at another …'`, three list tests with `TypeError: Cannot read properties of undefined (reading 'trigger')` (no "Deploy a tag…" button yet), and "Fetch and rebuild" with `expected "vi.fn()" to not be called at all, but actually been called 1 times`. `neither warns nor asks on the tag a check kept…` passes already, since nothing warns or asks yet: it guards the two `check.error` conditions of Step 4.

- [ ] **Step 3: The template**

In `src/components/Apps/GitRepoTab.vue`, replace:

```html
		<b-message v-else-if="gitApp.auto_paused" size="is-small" type="is-warning">
			{{ $t('Automatic rebuild is paused since a revert. Turn it on again, or deploy by hand, to resume it.') }}
		</b-message>
```

with:

```html
		<b-message v-else-if="gitApp.auto_paused" size="is-small" type="is-warning">
			{{ $t('Automatic rebuild is paused since a revert. Turn it on again, or deploy by hand, to resume it.') }}
		</b-message>
		<!-- the same name on another commit: never redeployed automatically -->
		<b-message v-if="tagMoved" size="is-small" type="is-warning">
			<p>{{ $t('The tag {tag} now points at another commit: it is not redeployed automatically.', { tag: gitApp.check.remote_tag }) }}</p>
			<b-button :disabled="!canAct" :loading="busy === 'redeploy'" class="mt-2" rounded size="is-small"
				@click="deployTag(gitApp.check.remote_tag, 'redeploy')">
				{{ $t('Redeploy {tag}', { tag: gitApp.check.remote_tag }) }}
			</b-button>
		</b-message>
```

Replace:

```html
			<b-button :disabled="!canAct" :loading="busy === 'deploy'" rounded size="is-small" type="is-primary" @click="deploy">
				{{ $t('Fetch and rebuild') }}
			</b-button>
		</div>
```

with (the name and the commit of a row stay on one line, so the space between them survives the template compiler):

```html
			<b-button :disabled="!canAct" :loading="busy === 'deploy'" rounded size="is-small" type="is-primary" @click="deploy">
				{{ $t('Fetch and rebuild') }}
			</b-button>
			<b-button v-if="tagMode" :disabled="!canAct" :loading="busy === 'tags'" class="ml-2" rounded size="is-small" @click="listTags">
				{{ $t('Deploy a tag…') }}
			</b-button>
		</div>

		<!-- the remote's eligible tags, highest first, as the server answered them -->
		<template v-if="tags && tagMode">
			<p v-if="!tags.length" class="is-size-7 has-text-full-03 mt-2">{{ $t('No tag to deploy.') }}</p>
			<table v-else class="table is-narrow is-fullwidth is-size-7 mt-2">
				<tbody>
					<tr v-for="(tag, index) in tags" :key="tag.name" class="git-repo-tab__tag">
						<td>
							<span class="git-repo-tab__mono">{{ tag.name }}</span> <span class="git-repo-tab__mono has-text-full-03">{{ short(tag.commit) }}</span>
						</td>
						<td>
							<b-tag v-for="label in labelsOf(tag, index)" :key="label" class="mr-1">{{ $t(label) }}</b-tag>
						</td>
						<td class="has-text-right">
							<b-button v-if="!labelsOf(tag, index).includes('deployed')" :disabled="!canAct" :loading="busy === `tag:${tag.name}`"
								rounded size="is-small" @click="pickTag(tag)">
								{{ $t('Deploy') }}
							</b-button>
						</td>
					</tr>
				</tbody>
			</table>
		</template>
```

- [ ] **Step 4: The script**

Replace:

```js
import { appendLog, canFollow, canRevert, checkEnded, checkSummary, gitBadge, outcomeTag, shortCommit, timeAgo, versionName } from './gitApps'
```

with:

```js
import { appendLog, canFollow, canRevert, checkEnded, checkSummary, gitBadge, isOlderTag, outcomeTag, shortCommit, tagLabels, timeAgo, versionName } from './gitApps'
```

Replace:

```js
			prereleases: Boolean(this.gitApp.prereleases),
			webhookOn: Boolean(this.gitApp.webhook && this.gitApp.webhook.enabled),
			showSecret: false,
			howTo: HOW_TO[0].forge,
			howTos: HOW_TO,
			// '' or what runs: access, auto, follow, webhook, secret, test, check, deploy, or the commit of a revert
			busy: '',
```

with:

```js
			prereleases: Boolean(this.gitApp.prereleases),
			// null until "Deploy a tag…" lists them, then [{ name, commit }]
			tags: null,
			webhookOn: Boolean(this.gitApp.webhook && this.gitApp.webhook.enabled),
			showSecret: false,
			howTo: HOW_TO[0].forge,
			howTos: HOW_TO,
			// '' or what runs: access, auto, follow, webhook, secret, test, check, deploy,
			// tags, redeploy, tag:<name> of a listed tag, or the commit of a revert
			busy: '',
```

Replace:

```js
		tagMode() {
			return this.gitApp.follow === 'tags'
		},
```

with:

```js
		tagMode() {
			return this.gitApp.follow === 'tags'
		},
		// a check that found no eligible tag keeps the tag it found before, and the
		// view still compares it: nothing to warn of then
		tagMoved() {
			const check = this.gitApp.check
			return this.tagMode && Boolean(check && !check.error && check.tag_moved)
		},
```

Replace:

```js
		short: shortCommit,
		version: versionName,
```

with:

```js
		short: shortCommit,
		version: versionName,

		labelsOf(tag, index) {
			return tagLabels(this.gitApp, tag, index)
		},
```

Replace:

```js
		deploy() {
			return this.run('deploy', () => this.$api.gitApps.deploy(this.appId).then(appOf))
		},
```

with:

```js
		// With no tag named, an app that follows tags gets the newest one there is,
		// which is older than the deployed one once that tag is gone: say so first.
		// ponytail: best-effort, from the last check; the server decides at the request
		// and a lower tag by hand is allowed, so a tag deleted since goes unasked.
		deploy() {
			const check = this.gitApp.check
			const newest = this.tagMode && check && !check.error ? check.remote_tag : ''
			return this.confirmOlder(newest, () => this.run('deploy', () => this.$api.gitApps.deploy(this.appId).then(appOf)))
		},

		// At once, or once confirmed when `tag` is an older version than the one
		// deployed; the server then pauses automatic deployment, as after a revert.
		// The title carries the tag: Buefy renders the message as HTML, the title as
		// text.
		confirmOlder(tag, action) {
			if (!isOlderTag(tag, this.gitApp.deployed && this.gitApp.deployed.tag))
				return action()
			this.$buefy.dialog.confirm({
				title: tag,
				message: this.$t('This is an older version than the one deployed; automatic rebuild is paused until you turn it on again or deploy by hand.'),
				confirmText: this.$t('Deploy'),
				cancelText: this.$t('Cancel'),
				type: 'is-warning',
				onConfirm: action,
			})
		},

		// The remote's eligible tags: it asks the remote, as a check does.
		async listTags() {
			this.busy = 'tags'
			this.error = ''
			try {
				this.tags = (await this.$api.gitApps.tags(this.appId)).data.data
			} catch (error) {
				this.error = this.messageOf(error)
			} finally {
				this.busy = ''
			}
		},

		pickTag(tag) {
			return this.confirmOlder(tag.name, () => this.deployTag(tag.name, `tag:${tag.name}`))
		},

		// Any eligible tag by hand, a moved one included; the list is done with once
		// the server took it.
		async deployTag(tag, kind) {
			if (await this.run(kind, () => this.$api.gitApps.deploy(this.appId, { tag }).then(appOf)))
				this.tags = null
		},
```

- [ ] **Step 5: Run them and see them pass**

Run: `npx vitest run src/components/Apps/GitRepoTab.spec.js src/components/Apps/AppPanel.spec.js src/components/Apps/AppCard.spec.js`

Expected: PASS, `75 passed (75)` (33 in `GitRepoTab.spec.js`, 42 in the two others).

Then run: `npx eslint src/components/Apps/GitRepoTab.vue src/components/Apps/GitRepoTab.spec.js --quiet; echo "exit=$?"`

Expected: no report, `exit=0`.

- [ ] **Step 6: Commit**

```bash
git add src/components/Apps/GitRepoTab.vue src/components/Apps/GitRepoTab.spec.js
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): deploy a tag by hand, and warn of a tag that moved"
```

---

### Task 6: The words of tag apps in English and French

**Files:**
- Modify: `src/assets/lang/en_US.json` (line 823; the end of the file)
- Modify: `src/assets/lang/fr_FR.json` (line 823; the end of the file)
- Test: the key check below (reads `GitRepoTab.vue`, `GitAppModal.vue`, `gitApps.js` and both language files; fails on a missing, wrong, stale or doubled key)

**Interfaces:**
- Consumes: every `$t('…')` key of `GitRepoTab.vue` and `GitAppModal.vue`; the `HOW_TO` steps (one quoted string per line, three tabs deep); every `message: '…'` of `gitApps.js`; the tag labels `deployed`, `newest`, `in history` (shown through `$t(label)`).
- Produces: in each file, `"Trigger: Push events."` replaced in place by `"Trigger: Push events and Tag push events."`, and 23 keys appended. `Branch`, `Save`, `Deploy`, `Cancel`, `Not checked yet.` and `The check failed: {error}` already exist and are reused.

- [ ] **Step 1: Run the key check and see it fail**

Run:

```bash
node - <<'EOF'
const fs = require('node:fs')
const text = name => fs.readFileSync(`src/assets/lang/${name}.json`, 'utf8')
const en = JSON.parse(text('en_US'))
const fr = JSON.parse(text('fr_FR'))
const read = name => fs.readFileSync(`src/components/Apps/${name}`, 'utf8')
// the labels of a listed tag reach $t through a variable
const keys = new Set(['deployed', 'newest', 'in history'])
for (const name of ['GitRepoTab.vue', 'GitAppModal.vue']) {
	for (const match of read(name).matchAll(/\$t\('([^']+)'/g))
		keys.add(match[1])
}
// the steps of HOW_TO, one quoted string per line
for (const match of read('GitRepoTab.vue').matchAll(/^\t{3}'([^']+)',\r?$/gm))
	keys.add(match[1])
// the messages of the last check
for (const match of read('gitApps.js').matchAll(/message: '([^']+)'/g))
	keys.add(match[1])
const wrong = [...keys].filter(key => en[key] !== key || !fr[key] || /[|@]/.test(key))
// the GitLab step it replaces, gone from both files
const stale = ['Trigger: Push events.'].filter(key => key in en || key in fr)
// JSON.parse keeps the last of two equal keys without a word: count them in the text
const doubled = ['en_US', 'fr_FR'].flatMap((name) => {
	const seen = {}
	for (const match of text(name).matchAll(/^ {2}"(.+?)": /gm))
		seen[match[1]] = (seen[match[1]] || 0) + 1
	return Object.keys(seen).filter(key => seen[key] > 1).map(key => `${name}: ${key}`)
})
console.log(`${keys.size} keys checked, ${wrong.length} wrong: ${JSON.stringify(wrong)}`)
console.log(`${stale.length} stale: ${JSON.stringify(stale)}`)
console.log(`${doubled.length} doubled: ${JSON.stringify(doubled)}`)
process.exit(wrong.length || stale.length || doubled.length ? 1 : 0)
EOF
echo "exit=$?"
```

Expected (on `inkly/main@0453168` plus Tasks 1-5): `124 keys checked, 24 wrong: [...]` (the 23 keys of Step 2's appended block and `Trigger: Push events and Tag push events.`), `1 stale: ["Trigger: Push events."]`, `0 doubled: []`, then `exit=1`. If a key of Step 2's list is not reported wrong, it already exists and right: leave its line out in Steps 2 and 3.

- [ ] **Step 2: The English strings**

In `src/assets/lang/en_US.json`, replace line 823:

```json
  "Trigger: Push events.": "Trigger: Push events.",
```

with:

```json
  "Trigger: Push events and Tag push events.": "Trigger: Push events and Tag push events.",
```

Then run `tail -n 2 src/assets/lang/en_US.json`. It prints the file's last entry, without a trailing comma, then `}`; on `0453168` that entry is `"The secret is forgotten. Turning the webhook on again makes a new one, to give the forge again.": "…"`; take whatever it is. With the Edit tool, replace those two lines with the same last entry followed by `,`, then the lines below, the last one without a trailing comma, then `}`. On `0453168` that is, exactly, replacing:

```json
  "The secret is forgotten. Turning the webhook on again makes a new one, to give the forge again.": "The secret is forgotten. Turning the webhook on again makes a new one, to give the forge again."
}
```

with:

```json
  "The secret is forgotten. Turning the webhook on again makes a new one, to give the forge again.": "The secret is forgotten. Turning the webhook on again makes a new one, to give the forge again.",
  "Mode": "Mode",
  "Follow a branch": "Follow a branch",
  "Follow tags": "Follow tags",
  "Tag pattern": "Tag pattern",
  "For example v2.* to stay on version 2; empty for every version tag.": "For example v2.* to stay on version 2; empty for every version tag.",
  "Include pre-releases (-rc, -beta)": "Include pre-releases (-rc, -beta)",
  "This CasaOS cannot follow tags yet: update it, or follow a branch.": "This CasaOS cannot follow tags yet: update it, or follow a branch.",
  "Every version tag": "Every version tag",
  "Default branch": "Default branch",
  "The first deployment in this mode is manual; automatic deployment resumes after it.": "The first deployment in this mode is manual; automatic deployment resumes after it.",
  "Up to date ({tag})": "Up to date ({tag})",
  "The tag {tag} moved.": "The tag {tag} moved.",
  "Newest tag {tag} · deployed {deployed}": "Newest tag {tag} · deployed {deployed}",
  "Deploy a newer tag as soon as a check finds it": "Deploy a newer tag as soon as a check finds it",
  "It runs whatever is tagged with a higher version.": "It runs whatever is tagged with a higher version.",
  "The tag {tag} now points at another commit: it is not redeployed automatically.": "The tag {tag} now points at another commit: it is not redeployed automatically.",
  "Redeploy {tag}": "Redeploy {tag}",
  "Deploy a tag…": "Deploy a tag…",
  "No tag to deploy.": "No tag to deploy.",
  "deployed": "deployed",
  "newest": "newest",
  "in history": "in history",
  "This is an older version than the one deployed; automatic rebuild is paused until you turn it on again or deploy by hand.": "This is an older version than the one deployed; automatic rebuild is paused until you turn it on again or deploy by hand."
}
```

- [ ] **Step 3: The French strings**

In `src/assets/lang/fr_FR.json`, replace line 823:

```json
  "Trigger: Push events.": "Trigger : Push events.",
```

with:

```json
  "Trigger: Push events and Tag push events.": "Trigger : Push events et Tag push events.",
```

Then, as in Step 2, run `tail -n 2 src/assets/lang/fr_FR.json` and replace its last entry and `}` with that entry followed by `,`, the lines below and `}`. On `0453168` that is, exactly, replacing:

```json
  "The secret is forgotten. Turning the webhook on again makes a new one, to give the forge again.": "Le secret est oublié. Réactiver le webhook en crée un nouveau, à redonner à la forge."
}
```

with:

```json
  "The secret is forgotten. Turning the webhook on again makes a new one, to give the forge again.": "Le secret est oublié. Réactiver le webhook en crée un nouveau, à redonner à la forge.",
  "Mode": "Mode",
  "Follow a branch": "Suivre une branche",
  "Follow tags": "Suivre les tags",
  "Tag pattern": "Motif de tag",
  "For example v2.* to stay on version 2; empty for every version tag.": "Par exemple v2.* pour rester en version 2 ; vide pour tous les tags de version.",
  "Include pre-releases (-rc, -beta)": "Inclure les préversions (-rc, -beta)",
  "This CasaOS cannot follow tags yet: update it, or follow a branch.": "Ce CasaOS ne sait pas encore suivre des tags : mettez-le à jour, ou suivez une branche.",
  "Every version tag": "Tous les tags de version",
  "Default branch": "Branche par défaut",
  "The first deployment in this mode is manual; automatic deployment resumes after it.": "Le premier déploiement dans ce mode se fait à la main ; le déploiement automatique reprend ensuite.",
  "Up to date ({tag})": "À jour ({tag})",
  "The tag {tag} moved.": "Le tag {tag} a été déplacé.",
  "Newest tag {tag} · deployed {deployed}": "Tag le plus récent {tag} · déployé {deployed}",
  "Deploy a newer tag as soon as a check finds it": "Déployer un tag plus récent dès qu'une vérification le trouve",
  "It runs whatever is tagged with a higher version.": "Elle exécute tout ce qui reçoit un tag de version plus haute.",
  "The tag {tag} now points at another commit: it is not redeployed automatically.": "Le tag {tag} pointe maintenant sur un autre commit : il n'est pas redéployé automatiquement.",
  "Redeploy {tag}": "Redéployer {tag}",
  "Deploy a tag…": "Déployer un tag…",
  "No tag to deploy.": "Aucun tag à déployer.",
  "deployed": "déployé",
  "newest": "le plus récent",
  "in history": "dans l'historique",
  "This is an older version than the one deployed; automatic rebuild is paused until you turn it on again or deploy by hand.": "C'est une version plus ancienne que celle qui est déployée ; la reconstruction automatique est mise en pause jusqu'à ce que vous la réactiviez ou déployiez à la main."
}
```

- [ ] **Step 4: Run the key check and see it pass**

Run the same `node - <<'EOF' … EOF` command as Step 1.

Expected: `124 keys checked, 0 wrong: []`, `0 stale: []`, `0 doubled: []`, then `exit=0`. The script also proves that both files still parse as JSON. (Checked on a copy of `0453168` with Tasks 1-5 applied: 24 wrong and 1 stale before, none after; every new message, English and French, compiles in vue-i18n 9 and fills `{tag}` and `{deployed}`.)

- [ ] **Step 5: Commit**

```bash
git add src/assets/lang/en_US.json src/assets/lang/fr_FR.json
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): the words of tag apps in English and French"
```

---

### Task 7: The branch as a whole

**Files:**
- None changed. If a step fails, fix the file the failure names in the task that created it, re-run that task's spec, and commit the fix on its own.

**Interfaces:**
- Consumes: Tasks 1-6.
- Produces: branch `feat/git-tags`, six commits on `inkly/main`, green on the whole test suite, the lint gate and the build, not pushed.

- [ ] **Step 1: The whole test suite, once**

Run:

```bash
npx vitest run > "${TMPDIR:-/tmp}/git-tags-vitest.log" 2>&1; echo "exit=$?"; grep -E "Test Files|Tests  " "${TMPDIR:-/tmp}/git-tags-vitest.log"
```

Expected: `exit=0`, no failed file and no failed test; the `Tests` line counts 33 more than on `inkly/main` (1 in `service/gitApps.spec.js`, 18 in `gitApps.spec.js`, 2 in `GitAppModal.spec.js`, 12 in `GitRepoTab.spec.js`): `589 passed (589)` on `0453168`. If a few suites fail while loading ("Failed to load url", a smoke test timing out after 5000 ms) and none fails an assertion, run the same command once more; read the log file for details instead of re-running.

- [ ] **Step 2: The lint gate**

Run: `npx eslint . --quiet; echo "exit=$?"`

Expected: no report, `exit=0`.

- [ ] **Step 3: The build**

Run:

```bash
NODE_OPTIONS=--max-old-space-size=4096 npx -y pnpm@9.0.6 build > "${TMPDIR:-/tmp}/git-tags-build.log" 2>&1; echo "exit=$?"; grep -E "Build complete|ERROR" "${TMPDIR:-/tmp}/git-tags-build.log" | head -5
```

Expected: `exit=0` and a `Build complete.` line, no `ERROR`. The build writes only to `build/` and `.cache/`, both ignored by git.

- [ ] **Step 4: The branch holds this plan's work and nothing else**

Run:

```bash
git status --short --untracked-files=no; git log --oneline inkly/main..HEAD; git diff --stat inkly/main..HEAD | tail -1
```

Expected: `git status` lists nothing; the log lists six commits, newest first: `feat(apps): the words of tag apps in English and French`, `feat(apps): deploy a tag by hand, and warn of a tag that moved`, `feat(apps): choose what a git app follows in its Repository tab`, `feat(apps): register a git app that follows tags`, `feat(apps): word the check and the versions of an app that follows tags`, `feat(apps): list the tags of a git app, and deploy one by name`; the stat line reads `10 files changed`.

The branch stays local, and the task ends here.

**For the owner, not the executor: do not run.** A manual check on a test repository, once the AppManagement side of this feature runs on a box (it tags and moves tags, which this plan forbids its executor): register an app in tag mode on a repository tagged `v1.0.0`, deploy it; tag `v1.1.0`, "Check now", read "Newest tag v1.1.0 · deployed v1.0.0", deploy it from "Deploy a tag…"; deploy `v1.0.0` from the list and confirm the older-version dialog; move `v1.1.0` and read the warning and its Redeploy button; switch the app to a branch and back, reading the first-deployment note; in English, then in French.

---

## Spec coverage

| Spec (Dashboard, Tests › Dashboard, and the shared contract) | Task |
|---|---|
| Registration: a choice "Follow a branch / Follow tags"; branch: the current branch field; tags: an optional pattern with its example, the "Include pre-releases (-rc, -beta)" checkbox | 3 (the options carry the spec's words under a `Mode` label; sent through the client of 1) |
| Repository tab, status line: "Newest tag v1.4.2 · deployed v1.4.1", or "Up to date (v1.4.2)" | 2 (`checkSummary`), 4 (shown in "Last check") |
| A moved tag: "The tag v1.4.2 now points at another commit: it is not redeployed automatically", with "Redeploy v1.4.2" | 5 (not shown while the check has an error: the tag it compares is an old one) |
| No eligible tag: the check's message, which names the pattern and the pre-release setting | 2, 4 (`check.error` shown as the server wrote it, after the existing `The check failed:`; the tag the check kept from before is not read) |
| Mode, pattern and checkbox edited where the branch is today; the note "The first deployment in this mode is manual; automatic deployment resumes after it" on a change of mode | 4 (the Mode row replaces the Branch row; the note shows while a new mode is chosen and until the first deployment in the saved mode) |
| "Deploy a tag…" opens the list from `GET …/tags`: name, short commit, label deployed / newest / in history | 1 (client), 2 (`tagLabels`: "in history" only for a version that ran), 5 |
| Deploying a tag lower than the deployed one asks for confirmation ("This is an older version than the one deployed") | 2 (`isOlderTag`), 5 (from the list; the sentence goes on to say that automatic rebuild is paused, as the AppManagement plan decides) |
| History shows "v1.4.1 (abc1234)" instead of the commit alone | 2 (`versionName`), 4 (history) |
| App grid: the badge follows the check, never lit for a lower or a moved tag | no dashboard change: the card's `gitBadge` reads the grid's `new_commits`, which the server computes in semver (Global Constraints); `gitApps.spec.js` already pins that the badge follows `new_commits` alone |
| Webhook guide: GitLab lists "Push events" and "Tag push events" | 4 (step), 6 (words) |
| Every string in the language files, English and French | 6 |
| Tests › Dashboard: mode choice and fields at registration and in the tab; the tag list and its labels; the confirmation of an older tag; the moved-tag warning; history with tags; the GitLab guide | 3, 4, 5 |
| A server older than this feature sends no `follow`: the dashboard behaves as today | 4 (`shows the branch, and nothing of tags, to a server that knows none`; the 21 tests of `inkly/main` run on a fixture without `follow` and pass unchanged) |

### Additions to the spec

None contradicts the spec; each is small and can be dropped on the owner's word without touching the rest.

| Addition | Task | Why |
|---|---|---|
| A tag registration answered without `follow: "tags"` (a server older than this feature) is deleted, and the dialog says `This CasaOS cannot follow tags yet: update it, or follow a branch.` | 3 | such a server registers a branch app, which would deploy every push |
| "Fetch and rebuild" asks the older-version question when the last check's newest tag is older than the deployed one | 5 | the server deploys the highest eligible tag at the time of the request. The guard is best-effort: it reads the last check, so a tag deleted since goes unasked, which is allowed, since a lower tag by hand is |
| The status line `The tag {tag} moved.` for a moved tag, instead of "Newest tag … · deployed …" with the same tag on both sides | 2 | the warning below it says the rest |
| The automatic-rebuild switch and its warning in tag words: `Deploy a newer tag as soon as a check finds it`, `It runs whatever is tagged with a higher version.` | 4 | the branch words are wrong in tag mode |
| The Deployed row names the version as the history does (`v1.4.1 (abc1234)`) | 4 | the same version, named the same way |
| No Deploy button on the listed tag that runs, and the older-version dialog titled with the tag | 5 | nothing to deploy there; the title says which tag is asked about |
| The label `Mode` above the choice, in the dialog and in the tab | 3, 4 | the options carry "Follow a branch" / "Follow tags"; the first-deployment note speaks of "this mode" |
