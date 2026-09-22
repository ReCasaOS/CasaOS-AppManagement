# Git app webhooks: the dashboard (CasaOS-UI) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In CasaOS-UI, add the "Webhook" section to a git app's Repository tab: a "Check on every push" switch, the URL built from the dashboard's origin, the secret hidden behind Show, Copy and a confirmed Regenerate, three how-to tabs, the last delivery in relative time, and a confirmed Off, all backed by the PUT fields `webhook_enabled` and `regenerate_webhook_secret`.

**Architecture:** `GitRepoTab.vue` gets the whole section and reuses the tab's own `run()`, `busy`, `canAct` and `error` for every request, since `$api.gitApps.update` already sends any PUT body. The one piece of logic worth checking without a mount, "how long ago", goes to `src/components/Apps/gitApps.js` as `timeAgo`, built on the browser's `Intl.RelativeTimeFormat`, so no dependency is added. The words go to `en_US.json` and `fr_FR.json`, keyed by their English text like every other GitRepoTab string.

**Tech Stack:** Vue 3.5 (options API), Buefy 3.1.0 (`b-switch`, `b-button`, `b-tabs`/`b-tab-item`, `$buefy.dialog.confirm`, `$buefy.toast`), vue-i18n 9 in legacy mode, `clipboard-copy`, vitest 4.1 with happy-dom 20 and @vue/test-utils 2.

**Spec:** D:/clients/casaos/CasaOS-AppManagement/docs/superpowers/specs/2026-09-23-git-webhooks-design.md

## Global Constraints

- Repository `D:/clients/casaos/CasaOS-UI`, branch `feat/git-webhooks` created from a freshly fetched `inkly/main` when the plan is executed (`inkly/main` was `4dd9720` when this was written). Every command runs in Git Bash from `D:/clients/casaos/CasaOS-UI`.
- `feat/telemetry` (CasaOS-UI, `7a9a94e`, not on `inkly/main` when this was written) appends 8 keys at the end of the same two language files, `"Turn off"` among them. Task 3 appends after whatever ends each file and adds only the keys its check finds missing, so an existing `"Turn off"` is reused, never doubled. If `feat/telemetry` lands after this branch instead, the second merge conflicts at the end of both files: keep both blocks with a single `"Turn off"` (same value on both sides), then re-run Task 3's check.
- Route of the webhook: `POST /v2/app_management/git/{app}/webhook`. The dashboard never calls it. It shows it as `window.location.origin + webhook.path`.
- App view (`GET /v2/app_management/git/{app}` and every answer that returns the app) carries `"webhook": {"enabled": bool, "path": "/v2/app_management/git/<app>/webhook", "secret": "<64 hex>" (only while enabled), "last_delivery": {"at": RFC 3339, "forge": "github"|"gitea"|"forgejo"|"gogs"|"gitlab"|"unknown", "event": string, "result": "checked"|"coalesced"|"queued"|"ignored"|"pong"} | null}`.
- `PUT /v2/app_management/git/{app}` takes two more optional fields, `"webhook_enabled": bool` and `"regenerate_webhook_secret": bool` (400 when the webhook is disabled). The dashboard sends exactly `{ webhook_enabled: true }`, `{ webhook_enabled: false }` or `{ regenerate_webhook_secret: true }`, one per request.
- Enabling generates the secret, disabling forgets it, regenerating replaces it at once. A restored app comes back with its webhook disabled, and the dashboard says so.
- A server whose view has no `webhook` (an AppManagement older than this feature) shows no Webhook section at all. The dashboard and AppManagement are released separately.
- Relative time comes from `Intl.RelativeTimeFormat`. Add no dependency: `dayjs` is installed but no file under `src/` uses it.
- New strings go to `src/assets/lang/en_US.json` and `src/assets/lang/fr_FR.json` only; the other languages fall back to `en_us`, as every git app string does today. In `en_US.json` the value is the key. `fr_FR.json` gets real French with a space before `:`. No key contains `|` or `@`.
- Code style is `eslint.config.mjs` (@antfu/eslint-config): tabs, single quotes, no semicolons, `<template>` then `<script>` then `<style>`. Lint gate: `npx eslint . --quiet` exits 0. Never run a linter or formatter with `--fix`.
- pnpm is `npx -y pnpm@9.0.6`. One spec: `npx -y pnpm@9.0.6 exec vitest run <spec path>`. `node_modules` is already installed: do not run `pnpm install`.
- A component spec mounts with `mount(Component, { global: { plugins: [Buefy, i18n], mocks } })` and mocks `@/assets/lang`, so `$t` returns its key uninterpolated unless the mock table holds that key.
- The working tree has CRLF line endings (`core.autocrlf=true`). Change an existing file with the Edit tool, replacing exactly the block a step shows. Never rewrite an existing file whole.
- Commits: stage files by name, then `git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "..."`. No `Co-Authored-By` line and no tool or AI attribution in any message. Never push.
- Forbidden: `git push`, `git merge`, `git pull`, `git reset --hard`, `git checkout -- <path>`, `git restore`, `git stash`, `rm -rf`, `pnpm install`, `npx eslint --fix`, `npm run lint -- --fix`, editing anything under `src/openapi/`, editing a file this plan does not list.

---

## File structure

