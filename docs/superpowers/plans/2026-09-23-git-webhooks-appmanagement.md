# Git App Webhooks (AppManagement) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A signed push from a forge to `POST /v2/app_management/git/{app}/webhook` makes AppManagement check that git app at once, through the same path as "Check now".

**Architecture:** Pure functions verify the signature headers and name the forge and the event (`service/git_webhook_signature.go`). The secret and the last delivery live in one root-only file per app, `<app>.webhook`, beside the app's state; it exists exactly while the webhook is on and shows in the app's view and in `PUT` (`service/git_webhook.go`). `ReceiveGitWebhook` guards, verifies, then schedules `beginGitCheck` + `runGitCheck` with a per-app debounce and one queued check for a busy app (`service/git_webhook_receive.go`); the route is declared in the OpenAPI spec, and `route/v2.go` lets that one method and path past the JWT and the request validator.

**Tech Stack:** Go 1.26.8, module `github.com/ReCasaOS/CasaOS-AppManagement`, echo v4.15.4, echo-jwt v4.4.0, oapi-codegen v1.12.4 (deepmap), kin-openapi v0.149.0, CasaOS-Common v0.4.27 (logger, external), gotest.tools/v3.

**Spec:** D:/clients/casaos/CasaOS-AppManagement/docs/superpowers/specs/2026-09-23-git-webhooks-design.md

## Global Constraints

- Repository: `D:/clients/casaos/CasaOS-AppManagement`, branch `feat/git-webhooks` (checked out, holds the spec). Work on it; never switch branch.
- Route: `POST /v2/app_management/git/{app}/webhook`, operationId `receiveGitWebhook`, `security: []`.
- JWT exemption: `POST` on a path matching `^/v2/app_management/git/[^/]+/webhook$`, and nothing else; every other method on that path and every other route keeps the JWT.
- The OpenAPI request validator skips that same method and path (forges send form-encoded bodies too). echo applies `e.Use` middleware to every route whatever the registration order, so the exemption is a `Skipper`, not a registration placed before the validator.
- Answers: 202 `{"message":"checking"}`, 202 `{"message":"coalesced"}`, 202 `{"message":"queued"}`, 200 `{"message":"pong"}` (GitHub `ping`), 202 `{"message":"ignored"}` (any other event), 401 `{"message":"invalid signature"}`, 404 `{"message":"not found"}`, 413 `{"message":"the body is over 5 MiB"}`, 500 `{"message":"internal error"}`.
- 404 is the same answer for an unknown app, a name that is no app's name, and a webhook that is off.
- Guard order: app name valid, then body size (413), then webhook on and app registered (404), then signature (401), then event.
- Body: read up to 5 MiB = `5 << 20` bytes; exactly 5 MiB is accepted, one byte more is 413. Never parsed.
- Signature headers: `X-Hub-Signature-256: sha256=<hex>`, `X-Gitea-Signature: <hex>`, `X-Forgejo-Signature: <hex>`, `X-Gogs-Signature: <hex>` (hex HMAC-SHA256 of the raw body), `X-Gitlab-Token: <secret>` (equality).
- HMAC key: the 64-character hex secret string as bytes, exactly as pasted into the forge (not its 32 decoded bytes).
- Accepted when at least one signature header is present and every signature header present is valid; a header contradicting another is 401. Comparisons with `hmac.Equal` / `subtle.ConstantTimeCompare`. This is stricter than the spec's first wording ("when one of these headers is valid"): Task 1 amends the spec's Authentication section and its test list so that the spec, this plan and the OpenAPI description say the same.
- Event headers: `X-GitHub-Event`, `X-Gitea-Event`, `X-Forgejo-Event`, `X-Gogs-Event` (`push` checks), `X-Gitlab-Event` (`Push Hook`, `Tag Push Hook` check); GitHub `ping` answers pong; any other value is ignored; no event header at all checks.
- Forge naming precedence (Forgejo also sends Gitea's and Gogs' headers, Gitea sends Gogs' and GitHub's): `forgejo`, `gitea`, `gogs`, `gitlab`, `github`; by event header first, else by signature header (`X-Hub-Signature-256` is `github`), else `unknown`.
- Event recorded and logged: the header's value cut to 64 bytes; `""` when no event header was sent.
- View: every answer carrying the app gains `"webhook": {"enabled": bool, "path": "/v2/app_management/git/<app>/webhook", "secret": "<64 hex>" (only while enabled), "last_delivery": {"at": RFC 3339, "forge", "event", "result"} | null}`.
- `result` is one of `checked` (answer `checking`), `coalesced`, `queued`, `ignored`, `pong`.
- `PUT /v2/app_management/git/{app}` gains `webhook_enabled` (bool) and `regenerate_webhook_secret` (bool); regenerate while the webhook is (or ends) off is 400 `the webhook is off: turn it on to get a secret`, checked before anything changes.
- Secret: 32 bytes from `crypto/rand`, hex (64 chars). On: made when there is none; on again: kept; off: file removed (secret and last delivery forgotten); regenerate: replaced at once.
- Storage: `/var/lib/casaos/git_apps/<app>.webhook`, JSON `{"secret": "...", "last_delivery": ... }`, mode 0600, written with `writeGitFile`. Not `<app>.json`: a check or a deployment rewrites that file whole from its own copy and would drop, or be overwritten by, a delivery written meanwhile.
- The suffix is `.webhook`, never `*.json` (`GitAppNames` lists every `*.json` as an app).
- `forgetGitApp` (uninstall, `DeleteGitApp`) removes `<app>.webhook`, under the webhook lock; every restore of a git app (`installGitAppFromBackup`, whether it registers the app or finds it registered) turns the webhook off once the backup's origin is accepted and before it writes anything else; backups are unchanged (remote, branch, commit only).
- Line numbers in **Files:** are those of the file as it stands when the task starts (after the earlier tasks); each step also names a text anchor, which wins if they disagree.
- Triggering: `beginGitCheck` then `runGitCheck` (`checkGitApp(ctx, st, true)` then `deployGitAppAutomatically`), exactly what `CheckGitApp` runs.
- Debounce: at most one webhook-started check per app every 10 s (`gitWebhookDebounce`); pushes inside the window answer `coalesced`.
- Busy app (`Begin` refuses with `ErrAppBusy`): one queued check per app through `beginWaiting(ctx, app, "check", 30*time.Minute)`, answer `queued`; pushes while it waits answer `coalesced`; the wait ends the debounce clock again when the check starts.
- A refused request (404, 413, 401) writes nothing. Log line: message `git webhook`, fields `app`, `forge`, `event`, `outcome`; never the body, a header's value other than the event name, a signature or the secret.
- Lock: one `sync.Mutex` `gitWebhooks` for every app, around the webhook file and the debounce/queue maps; `Begin` is never waited on while it is held.
- OpenAPI: no `enum` added (oapi-codegen v1.12.4 renames existing enum constants when one is added); the enum-constant count stays **34** after each regeneration.
- `codegen/` is gitignored and generated (CI runs `go generate`): regenerate locally, never commit it.
- Codegen command (Git Bash, repository root): `go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,server,spec -package codegen api/app_management/openapi.yaml > codegen/app_management_api.go`.
- Count command: `grep -cE '^[[:blank:]][A-Z][[:alnum:]_]* +[[:alnum:]_]+ = "' codegen/app_management_api.go` prints `34`.
- Local host is Windows and Docker is down: the packages only build for Linux (`pkg/git` uses `syscall.Stat_t`), so locally run `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ...` (it compiles the tests too); `go test` runs on Linux (CI: `go generate ./...` then `go test ./...`, the coverage job with `-race`).
- Files: write new files with the Write tool (inline Bash unescapes backslashes). `openapi.yaml` has CRLF line endings: keep them.
- Commits: `git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "..."`; no `Co-Authored-By` trailer, no AI attribution anywhere.
- Forbidden: `git push`, `git merge`, `git pull`, `git rebase`, `git checkout --`, `git reset --hard`, `git clean`, `rm -rf`, any lint `--fix` over the tree.
- Release: AppManagement is tagged first (its own tag), then the dashboard; the installer's install check proves the chain in one distribution release. Tagging is not part of this plan.

---

## File structure

| File | Change | Responsibility |
|---|---|---|
| `docs/superpowers/specs/2026-09-23-git-webhooks-design.md` | modify | Authentication section and test list say every signature header sent must be valid. |
| `service/git_webhook_signature.go` | create | Pure: signature verification of every header, forge and event naming, the answer constants. |
| `service/git_webhook_signature_internal_test.go` | create | Table tests of the signatures and of forge/event naming; header and signing helpers the other tests reuse. |
| `service/git_webhook.go` | create | The `<app>.webhook` file: load, save, turn on/off, regenerate; the `webhook` part of the view; the `gitWebhooks` lock. |
| `service/git_webhook_internal_test.go` | create | Secret lifecycle through `UpdateGitApp`, the view's JSON, deletion, restore coming back off. |
| `service/git_webhook_receive.go` | create | `ReceiveGitWebhook`: guards, verification, debounce, queued check for a busy app, last delivery, log line. |
| `service/git_webhook_receive_internal_test.go` | create | Guards, refusals writing nothing, ping/ignored, check, coalesced, queued, automatic deployment off, nothing secret in the log. |
| `service/git_apps.go` | modify | `GitAppChanges` gains the two webhook fields; `UpdateGitApp` validates and applies them; `CheckGitApp`/`beginGitCheck` split into `prepareGitCheck` and `runGitCheck`. |
| `service/git_app_view.go` | modify | `GitAppView.Webhook`. |
| `service/git_app_view_internal_test.go` | modify | The view's contract test: `webhook` among the view's keys, off by default. |
| `service/git_app_state.go` | modify | `forgetGitApp` removes `.webhook` under the webhook lock. |
| `service/git_restore.go` | modify | Every restore of a git app turns its webhook off before it registers or touches the app. |
| `api/app_management/openapi.yaml` | modify | `GitWebhook`, `GitWebhookDelivery`, `GitApp.webhook`, the two `PUT` fields, the webhook route. |
| `codegen/app_management_api.go` | regenerate (gitignored) | Generated types and `ServerInterface.ReceiveGitWebhook`. Never committed. |
| `route/v2/git.go` | modify | `gitAppChanges` (PUT body to service changes); `ReceiveGitWebhook` handler and `gitWebhookAnswer`. |
| `route/v2/git_internal_test.go` | modify | The view matches the generated `GitApp`; the PUT fields reach the service; each webhook answer's status. |
| `route/v2.go` | modify | `gitWebhookPath`, `isGitWebhook`, the JWT and validator skippers. |
| `route/v2_git_test.go` | modify | PUT webhook fields validated by the spec; the JWT exemption covers the one pair. |

---

### Task 1: Signature verification and forge/event naming

**Files:**
- Create: `service/git_webhook_signature.go`
- Modify: `docs/superpowers/specs/2026-09-23-git-webhooks-design.md:33-34` (Authentication: the acceptance rule), `:137-139` (test list: contradicting headers are 401)
- Test: `service/git_webhook_signature_internal_test.go` (create)

**Interfaces:**
- Consumes: nothing beyond the standard library.
- Produces:
  - `func gitWebhookSigned(header http.Header, body []byte, secret string) bool`
  - `func gitWebhookSignatureValid(name, value string, want []byte, secret string) bool`
  - `func gitWebhookEvent(header http.Header) (forge, event, action string)`; `action` is `gitWebhookCheck`, `GitWebhookPong` or `GitWebhookIgnored`.
  - constants `gitForgeGitHub = "github"`, `gitForgeGitea = "gitea"`, `gitForgeForgejo = "forgejo"`, `gitForgeGogs = "gogs"`, `gitForgeGitLab = "gitlab"`, `gitForgeUnknown = "unknown"`, `gitWebhookCheck = "check"`, `gitWebhookEventCap = 64`.
  - exported answers `GitWebhookChecking = "checking"`, `GitWebhookCoalesced = "coalesced"`, `GitWebhookQueued = "queued"`, `GitWebhookIgnored = "ignored"`, `GitWebhookPong = "pong"`.
  - `var gitWebhookForges []struct{ forge, event, signature string }`.
  - test helpers `webhookHeader(pairs ...string) http.Header`, `webhookHMAC(secret string, body []byte) string`, `signedDelivery(secret, event string, body []byte) http.Header`.

- [ ] **Step 1: Write the failing test**

Create `service/git_webhook_signature_internal_test.go`:

```go
package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// webhookHeader is a delivery's headers, given as name, value, name, value...
func webhookHeader(pairs ...string) http.Header {
	header := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		header.Add(pairs[i], pairs[i+1])
	}

	return header
}

// webhookHMAC is the hex HMAC-SHA256 of body with secret, what the forges send.
func webhookHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)

	return hex.EncodeToString(mac.Sum(nil))
}

// signedDelivery is the headers GitHub sends with event, signed with secret.
func signedDelivery(secret, event string, body []byte) http.Header {
	return webhookHeader("X-GitHub-Event", event, "X-Hub-Signature-256", "sha256="+webhookHMAC(secret, body))
}

func TestAWebhookIsSignedOnlyWithTheAppsSecret(t *testing.T) {
	secret := strings.Repeat("5e", 32)
	other := strings.Repeat("0f", 32)
	body := []byte(`{"ref":"refs/heads/main"}`)
	good, wrong := webhookHMAC(secret, body), webhookHMAC(other, body)

	for _, c := range []struct {
		name   string
		header http.Header
		signed bool
	}{
		{"github, right secret", webhookHeader("X-Hub-Signature-256", "sha256="+good), true},
		{"github, upper-case hex", webhookHeader("X-Hub-Signature-256", "sha256="+strings.ToUpper(good)), true},
		{"github, wrong secret", webhookHeader("X-Hub-Signature-256", "sha256="+wrong), false},
		{"github, truncated", webhookHeader("X-Hub-Signature-256", "sha256="+good[:62]), false},
		{"github, odd length", webhookHeader("X-Hub-Signature-256", "sha256="+good[:63]), false},
		{"github, no prefix", webhookHeader("X-Hub-Signature-256", good), false},
		{"github, wrong prefix", webhookHeader("X-Hub-Signature-256", "sha1="+good), false},
		{"github, empty", webhookHeader("X-Hub-Signature-256", ""), false},
		{"gitea, right secret", webhookHeader("X-Gitea-Signature", good), true},
		{"gitea, wrong secret", webhookHeader("X-Gitea-Signature", wrong), false},
		{"gitea, truncated", webhookHeader("X-Gitea-Signature", good[:62]), false},
		{"gitea, with github's prefix", webhookHeader("X-Gitea-Signature", "sha256="+good), false},
		{"gitea, empty", webhookHeader("X-Gitea-Signature", ""), false},
		{"forgejo, right secret", webhookHeader("X-Forgejo-Signature", good), true},
		{"forgejo, wrong secret", webhookHeader("X-Forgejo-Signature", wrong), false},
		{"forgejo, truncated", webhookHeader("X-Forgejo-Signature", good[:62]), false},
		{"forgejo, with github's prefix", webhookHeader("X-Forgejo-Signature", "sha256="+good), false},
		{"forgejo, empty", webhookHeader("X-Forgejo-Signature", ""), false},
		{"gogs, right secret", webhookHeader("X-Gogs-Signature", good), true},
		{"gogs, wrong secret", webhookHeader("X-Gogs-Signature", wrong), false},
		{"gogs, truncated", webhookHeader("X-Gogs-Signature", good[:62]), false},
		{"gogs, with github's prefix", webhookHeader("X-Gogs-Signature", "sha256="+good), false},
		{"gogs, empty", webhookHeader("X-Gogs-Signature", ""), false},
		{"gitlab, right secret", webhookHeader("X-Gitlab-Token", secret), true},
		{"gitlab, wrong secret", webhookHeader("X-Gitlab-Token", other), false},
		{"gitlab, truncated", webhookHeader("X-Gitlab-Token", secret[:63]), false},
		{"gitlab, the HMAC instead of the secret", webhookHeader("X-Gitlab-Token", good), false},
		{"gitlab, empty", webhookHeader("X-Gitlab-Token", ""), false},
		{"no signature at all", webhookHeader("X-GitHub-Event", "push"), false},
		{"gitea's three headers, all right", webhookHeader("X-Gitea-Signature", good, "X-Gogs-Signature", good, "X-Hub-Signature-256", "sha256="+good), true},
		{"two contradicting headers", webhookHeader("X-Gitea-Signature", good, "X-Hub-Signature-256", "sha256="+wrong), false},
		{"two contradicting headers, the wrong one first", webhookHeader("X-Gitea-Signature", wrong, "X-Hub-Signature-256", "sha256="+good), false},
		{"a right signature and a wrong token", webhookHeader("X-Hub-Signature-256", "sha256="+good, "X-Gitlab-Token", other), false},
		{"one header twice, contradicting itself", webhookHeader("X-Hub-Signature-256", "sha256="+good, "X-Hub-Signature-256", "sha256="+wrong), false},
	} {
		assert.Equal(t, gitWebhookSigned(c.header, body, secret), c.signed, c.name)
		assert.Equal(t, gitWebhookSigned(c.header, body, ""), false, "%s: no secret signs nothing", c.name)
		if c.header.Get("X-Gitlab-Token") == "" {
			// GitLab sends the secret itself; every other forge signs the body
			assert.Equal(t, gitWebhookSigned(c.header, append([]byte(" "), body...), secret), false, "%s: another body", c.name)
		}
	}
}

func TestAWebhookNamesItsForgeAndItsEvent(t *testing.T) {
	long := strings.Repeat("x", 100)

	for _, c := range []struct {
		header               http.Header
		forge, event, action string
	}{
		{webhookHeader("X-GitHub-Event", "ping", "X-Hub-Signature-256", "sha256=00"), "github", "ping", GitWebhookPong},
		{webhookHeader("X-GitHub-Event", "push"), "github", "push", gitWebhookCheck},
		{webhookHeader("X-GitHub-Event", "issues"), "github", "issues", GitWebhookIgnored},
		{webhookHeader("X-Gitea-Event", "push", "X-Gogs-Event", "push", "X-GitHub-Event", "push"), "gitea", "push", gitWebhookCheck},
		{webhookHeader("X-Gitea-Event", "ping"), "gitea", "ping", GitWebhookIgnored},
		{webhookHeader("X-Forgejo-Event", "push", "X-Gitea-Event", "push", "X-Gogs-Event", "push"), "forgejo", "push", gitWebhookCheck},
		{webhookHeader("X-Forgejo-Event", "release"), "forgejo", "release", GitWebhookIgnored},
		{webhookHeader("X-Gogs-Event", "push"), "gogs", "push", gitWebhookCheck},
		{webhookHeader("X-Gitlab-Event", "Push Hook"), "gitlab", "Push Hook", gitWebhookCheck},
		{webhookHeader("X-Gitlab-Event", "Tag Push Hook"), "gitlab", "Tag Push Hook", gitWebhookCheck},
		{webhookHeader("X-Gitlab-Event", "Merge Request Hook"), "gitlab", "Merge Request Hook", GitWebhookIgnored},
		{webhookHeader("X-Gitlab-Event", "push"), "gitlab", "push", GitWebhookIgnored},
		{webhookHeader("X-GitHub-Event", long), "github", long[:64], GitWebhookIgnored},
		{webhookHeader("X-Hub-Signature-256", "sha256=00"), "github", "", gitWebhookCheck},
		{webhookHeader("X-Gitea-Signature", "00", "X-Hub-Signature-256", "sha256=00"), "gitea", "", gitWebhookCheck},
		{webhookHeader("X-Forgejo-Signature", "00", "X-Gitea-Signature", "00"), "forgejo", "", gitWebhookCheck},
		{webhookHeader("X-Gogs-Signature", "00"), "gogs", "", gitWebhookCheck},
		{webhookHeader("X-Gitlab-Token", "t"), "gitlab", "", gitWebhookCheck},
		{webhookHeader(), "unknown", "", gitWebhookCheck},
	} {
		forge, event, action := gitWebhookEvent(c.header)
		assert.Equal(t, forge, c.forge, "%v", c.header)
		assert.Equal(t, event, c.event, "%v", c.header)
		assert.Equal(t, action, c.action, "%v", c.header)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/`

Expected: FAIL, compile errors in `git_webhook_signature_internal_test.go`: `undefined: gitWebhookSigned`, `undefined: gitWebhookEvent`, `undefined: GitWebhookPong`.

- [ ] **Step 3: Implement**

Create `service/git_webhook_signature.go`:

```go
package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// What a forge's webhook delivery says of itself: who signed it, which forge sent it, and
// which event it reports. Nothing here reads the body but to sign it.

// Forges, as the last delivery names them.
const (
	gitForgeGitHub  = "github"
	gitForgeGitea   = "gitea"
	gitForgeForgejo = "forgejo"
	gitForgeGogs    = "gogs"
	gitForgeGitLab  = "gitlab"
	gitForgeUnknown = "unknown"
)

// What a delivery asks for: gitWebhookCheck, or one of the answers GitWebhookPong and
// GitWebhookIgnored.
const gitWebhookCheck = "check"

// Answers a forge is given: 200 for GitWebhookPong, 202 for the others.
const (
	GitWebhookChecking  = "checking"
	GitWebhookCoalesced = "coalesced"
	GitWebhookQueued    = "queued"
	GitWebhookIgnored   = "ignored"
	GitWebhookPong      = "pong"
)

// gitWebhookEventCap is as much of an event's name as is logged and kept.
const gitWebhookEventCap = 64

// gitWebhookForges pairs each forge with its event and its signature header, the most
// specific first: Forgejo sends Gitea's and Gogs' headers too, and Gitea sends Gogs' and
// GitHub's.
var gitWebhookForges = []struct{ forge, event, signature string }{
	{gitForgeForgejo, "X-Forgejo-Event", "X-Forgejo-Signature"},
	{gitForgeGitea, "X-Gitea-Event", "X-Gitea-Signature"},
	{gitForgeGogs, "X-Gogs-Event", "X-Gogs-Signature"},
	{gitForgeGitLab, "X-Gitlab-Event", "X-Gitlab-Token"},
	{gitForgeGitHub, "X-GitHub-Event", "X-Hub-Signature-256"},
}

// gitWebhookSigned reports whether body was sent by someone holding secret: at least one
// signature header, and every signature header sent valid, each compared in constant time.
// A header that contradicts another is a request somebody changed on the way.
func gitWebhookSigned(header http.Header, body []byte, secret string) bool {
	if secret == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)

	signed := false
	for _, forge := range gitWebhookForges {
		for _, value := range header.Values(forge.signature) {
			if !gitWebhookSignatureValid(forge.signature, value, want, secret) {
				return false
			}
			signed = true
		}
	}

	return signed
}

// gitWebhookSignatureValid judges one signature header: GitLab's is the secret itself,
// GitHub's the hex HMAC-SHA256 of the body after `sha256=`, the others that HMAC alone.
func gitWebhookSignatureValid(name, value string, want []byte, secret string) bool {
	switch name {
	case "X-Gitlab-Token":
		return subtle.ConstantTimeCompare([]byte(value), []byte(secret)) == 1
	case "X-Hub-Signature-256":
		hexed, ok := strings.CutPrefix(value, "sha256=")
		if !ok {
			return false
		}
		value = hexed
	}

	got, err := hex.DecodeString(value)

	return err == nil && hmac.Equal(got, want)
}

// gitWebhookEvent names the forge after its event header, else after its signature header,
// else `unknown`; and says what the delivery asks for: a push is checked, GitHub's ping
// is answered pong, any other named event is ignored, and a delivery naming no event, from
// a less common forge, is checked.
func gitWebhookEvent(header http.Header) (forge, event, action string) {
	for _, f := range gitWebhookForges {
		if values := header.Values(f.event); len(values) > 0 {
			forge, event = f.forge, values[0]
			break
		}
	}

	if forge == "" {
		forge = gitForgeUnknown
		for _, f := range gitWebhookForges {
			if len(header.Values(f.signature)) > 0 {
				forge = f.forge
				break
			}
		}

		return forge, "", gitWebhookCheck
	}

	if len(event) > gitWebhookEventCap {
		event = event[:gitWebhookEventCap]
	}

	switch {
	case forge == gitForgeGitHub && event == "ping":
		return forge, event, GitWebhookPong
	case forge == gitForgeGitLab && (event == "Push Hook" || event == "Tag Push Hook"),
		forge != gitForgeGitLab && event == "push":
		return forge, event, gitWebhookCheck
	}

	return forge, event, GitWebhookIgnored
}
```

In `docs/superpowers/specs/2026-09-23-git-webhooks-design.md` (LF line endings), make the spec say the rule `gitWebhookSigned` applies. Replace:

```markdown
With the app's webhook secret, every comparison in constant time. The request is accepted when
one of these headers is valid:
```

with:

```markdown
With the app's webhook secret, every comparison in constant time. The request is accepted when
at least one of these headers is sent and every one of them sent is valid: a header that
contradicts another is a request changed on the way, and is refused. Real forges send consistent
headers (Gitea sends three, Forgejo four, all carrying the same HMAC).
```

and replace:

```markdown
- Signatures, as a table for every header: right secret, wrong secret, truncated signature,
  missing or wrong `sha256=` prefix, empty header, two contradicting headers. No request gets a
  2xx without a valid signature.
```

with:

```markdown
- Signatures, as a table for every header: right secret, wrong secret, truncated signature,
  missing or wrong `sha256=` prefix, empty header, two contradicting headers (401, whichever
  comes first). No request gets a 2xx without a valid signature.
```

- [ ] **Step 4: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && grep -c 'every one of them sent is valid' docs/superpowers/specs/2026-09-23-git-webhooks-design.md && grep -c 'one of these headers is valid:' docs/superpowers/specs/2026-09-23-git-webhooks-design.md`

Expected: `1`, then `0` (the second grep exits 1: the old wording is gone).

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && gofmt -l service/git_webhook_signature.go service/git_webhook_signature_internal_test.go && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/`

Expected: no output (gofmt lists nothing, vet reports nothing).

Run (Linux, CI or a Linux box): `go test ./service/ -run 'TestAWebhookIsSignedOnlyWithTheAppsSecret|TestAWebhookNamesItsForgeAndItsEvent' -count=1 -v`

Expected: `--- PASS: TestAWebhookIsSignedOnlyWithTheAppsSecret`, `--- PASS: TestAWebhookNamesItsForgeAndItsEvent`, `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`.

- [ ] **Step 5: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add service/git_webhook_signature.go service/git_webhook_signature_internal_test.go docs/superpowers/specs/2026-09-23-git-webhooks-design.md
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): verify webhook signatures and name the forge and the event"
```

---

### Task 2: The webhook secret, its view and the PUT fields

**Files:**
- Create: `service/git_webhook.go`
- Modify: `service/git_apps.go:43-49` (`GitAppChanges`), `service/git_apps.go:239-278` (`UpdateGitApp`: validation after the access check, application after `setGitAccess`)
- Modify: `service/git_app_view.go:44-48` (`GitAppView`), `service/git_app_view.go:181-184` (`newGitAppView`)
- Modify: `service/git_app_state.go:183-184` (comment of `gitAppFile`), `service/git_app_state.go:241-251` (`forgetGitApp`)
- Modify: `service/git_restore.go:48` (`installGitAppFromBackup`: insert before `st, err := loadGitApp(name)`)
- Test: `service/git_webhook_internal_test.go` (create); `service/git_app_view_internal_test.go:126-130` (`TestTheViewOfAGitAppIsTheContract`: expected keys, and the `webhook` value)

**Interfaces:**
- Consumes: `gitAppFile`, `writeGitFile`, `pathExists`, `GitRequestError`, `UpdateGitApp`, `GetGitApp`, `CreateGitApp`, `DeleteGitApp`, `installGitAppFromBackup`, `BackupGit`, `newGitAppView`; test helpers `gitAppsIn`, `withFakeGitDocker`, `assertBadRequest`, `newTestRemote`, `gitShell`.
- Produces:
  - `type GitWebhookView struct { Enabled bool `json:"enabled"`; Path string `json:"path"`; Secret string `json:"secret,omitempty"`; LastDelivery *GitWebhookDelivery `json:"last_delivery"` }`; JSON while off: `{"enabled":false,"path":"/v2/app_management/git/<app>/webhook","last_delivery":null}`.
  - `type GitWebhookDelivery struct { At time.Time `json:"at"`; Forge string `json:"forge"`; Event string `json:"event"`; Result string `json:"result"` }`.
  - `type gitWebhook struct { Secret string `json:"secret"`; LastDelivery *GitWebhookDelivery `json:"last_delivery"` }`, the content of `<app>.webhook`.
  - `var gitWebhooks sync.Mutex`.
  - `func gitWebhookPath(app string) string`, `func loadGitWebhook(app string) (*gitWebhook, error)` (nil, nil while off), `func saveGitWebhook(app string, hook *gitWebhook) error`, `func setGitWebhook(app string, enabled *bool, regenerate bool) error`, `func gitWebhookView(app string) GitWebhookView`.
  - `GitAppChanges.WebhookEnabled *bool`, `GitAppChanges.RegenerateWebhookSecret *bool`.
  - `GitAppView.Webhook GitWebhookView `json:"webhook"``.
  - test helper `enableTestWebhook(t *testing.T) string` (turns jarvis's webhook on, returns the secret).

- [ ] **Step 1: Write the failing test**

Create `service/git_webhook_internal_test.go`:

```go
package service

import (
	"context"
	stdjson "encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

var webhookSecretPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// enableTestWebhook turns jarvis's webhook on and returns its secret.
func enableTestWebhook(t *testing.T) string {
	t.Helper()

	on := true
	view, err := UpdateGitApp(context.Background(), "jarvis", GitAppChanges{WebhookEnabled: &on})
	assert.NilError(t, err)
	assert.Equal(t, view.Webhook.Enabled, true)

	return view.Webhook.Secret
}

func TestAWebhookSecretIsMadeReplacedAndForgotten(t *testing.T) {
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	path := "/v2/app_management/git/jarvis/webhook"
	off, regenerate := false, true

	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: "https://github.com/owner/jarvis.git", Access: "none"})
	assert.NilError(t, err)

	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Webhook, GitWebhookView{Path: path})
	shown, err := stdjson.Marshal(view.Webhook)
	assert.NilError(t, err)
	assert.Equal(t, string(shown), `{"enabled":false,"path":"`+path+`","last_delivery":null}`, "no secret while off")

	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{RegenerateWebhookSecret: &regenerate})
	assertBadRequest(t, err, "the webhook is off")

	first := enableTestWebhook(t)
	assert.Assert(t, webhookSecretPattern.MatchString(first), first)
	info, err := os.Stat(gitAppFile("jarvis", ".webhook"))
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))
	view, err = GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Webhook, GitWebhookView{Enabled: true, Path: path, Secret: first})
	assert.Equal(t, enableTestWebhook(t), first, "turning it on again keeps the secret")

	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{RegenerateWebhookSecret: &regenerate})
	assert.NilError(t, err)
	second := view.Webhook.Secret
	assert.Assert(t, webhookSecretPattern.MatchString(second), second)
	assert.Assert(t, second != first, "regenerating replaces the secret")

	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{WebhookEnabled: &off, RegenerateWebhookSecret: &regenerate})
	assertBadRequest(t, err, "the webhook is off")
	view, err = GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.Equal(t, view.Webhook.Secret, second, "a refused change changes nothing")

	view, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{WebhookEnabled: &off})
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Webhook, GitWebhookView{Path: path})
	_, err = os.Stat(gitAppFile("jarvis", ".webhook"))
	assert.Assert(t, os.IsNotExist(err), "turning it off forgets the secret")

	third := enableTestWebhook(t)
	assert.Assert(t, third != first && third != second, "turning it on again after off makes a new secret")

	assert.NilError(t, DeleteGitApp(ctx, "jarvis"))
	_, err = os.Stat(gitAppFile("jarvis", ".webhook"))
	assert.Assert(t, os.IsNotExist(err), "the secret goes with the app")
}

// A backup holds no secret: whatever this machine kept, the restored app's webhook is off,
// whether the restore registers the app or finds it registered already.
func TestARestoredGitAppComesBackWithItsWebhookOff(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	ctx := context.Background()
	url, work := newTestRemote(t)
	origin := BackupGit{Remote: url, Branch: "main", Commit: gitShell(t, work, "git rev-parse HEAD")}
	off := GitWebhookView{Path: "/v2/app_management/git/jarvis/webhook"}
	// a secret without a state, as an uninstall that could not remove everything leaves it
	assert.NilError(t, saveGitWebhook("jarvis", &gitWebhook{Secret: strings.Repeat("ab", 32)}))

	_, err := installGitAppFromBackup(ctx, "jarvis", origin, nil)
	assert.NilError(t, err)
	view, err := GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Webhook, off)

	// registered already, as a restore that asked for access leaves it, its webhook turned on
	enableTestWebhook(t)
	_, err = installGitAppFromBackup(ctx, "jarvis", origin, nil)
	assert.NilError(t, err)
	view, err = GetGitApp(ctx, "jarvis")
	assert.NilError(t, err)
	assert.DeepEqual(t, view.Webhook, off)
}
```

In `service/git_app_view_internal_test.go` (`TestTheViewOfAGitAppIsTheContract`, lines 126-130), replace:

```go
	assert.DeepEqual(t, keys, []string{
		"access", "app", "auto_deploy", "auto_paused", "blocked", "branch", "build_log", "check", "cloned", "compose",
		"compose_example", "deployed", "dir", "env_template", "env_tracked", "head", "history", "new_commits", "operation",
		"origin", "remote", "state", "token_set",
	})
```

with (the list stays sorted; `webhook` comes last):

```go
	assert.DeepEqual(t, keys, []string{
		"access", "app", "auto_deploy", "auto_paused", "blocked", "branch", "build_log", "check", "cloned", "compose",
		"compose_example", "deployed", "dir", "env_template", "env_tracked", "head", "history", "new_commits", "operation",
		"origin", "remote", "state", "token_set", "webhook",
	})
	// off by default, and no secret while off
	assert.DeepEqual(t, view["webhook"], map[string]any{
		"enabled": false, "path": "/v2/app_management/git/jarvis/webhook", "last_delivery": nil,
	})
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/`

Expected: FAIL, compile errors in `git_webhook_internal_test.go`: `unknown field WebhookEnabled in struct literal of type GitAppChanges`, `view.Webhook undefined (type *GitAppView has no field or method Webhook)`, `undefined: GitWebhookView`, `undefined: saveGitWebhook`, `undefined: gitWebhook`. (Were it to compile, `TestTheViewOfAGitAppIsTheContract` would fail on Linux too: the view has no `webhook` key yet.)

- [ ] **Step 3: Implement**

Create `service/git_webhook.go`:

```go
package service

import (
	"crypto/rand"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// A git app's webhook: its secret and its last delivery, in `<app>.webhook` beside the
// app's state, readable by root only. The file exists while the webhook is on.
//
// Not in the state file: whatever holds the app (a check, a deployment) rewrites that one
// whole from its own copy, and would drop a delivery written meanwhile, or lose what it
// wrote itself under a delivery written from an older copy.

// GitWebhookView is the `webhook` of a git app in the API.
type GitWebhookView struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
	// Secret is set while the webhook is on.
	Secret       string              `json:"secret,omitempty"`
	LastDelivery *GitWebhookDelivery `json:"last_delivery"`
}

// GitWebhookDelivery is the last delivery whose signature was valid.
type GitWebhookDelivery struct {
	At     time.Time `json:"at"`
	Forge  string    `json:"forge"`
	Event  string    `json:"event"`
	Result string    `json:"result"`
}

type gitWebhook struct {
	Secret       string              `json:"secret"`
	LastDelivery *GitWebhookDelivery `json:"last_delivery"`
}

// gitWebhooks serialises what reads and writes the webhook files: a change from the API and
// a delivery.
//
// ponytail: one lock for every app; per-app locks if deliveries to many apps ever queue
// behind it.
var gitWebhooks sync.Mutex

// gitWebhookPath is the route a forge is given for app, after the address it reaches the
// box by.
func gitWebhookPath(app string) string {
	return "/v2/app_management/git/" + app + "/webhook"
}

