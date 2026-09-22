# Git app webhooks — design

Status: approved in conversation on 2026-09-23, section by section. Builds on
`2026-09-16-git-apps-design.md` (git apps), whose "Out of scope" listed webhooks.

## Goal

A push to a git app's repository makes the box check the app at once, instead of waiting up to
five minutes for the poll. Both situations are served: a public forge (GitHub, GitLab) reaching
a box exposed through a reverse proxy, a tunnel or a forwarded port, and a forge on the local
network (Gitea, Forgejo) reaching the box directly.

## Decisions (from the owner)

- One webhook per app, handled by AppManagement (approach A). No new component.
- A webhook only **checks**: it runs exactly what "Check now" runs. It deploys only when the
  app's automatic deployment would (on, not paused, not blocked); otherwise the dashboard shows
  the new commits and the owner deploys.
- The five-minute poll stays; the webhook is added to it.

## The route

`POST /v2/app_management/git/{app}/webhook`

- Exempt from the JWT for this method and this path only (`^/v2/app_management/git/[^/]+/webhook$`);
  every other route, and any other method on this path, keeps the JWT.
- Registered outside the OpenAPI request validator: forges may send a form-encoded body, and the
  body is not interpreted anyway.
- Reached through the gateway like the rest of AppManagement.

### Authentication

With the app's webhook secret, every comparison in constant time. The request is accepted when
one of these headers is valid:

| Header | Forges | Check |
|---|---|---|
| `X-Hub-Signature-256: sha256=<hex>` | GitHub, Gitea, Forgejo, Bitbucket | HMAC-SHA256 of the raw body |
| `X-Gitea-Signature: <hex>` | Gitea | HMAC-SHA256 of the raw body |
| `X-Forgejo-Signature: <hex>` | Forgejo | HMAC-SHA256 of the raw body |
| `X-Gogs-Signature: <hex>` | Gogs | HMAC-SHA256 of the raw body |
| `X-Gitlab-Token: <secret>` | GitLab | equality with the secret |

Otherwise **401**.

### Guards

- Webhook disabled, or no such app: **404**, the same answer in both cases, so that the route
  does not tell which apps exist.
- Body read up to 5 MiB; beyond, **413**. The body is used for the signature only; nothing in it
  is parsed or trusted.
- The log line names the app, the forge, the event and the outcome; never the body, the headers'
  values or the signature.

### Events

| Forge header | Value | Action |
|---|---|---|
| `X-GitHub-Event` | `ping` | 200 `{"message":"pong"}` |
| `X-GitHub-Event`, `X-Gitea-Event`, `X-Forgejo-Event`, `X-Gogs-Event` | `push` | check |
| `X-Gitlab-Event` | `Push Hook`, `Tag Push Hook` | check |
| any of the above | anything else | 202 `{"message":"ignored"}` |
| none of the above | — | check (a signed request from a less common forge) |

The forge is named after the event header present (`github`, `gitea`, `forgejo`, `gogs`,
`gitlab`), else after the signature header, else `unknown`.

### Answer

202 as soon as the check is scheduled, with `{"message": "checking" | "coalesced" | "queued"}`.
The check runs in the background.

## Triggering

- **Same path as the button**: a check (`ls-remote` of the tracked branch, clone if needed),
  then `deployGitAppAutomatically`, exactly as `CheckGitApp` runs them.
- **Debounce**: at most one webhook-triggered check every 10 seconds per app. A push inside that
  window answers `coalesced` (forges may send several events for one `git push`).
- **Busy app** (a build or another operation holds it): one pending check is kept for the app
  and runs as soon as the app is free, waiting up to 30 minutes (the same waiting mechanism as
  backups since v0.4.94). Answer `queued`. Further pushes while one is pending answer
  `coalesced`.
- **Last delivery**: the app keeps the last valid delivery, `{at, forge, event, result}` with
  `result` one of `checked`, `coalesced`, `queued`, `ignored`, `pong`. Refused requests (bad
  signature, too large) are logged, never written to the state: nobody unauthenticated can make
  the box write.

## The secret and the API