| File | Change | Responsibility |
|---|---|---|
| `src/components/Apps/gitApps.js` | modify (append after `appendLog`, line 125) | `timeAgo(at, locale, now)`: how long ago, in the owner's language |
| `src/components/Apps/gitApps.spec.js` | modify (import line 2, append at the end) | checks `timeAgo` in English and French, and a time in the future |
| `src/components/Apps/GitRepoTab.vue` | modify (lines 80, 87-89, 127-130, 150-154, 179-182, 187-189, 268-271, style) | the Webhook section and its three requests |
| `src/components/Apps/GitRepoTab.spec.js` | modify (lines 6-9, 40-41, append after line 219) | the webhook specs: URL from the origin, secret, Regenerate and Off confirmations, last delivery, PUT bodies, guides |
| `src/service/gitApps.js` | modify (line 21, comment only) | documents the two new PUT fields |
| `src/assets/lang/en_US.json` | modify (append after the file's last entry at execution time) | the new English strings: 36, minus those already there (35 if `feat/telemetry` is merged) |
| `src/assets/lang/fr_FR.json` | modify (append after the file's last entry at execution time) | the same strings in French |

---

### Task 1: How long ago, in the owner's language

**Files:**
- Modify: `src/components/Apps/gitApps.js` (append after the end of `appendLog`, lines 117-125)
- Test: `src/components/Apps/gitApps.spec.js` (import on line 2, append after the last `describe`)

**Interfaces:**
- Consumes: nothing new.
- Produces: `export function timeAgo(at: string, locale: string, now: number = Date.now()): string`. `at` is an RFC 3339 time. `locale` is a vue-i18n locale such as `en_us` or `fr_fr`. The result is `Intl.RelativeTimeFormat(locale with "_" turned into "-", { numeric: 'auto' })` wording in the largest whole unit among day, hour, minute and second: `"3 minutes ago"`, `"il y a 3 minutes"`, `"now"`. A time in the future reads `"now"`.

- [ ] **Step 1: Create the branch**

Run:

```bash
git fetch inkly && test -z "$(git status --short --untracked-files=no)" && git switch -c feat/git-webhooks inkly/main && git log --oneline -1
```

Expected: `Switched to a new branch 'feat/git-webhooks'`, then the head commit of the freshly fetched `inkly/main`. If the command stops before the switch (the fetch fails, or a tracked file is modified or staged), stop and report: nothing has been switched, and the tree belongs to another piece of work.

- [ ] **Step 2: Write the failing test**

In `src/components/Apps/gitApps.spec.js`, replace line 2:

```js
import { appendLog, canFollow, canRevert, checkEnded, checkSummary, gitBadge, outcomeTag, projectName, sensitiveLabel, sensitiveServices, shortCommit, withoutContainer } from './gitApps'
```

with:

```js
import { appendLog, canFollow, canRevert, checkEnded, checkSummary, gitBadge, outcomeTag, projectName, sensitiveLabel, sensitiveServices, shortCommit, timeAgo, withoutContainer } from './gitApps'
```

Then replace the end of the file:

```js
		expect(log.endsWith('xlast\n')).toBe(true)
	})
})
```

with:

```js
		expect(log.endsWith('xlast\n')).toBe(true)
	})
})

describe('how long ago a webhook delivery came', () => {
	const now = Date.UTC(2026, 8, 23, 12, 0, 0)
	const before = seconds => new Date(now - seconds * 1000).toISOString()

	it.each([
		[0, 'en_us', 'now'],
		[45, 'en_us', '45 seconds ago'],
		[180, 'en_us', '3 minutes ago'],
		[180, 'fr_fr', 'il y a 3 minutes'],
		[5400, 'en_us', '1 hour ago'],
		[3 * 86400, 'fr_fr', 'il y a 3 jours'],
	])('%i seconds before, in %s, is %s', (seconds, locale, words) => {
		expect(timeAgo(before(seconds), locale, now)).toBe(words)
	})

	it('reads a time in the future as now: the clocks disagree, nothing came later', () => {
		expect(timeAgo(before(-30), 'en_us', now)).toBe('now')
	})
})
```

- [ ] **Step 3: Run it and see it fail**

Run: `npx -y pnpm@9.0.6 exec vitest run src/components/Apps/gitApps.spec.js`

Expected: FAIL. Either the 7 new tests fail with `TypeError: ... timeAgo is not a function`, or the file fails to load with `does not provide an export named 'timeAgo'`. The existing tests of the file are unaffected in the first case.

- [ ] **Step 4: Implement `timeAgo`**

In `src/components/Apps/gitApps.js`, replace the end of `appendLog`:

```js
	const next = log + (lines.endsWith('\n') ? lines : `${lines}\n`)
	return next.length > LOG_LIMIT ? next.slice(-LOG_LIMIT) : next
}
```

with:

```js
	const next = log + (lines.endsWith('\n') ? lines : `${lines}\n`)
	return next.length > LOG_LIMIT ? next.slice(-LOG_LIMIT) : next
}

// Seconds in each unit timeAgo words a time in, largest first.
const UNITS = [['day', 86400], ['hour', 3600], ['minute', 60], ['second', 1]]

// How long ago `at` (RFC 3339) was, in the words of `locale`, a vue-i18n locale
// such as en_us: "3 minutes ago", "il y a 3 minutes". A time in the future is a
// clock that disagrees with this one, and reads "now".
export function timeAgo(at, locale, now = Date.now()) {
	const seconds = Math.max(0, Math.round((now - new Date(at).getTime()) / 1000))
	const [unit, size] = UNITS.find(entry => seconds >= entry[1]) || UNITS.at(-1)
	return new Intl.RelativeTimeFormat(locale.replace('_', '-'), { numeric: 'auto' }).format(-Math.floor(seconds / size), unit)
}
```

- [ ] **Step 5: Run it and see it pass**

Run: `npx -y pnpm@9.0.6 exec vitest run src/components/Apps/gitApps.spec.js`

Expected: PASS, every test of the file, 7 more than on `inkly/main`.

Then run: `npx eslint src/components/Apps/gitApps.js src/components/Apps/gitApps.spec.js --quiet; echo "exit=$?"`

Expected: no report, `exit=0`.

- [ ] **Step 6: Commit**

```bash
git add src/components/Apps/gitApps.js src/components/Apps/gitApps.spec.js
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): how long ago, in the owner's language"
```

---

### Task 2: The Webhook section of the Repository tab

**Files:**
- Modify: `src/components/Apps/GitRepoTab.vue` (template line 80 and lines 87-89; script lines 127-130, 150-154, 179-182, 187-189, 268-271; style `.git-repo-tab__log`, lines 316-319)
- Modify: `src/service/gitApps.js` (line 21, comment only)
- Test: `src/components/Apps/GitRepoTab.spec.js` (lines 6-9, 40-41, append after line 219)

**Interfaces:**
- Consumes: `timeAgo(at, locale, now?)` from Task 1; `this.$api.gitApps.update(app: string, body: object): Promise<{ data: { data: GitApp } }>` (unchanged client, `PUT /v2/app_management/git/{app}`); `GitApp.webhook` as in the Global Constraints; the tab's existing `run(kind, request)`, `busy`, `canAct`, `error`, `appOf`.
- Produces, in `GitRepoTab`:
  - data `webhookOn: boolean` (the switch, `gitApp.webhook.enabled` until the owner moves it), `showSecret: boolean` (false), `howTo: 'GitHub'|'Gitea/Forgejo'|'GitLab'` ('GitHub'), `howTos` (the `HOW_TO` table);
  - computed `webhook: object|null`, `webhookUrl: string` (`window.location.origin + webhook.path`), `deliveryLine: string` (`"Received 3 minutes ago · GitHub · push · checked"`, the event left out when empty; the time is worded in the owner's locale only when that locale holds `Received {when}`, in `en_us` otherwise, like the message itself);
  - methods `setWebhook(value: boolean)`, `saveWebhook(body: object): Promise<void>`, `confirmRegenerate()`, `copyText(text: string)` (replaces `copyKey`);
  - `busy` values `'webhook'` and `'secret'`;
  - DOM: the webhook switch is the second `input[type="checkbox"]` of the tab; buttons labelled `Copy` (URL first, secret second), `Show`/`Hide`, `Regenerate`; `.tabs li` labelled `GitHub`, `Gitea/Forgejo`, `GitLab`.
  - Requests: `update(appId, { webhook_enabled: true })` at once when switched on; `update(appId, { webhook_enabled: false })` only after the Off confirmation; `update(appId, { regenerate_webhook_secret: true })` only after the Regenerate confirmation.

- [ ] **Step 1: Write the failing tests**

In `src/components/Apps/GitRepoTab.spec.js`, replace lines 6-9:

```js
import i18n from '@/plugins/i18n'

// require.context has no Vite equivalent; an empty table makes $t return its key.
vi.mock('@/assets/lang', () => ({ default: { en_us: {} } }))
```

with:

```js
import i18n from '@/plugins/i18n'

// require.context has no Vite equivalent; a table this small makes $t return its
// key, except for the one message the last delivery needs filled in.
vi.mock('@/assets/lang', () => ({ default: { en_us: { 'Received {when}': 'Received {when}' } } }))

const copy = vi.hoisted(() => vi.fn())
vi.mock('clipboard-copy', () => ({ default: copy }))
```

Replace lines 40-41 of the fixture:

```js
		build_log: 'old build\n',
		...fields,
```

with:

```js
		build_log: 'old build\n',
		webhook: { enabled: false, path: '/v2/app_management/git/jarvis/webhook', last_delivery: null },
		...fields,
```

Replace the end of the file (lines 213-219):

```js
	it('shows a stopped automatic rebuild before a paused one', () => {
		const { wrapper } = setup({ blocked: true, auto_paused: true })
		expect(wrapper.text()).toContain('A rollback failed')
		expect(wrapper.text()).not.toContain('Automatic rebuild is paused')
		wrapper.unmount()
	})
})
```

with:

```js
	it('shows a stopped automatic rebuild before a paused one', () => {
		const { wrapper } = setup({ blocked: true, auto_paused: true })
		expect(wrapper.text()).toContain('A rollback failed')
		expect(wrapper.text()).not.toContain('Automatic rebuild is paused')
		wrapper.unmount()
	})
})

describe('the webhook of a git app', () => {
	const PATH = '/v2/app_management/git/jarvis/webhook'
	const SECRET = '0123456789abcdef'.repeat(4)
	const on = (fields = {}) => ({ webhook: { enabled: true, path: PATH, secret: SECRET, last_delivery: null, ...fields } })
	const off = { webhook: { enabled: false, path: PATH, last_delivery: null } }
	// the first switch of the tab is automatic rebuild's
	const webhookSwitch = wrapper => wrapper.findAll('input[type="checkbox"]')[1]
	const copyButtons = wrapper => wrapper.findAll('button').filter(b => b.text() === 'Copy')

	afterEach(() => {
		vi.unstubAllGlobals()
		copy.mockClear()
		i18n.global.locale = 'en_us'
	})

	it('turns on at once, and the switch goes back when the server refuses', async () => {
		const { wrapper, gitApps, confirm } = setup()
		expect(wrapper.text()).toContain('A restored app comes back with its webhook off')
		expect(wrapper.text()).not.toContain('How to set it up')

		await webhookSwitch(wrapper).setValue(true)
		await flushPromises()
		expect(confirm).not.toHaveBeenCalled()
		expect(gitApps.update).toHaveBeenCalledWith('jarvis', { webhook_enabled: true })
		expect(wrapper.emitted('change')).toHaveLength(1)
		wrapper.unmount()

		const update = vi.fn().mockRejectedValue({ response: { status: 409, data: { message: 'deploy is running' } } })
		const refused = setup({}, { update })
		await webhookSwitch(refused.wrapper).setValue(true)
		await flushPromises()
		expect(update).toHaveBeenCalledWith('jarvis', { webhook_enabled: true })
		expect(webhookSwitch(refused.wrapper).element.checked).toBe(false)
		expect(refused.wrapper.text()).toContain('deploy is running')
		refused.wrapper.unmount()
	})

	it('shows nothing of a webhook to a server that has none', () => {
		const { wrapper } = setup({ webhook: undefined })
		expect(wrapper.text()).not.toContain('Check on every push')
		expect(wrapper.findAll('input[type="checkbox"]')).toHaveLength(1)
		wrapper.unmount()
	})

	it('builds the URL from the address the dashboard is open at, and copies it', async () => {
		vi.stubGlobal('location', { origin: 'https://casa.example.com' })
		const { wrapper } = setup(on())
		const url = `https://casa.example.com${PATH}`
		expect(wrapper.text()).toContain(url)
		expect(wrapper.text()).toContain('If your forge reaches the box by another address')

		await copyButtons(wrapper)[0].trigger('click')
		expect(copy).toHaveBeenCalledWith(url)
		wrapper.unmount()
	})

	it('keeps the secret hidden until asked, and copies it either way', async () => {
		const { wrapper, click } = setup(on())
		expect(wrapper.text()).not.toContain(SECRET)
		await copyButtons(wrapper)[1].trigger('click')
		expect(copy).toHaveBeenCalledWith(SECRET)

		await click('Show')
		expect(wrapper.text()).toContain(SECRET)
		await click('Hide')
		expect(wrapper.text()).not.toContain(SECRET)
		wrapper.unmount()
	})

	it('regenerates the secret only once confirmed', async () => {
		const { wrapper, gitApps, confirm, click } = setup(on())
		await click('Regenerate')
		expect(gitApps.update).not.toHaveBeenCalled()
		expect(confirm.mock.calls[0][0].message).toBe('The forge is refused from this moment until it is given the new secret.')

		confirm.mock.calls[0][0].onConfirm()
		await flushPromises()
		expect(gitApps.update).toHaveBeenCalledWith('jarvis', { regenerate_webhook_secret: true })
		wrapper.unmount()
	})

	it('turns off only once confirmed, then folds the section', async () => {
		const update = vi.fn().mockResolvedValue({ data: { data: gitApp(off) } })
		const { wrapper, confirm } = setup(on(), { update })

		await webhookSwitch(wrapper).setValue(false)
		expect(update).not.toHaveBeenCalled()
		expect(confirm.mock.calls[0][0].confirmText).toBe('Turn off')
		confirm.mock.calls[0][0].onCancel()
		await flushPromises()
		expect(webhookSwitch(wrapper).element.checked).toBe(true)

		await webhookSwitch(wrapper).setValue(false)
		confirm.mock.calls[1][0].onConfirm()
		await flushPromises()
		expect(update).toHaveBeenCalledWith('jarvis', { webhook_enabled: false })

		// the panel hands the answer back down
		await wrapper.setProps({ gitApp: wrapper.emitted('change').at(-1)[0] })
		expect(wrapper.text()).not.toContain('How to set it up')
		expect(webhookSwitch(wrapper).element.checked).toBe(false)
		wrapper.unmount()
	})

	it('says what the last delivery was, and how long ago', () => {
		const at = new Date(Date.now() - 3 * 60 * 1000).toISOString()
		const github = setup(on({ last_delivery: { at, forge: 'github', event: 'push', result: 'checked' } }))
		expect(github.wrapper.text()).toContain('Received 3 minutes ago · GitHub · push · checked')
		github.wrapper.unmount()

		// a signed request with no event header the server knows
		const unknown = setup(on({ last_delivery: { at, forge: 'unknown', event: '', result: 'queued' } }))
		expect(unknown.wrapper.text()).toContain('Received 3 minutes ago · Unknown forge · queued')
		unknown.wrapper.unmount()

		const none = setup(on())
		expect(none.wrapper.text()).toContain('No delivery yet.')
		expect(none.wrapper.text()).toContain('GitHub sends a ping as the webhook is saved')
		none.wrapper.unmount()

		// a language without the message falls back to English, and so does the time
		i18n.global.locale = 'de_de'
		const german = setup(on({ last_delivery: { at, forge: 'github', event: 'push', result: 'checked' } }))
		expect(german.wrapper.text()).toContain('Received 3 minutes ago · GitHub · push · checked')
		german.wrapper.unmount()
	})

	it('shows how to set it up on GitHub, on Gitea or Forgejo, and on GitLab', async () => {
		const { wrapper } = setup(on())
		await flushPromises()
		expect(wrapper.findAll('.tabs li').map(li => li.text())).toEqual(['GitHub', 'Gitea/Forgejo', 'GitLab'])
		expect(wrapper.text()).toContain('Which events: “Just the push event”.')
		wrapper.unmount()
	})
})
```

- [ ] **Step 2: Run them and see them fail**

Run: `npx -y pnpm@9.0.6 exec vitest run src/components/Apps/GitRepoTab.spec.js`

Expected: FAIL, `7 failed | 12 passed (19)`. The 11 tests of `describe('gitRepoTab')` pass (the component ignores `webhook` for now), and so does `shows nothing of a webhook to a server that has none`, which guards the absence of the section. The 7 other webhook tests fail with AssertionErrors on missing texts (for example `expected [] to deeply equal [ 'GitHub', … ]`) and with `TypeError: Cannot read properties of undefined (reading 'setValue')` or `(reading 'trigger')`.

- [ ] **Step 3: The template**

In `src/components/Apps/GitRepoTab.vue`, replace line 80:

```html
			<b-button :label="$t('Copy')" rounded size="is-small" @click="copyKey"></b-button>
