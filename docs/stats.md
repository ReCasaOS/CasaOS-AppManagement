# Container stats and resource alerts

A sampler (`pkg/dockerstats`) measures every container through one shared Docker client every 5 s. The history is stored by CasaOS-Common's `pkg/stats`: per-minute avg/min/max for 7 days, in `/var/lib/casaos/stats/app_management` (alert settings and state in `alerts.json` beside the rings). This service never writes those files itself.

## Routes

All behind the JWT middleware. Success bodies are bare (no `data` wrapper); an error is `{"message":"<code>"}`.

| Route | Answer |
| --- | --- |
| `GET /v2/app_management/stats` | `{items, notes}`: one row per container, plus stopped or removed ones whose history is under 7 days (state `stopped`, no numbers) |
| `GET /v2/app_management/stats/{id}?range=live\|15m\|1h\|24h\|7d` | series with `scale.cpu_allocation` (cores) and `scale.memory_limit` (bytes or `null`); `range` absent means `live` |
| `GET /v2/app_management/stats/{id}/alerts` | `{rules, active}` |
| `PUT /v2/app_management/stats/{id}/alerts` | `{rules}` merged per key; answers like the `GET` |

`{id}` is the item URL-encoded as one segment. Errors: `404 unknown_item`, `400 bad_range`, `400 bad_rules: rules[0].threshold` (field named, nothing else), `503 stats_unavailable`. A 503 on `PUT` can also mean the rules are applied in memory but could not be saved. No text from a container or from Docker appears in an error.

Docker down: the list answers 503 (rows need live state); series and alerts keep answering from disk, the outage is a gap. Every route answers 503 when the store did not open.

## Identities

Compose container: `app:<application>:<service>:<n>` (n is `com.docker.compose.container-number`, `1` if absent), group `Conteneurs`. Any other container, `compose run` one-offs included: `container:<name>`, group `Autres conteneurs`. Docker's container ID is never part of an identity.

## What is measured

`cpu/user`, `cpu/system`, `cpu/throttled` (percent of one core); `memory/rss`, `memory/cache`, `memory/limit`, and `memory/swap` on cgroup v1 with swap accounting; `network/rx|tx|dropped:<interface>`; `disk/read|write|iops_read|iops_write`; `pids/count`; `restarts/count` (Docker's `RestartCount`: a manual `docker restart` does not raise it); `health/ok` for containers with a probe.

**Not measured** (Docker's API has no source): `cpu/iowait`, `pids/fds`, and `memory/swap` on cgroup v2. These series are absent, never written as 0. A counter that goes backwards (container recreated) gives a gap, not a rate.

## Alerts

| Key | Default | Measured against |
| --- | --- | --- |
| `cpu_high` | 90 % for 10 min | the container's CPU limit, else the machine's cores (note `no_cpu_limit`) |
| `mem_high` | 90 % for 5 min | the container's memory limit, else the machine's RAM (note `no_memory_limit`) |

Evaluated on per-minute averages. One `resource:alert-firing` when a rule trips, one `resource:alert-resolved` when it ends or the container stops being measured. Properties: `item`, `name` (`<application> / <service>` for an app container, e.g. `immich / server`, the container's name for any other), `kind` (`app-container` or `container`), `situation`, `value` (integer percent, absent when resolved), `minutes`, `since` (RFC 3339: when the alert fired; a resolved event carries the same time), `source` (`app_management`). Events go through one queue of 64 and one publisher, so a firing and its resolved reach the bus in order; if the bus is stuck and the queue is full, the newest event is dropped and logged. A failed stats call for a running container is a gap and resolves nothing.

Notes codes: `no_cpu_limit`, `no_memory_limit`, `container_gone`; the list can carry `stats_capacity` (more than 256 live containers: the rest are listed, not measured; logged once an hour).

## What is verified, and what is not

Verified: unit tests with a fake Docker source and an injected clock (identities, rates, counter resets, series keys, alert wiring, routes, error shapes), `gofmt` and `go vet`.

**Not verified yet:**

- **The sampler's CPU cost.** The budget is 0.5 % of one core with 30 containers; it has not been measured on a real Docker. See [stats-sampling-cost.md](stats-sampling-cost.md) for the method and the decision rule.
- **`go test -race`.** Never run on the development host (no C compiler). CI runs it, and `scripts/stats-proof.sh` runs it on a Linux machine.
- **The end-to-end proof** (`TestProof`, `linux` build tag) and the full-service proof, which run later on a QEMU machine with a real Docker.

## Known limits

- The sampler skips the first reading of a minute after a gap when it cannot be given a CPU rate; the prediction of that minute boundary can be a tick late.
- If Docker comes back at :50 and the call at :55 fails, an alert can be resolved by mistake (rare).
- `StopStats` is not wired to a shutdown path; the history files are crash-safe, so a stop loses at most the minute in progress.
- A listed container with no history answers an empty series; the page must tolerate it.

## Upgrade note

CasaOS-Common will get a follow-up release, v0.4.29, adding `Store.Unreadable(item) bool` and creating its directory with mode 0750 and its files with 0640. AppManagement will then drop its own copy of the ring file-name rule (`ringName` and the `skipped` set in `pkg/dockerstats/sampler.go`) in favour of `Unreadable`, and bump Common. Not done in this release.