// loadGitWebhook is app's webhook, nil while it is off.
func loadGitWebhook(app string) (*gitWebhook, error) {
	buf, err := os.ReadFile(gitAppFile(app, ".webhook"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var hook gitWebhook
	if err := stdjson.Unmarshal(buf, &hook); err != nil {
		return nil, fmt.Errorf("%s: %w", gitAppFile(app, ".webhook"), err)
	}

	return &hook, nil
}

func saveGitWebhook(app string, hook *gitWebhook) error {
	buf, err := stdjson.Marshal(hook)
	if err != nil {
		return err
	}

	return writeGitFile(gitAppFile(app, ".webhook"), buf)
}

// setGitWebhook turns app's webhook on or off as enabled says, nil leaving it as it is, and
// replaces its secret when regenerate is set. Turning it on makes a secret when there is
// none; turning it off forgets the secret, and the last delivery with it.
func setGitWebhook(app string, enabled *bool, regenerate bool) error {
	gitWebhooks.Lock()
	defer gitWebhooks.Unlock()

	if enabled != nil && !*enabled {
		if err := os.Remove(gitAppFile(app, ".webhook")); err != nil && !os.IsNotExist(err) {
			return err
		}

		return nil
	}

	hook, err := loadGitWebhook(app)
	if err != nil {
		return err
	}

	switch {
	case hook == nil && enabled == nil, hook != nil && !regenerate:
		return nil
	case hook == nil:
		hook = &gitWebhook{}
	}

	secret := make([]byte, 32)
	_, _ = rand.Read(secret) // never fails since Go 1.24: it crashes instead
	hook.Secret = hex.EncodeToString(secret)

	return saveGitWebhook(app, hook)
}

// gitWebhookView is what the API shows of app's webhook.
func gitWebhookView(app string) GitWebhookView {
	view := GitWebhookView{Path: gitWebhookPath(app)}
	if hook, err := loadGitWebhook(app); err == nil && hook != nil {
		view.Enabled, view.Secret, view.LastDelivery = true, hook.Secret, hook.LastDelivery
	}

	return view
}
```

In `service/git_apps.go`, replace `GitAppChanges` (lines 43-49):

```go
// GitAppChanges changes a git app; nil leaves a field as it is.
type GitAppChanges struct {
	Branch     *string
	AutoDeploy *bool
	Access     *string
	Token      *string
	// WebhookEnabled turns the webhook on, with a new secret when it has none, or off,
	// forgetting the secret.
	WebhookEnabled *bool
	// RegenerateWebhookSecret replaces the secret at once; refused while the webhook is off.
	RegenerateWebhookSecret *bool
}
```

In `UpdateGitApp`, between the `if accessChanged { ... checkGitAccess ... }` block and `if changes.Branch != nil && *changes.Branch != st.Branch {`, insert:

```go
	webhookOn := pathExists(gitAppFile(name, ".webhook"))
	if changes.WebhookEnabled != nil {
		webhookOn = *changes.WebhookEnabled
	}
	if lo.FromPtr(changes.RegenerateWebhookSecret) && !webhookOn {
		return nil, GitRequestError("the webhook is off: turn it on to get a secret")
	}

```

In `UpdateGitApp`, replace:

```go
	if accessChanged {
		if err := setGitAccess(st, access, token); err != nil {
			return nil, err
		}
	}

	if err := saveGitApp(st); err != nil {
```

with:

```go
	if accessChanged {
		if err := setGitAccess(st, access, token); err != nil {
			return nil, err
		}
	}
	if err := setGitWebhook(name, changes.WebhookEnabled, lo.FromPtr(changes.RegenerateWebhookSecret)); err != nil {
		return nil, err
	}

	if err := saveGitApp(st); err != nil {
```

In `service/git_app_view.go`, replace the end of `GitAppView` (lines 44-48):

```go
	// ComposeExample is set when the last check found no compose file in the repository.
	ComposeExample *string `json:"compose_example"`
	EnvTemplate    *string `json:"env_template"`
	BuildLog       string  `json:"build_log"`
}
```

with (gofmt alignment):

```go
	// ComposeExample is set when the last check found no compose file in the repository.
	ComposeExample *string        `json:"compose_example"`
	EnvTemplate    *string        `json:"env_template"`
	BuildLog       string         `json:"build_log"`
	Webhook        GitWebhookView `json:"webhook"`
}
```

In `newGitAppView`, replace:

```go
		History:  []GitHistoryView{},
		BuildLog: readGitBuildLogTail(st.App),
	}
```

with:

```go
		History:  []GitHistoryView{},
		BuildLog: readGitBuildLogTail(st.App),
		Webhook:  gitWebhookView(st.App),
	}
```

In `service/git_app_state.go`, replace the comment of `gitAppFile`:

```go
// gitAppFile is one of an app's files: .json, .key, .key.pub, .token, .known_hosts or
// .build.log.
```

with:

```go
// gitAppFile is one of an app's files: .json, .key, .key.pub, .token, .known_hosts,
// .build.log or .webhook.
```

and replace `forgetGitApp` (lines 241-251):

```go
// forgetGitApp removes an app's state and secrets.
func forgetGitApp(app string) error {
	// a delivery recorded meanwhile would write the webhook's secret back
	gitWebhooks.Lock()
	defer gitWebhooks.Unlock()

	failures := []error{}
	for _, suffix := range []string{".json", ".key", ".key.pub", ".token", ".known_hosts", ".build.log", ".webhook"} {
		if err := os.Remove(gitAppFile(app, suffix)); err != nil && !os.IsNotExist(err) {
			failures = append(failures, err)
		}
	}

	return errors.Join(failures...)
}
```

In `service/git_restore.go` (`installGitAppFromBackup`), after the `switch` that refuses a manifest's origin and before `st, err := loadGitApp(name)` (line 48), insert:

```go
	// a backup holds no webhook secret: whatever this machine kept, the app comes back with
	// its webhook off, turned off before anything is registered so that no delivery signed
	// with an older secret is taken meanwhile
	off := false
	if err := setGitWebhook(name, &off, false); err != nil {
		return nil, err
	}

```

The registration block below it stays as it is. A manifest refused by the `switch` still writes nothing.

- [ ] **Step 4: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && gofmt -l service/git_webhook.go service/git_webhook_internal_test.go service/git_apps.go service/git_app_view.go service/git_app_view_internal_test.go service/git_app_state.go service/git_restore.go && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/ ./route/...`

Expected: no output.

Run (Linux): `go test ./service/ -run 'TestAWebhookSecretIsMadeReplacedAndForgotten|TestARestoredGitAppComesBackWithItsWebhookOff|TestTheViewOfAGitAppIsTheContract|TestTheAccessOfAGitAppChangesAndItsTokenIsNeverShown|TestRemovingAGitAppThatNeverRan|TestABackupOfAGitAppRecordsWhereItsCodeComesFrom|TestAGitAppIsInstalledFromItsBackupAtTheBackedUpCommit|TestARestoreOfTheDeployedCommitStartsItFromItsImages|TestARestoreThatCannotReachTheRepositoryAsksForAccess|TestARestoreRefusesAnOriginGitCannotBeGiven' -count=1 -v`

Expected: `--- PASS` for each test named, `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`. (The filter names whole tests on purpose: a looser `Backup` also matches `TestABackupThatHoldsTheAppStillWaitsForItsTurn`, which logs through a logger only other tests initialise and panics when run first.)

- [ ] **Step 5: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add service/git_webhook.go service/git_webhook_internal_test.go service/git_apps.go service/git_app_view.go service/git_app_view_internal_test.go service/git_app_state.go service/git_restore.go
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): a webhook secret per git app, in its view and its PUT"
```

---

### Task 3: A delivery checks the app: debounce, queued check, last delivery

**Files:**
- Create: `service/git_webhook_receive.go`
- Modify: `service/git_apps.go:358-402` after Task 2 (`CheckGitApp`, `beginGitCheck`: split into `prepareGitCheck` and `runGitCheck`)
- Test: `service/git_webhook_receive_internal_test.go` (create)

**Interfaces:**
- Consumes: Task 1 (`gitWebhookEvent`, `gitWebhookSigned`, `gitWebhookCheck`, the `GitWebhook*` answers), Task 2 (`gitWebhooks`, `loadGitWebhook`, `saveGitWebhook`, `GitWebhookDelivery`, `enableTestWebhook`), `Begin`, `beginWaiting`, `ErrAppBusy`, `howOftenToAskAgain`, `gitOperationCheck`, `gitOperationDeploy`, `findGitApp`, `adoptGitApp`, `saveGitApp`, `checkGitApp`, `deployGitAppAutomatically`, `pathExists`; test helpers `deployedTestApp`, `pushTestCommit`, `waitForGitApp`, `signedDelivery`, `webhookHeader`.
- Produces:
  - `func ReceiveGitWebhook(app string, header http.Header, body io.Reader) (string, error)`: returns one of the five answers, or `ErrGitWebhookNotFound`, `ErrGitWebhookTooLarge`, `ErrGitWebhookSignature`, or an unexpected error (logged).
  - `var ErrGitWebhookNotFound = errors.New("not found")`, `var ErrGitWebhookSignature = errors.New("invalid signature")`, `var ErrGitWebhookTooLarge = errors.New("the body is over 5 MiB")`.
  - `const gitWebhookMaxBody = 5 << 20`, `const gitWebhookPatience = 30 * time.Minute`, `var gitWebhookDebounce = 10 * time.Second`, `var gitWebhookRuns struct { last map[string]time.Time; pending map[string]bool }` (guarded by `gitWebhooks`).
  - `func scheduleGitWebhookCheck(app string) (string, error)`, `func runQueuedGitWebhookCheck(app string)`.
  - `func prepareGitCheck(ctx context.Context, name string) (*gitApp, error)` (the app already held), `func runGitCheck(st *gitApp, end func())`.
  - test helpers `freshGitWebhookRuns(t)`, `deliver(header http.Header, body []byte) (string, error)`, `waitForGitCheckAfter(t, name string, since time.Time) *gitApp`, `lastDelivery(t) *GitWebhookDelivery`, `type syncBuffer`.

- [ ] **Step 1: Write the failing test**

Create `service/git_webhook_receive_internal_test.go`:

```go
package service

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap/zapcore"
	"gotest.tools/v3/assert"
)

// freshGitWebhookRuns forgets what earlier tests' webhooks started: the debounce and the
// queued checks are kept per app name, and every test's app is jarvis.
func freshGitWebhookRuns(t *testing.T) {
	t.Helper()

	gitWebhooks.Lock()
	defer gitWebhooks.Unlock()
	gitWebhookRuns.last, gitWebhookRuns.pending = map[string]time.Time{}, map[string]bool{}
}

// deliver sends body to jarvis's webhook with header.
func deliver(header http.Header, body []byte) (string, error) {
	return ReceiveGitWebhook("jarvis", header, bytes.NewReader(body))
}

// waitForGitCheckAfter waits until a check that started after since has ended, and
// whatever it started too, and returns the app's state.
func waitForGitCheckAfter(t *testing.T, name string, since time.Time) *gitApp {
	t.Helper()

	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if st, err := loadGitApp(name); err == nil && st.Check != nil && st.Check.At.After(since) && st.Operation == nil {
			return waitForGitApp(t, name)
		}
	}
	t.Fatal("no check ran")

	return nil
}

func lastDelivery(t *testing.T) *GitWebhookDelivery {
	t.Helper()

	view, err := GetGitApp(context.Background(), "jarvis")
	assert.NilError(t, err)
	assert.Assert(t, view.Webhook.LastDelivery != nil)

	return view.Webhook.LastDelivery
}

// The route does not tell which apps exist: an app that is not there, a name that is no
// app's, and an app whose webhook is off get the same refusal, and nothing is written.
func TestAWebhookIsRefusedAlikeForAnUnknownAppAndOneTurnedOff(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	freshGitWebhookRuns(t)
	_, err := CreateGitApp(context.Background(), GitAppRegistration{Name: "jarvis", URL: "https://github.com/owner/jarvis.git", Access: "none"})
	assert.NilError(t, err)
	body := []byte(`{"ref":"refs/heads/main"}`)
	header := signedDelivery(strings.Repeat("5e", 32), "push", body)

	_, unknown := ReceiveGitWebhook("nextcloud", header, bytes.NewReader(body))
	_, notAName := ReceiveGitWebhook("../jarvis", header, bytes.NewReader(body))
	_, off := deliver(header, body)

	assert.ErrorIs(t, unknown, ErrGitWebhookNotFound)
	assert.Equal(t, notAName, unknown)
	assert.Equal(t, off, unknown)
	for _, app := range []string{"jarvis", "nextcloud"} {
		_, err := os.Stat(gitAppFile(app, ".webhook"))
		assert.Assert(t, os.IsNotExist(err), "%s: nothing written", app)
	}
}