```

with:

```html
			<b-button :label="$t('Copy')" rounded size="is-small" @click="copyText(gitApp.public_key)"></b-button>
```

Replace lines 87-89:

```html
		<p class="is-size-7 has-text-danger mt-1">{{ $t('It runs whatever is pushed to the branch.') }}</p>

		<div class="is-flex mt-4">
```

with:

```html
		<p class="is-size-7 has-text-danger mt-1">{{ $t('It runs whatever is pushed to the branch.') }}</p>

		<!-- an AppManagement older than webhooks sends no `webhook`: nothing to show -->
		<template v-if="webhook">
			<p class="has-text-weight-bold is-size-7 mt-3 mb-2">{{ $t('Webhook') }}</p>
			<b-switch :disabled="!canAct" :model-value="webhookOn" size="is-small" @update:model-value="setWebhook">
				{{ $t('Check on every push') }}
			</b-switch>
			<p v-if="!webhook.enabled" class="is-size-7 has-text-full-03 mt-1">
				{{ $t('A restored app comes back with its webhook off: turn it on again, and give the forge its new secret.') }}
			</p>
			<div v-else class="is-size-7 mt-2">
				<p class="has-text-weight-bold mb-1">{{ $t('URL') }}</p>
				<div class="is-flex is-align-items-center mb-1">
					<code class="git-repo-tab__mono is-flex-grow-1 mr-2">{{ webhookUrl }}</code>
					<b-button :label="$t('Copy')" rounded size="is-small" @click="copyText(webhookUrl)"></b-button>
				</div>
				<p class="has-text-full-03 mb-2">{{ $t('If your forge reaches the box by another address (a domain, a tunnel), use that one instead.') }}</p>

				<p class="has-text-weight-bold mb-1">{{ $t('Secret') }}</p>
				<div class="is-flex is-align-items-center is-flex-wrap-wrap mb-2">
					<!-- hidden until asked: the screen may be shared -->
					<code class="git-repo-tab__mono is-flex-grow-1 mr-2 mb-1">{{ showSecret ? webhook.secret : '••••••••••••••••' }}</code>
					<b-button :label="showSecret ? $t('Hide') : $t('Show')" class="mr-2 mb-1" rounded size="is-small" @click="showSecret = !showSecret"></b-button>
					<b-button :label="$t('Copy')" class="mr-2 mb-1" rounded size="is-small" @click="copyText(webhook.secret)"></b-button>
					<b-button :disabled="!canAct" :label="$t('Regenerate')" :loading="busy === 'secret'" class="mb-1" rounded size="is-small" @click="confirmRegenerate"></b-button>
				</div>

				<p class="has-text-weight-bold mb-1">{{ $t('How to set it up') }}</p>
				<b-tabs v-model="howTo" :animated="false" size="is-small">
					<b-tab-item v-for="guide in howTos" :key="guide.forge" :label="guide.forge" :value="guide.forge">
						<ol class="git-repo-tab__steps">
							<li v-for="step in guide.steps" :key="step">{{ $t(step) }}</li>
						</ol>
					</b-tab-item>
				</b-tabs>

				<p class="has-text-weight-bold mb-1">{{ $t('Last delivery') }}</p>
				<p v-if="webhook.last_delivery">{{ deliveryLine }}</p>
				<p v-else class="has-text-full-03">
					{{ $t('No delivery yet.') }} {{ $t('The forge must be able to reach the box. GitHub sends a ping as the webhook is saved: that is enough to check.') }}
				</p>
			</div>
		</template>

		<div class="is-flex mt-4">
