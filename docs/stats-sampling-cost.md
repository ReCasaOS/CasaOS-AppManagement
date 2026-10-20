# Stats sampling cost

**Status: NOT MEASURED YET.** No Docker daemon was available where this code was written. The tests and the script below are ready; the numbers go here once a Linux machine with Docker has run them. Until then the budget (at most 0.5 % of one core with 30 containers) is not proven.

To fill in at the measurement: host (CPU model, cores), kernel, Docker version, cgroup version, the A, B and Sampler figures with their per-process breakdown, and the date.

## How to measure

On Linux, as root (or a member of the docker group), with a reachable Docker, `docker compose`, Go, and network access for the `busybox` image, from the repository root:

```sh
STATS_COST=1 go test ./pkg/dockerstats -run TestCost -v -timeout 10m
STATS_PROOF=1 go test ./pkg/dockerstats -run TestProof -v -timeout 10m
go test -race ./...
```

or all three, with PASS/FAIL per step and the numbers: `sh scripts/stats-proof.sh` (installs nothing; logs in `$STATS_PROOF_LOGS` or a temp dir; exit 0 only when every step passes).

## Method

`pkg/dockerstats/cost_linux_test.go` starts 30 containers (`docker run -d --label stats-cost=1 --name statscost-N busybox sleep 3600`), waits 10 s, then measures for 60 s each:

- `TestCost/Strategies`, A, poll: every 5 s one `ContainerList` and one `ContainerStatsOneShot` per container, 8 in flight at most.
- `TestCost/Strategies`, B, streams: one `ContainerStats(stream=true)` goroutine per container, every frame decoded (one per second). This sub-test fails only when A and B are both over the budget.
- `TestCost/Sampler`: the finished `Sampler` (real `DockerSource`, real store in a temp dir) with `Tick` every 5 s. It fails when it is over 0.5 % of one core or when it did not measure all 30 containers.

The cost is the CPU of the test process (`getrusage`, user plus system) plus the growth of the CPU of `dockerd`, `containerd` and every `containerd-shim*` process (`/proc/<pid>/stat` fields 14 and 15, in USER_HZ ticks of 100 per second), as percent of one core. A stats call goes dockerd, containerd, shim, cgroup files, so dockerd alone would undercount. Processes are found by scanning the comm names in `/proc` rather than reading cgroup counters: it needs no knowledge of the cgroup version or of systemd unit names. Each measure logs its per-process breakdown; the parser is covered without Docker by `proccpu_test.go`.

Leftover containers of a killed run are removed before the start; the containers are removed at the end, even on failure.

`pkg/dockerstats/proof_linux_test.go` (`TestProof`) runs the sampler for 150 s at a 1 s interval against three labelled containers (a compose service with `restart: always` and `cpus: 0.5`, a `docker run` with `--memory 64m` and a health check, a plain one) and checks the identities, the series, the declared limits and allocations, the health probe, a stopped container kept and a removed one forgotten (`container_gone`), the history across a service restart, and an unreachable Docker (`ErrUnavailable`, no panic).

## Decision rule

- Sampler at most 0.5 %: keep polling (also when A and B are close: no goroutine lifecycle to manage).
- Over 0.5 % and B under: implement B (per-container streams) behind `Source`.
- Both over: stop; the 5 s interval or the per-minute design must change.

## Current implementation

Strategy A, the default: `Source.Stats` polls with at most 8 calls in flight; each call also has a 2 s deadline; a container whose call fails, hangs or answers garbage is absent from the result.