func TestAWebhookAnswersPingsAndOtherEventsAndRefusesWhatIsNotSigned(t *testing.T) {
	logger.LogInitConsoleOnly()
	gitAppsIn(t)
	withFakeGitDocker(t)
	freshGitWebhookRuns(t)
	ctx := context.Background()
	_, err := CreateGitApp(ctx, GitAppRegistration{Name: "jarvis", URL: "https://github.com/owner/jarvis.git", Access: "none"})
	assert.NilError(t, err)
	secret := enableTestWebhook(t)
	body := []byte(`{"zen":"Design for failure."}`)

	answer, err := deliver(signedDelivery(secret, "ping", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookPong)
	delivery := lastDelivery(t)
	assert.Equal(t, delivery.Forge, "github")
	assert.Equal(t, delivery.Event, "ping")
	assert.Equal(t, delivery.Result, "pong")
	assert.Assert(t, time.Since(delivery.At) < time.Minute)

	answer, err = deliver(signedDelivery(secret, "issues", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookIgnored)
	assert.Equal(t, lastDelivery(t).Result, "ignored")

	// 5 MiB exactly is read and signed; one byte more is refused
	largest := bytes.Repeat([]byte(" "), gitWebhookMaxBody)
	answer, err = deliver(signedDelivery(secret, "issues", largest), largest)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookIgnored)

	// nothing a refused request sends is written down
	kept, err := os.ReadFile(gitAppFile("jarvis", ".webhook"))
	assert.NilError(t, err)
	tooLarge := append(largest, ' ')
	_, err = deliver(signedDelivery(secret, "push", tooLarge), tooLarge)
	assert.ErrorIs(t, err, ErrGitWebhookTooLarge)
	_, err = deliver(signedDelivery(strings.Repeat("0f", 32), "push", body), body)
	assert.ErrorIs(t, err, ErrGitWebhookSignature)
	_, err = deliver(webhookHeader("X-GitHub-Event", "push"), body)
	assert.ErrorIs(t, err, ErrGitWebhookSignature, "unsigned")
	_, err = deliver(webhookHeader("X-Gitlab-Event", "Push Hook", "X-Gitlab-Token", strings.Repeat("0f", 32)), body)
	assert.ErrorIs(t, err, ErrGitWebhookSignature)
	now, err := os.ReadFile(gitAppFile("jarvis", ".webhook"))
	assert.NilError(t, err)
	assert.Equal(t, string(now), string(kept))

	// a new secret refuses the old one at once
	regenerate := true
	view, err := UpdateGitApp(ctx, "jarvis", GitAppChanges{RegenerateWebhookSecret: &regenerate})
	assert.NilError(t, err)
	_, err = deliver(signedDelivery(secret, "ping", body), body)
	assert.ErrorIs(t, err, ErrGitWebhookSignature)
	answer, err = deliver(signedDelivery(view.Webhook.Secret, "ping", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookPong)

	// turned off, the app is one that does not exist
	off := false
	_, err = UpdateGitApp(ctx, "jarvis", GitAppChanges{WebhookEnabled: &off})
	assert.NilError(t, err)
	_, err = deliver(signedDelivery(view.Webhook.Secret, "ping", body), body)
	assert.ErrorIs(t, err, ErrGitWebhookNotFound)
}

// A push runs what "Check now" runs, the automatic deployment included; the pushes a forge
// sends within ten seconds of it run nothing more.
func TestAPushChecksTheAppOnceWithinTenSeconds(t *testing.T) {
	fake, work, _ := deployedTestApp(t, true)
	freshGitWebhookRuns(t)
	secret := enableTestWebhook(t)
	body := []byte(`{"ref":"refs/heads/main"}`)
	second := pushTestCommit(t, work, "index.html", "v2")
	assert.Equal(t, gitWebhookDebounce, 10*time.Second)
	previous := gitWebhookDebounce
	t.Cleanup(func() { gitWebhookDebounce = previous })
	// the window outlasts a slow runner's build
	gitWebhookDebounce = time.Hour

	answer, err := deliver(signedDelivery(secret, "push", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookChecking)
	st := waitForGitApp(t, "jarvis")
	assert.Equal(t, st.Check.RemoteCommit, second)
	assert.Equal(t, st.Deployed.Commit, second, "the automatic rebuild deploys what the check saw")
	assert.DeepEqual(t, fake.Calls(), []string{"build " + second[:12], "retag " + second[:12], "start " + second[:12]})
	delivery := lastDelivery(t)
	assert.Equal(t, delivery.Forge, "github")
	assert.Equal(t, delivery.Event, "push")
	assert.Equal(t, delivery.Result, "checked")

	checked := st.Check.At
	pushTestCommit(t, work, "index.html", "v3")
	answer, err = deliver(signedDelivery(secret, "push", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookCoalesced)
	st = waitForGitApp(t, "jarvis")
	assert.Assert(t, st.Check.At.Equal(checked), "no second check")
	assert.Equal(t, st.Deployed.Commit, second)
	assert.Equal(t, lastDelivery(t).Result, "coalesced")

	// past the window, a push checks again
	gitWebhookDebounce = 0
	answer, err = deliver(signedDelivery(secret, "push", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookChecking)
	assert.Assert(t, waitForGitApp(t, "jarvis").Check.At.After(checked))
}

// A push to an app something else holds gets one check, run once the app is free; with the
// automatic rebuild off, that check deploys nothing.
func TestAPushToABusyAppQueuesOneCheckThatRunsOnceItIsFree(t *testing.T) {
	fake, work, first := deployedTestApp(t, false)
	freshGitWebhookRuns(t)
	secret := enableTestWebhook(t)
	body := []byte(`{"ref":"refs/heads/main"}`)
	second := pushTestCommit(t, work, "index.html", "v2")
	before := waitForGitApp(t, "jarvis").Check.At

	end, err := Begin("jarvis", gitOperationDeploy)
	assert.NilError(t, err)
	answer, err := deliver(signedDelivery(secret, "push", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookQueued)
	assert.Equal(t, lastDelivery(t).Result, "queued")
	answer, err = deliver(signedDelivery(secret, "push", body), body)
	assert.NilError(t, err)
	assert.Equal(t, answer, GitWebhookCoalesced, "one check waits, not two")

	time.Sleep(2 * howOftenToAskAgain)
	st, err := loadGitApp("jarvis")
	assert.NilError(t, err)
	assert.Assert(t, st.Check.At.Equal(before), "nothing checks while the app is held")

	end()
	st = waitForGitCheckAfter(t, "jarvis", before)
	assert.Equal(t, st.Check.RemoteCommit, second)
	assert.Equal(t, st.Deployed.Commit, first, "the automatic rebuild is off: nothing is deployed")
	assert.Equal(t, len(fake.Calls()), 0, "nothing is built")

	checked := st.Check.At
	time.Sleep(2 * howOftenToAskAgain)
	assert.Assert(t, waitForGitApp(t, "jarvis").Check.At.Equal(checked), "and only one check ran")
}

// syncBuffer is a log the checks running in the background write to while a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func TestAWebhookLogsNeitherItsBodyNorItsSignatures(t *testing.T) {
	_, work, _ := deployedTestApp(t, false)
	freshGitWebhookRuns(t)
	secret := enableTestWebhook(t)
	pushTestCommit(t, work, "index.html", "v2")

	var out syncBuffer
	logger.LogInitWithWriterSyncers(zapcore.AddSync(&out))
	t.Cleanup(func() { logger.LogInitWithWriterSyncers(zapcore.AddSync(os.Stdout)) })

	body := []byte(`{"ref":"refs/heads/main","marker":"body-marker-7f3a"}`)
	good := signedDelivery(secret, "push", body)
	bad := signedDelivery(strings.Repeat("0f", 32), "push", body)
	tooLarge := append(bytes.Repeat([]byte(" "), gitWebhookMaxBody), body...)

	_, err := deliver(good, body)
	assert.NilError(t, err)
	waitForGitApp(t, "jarvis")
	_, err = deliver(bad, body)
	assert.ErrorIs(t, err, ErrGitWebhookSignature)
	_, err = deliver(webhookHeader("X-Gitlab-Event", "Push Hook", "X-Gitlab-Token", "token-marker-91c2"), body)
	assert.ErrorIs(t, err, ErrGitWebhookSignature)
	_, err = deliver(signedDelivery(secret, "push", tooLarge), tooLarge)
	assert.ErrorIs(t, err, ErrGitWebhookTooLarge)

	log := out.String()
	for _, line := range []string{"checked", "refused: invalid signature", "refused: body over 5 MiB"} {
		assert.Assert(t, strings.Contains(log, line), "the log says %q:\n%s", line, log)
	}
	for name, value := range map[string]string{
		"the body":             "body-marker-7f3a",
		"the secret":           secret,
		"the right signature":  strings.TrimPrefix(good.Get("X-Hub-Signature-256"), "sha256="),
		"a wrong signature":    strings.TrimPrefix(bad.Get("X-Hub-Signature-256"), "sha256="),
		"a wrong gitlab token": "token-marker-91c2",
	} {
		assert.Assert(t, !strings.Contains(log, value), "%s is in the log", name)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/`

Expected: FAIL, compile errors in `git_webhook_receive_internal_test.go`: `undefined: gitWebhookRuns`, `undefined: ReceiveGitWebhook`, `undefined: ErrGitWebhookNotFound`, `undefined: gitWebhookMaxBody`, `undefined: gitWebhookDebounce`.

- [ ] **Step 3: Implement**

In `service/git_apps.go`, replace `CheckGitApp` and `beginGitCheck` (lines 358-402 once Task 2 is in, from `// CheckGitApp is "Check now".` down to the closing brace of `beginGitCheck`) with:

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
	go runGitCheck(st, end)

	return view, nil
}

// beginGitCheck claims the app for a check, adopts it when it is adoptable, and records
// the operation. The caller releases end once the check and what it starts are over.
func beginGitCheck(ctx context.Context, name string) (*gitApp, func(), error) {
	end, err := Begin(name, gitOperationCheck)
	if err != nil {
		return nil, nil, err
	}

	st, err := prepareGitCheck(ctx, name)
	if err != nil {
		end()
		return nil, nil, err
	}

	return st, end, nil
}

// prepareGitCheck is beginGitCheck for a caller that holds the app already.
func prepareGitCheck(ctx context.Context, name string) (*gitApp, error) {
	st, err := findGitApp(ctx, name)
	if err == nil && st.Origin == gitOriginAdoptable {
		err = adoptGitApp(ctx, st)
	}
	if err == nil {
		st.Operation = &gitOperation{Kind: gitOperationCheck, StartedAt: time.Now().UTC()}
		err = saveGitApp(st)
	}

	return st, err
}

// runGitCheck runs the check beginGitCheck prepared, cloning an app not cloned yet, then
// whatever the automatic rebuild may deploy, and releases end once all of it is over:
// "Check now" and a webhook alike.
func runGitCheck(st *gitApp, end func()) {
	ctx := context.Background()
	checkGitApp(ctx, st, true)
	deployGitAppAutomatically(ctx, st.App, end)
}
```

Create `service/git_webhook_receive.go`:

```go
package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/compose-spec/compose-go/v2/loader"
	"go.uber.org/zap"
)

// A forge telling CasaOS that an app's branch moved: the app is checked at once, the way
// "Check now" checks it, instead of at the next poll.

// Refusals of a delivery, answered 404, 401 and 413.
var (
	// ErrGitWebhookNotFound is an unknown app and a webhook turned off alike, so that the
	// route does not tell which apps exist.
	ErrGitWebhookNotFound  = errors.New("not found")
	ErrGitWebhookSignature = errors.New("invalid signature")
	ErrGitWebhookTooLarge  = errors.New("the body is over 5 MiB")
)

const (
	// gitWebhookMaxBody is as much body as is read and signed.
	gitWebhookMaxBody = 5 << 20
	// gitWebhookPatience is how long a check a webhook queued waits for a busy app.
	gitWebhookPatience = 30 * time.Minute
)

// gitWebhookDebounce is the least time between two checks webhooks start for one app: a
// forge may send several events for one push. A var, so tests can shorten it.
var gitWebhookDebounce = 10 * time.Second

// gitWebhookRuns is what webhooks started, guarded by gitWebhooks: when a webhook last
// started a check of each app, and the apps a queued check waits for.
var gitWebhookRuns = struct {
	last    map[string]time.Time
	pending map[string]bool
}{last: map[string]time.Time{}, pending: map[string]bool{}}

// ReceiveGitWebhook is a forge's delivery for app: the answer the forge is given, or one
// of the refusals. A refused delivery writes nothing, and no body, no header's value and
// no signature reaches the log.
func ReceiveGitWebhook(app string, header http.Header, body io.Reader) (string, error) {
	// a name from a request: an app's name or nothing, never a path
	if app == "" || app != loader.NormalizeProjectName(app) {
		return "", ErrGitWebhookNotFound
	}

	forge, event, action := gitWebhookEvent(header)
	log := func(outcome string) {
		logger.Info("git webhook", zap.String("app", app), zap.String("forge", forge), zap.String("event", event), zap.String("outcome", outcome))
	}

	payload, err := io.ReadAll(io.LimitReader(body, gitWebhookMaxBody+1))
	if err != nil {
		logger.Error("the body of a git webhook could not be read", zap.String("app", app), zap.Error(err))
		return "", err
	}
	if len(payload) > gitWebhookMaxBody {
		log("refused: body over 5 MiB")
		return "", ErrGitWebhookTooLarge
	}

	gitWebhooks.Lock()
	defer gitWebhooks.Unlock()

	hook, err := loadGitWebhook(app)
	if err != nil {
		logger.Error("a git webhook could not be read", zap.String("app", app), zap.Error(err))
		return "", err
	}
	if hook == nil || !pathExists(gitAppFile(app, ".json")) {
		return "", ErrGitWebhookNotFound
	}
	if !gitWebhookSigned(header, payload, hook.Secret) {
		log("refused: invalid signature")
		return "", ErrGitWebhookSignature
	}

	answer := action
	if action == gitWebhookCheck {
		if answer, err = scheduleGitWebhookCheck(app); err != nil {
			logger.Error("the check a git webhook asked for could not start", zap.String("app", app), zap.Error(err))
			return "", err
		}
	}

	result := answer
	if answer == GitWebhookChecking {
		result = "checked"
	}
	hook.LastDelivery = &GitWebhookDelivery{At: time.Now().UTC(), Forge: forge, Event: event, Result: result}
	if err := saveGitWebhook(app, hook); err != nil {
		logger.Error("the last delivery of a git webhook could not be written down", zap.String("app", app), zap.Error(err))
	}
	log(result)

	return answer, nil
}

// scheduleGitWebhookCheck starts the check a push asks for, gitWebhooks held: at once when
// the app is free, once it is when it is busy, and not at all when a webhook started one
// less than gitWebhookDebounce ago or one waits already.
func scheduleGitWebhookCheck(app string) (string, error) {
	if gitWebhookRuns.pending[app] || time.Since(gitWebhookRuns.last[app]) < gitWebhookDebounce {
		return GitWebhookCoalesced, nil
	}

	st, end, err := beginGitCheck(context.Background(), app)
	if errors.As(err, new(ErrAppBusy)) {
		gitWebhookRuns.pending[app] = true
		go runQueuedGitWebhookCheck(app)

		return GitWebhookQueued, nil
	}
	if err != nil {
		return "", err
	}

	gitWebhookRuns.last[app] = time.Now()
	go runGitCheck(st, end)

	return GitWebhookChecking, nil
}

// runQueuedGitWebhookCheck waits for the busy app to come free, up to gitWebhookPatience,
// then checks it as the webhook would have.
func runQueuedGitWebhookCheck(app string) {
	ctx := context.Background()
	end, err := beginWaiting(ctx, app, gitOperationCheck, gitWebhookPatience)

	gitWebhooks.Lock()
	delete(gitWebhookRuns.pending, app)
	if err == nil {
		gitWebhookRuns.last[app] = time.Now()
	}
	gitWebhooks.Unlock()

	if err != nil {
		logger.Info("a check a git webhook queued gave up waiting", zap.String("app", app), zap.Error(err))
		return
	}

	st, err := prepareGitCheck(ctx, app)
	if err != nil {
		end()
		logger.Error("a check a git webhook queued could not start", zap.String("app", app), zap.Error(err))

		return
	}

	runGitCheck(st, end)
}
```

Lock order note for the reviewer: `ReceiveGitWebhook` holds `gitWebhooks` and calls `Begin`, which never waits; `runQueuedGitWebhookCheck` waits in `beginWaiting` without `gitWebhooks`; `UpdateGitApp` and `DeleteGitApp` hold the app and take `gitWebhooks`. No cycle.

- [ ] **Step 4: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && gofmt -l service/git_webhook_receive.go service/git_webhook_receive_internal_test.go service/git_apps.go && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./service/ ./route/...`

Expected: no output.

Run (Linux): `go test ./service/ -run 'Webhook|Push' -count=1 -v`

Expected: `--- PASS` for `TestAWebhookIsRefusedAlikeForAnUnknownAppAndOneTurnedOff`, `TestAWebhookAnswersPingsAndOtherEventsAndRefusesWhatIsNotSigned`, `TestAPushChecksTheAppOnceWithinTenSeconds`, `TestAPushToABusyAppQueuesOneCheckThatRunsOnceItIsFree`, `TestAWebhookLogsNeitherItsBodyNorItsSignatures`, and the Task 1 and Task 2 tests; `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`.

Then once, for the `CheckGitApp` split (Linux): `go test ./service/ -count=1`

Expected: `ok  	github.com/ReCasaOS/CasaOS-AppManagement/service`.

- [ ] **Step 5: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add service/git_webhook_receive.go service/git_webhook_receive_internal_test.go service/git_apps.go
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(git): a signed push checks the app, debounced, queued while it is busy"
```

---

### Task 4: The OpenAPI view and PUT fields, and the PUT handler

**Files:**
- Modify: `api/app_management/openapi.yaml:1309-1315` (PUT summary and description), `:3235-3258` (`GitApp.required`), `:3330-3332` (after `build_log`: `webhook`, `GitWebhook`, `GitWebhookDelivery`), `:3508-3518` (`GitAppUpdateRequest`)
- Regenerate: `codegen/app_management_api.go` (gitignored)
- Modify: `route/v2/git.go:43-54` (`UpdateGitApp` handler, new `gitAppChanges`)
- Test: `route/v2/git_internal_test.go:3-13` (imports) and appended tests; `route/v2_git_test.go:33` (three rows)

**Interfaces:**
- Consumes: `service.GitAppView`, `service.GitWebhookView`, `service.GitWebhookDelivery`, `service.GitAppChanges` (Task 2).
- Produces:
  - Schemas `GitWebhook` (`enabled`, `path`, `last_delivery` required; `secret` optional) and `GitWebhookDelivery` (nullable; `at` date-time, `forge`, `event`, `result` strings, no `enum`); `GitApp.webhook` required; `GitAppUpdateRequest.webhook_enabled` and `.regenerate_webhook_secret` (boolean).
  - Generated: `codegen.GitWebhook{Enabled bool; LastDelivery *GitWebhookDelivery; Path string; Secret *string}`, `codegen.GitWebhookDelivery{At time.Time; Event, Forge, Result string}`, `codegen.GitApp.Webhook GitWebhook`, `codegen.GitAppUpdateRequest.WebhookEnabled *bool`, `.RegenerateWebhookSecret *bool`.
  - `func gitAppChanges(body codegen.GitAppUpdateRequest) service.GitAppChanges` in package `v2`.

- [ ] **Step 1: Write the failing test**

In `route/v2/git_internal_test.go`, replace the import block (lines 3-13) with:

```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/labstack/echo/v4"
	"github.com/samber/lo"
	"gotest.tools/v3/assert"
)
```

and append at the end of the file:

```go
// The service builds the view itself: what it answers is what the spec declares.
func TestAGitAppsWebhookIsAnsweredAsTheSpecDeclaresIt(t *testing.T) {
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	view := &service.GitAppView{App: "jarvis", History: []service.GitHistoryView{}, Webhook: service.GitWebhookView{
		Enabled: true, Path: "/v2/app_management/git/jarvis/webhook", Secret: strings.Repeat("5e", 32),
		LastDelivery: &service.GitWebhookDelivery{At: at, Forge: "github", Event: "push", Result: "checked"},
	}}
	raw, err := json.Marshal(view)
	assert.NilError(t, err)

	var answered codegen.GitApp
	assert.NilError(t, json.Unmarshal(raw, &answered))
	assert.Equal(t, answered.Webhook.Enabled, true)
	assert.Equal(t, answered.Webhook.Path, "/v2/app_management/git/jarvis/webhook")
	assert.Equal(t, lo.FromPtr(answered.Webhook.Secret), strings.Repeat("5e", 32))
	assert.Assert(t, answered.Webhook.LastDelivery.At.Equal(at))
	assert.Equal(t, answered.Webhook.LastDelivery.Forge, "github")
	assert.Equal(t, answered.Webhook.LastDelivery.Event, "push")
	assert.Equal(t, answered.Webhook.LastDelivery.Result, "checked")
}

func TestAPutPassesTheWebhookFieldsOn(t *testing.T) {
	var body codegen.GitAppUpdateRequest
	assert.NilError(t, json.Unmarshal([]byte(`{"auto_deploy":true,"webhook_enabled":false,"regenerate_webhook_secret":true}`), &body))

	assert.DeepEqual(t, gitAppChanges(body), service.GitAppChanges{
		AutoDeploy: lo.ToPtr(true), WebhookEnabled: lo.ToPtr(false), RegenerateWebhookSecret: lo.ToPtr(true),
	})
}
```

In `route/v2_git_test.go`, after the row `{http.MethodPut, "/v2/app_management/git/no-such-app", `{"auto_deploy":true}`, http.StatusNotFound},` add:

```go
		{http.MethodPut, "/v2/app_management/git/no-such-app", `{"webhook_enabled":true,"regenerate_webhook_secret":true}`, http.StatusNotFound},
		{http.MethodPut, "/v2/app_management/git/no-such-app", `{"webhook_enabled":"yes"}`, http.StatusBadRequest},
		{http.MethodPut, "/v2/app_management/git/no-such-app", `{"regenerate_webhook_secret":1}`, http.StatusBadRequest},
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./route/...`

Expected: FAIL, compile errors in `route/v2/git_internal_test.go`: `answered.Webhook undefined (type codegen.GitApp has no field or method Webhook)`, `undefined: gitAppChanges`. (On Linux the two `"yes"`/`1` rows of `TestTheGitRoutesAreServed` answer 404 instead of 400 until the spec declares the fields.)

- [ ] **Step 3: Implement**

In `api/app_management/openapi.yaml` (CRLF line endings, keep them), replace the start of `PUT /git/{app}` (lines 1309-1315):

```yaml
    put:
      summary: Change the branch, the automatic rebuild or the access of a git app
      description: |
        Every field is optional. The branch changes until the app is cloned. Turning
        `auto_deploy` on clears `auto_paused`. A token is accepted and never returned; an empty
        one is refused, and switching `access` to `none` or `key` forgets the one kept. Adopts an
        adoptable app: 400 when its folder is in detached HEAD or has no remote.
```

with:

```yaml
    put:
      summary: Change the branch, the automatic rebuild, the access or the webhook of a git app
      description: |
        Every field is optional. The branch changes until the app is cloned. Turning
        `auto_deploy` on clears `auto_paused`. A token is accepted and never returned; an empty
        one is refused, and switching `access` to `none` or `key` forgets the one kept. Adopts an
        adoptable app: 400 when its folder is in detached HEAD or has no remote.

        `webhook_enabled` turns the webhook on, generating its secret when there is none, or
        off, forgetting the secret. `regenerate_webhook_secret` replaces the secret at once, the
        old one refused from then on; 400 while the webhook is off.
```

In `GitApp.required`, replace:

```yaml
        - env_template
        - build_log
      properties:
        app:
```

with:

```yaml
        - env_template
        - build_log
        - webhook
      properties:
        app:
```

Replace the `build_log` property of `GitApp` (lines 3330-3332):

```yaml
        build_log:
          type: string
          description: The last 64 KiB of the last build's log.
```

with:

```yaml
        build_log:
          type: string
          description: The last 64 KiB of the last build's log.
        webhook:
          $ref: "#/components/schemas/GitWebhook"

    GitWebhook:
      description: |
        The app's webhook: a push the forge POSTs to `path`, signed with `secret`, checks the
        app at once. The secret is not in backups: a restored app comes back with its webhook off.
      required:
        - enabled
        - path
        - last_delivery
      properties:
        enabled:
          type: boolean
        path:
          type: string
          description: The route to give the forge, after the address it reaches the box by.
          example: /v2/app_management/git/jarvis/webhook
        secret:
          type: string
          description: 64 hex characters, only while `enabled`.
          example: 5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e
        last_delivery:
          $ref: "#/components/schemas/GitWebhookDelivery"

    GitWebhookDelivery:
      description: The last delivery whose signature was valid. Null until the first one.
      nullable: true
      required:
        - at
        - forge
        - event
        - result
      properties:
        at:
          type: string
          format: date-time
        forge:
          type: string
          description: |
            One of `github`, `gitea`, `forgejo`, `gogs`, `gitlab`, `unknown`: named after the
            event header, else after the signature header. Not declared as an enum, for the
            reason given on `GitAppState`.
          example: github
        event:
          type: string
          description: The event header's value, at most 64 characters; empty when the forge sent none.
          example: push
        result:
          type: string
          description: |
            One of `checked`, `coalesced`, `queued`, `ignored`, `pong`. Not declared as an enum,
            for the reason given on `GitAppState`.
          example: checked
```

In `GitAppUpdateRequest` (lines 3508-3518), replace:

```yaml
        token:
          type: string
          description: Never returned; an empty token is refused.
```

with:

```yaml
        token:
          type: string
          description: Never returned; an empty token is refused.
        webhook_enabled:
          type: boolean
          description: On generates the secret when there is none; off forgets it.
        regenerate_webhook_secret:
          type: boolean
          description: Replaces the secret at once; 400 while the webhook is off.
```

Regenerate (Git Bash):

```bash
cd D:/clients/casaos/CasaOS-AppManagement && go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,server,spec -package codegen api/app_management/openapi.yaml > codegen/app_management_api.go && grep -cE '^[[:blank:]][A-Z][[:alnum:]_]* +[[:alnum:]_]+ = "' codegen/app_management_api.go
```

Expected: `34` (another number means an enum constant was renamed: stop and look at `codegen/app_management_api.go`).

In `route/v2/git.go`, replace the end of the `UpdateGitApp` handler:

```go
	view, err := service.UpdateGitApp(ctx.Request().Context(), app, service.GitAppChanges{
		Branch: body.Branch, AutoDeploy: body.AutoDeploy, Access: body.Access, Token: body.Token,
	})

	return gitAppAnswer(ctx, http.StatusOK, view, err)
}
```

with:

```go
	view, err := service.UpdateGitApp(ctx.Request().Context(), app, gitAppChanges(body))

	return gitAppAnswer(ctx, http.StatusOK, view, err)
}

// gitAppChanges is what a PUT asks the service to change.
func gitAppChanges(body codegen.GitAppUpdateRequest) service.GitAppChanges {
	return service.GitAppChanges{
		Branch: body.Branch, AutoDeploy: body.AutoDeploy, Access: body.Access, Token: body.Token,
		WebhookEnabled: body.WebhookEnabled, RegenerateWebhookSecret: body.RegenerateWebhookSecret,
	}
}
```

- [ ] **Step 4: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && gofmt -l route/v2/git.go route/v2/git_internal_test.go route/v2_git_test.go && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./... && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./... && echo build-ok`

Expected: `build-ok` and nothing else.

Run (Linux, after `go generate ./...`): `go test ./route/ ./route/v2/ -run 'TestAGitAppsWebhookIsAnsweredAsTheSpecDeclaresIt|TestAPutPassesTheWebhookFieldsOn|TestTheGitRoutesAreServed|TestAGitAppIsAnsweredUnderData' -count=1 -v`

Expected: `--- PASS` for each, `ok` for both packages.

- [ ] **Step 5: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add api/app_management/openapi.yaml route/v2/git.go route/v2/git_internal_test.go route/v2_git_test.go
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(api): a git app's webhook in its view and its PUT"
```

(`codegen/` is gitignored: it is not staged.)

---

### Task 5: The webhook route: handler, JWT exemption, validator bypass

**Files:**
- Modify: `api/app_management/openapi.yaml:1419` after Task 4 (insert `/git/{app}/webhook` before the comment `# the endpoint is newer version of POST /appstore ...`)
- Regenerate: `codegen/app_management_api.go` (gitignored)
- Modify: `route/v2/git.go:90` after Task 4 (insert the handler and `gitWebhookAnswer` before `gitAppAnswer`)
- Modify: `route/v2.go:3-22` (import `regexp`), `:48` (before `InitV2Router`: `gitWebhookPath`, `isGitWebhook`), `:67-69` (JWT skipper), `:109-111` (validator skipper)
- Test: `route/v2/git_internal_test.go:45` after Task 4 (insert before `TestAGitAppIsAnsweredUnderData`); `route/v2_git_test.go:3-14` (import `logger`) and an appended test

**Interfaces:**
- Consumes: `service.ReceiveGitWebhook`, `service.ErrGitWebhookNotFound`, `service.ErrGitWebhookSignature`, `service.ErrGitWebhookTooLarge`, `service.GitWebhookPong` and the other answers (Tasks 1 and 3), `codegen.ResponseOK` (= `BaseResponse{Message *string}`), `codegen.GitAppName`.
- Produces:
  - Spec path `/git/{app}/webhook`, `post`, operationId `receiveGitWebhook`, `security: []`, no request body, responses 200, 202, 401, 404, 413, 500.
  - Generated `ServerInterface.ReceiveGitWebhook(ctx echo.Context, app GitAppName) error`, registered by `RegisterHandlersWithBaseURL` as `POST <base>/git/:app/webhook`.
  - `func (a *AppManagement) ReceiveGitWebhook(ctx echo.Context, app codegen.GitAppName) error`.
  - `func gitWebhookAnswer(ctx echo.Context, answer string, err error) error`: 200 pong, 202 other answers, 404/401/413 with the error's text, 500 `internal error`; body `{"message":"..."}`.
  - `var gitWebhookPath = regexp.MustCompile(`^/v2/app_management/git/[^/]+/webhook$`)`, `func isGitWebhook(c echo.Context) bool` in package `route`.

- [ ] **Step 1: Write the failing test**

In `route/v2/git_internal_test.go`, insert before `func TestAGitAppIsAnsweredUnderData(t *testing.T) {`:

```go
// A forge learns how its delivery went, and a caller without a valid signature only which
// refusal it got.
func TestAGitWebhookIsAnsweredWithItsStatus(t *testing.T) {
	for _, c := range []struct {
		answer string
		err    error
		status int
		body   string
	}{
		{service.GitWebhookPong, nil, http.StatusOK, "pong"},
		{service.GitWebhookChecking, nil, http.StatusAccepted, "checking"},
		{service.GitWebhookCoalesced, nil, http.StatusAccepted, "coalesced"},
		{service.GitWebhookQueued, nil, http.StatusAccepted, "queued"},
		{service.GitWebhookIgnored, nil, http.StatusAccepted, "ignored"},
		{"", service.ErrGitWebhookSignature, http.StatusUnauthorized, "invalid signature"},
		{"", service.ErrGitWebhookNotFound, http.StatusNotFound, "not found"},
		{"", service.ErrGitWebhookTooLarge, http.StatusRequestEntityTooLarge, "the body is over 5 MiB"},
		{"", errors.New("open /var/lib/casaos/git_apps/jarvis.webhook: permission denied"), http.StatusInternalServerError, "internal error"},
	} {
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), recorder)
		assert.NilError(t, gitWebhookAnswer(ctx, c.answer, c.err))

		assert.Equal(t, recorder.Code, c.status, c.body)
		assert.Equal(t, recorder.Body.String(), fmt.Sprintf("{\"message\":%q}\n", c.body))
	}
}

```

In `route/v2_git_test.go`, replace the import block (lines 3-14) with:

```go
import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/config"
	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)
```

and append at the end of the file:

```go
// A forge's delivery is the one request served without a token, and only that method on that
// path: every other method there, and every other route, still wants one.
func TestOnlyAGitWebhookIsServedWithoutAToken(t *testing.T) {
	runtime := t.TempDir()
	previous := config.CommonInfo.RuntimePath
	config.CommonInfo.RuntimePath = runtime
	t.Cleanup(func() { config.CommonInfo.RuntimePath = previous })
	assert.NilError(t, os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret"), 0o600))
	logger.LogInitConsoleOnly()

	router := InitV2Router()

	for _, call := range []struct {
		method, path, contentType, body string
		want                            int
		answer                          string
	}{
		// the forge's route answers, whatever the body: a 404 says it was reached
		{http.MethodPost, "/v2/app_management/git/no-such-app/webhook", "application/json", `{"ref":"refs/heads/main"}`, http.StatusNotFound, `{"message":"not found"}`},
		{http.MethodPost, "/v2/app_management/git/no-such-app/webhook", "application/x-www-form-urlencoded", "payload=%7B%22ref%22%3A%22refs%2Fheads%2Fmain%22%7D", http.StatusNotFound, `{"message":"not found"}`},
		{http.MethodPost, "/v2/app_management/git/no-such-app/webhook", "", "", http.StatusNotFound, `{"message":"not found"}`},
		{http.MethodPost, "/v2/app_management/git/no-such-app/webhook", "application/json", strings.Repeat(" ", 5<<20+1), http.StatusRequestEntityTooLarge, `{"message":"the body is over 5 MiB"}`},
		// everything else keeps the token
		{http.MethodGet, "/v2/app_management/git/no-such-app/webhook", "", "", http.StatusUnauthorized, ""},
		{http.MethodPut, "/v2/app_management/git/no-such-app/webhook", "", "", http.StatusUnauthorized, ""},
		{http.MethodDelete, "/v2/app_management/git/no-such-app/webhook", "", "", http.StatusUnauthorized, ""},
		{http.MethodPost, "/v2/app_management/git/no-such-app/webhook/more", "", "", http.StatusUnauthorized, ""},
		{http.MethodPost, "/v2/app_management/git/webhook", "", "", http.StatusUnauthorized, ""},
		{http.MethodPost, "/v2/app_management/git/no-such-app/check", "", "", http.StatusUnauthorized, ""},
		{http.MethodPost, "/v2/app_management/git/no-such-app/deploy", "", "", http.StatusUnauthorized, ""},
		{http.MethodGet, "/v2/app_management/git/no-such-app", "", "", http.StatusUnauthorized, ""},
		{http.MethodPut, "/v2/app_management/git/no-such-app", "application/json", `{"webhook_enabled":true}`, http.StatusUnauthorized, ""},
		{http.MethodPost, "/v2/app_management/git", "application/json", `{"name":"jarvis","url":"https://example.invalid/r.git","access":"none"}`, http.StatusUnauthorized, ""},
		{http.MethodGet, "/v2/app_management/web/appgrid", "", "", http.StatusUnauthorized, ""},
	} {
		request := httptest.NewRequest(call.method, call.path, strings.NewReader(call.body))
		// from another machine, as a forge calls: the internal secret's exemption does not apply
		request.RemoteAddr = "203.0.113.7:40000"
		if call.contentType != "" {
			request.Header.Set("Content-Type", call.contentType)
		}

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)

		assert.Equal(t, recorder.Code, call.want, "%s %s: %s", call.method, call.path, recorder.Body.String())
		if call.answer != "" {
			assert.Equal(t, strings.TrimSpace(recorder.Body.String()), call.answer, "%s %s", call.method, call.path)
		}
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./route/...`

Expected: FAIL, `undefined: gitWebhookAnswer` in `route/v2/git_internal_test.go`. (On Linux, `TestOnlyAGitWebhookIsServedWithoutAToken` fails too: the POST rows answer 401, the JWT still applying.)

- [ ] **Step 3: Implement**

In `api/app_management/openapi.yaml` (CRLF, keep them), insert before the line `  # the endpoint is newer version of POST /appstore for the demand of @ETWang1991` (after the `/git/{app}/deploy` block and its blank line):

```yaml
  /git/{app}/webhook:
    post:
      summary: A forge tells that the repository changed
      description: |
        Called by the forge, not the dashboard: no token, but the body signed with the app's
        webhook secret. `X-Hub-Signature-256: sha256=<hex>`, `X-Gitea-Signature`,
        `X-Forgejo-Signature` and `X-Gogs-Signature` carry the hex HMAC-SHA256 of the raw body,
        `X-Gitlab-Token` the secret itself; at least one is sent and every one sent is valid, or
        401. The body, read up to 5 MiB (413 beyond), is signed and never interpreted, so this
        route is not checked against this spec: forges send form-encoded bodies too.

        A push (`X-GitHub-Event`, `X-Gitea-Event`, `X-Forgejo-Event` or `X-Gogs-Event` `push`;
        `X-Gitlab-Event` `Push Hook` or `Tag Push Hook`), or a signed delivery naming no event,
        starts what `POST /git/{app}/check` starts and answers 202 `checking`; a push less than
        10 seconds after the last check a webhook started, or while one is queued, answers 202
        `coalesced`; a push to an app another operation holds queues one check that runs once
        the app is free, waiting up to 30 minutes, and answers 202 `queued`. GitHub's `ping`
        answers 200 `pong`; any other event 202 `ignored`. 404 alike for an app that does not
        exist and a webhook that is off.
      operationId: receiveGitWebhook
      security: []
      tags:
        - Git methods
      parameters:
        - $ref: "#/components/parameters/GitAppName"
      responses:
        "200":
          $ref: "#/components/responses/ResponseOK"
        "202":
          $ref: "#/components/responses/ResponseOK"
        "401":
          description: No valid signature
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/BaseResponse"
              example:
                message: invalid signature
        "404":
          $ref: "#/components/responses/ResponseNotFound"
        "413":
          description: A body over 5 MiB
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/BaseResponse"
              example:
                message: the body is over 5 MiB
        "500":
          $ref: "#/components/responses/ResponseInternalServerError"

```

Regenerate (Git Bash):

```bash
cd D:/clients/casaos/CasaOS-AppManagement && go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,server,spec -package codegen api/app_management/openapi.yaml > codegen/app_management_api.go && grep -cE '^[[:blank:]][A-Z][[:alnum:]_]* +[[:alnum:]_]+ = "' codegen/app_management_api.go
```

Expected: `34`. Until the handler below exists, `go vet ./route/...` reports `*AppManagement does not implement codegen.ServerInterface (missing method ReceiveGitWebhook)`.

In `route/v2/git.go`, insert before `func gitAppAnswer(ctx echo.Context, status int, view *service.GitAppView, err error) error {`:

```go
// ReceiveGitWebhook is a forge's delivery. It carries no token: route/v2.go lets this
// method and path through, and the service checks the signature instead.
func (a *AppManagement) ReceiveGitWebhook(ctx echo.Context, app codegen.GitAppName) error {
	answer, err := service.ReceiveGitWebhook(app, ctx.Request().Header, ctx.Request().Body)

	return gitWebhookAnswer(ctx, answer, err)
}

// gitWebhookAnswer tells the forge how its delivery went, and a caller not proven by a
// signature nothing more than which refusal it got.
func gitWebhookAnswer(ctx echo.Context, answer string, err error) error {
	status := http.StatusAccepted
	switch {
	case errors.Is(err, service.ErrGitWebhookNotFound):
		status, answer = http.StatusNotFound, err.Error()
	case errors.Is(err, service.ErrGitWebhookSignature):
		status, answer = http.StatusUnauthorized, err.Error()
	case errors.Is(err, service.ErrGitWebhookTooLarge):
		status, answer = http.StatusRequestEntityTooLarge, err.Error()
	case err != nil:
		// the service logged what failed
		status, answer = http.StatusInternalServerError, "internal error"
	case answer == service.GitWebhookPong:
		status = http.StatusOK
	}

	return ctx.JSON(status, codegen.ResponseOK{Message: &answer})
}

```

In `route/v2.go`, add `"regexp"` to the standard imports:

```go
import (
	"crypto/ecdsa"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
```

Insert before `func InitV2Router() http.Handler {`:

```go
// gitWebhookPath is the one route a request reaches without a token: a forge proves itself
// with the app's webhook secret instead, and sends bodies this spec does not describe.
var gitWebhookPath = regexp.MustCompile(`^/v2/app_management/git/[^/]+/webhook$`)

// isGitWebhook is a forge's delivery: that method on that path, and nothing else.
func isGitWebhook(c echo.Context) bool {
	return c.Request().Method == http.MethodPost && gitWebhookPath.MatchString(c.Request().URL.Path)
}

```

Replace the JWT skipper:

```go
		Skipper: func(c echo.Context) bool {
			return external.IsInternalRequest(c.RealIP(), c.Request().Header.Get(echo.HeaderAuthorization), config.CommonInfo.RuntimePath)
		},
```

with:

```go
		Skipper: func(c echo.Context) bool {
			return isGitWebhook(c) || external.IsInternalRequest(c.RealIP(), c.Request().Header.Get(echo.HeaderAuthorization), config.CommonInfo.RuntimePath)
		},
```

Replace the validator (the uncommented `e.Use(middleware.OapiRequestValidatorWithOptions(...))`, lines 109-111):

```go
	e.Use(middleware.OapiRequestValidatorWithOptions(_swagger, &middleware.Options{
		Options: openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}))
```

with:

```go
	e.Use(middleware.OapiRequestValidatorWithOptions(_swagger, &middleware.Options{
		Options: openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		Skipper: isGitWebhook,
	}))
```

- [ ] **Step 4: Run it and see it pass**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && gofmt -l route/v2.go route/v2/git.go route/v2/git_internal_test.go route/v2_git_test.go && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./... && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./... && echo build-ok`

Expected: `build-ok` and nothing else.

Run (Linux, after `go generate ./...`): `go test ./route/ ./route/v2/ -count=1 -v -run 'Git'`

Expected: `--- PASS` for `TestOnlyAGitWebhookIsServedWithoutAToken`, `TestTheGitRoutesAreServed`, `TestAGitWebhookIsAnsweredWithItsStatus`, `TestAGitAppRefusalIsAnsweredWithItsStatus`, `TestAGitAppIsAnsweredUnderData`, `TestAGitAppsWebhookIsAnsweredAsTheSpecDeclaresIt`; `ok` for both packages.

- [ ] **Step 5: Commit**

```bash
git -C D:/clients/casaos/CasaOS-AppManagement add api/app_management/openapi.yaml route/v2.go route/v2/git.go route/v2/git_internal_test.go route/v2_git_test.go
git -C D:/clients/casaos/CasaOS-AppManagement -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(route): serve a git app's webhook without a token"
```

---

### Task 6: Whole-branch verification

**Files:** none changed.

**Interfaces:** Consumes everything above; produces nothing.

- [ ] **Step 1: Regenerate and count from a clean spec**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,server,spec -package codegen api/app_management/openapi.yaml > codegen/app_management_api.go && grep -cE '^[[:blank:]][A-Z][[:alnum:]_]* +[[:alnum:]_]+ = "' codegen/app_management_api.go`

Expected: `34`.

- [ ] **Step 2: Vet and build for Linux**

Run (Git Bash): `cd D:/clients/casaos/CasaOS-AppManagement && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./... && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./... && echo build-ok`

Expected: `build-ok`.

- [ ] **Step 3: Nothing left over in the tree**

Run (Git Bash): `git -C D:/clients/casaos/CasaOS-AppManagement status --porcelain`

Expected: nothing tracked is modified (untracked files other agents left, such as other plans, stay untouched).

- [ ] **Step 4: The whole suite on Linux**

Run (Linux, as CI does): `go generate ./... && go test -race ./...`

Expected: `ok` for every package with tests. A failure of a test unrelated to git apps that also fails on the parent commit is reported, not fixed here.

No commit in this task.

---

## Spec coverage

| Spec requirement | Task(s) | Test(s) |
|---|---|---|
| `POST /v2/app_management/git/{app}/webhook`, reached like the rest of AppManagement | 5 | `TestOnlyAGitWebhookIsServedWithoutAToken` |
| JWT exempt for this method and path only; `GET` and every other route still 401 | 5 | `TestOnlyAGitWebhookIsServedWithoutAToken` |
| Outside the OpenAPI request validator (form-encoded body) | 5 | `TestOnlyAGitWebhookIsServedWithoutAToken` (form row) |
| Signature headers, HMAC-SHA256 of the raw body, `X-Gitlab-Token` equality, constant time | 1 | `TestAWebhookIsSignedOnlyWithTheAppsSecret` |
| Right secret, wrong secret, truncated, missing/wrong `sha256=` prefix, empty header, two contradicting headers (401; the spec amended to say every header sent must be valid) | 1 | `TestAWebhookIsSignedOnlyWithTheAppsSecret` |
| Otherwise 401; no 2xx without a valid signature | 1, 3, 5 | `TestAWebhookAnswersPingsAndOtherEventsAndRefusesWhatIsNotSigned`, `TestAGitWebhookIsAnsweredWithItsStatus` |
| Webhook disabled and unknown app: the same 404 | 3, 5 | `TestAWebhookIsRefusedAlikeForAnUnknownAppAndOneTurnedOff`, `TestOnlyAGitWebhookIsServedWithoutAToken`, `TestAGitWebhookIsAnsweredWithItsStatus` |
| Body read up to 5 MiB, 413 beyond, never parsed | 3, 5 | `TestAWebhookAnswersPingsAndOtherEventsAndRefusesWhatIsNotSigned`, `TestOnlyAGitWebhookIsServedWithoutAToken` |
| Log line names app, forge, event, outcome; never body, header values, signature | 3 | `TestAWebhookLogsNeitherItsBodyNorItsSignatures` |
| Events: GitHub `ping` 200 `pong`; `push` on four headers and GitLab `Push Hook`/`Tag Push Hook` check; others 202 `ignored`; none of them: check | 1, 3, 5 | `TestAWebhookNamesItsForgeAndItsEvent`, `TestAWebhookAnswersPingsAndOtherEventsAndRefusesWhatIsNotSigned`, `TestAGitWebhookIsAnsweredWithItsStatus` |
| Forge named after the event header, else the signature header, else `unknown` | 1 | `TestAWebhookNamesItsForgeAndItsEvent` |
| 202 `checking`/`coalesced`/`queued`, the check in the background | 3, 5 | `TestAPushChecksTheAppOnceWithinTenSeconds`, `TestAPushToABusyAppQueuesOneCheckThatRunsOnceItIsFree`, `TestAGitWebhookIsAnsweredWithItsStatus` |
| Same path as the button: check, then `deployGitAppAutomatically` | 3 | `TestAPushChecksTheAppOnceWithinTenSeconds` (deploys with automatic rebuild on) |
| Debounce 10 s per app, `coalesced` inside the window | 3 | `TestAPushChecksTheAppOnceWithinTenSeconds` |
| Busy app: one pending check, runs once free, up to 30 min; further pushes `coalesced` | 3 | `TestAPushToABusyAppQueuesOneCheckThatRunsOnceItIsFree` |
| Automatic deployment off: the check runs, nothing deployed | 3 | `TestAPushToABusyAppQueuesOneCheckThatRunsOnceItIsFree`, `TestAWebhookLogsNeitherItsBodyNorItsSignatures` |
| Last delivery `{at, forge, event, result}`; refused requests never written | 3 | `TestAWebhookAnswersPingsAndOtherEventsAndRefusesWhatIsNotSigned`, `TestAPushChecksTheAppOnceWithinTenSeconds`, `TestAWebhookIsRefusedAlikeForAnUnknownAppAndOneTurnedOff` |
| Secret: 32 random bytes hex, with the app's secrets, root 0600 | 2 | `TestAWebhookSecretIsMadeReplacedAndForgotten` |
| Enabling generates, disabling forgets (a later enable makes a new one), regenerating replaces | 2 | `TestAWebhookSecretIsMadeReplacedAndForgotten` |
| The old secret refused at once after regenerating | 3 | `TestAWebhookAnswersPingsAndOtherEventsAndRefusesWhatIsNotSigned` |
| View gains `webhook {enabled, path, secret (only while enabled), last_delivery}` | 2, 4 | `TestAWebhookSecretIsMadeReplacedAndForgotten`, `TestTheViewOfAGitAppIsTheContract`, `TestAGitAppsWebhookIsAnsweredAsTheSpecDeclaresIt` |
| `PUT` gains `webhook_enabled`, `regenerate_webhook_secret` (400 when disabled) | 2, 4 | `TestAWebhookSecretIsMadeReplacedAndForgotten`, `TestAPutPassesTheWebhookFieldsOn`, `TestTheGitRoutesAreServed` |
| Backups carry no secret; a restored app comes back disabled, registered by the restore or already | 2 | `TestARestoredGitAppComesBackWithItsWebhookOff` (backup manifest unchanged: `TestABackupOfAGitAppRecordsWhereItsCodeComesFrom`) |
| Uninstall/removal forgets the secret | 2 | `TestAWebhookSecretIsMadeReplacedAndForgotten` (`DeleteGitApp`) |
| OpenAPI schema and codegen; the enum-constant count stays green (34) | 4, 5, 6 | count command in Tasks 4, 5, 6 |
| The five-minute poll stays | none changed | `CheckGitApps` untouched; `go test ./service/` in Task 3 |
| AppManagement released first (own tag) | outside this plan | release step after merge |