```

- [ ] **Step 4: The script**

Replace lines 127-130:

```js
import copy from 'clipboard-copy'
import { appendLog, canFollow, canRevert, checkEnded, checkSummary, gitBadge, outcomeTag, shortCommit } from './gitApps'

const appOf = res => res.data.data
```

with:

```js
import copy from 'clipboard-copy'
import { appendLog, canFollow, canRevert, checkEnded, checkSummary, gitBadge, outcomeTag, shortCommit, timeAgo } from './gitApps'

const appOf = res => res.data.data

// The forge of a webhook delivery, as the server names it, as it is written.
const FORGES = { github: 'GitHub', gitea: 'Gitea', forgejo: 'Forgejo', gogs: 'Gogs', gitlab: 'GitLab' }

// Where each forge takes a webhook, and what to fill in. The field names are the
// forges' own English labels.
const HOW_TO = [
	{
		forge: 'GitHub',
		steps: [
			'In the repository: Settings › Webhooks › Add webhook.',
			'Payload URL: the URL above.',
			'Content type: application/json.',
			'Secret: the secret above.',
			'Which events: “Just the push event”.',
		],
	},
	{
		forge: 'Gitea/Forgejo',
		steps: [
			'In the repository: Settings › Webhooks › Add webhook › Gitea (or Forgejo).',
			'Target URL: the URL above.',
			'HTTP method: POST. POST content type: application/json.',
			'Secret: the secret above.',
			'Trigger on: Push events.',
		],
	},
	{
		forge: 'GitLab',
		steps: [
			'In the project: Settings › Webhooks › Add new webhook.',
			'URL: the URL above.',
			'Secret token: the secret above.',
			'Trigger: Push events.',
		],
	},
]
```

Replace lines 150-154:

```js
			access: this.gitApp.access,
			token: '',
			autoDeploy: this.gitApp.auto_deploy,
			// '' or what runs: access, auto, test, check, deploy, or the commit of a revert
			busy: '',