- 32 random bytes, hex-encoded (64 characters), per app, kept with the app's other secrets
  (deploy key, token) under `/var/lib/casaos/git_apps`, root, 0600.
- **Enabling** the webhook generates a secret when there is none. **Disabling** forgets it (a
  later enable generates a new one). **Regenerating** replaces it at once; the old one is refused
  from that moment.
- The app's view (`GET /v2/app_management/git/{app}` and every answer that returns it) gains:

  ```json
  "webhook": {
    "enabled": true,
    "path": "/v2/app_management/git/<app>/webhook",
    "secret": "<64 hex>",
    "last_delivery": {"at": "<RFC 3339>", "forge": "github", "event": "push", "result": "checked"}
  }
  ```

  `secret` is present only while the webhook is enabled; `last_delivery` is `null` until the
  first valid delivery.
- `PUT /v2/app_management/git/{app}` accepts two more fields: `webhook_enabled` (bool) and
  `regenerate_webhook_secret` (bool; 400 when the webhook is disabled).
- **Backups** do not carry the secret, as they carry neither the deploy key nor the token today
  (a backup records remote, branch and commit). A restored app comes back with its webhook
  disabled; the dashboard says so.

## Dashboard

In the git app's Repository tab, below the existing settings, a "Webhook" section:

- A switch "Check on every push".
- When on:
  - **URL**: the dashboard's current origin followed by `path`, with a copy button, and the note
    "If your forge reaches the box by another address (a domain, a tunnel), use that one instead."
  - **Secret**: hidden, with Show, Copy and Regenerate; Regenerate asks for confirmation (the
    forge will be refused until it gets the new secret).
  - **How to set it up**, three short tabs: GitHub (Payload URL, content type
    `application/json`, Secret, "Just the push event"), Gitea/Forgejo (Target URL, POST,
    `application/json`, Secret, push events), GitLab (URL, Secret token, Push events).
  - **Last delivery**: "Received 3 min ago · GitHub · push · checked", or "No delivery yet" with
    a reminder that the forge must be able to reach the box (GitHub's ping when the webhook is
    saved is enough to check).
- Turning it off asks for confirmation (the secret is forgotten) and folds the section.
- Every string in the language files (English and French at least), following GitRepoTab.

## Tests

### AppManagement (Go, unit)

- Signatures, as a table for every header: right secret, wrong secret, truncated signature,
  missing or wrong `sha256=` prefix, empty header, two contradicting headers. No request gets a
  2xx without a valid signature.
- Guards: webhook disabled and unknown app give the same 404; a body over 5 MiB gives 413;
  `ping` gives 200; a non-push event gives 202 `ignored`.
- The JWT exemption covers this pair only: `GET` on the same path, and every other route, still
  answer 401 without a token.
- Triggering: a push runs one check; a second one within 10 seconds is coalesced; a busy app
  gets one pending check that runs once the app is free; with automatic deployment off the check
  runs and nothing is deployed.
- Secret: enabling generates it, disabling forgets it, regenerating refuses the old one at once;
  `regenerate_webhook_secret` on a disabled webhook gives 400; a restored app comes back disabled.
- Nothing secret in the log: body and signatures absent from the log lines.

### Dashboard (vitest)

The URL built from the current origin, the confirmations of Regenerate and Off, the last
delivery display, the PUT requests sent.

### Install check

In the existing step "An app deployed from its git repository": enable the webhook through the
API and read the secret; push a commit to the test repository; POST a signed `push` computed with
`openssl`; assert 202 and a new check (`check.at` moves, the new commit is seen); assert a badly
signed body gives 401 and, after disabling, 404.

## Components and release

- **CasaOS-AppManagement**: the route, the verification, the triggering, the secret, the view and
  the PUT fields, the OpenAPI schema and its codegen (the enum-constant count check stays green).
- **CasaOS-UI**: the Webhook section.
- **CasaOS-Install**: the install-check additions.
- Shipped together in one distribution release.

## Out of scope

- A relay for boxes the forge cannot reach (smee.io and the like).
- Deploying the pushed ref directly, or trusting anything in the payload.
- Webhooks for anything but git apps.