```

with:

```js
			access: this.gitApp.access,
			token: '',
			autoDeploy: this.gitApp.auto_deploy,
			webhookOn: Boolean(this.gitApp.webhook && this.gitApp.webhook.enabled),
			showSecret: false,
			howTo: HOW_TO[0].forge,
			howTos: HOW_TO,
			// '' or what runs: access, auto, webhook, secret, test, check, deploy, or the commit of a revert
			busy: '',
```

Replace lines 179-182:

```js
		log() {
			return this.liveLog === null ? (this.gitApp.build_log || '') : this.liveLog
		},
	},
```

with:

```js
		log() {
			return this.liveLog === null ? (this.gitApp.build_log || '') : this.liveLog
		},
		webhook() {
			return this.gitApp.webhook || null
		},
		// the forge reaches the box where the owner does, unless told otherwise
		webhookUrl() {
			return window.location.origin + this.webhook.path
		},
		// ponytail: worded when the app is read, not redrawn while the tab stays open; add a minute timer if owners watch it
		deliveryLine() {
			const delivery = this.webhook.last_delivery
			// the time in the language of the message: a language without it falls
			// back to English, and a free-text locale never reaches Intl
			const locale = this.$te('Received {when}') ? this.$i18n.locale : 'en_us'
			return [
				this.$t('Received {when}', { when: timeAgo(delivery.at, locale) }),
				FORGES[delivery.forge] || this.$t('Unknown forge'),
				delivery.event,
				this.$t(delivery.result),
			].filter(Boolean).join(' · ')
		},
	},
```

Replace lines 187-189:

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
		'gitApp.webhook.enabled': function (value) {
			this.webhookOn = Boolean(value)
			this.showSecret = false
		},
```

Replace lines 268-271:

```js
		copyKey() {
			copy(this.gitApp.public_key)
			this.$buefy.toast.open({ message: this.$t('Copied to clipboard'), type: 'is-success' })
		},
```

with:

```js
		copyText(text) {
			copy(text)
			this.$buefy.toast.open({ message: this.$t('Copied to clipboard'), type: 'is-success' })
		},

		// On at once; off only once confirmed, since the secret goes with it.
		setWebhook(value) {
			this.webhookOn = value
			if (value)
				return this.saveWebhook({ webhook_enabled: true })
			this.$buefy.dialog.confirm({
				title: this.$t('Turn the webhook off'),
				message: this.$t('The secret is forgotten. Turning the webhook on again makes a new one, to give the forge again.'),
				confirmText: this.$t('Turn off'),
				cancelText: this.$t('Cancel'),
				type: 'is-warning',
				onConfirm: () => this.saveWebhook({ webhook_enabled: false }),
				onCancel: () => {
					this.webhookOn = true
				},
			})
		},

		// The switch goes back to what the server has when it refuses.
		async saveWebhook(body) {
			if (!await this.run('webhook', () => this.$api.gitApps.update(this.appId, body).then(appOf)))
				this.webhookOn = Boolean(this.webhook.enabled)
		},

		confirmRegenerate() {
			this.$buefy.dialog.confirm({
				title: this.$t('Regenerate the secret'),
				message: this.$t('The forge is refused from this moment until it is given the new secret.'),
				confirmText: this.$t('Regenerate'),
				cancelText: this.$t('Cancel'),
				type: 'is-warning',
				onConfirm: () => this.run('secret', () => this.$api.gitApps.update(this.appId, { regenerate_webhook_secret: true }).then(appOf)),
			})
		},
```

- [ ] **Step 5: The style**

Replace, in the `<style>` block:

```scss
.git-repo-tab__log {
	max-height: 16rem;
	overflow-y: auto;
}
```

with:

```scss
.git-repo-tab__log {
	max-height: 16rem;
	overflow-y: auto;
}

.git-repo-tab__steps {
	list-style: decimal;
	padding-left: 1.5rem;
}
```

- [ ] **Step 6: The client's comment**

In `src/service/gitApps.js`, replace line 21:

```js
	// Any of `{ branch, auto_deploy, access, token }`. Adopts an adoptable app.
```

with:

```js
	// Any of `{ branch, auto_deploy, access, token, webhook_enabled,
	// regenerate_webhook_secret }`. Adopts an adoptable app. Regenerating the
	// secret of a webhook that is off answers 400.
```

- [ ] **Step 7: Run them and see them pass**

Run: `npx -y pnpm@9.0.6 exec vitest run src/components/Apps/GitRepoTab.spec.js`

Expected: PASS, `19 passed (19)`. (`vi.stubGlobal('location', …)` reaches `window.location.origin` in this vitest and `.tabs li` holds the three labels: both checked on a `4dd9720` copy.)

Then run: `npx -y pnpm@9.0.6 exec vitest run src/components/Apps/AppPanel.spec.js src/service/gitApps.spec.js`

Expected: PASS, both files unchanged in count (AppPanel mounts the tab with a fixture that has no `webhook`, and the section stays hidden).

Then run: `npx eslint src/components/Apps/GitRepoTab.vue src/components/Apps/GitRepoTab.spec.js src/service/gitApps.js --quiet; echo "exit=$?"`

Expected: no report, `exit=0`.

- [ ] **Step 8: Commit**

```bash
git add src/components/Apps/GitRepoTab.vue src/components/Apps/GitRepoTab.spec.js src/service/gitApps.js
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): the webhook of a git app in its Repository tab"
```

---

### Task 3: The webhook's words in English and French

**Files:**
- Modify: `src/assets/lang/en_US.json` (append after the file's last entry at execution time, before the closing `}`)
- Modify: `src/assets/lang/fr_FR.json` (same place)
- Test: the key check below (reads `GitRepoTab.vue` and both language files; fails on a missing, wrong or doubled key)

**Interfaces:**
- Consumes: every `$t('…')` key of `GitRepoTab.vue`, the `HOW_TO` steps (one quoted string per line, three tabs deep), and the delivery results `checked`, `coalesced`, `queued`, `ignored`, `pong` (shown through `$t(delivery.result)`).
- Produces: in each file, the keys the Step 1 check lists as wrong: 36 on `inkly/main@4dd9720`, 35 if `feat/telemetry` is merged (it brings `"Turn off"`). `Show`, `Hide`, `Copy`, `Cancel` and `Copied to clipboard` already exist in both and are reused, as is any key already right.

- [ ] **Step 1: Run the key check and see it fail**

Run:

```bash
node - <<'EOF'
const fs = require('node:fs')
const text = name => fs.readFileSync(`src/assets/lang/${name}.json`, 'utf8')
const en = JSON.parse(text('en_US'))
const fr = JSON.parse(text('fr_FR'))
const source = fs.readFileSync('src/components/Apps/GitRepoTab.vue', 'utf8')
// the results of a delivery reach $t through a variable
const keys = new Set(['checked', 'coalesced', 'queued', 'ignored', 'pong'])
for (const match of source.matchAll(/\$t\('([^']+)'/g))
	keys.add(match[1])
// the steps of HOW_TO, one quoted string per line
for (const match of source.matchAll(/^\t{3}'([^']+)',\r?$/gm))
	keys.add(match[1])
const wrong = [...keys].filter(key => en[key] !== key || !fr[key] || /[|@]/.test(key))
// JSON.parse keeps the last of two equal keys without a word: count them in the text
const doubled = ['en_US', 'fr_FR'].flatMap((name) => {
	const seen = {}
	for (const match of text(name).matchAll(/^ {2}"(.+?)": /gm))
		seen[match[1]] = (seen[match[1]] || 0) + 1
	return Object.keys(seen).filter(key => seen[key] > 1).map(key => `${name}: ${key}`)
})
console.log(`${keys.size} keys checked, ${wrong.length} wrong: ${JSON.stringify(wrong)}`)
console.log(`${doubled.length} doubled: ${JSON.stringify(doubled)}`)
process.exit(wrong.length || doubled.length ? 1 : 0)
EOF
echo "exit=$?"
```

Expected: `72 keys checked, 36 wrong: [...]` on `inkly/main@4dd9720`, or `35 wrong` if `feat/telemetry` is merged (`"Turn off"` is already right); in general 36 minus the keys already present. Then `0 doubled: []` and `exit=1`. Keep the list: Steps 2 and 3 add exactly those keys.

- [ ] **Step 2: Add the English strings**

Run `tail -n 2 src/assets/lang/en_US.json`. It prints the file's last entry, without a trailing comma, then `}`. That entry is `"Cloning the repository…"` on `inkly/main@4dd9720` and `"The setting could not be saved."` once `feat/telemetry` is merged; take whatever it is.

With the Edit tool, replace those two lines with: the same last entry followed by `,`; then, in the order below, every line whose key Step 1 listed as wrong; then `}`. Leave out any line whose key Step 1 did not list (`"Turn off"` when `feat/telemetry` is merged): the file already has it. The last line added takes no trailing comma.

The lines, all 36:

```json
  "Webhook": "Webhook",
  "Check on every push": "Check on every push",
  "A restored app comes back with its webhook off: turn it on again, and give the forge its new secret.": "A restored app comes back with its webhook off: turn it on again, and give the forge its new secret.",
  "URL": "URL",
  "If your forge reaches the box by another address (a domain, a tunnel), use that one instead.": "If your forge reaches the box by another address (a domain, a tunnel), use that one instead.",
  "Secret": "Secret",
  "Regenerate": "Regenerate",
  "Regenerate the secret": "Regenerate the secret",
  "The forge is refused from this moment until it is given the new secret.": "The forge is refused from this moment until it is given the new secret.",
  "How to set it up": "How to set it up",
  "In the repository: Settings › Webhooks › Add webhook.": "In the repository: Settings › Webhooks › Add webhook.",
  "Payload URL: the URL above.": "Payload URL: the URL above.",
  "Content type: application/json.": "Content type: application/json.",
  "Secret: the secret above.": "Secret: the secret above.",
  "Which events: “Just the push event”.": "Which events: “Just the push event”.",
  "In the repository: Settings › Webhooks › Add webhook › Gitea (or Forgejo).": "In the repository: Settings › Webhooks › Add webhook › Gitea (or Forgejo).",
  "Target URL: the URL above.": "Target URL: the URL above.",
  "HTTP method: POST. POST content type: application/json.": "HTTP method: POST. POST content type: application/json.",
  "Trigger on: Push events.": "Trigger on: Push events.",
  "In the project: Settings › Webhooks › Add new webhook.": "In the project: Settings › Webhooks › Add new webhook.",
  "URL: the URL above.": "URL: the URL above.",
  "Secret token: the secret above.": "Secret token: the secret above.",
  "Trigger: Push events.": "Trigger: Push events.",
  "Last delivery": "Last delivery",
  "Received {when}": "Received {when}",
  "Unknown forge": "Unknown forge",
  "checked": "checked",
  "coalesced": "coalesced",
  "queued": "queued",
  "ignored": "ignored",
  "pong": "pong",
  "No delivery yet.": "No delivery yet.",
  "The forge must be able to reach the box. GitHub sends a ping as the webhook is saved: that is enough to check.": "The forge must be able to reach the box. GitHub sends a ping as the webhook is saved: that is enough to check.",
  "Turn the webhook off": "Turn the webhook off",
  "The secret is forgotten. Turning the webhook on again makes a new one, to give the forge again.": "The secret is forgotten. Turning the webhook on again makes a new one, to give the forge again.",
  "Turn off": "Turn off"
```

- [ ] **Step 3: Add the French strings**

Same as Step 2 in `src/assets/lang/fr_FR.json`: run `tail -n 2 src/assets/lang/fr_FR.json` (`"Cloning the repository…": "Clonage du dépôt…"` on `inkly/main@4dd9720`, `"The setting could not be saved.": "Le réglage n'a pas pu être enregistré."` once `feat/telemetry` is merged), then with the Edit tool replace those two lines with that entry followed by `,`, the lines below whose key Step 1 listed as wrong, in this order, the last one without a trailing comma, and `}`.

The lines, all 36:

```json
  "Webhook": "Webhook",
  "Check on every push": "Vérifier à chaque push",
  "A restored app comes back with its webhook off: turn it on again, and give the forge its new secret.": "Une application restaurée revient avec son webhook désactivé : réactivez-le, et donnez à la forge son nouveau secret.",
  "URL": "URL",
  "If your forge reaches the box by another address (a domain, a tunnel), use that one instead.": "Si votre forge joint la box par une autre adresse (un domaine, un tunnel), utilisez plutôt celle-ci.",
  "Secret": "Secret",
  "Regenerate": "Régénérer",
  "Regenerate the secret": "Régénérer le secret",
  "The forge is refused from this moment until it is given the new secret.": "La forge est refusée dès maintenant, jusqu'à ce qu'elle reçoive le nouveau secret.",
  "How to set it up": "Comment le configurer",
  "In the repository: Settings › Webhooks › Add webhook.": "Dans le dépôt : Settings › Webhooks › Add webhook.",
  "Payload URL: the URL above.": "Payload URL : l'URL ci-dessus.",
  "Content type: application/json.": "Content type : application/json.",
  "Secret: the secret above.": "Secret : le secret ci-dessus.",
  "Which events: “Just the push event”.": "Which events : « Just the push event ».",
  "In the repository: Settings › Webhooks › Add webhook › Gitea (or Forgejo).": "Dans le dépôt : Settings › Webhooks › Add webhook › Gitea (ou Forgejo).",
  "Target URL: the URL above.": "Target URL : l'URL ci-dessus.",
  "HTTP method: POST. POST content type: application/json.": "HTTP method : POST. POST content type : application/json.",
  "Trigger on: Push events.": "Trigger on : Push events.",
  "In the project: Settings › Webhooks › Add new webhook.": "Dans le projet : Settings › Webhooks › Add new webhook.",
  "URL: the URL above.": "URL : l'URL ci-dessus.",
  "Secret token: the secret above.": "Secret token : le secret ci-dessus.",
  "Trigger: Push events.": "Trigger : Push events.",
  "Last delivery": "Dernière livraison",
  "Received {when}": "Reçue {when}",
  "Unknown forge": "Forge inconnue",
  "checked": "vérification lancée",
  "coalesced": "regroupée avec la précédente",
  "queued": "en attente",
  "ignored": "ignorée",
  "pong": "pong",
  "No delivery yet.": "Aucune livraison pour l'instant.",
  "The forge must be able to reach the box. GitHub sends a ping as the webhook is saved: that is enough to check.": "La forge doit pouvoir joindre la box. GitHub envoie un ping à l'enregistrement du webhook : cela suffit pour vérifier.",
  "Turn the webhook off": "Désactiver le webhook",
  "The secret is forgotten. Turning the webhook on again makes a new one, to give the forge again.": "Le secret est oublié. Réactiver le webhook en crée un nouveau, à redonner à la forge.",
  "Turn off": "Désactiver"
```

- [ ] **Step 4: Run the key check and see it pass**

Run the same `node - <<'EOF' … EOF` command as Step 1.

Expected: `72 keys checked, 0 wrong: []`, `0 doubled: []`, then `exit=0`. The script also proves both files still parse as JSON and hold no key twice. (Checked on a `4dd9720` copy: 36 wrong before, 0 after; with `feat/telemetry`'s files, 35 wrong; with the 36 lines appended over `feat/telemetry` anyway, `2 doubled: ["en_US: Turn off","fr_FR: Turn off"]` and `exit=1`.)

- [ ] **Step 5: Commit**

```bash
git add src/assets/lang/en_US.json src/assets/lang/fr_FR.json
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(apps): the webhook's words in English and French"
```

---

### Task 4: The branch as a whole

**Files:**
- None changed. If a step fails, fix the file the failure names, in the task that created it, re-run that task's spec, and commit the fix on its own.

**Interfaces:**
- Consumes: Tasks 1-3.
- Produces: branch `feat/git-webhooks`, three commits on `inkly/main`, green on the whole test suite and the lint gate, not pushed.

- [ ] **Step 1: The whole test suite, once**

Run:

```bash
npx -y pnpm@9.0.6 exec vitest run > "${TMPDIR:-/tmp}/git-webhooks-vitest.log" 2>&1; echo "exit=$?"; grep -E "Test Files|Tests  " "${TMPDIR:-/tmp}/git-webhooks-vitest.log"
```

Expected: `exit=0`, no failed file and no failed test; the `Tests` line counts 15 more than on `inkly/main` (7 in `gitApps.spec.js`, 8 in `GitRepoTab.spec.js`). If a few suites fail while loading ("Failed to load url", a smoke test timing out after 5000 ms) and none fails an assertion, run the same command once more; read the log file for details instead of re-running.

- [ ] **Step 2: The lint gate**

Run: `npx eslint . --quiet; echo "exit=$?"`

Expected: no report, `exit=0`.

- [ ] **Step 3: The branch holds this plan's work and nothing else**

Run:

```bash
git status --short; git log --oneline inkly/main..HEAD; git diff --stat inkly/main..HEAD | tail -1
```

Expected: `git status --short` lists no modified tracked file; the log lists three commits, newest first: `feat(apps): the webhook's words in English and French`, `feat(apps): the webhook of a git app in its Repository tab`, `feat(apps): how long ago, in the owner's language`; the stat line reads `7 files changed`.

The branch stays local. On a box with the AppManagement side of this feature: turn the webhook on, copy the URL and the secret into a GitHub webhook, check that the ping shows "Received … · GitHub · ping · pong", push, and read the line again, with the language set to English and then to French.

---

## Spec coverage

| Spec (Dashboard, Tests › Dashboard, and the shared contract) | Task |
|---|---|
| A "Webhook" section in the Repository tab, below the existing settings (after Access and Automatic rebuild) | 2 |
| A switch "Check on every push"; on at once with `PUT {webhook_enabled: true}` | 2 |
| URL: the dashboard's current origin followed by `path`, a copy button, the note about another address | 2 |
| Secret: hidden, Show, Copy, Regenerate with confirmation, `PUT {regenerate_webhook_secret: true}` | 2 |
| How to set it up: GitHub (Payload URL, `application/json`, Secret, "Just the push event"), Gitea/Forgejo (Target URL, POST, `application/json`, Secret, push events), GitLab (URL, Secret token, Push events) | 2, 3 |
| Last delivery: "Received 3 min ago · GitHub · push · checked" in relative time, or "No delivery yet" with the reminder that the forge must reach the box and GitHub's ping is enough; the time in the language of the message (English where the message falls back) | 1, 2 |
| Turning it off asks for confirmation (the secret is forgotten), sends `PUT {webhook_enabled: false}`, and folds the section | 2 |
| A restored app comes back with its webhook disabled; the dashboard says so | 2 (the note shown while the webhook is off) |
| Every string in the language files, English and French at least, keyed like GitRepoTab | 3 |
| Tests › Dashboard: the URL built from the current origin, the confirmations of Regenerate and Off, the last delivery display, the PUT requests sent | 1, 2 |
| Contract: AppManagement and the dashboard ship separately, so a view without `webhook` shows no section | 2 |
