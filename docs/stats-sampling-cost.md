# Stats sampling cost

NOT MEASURED YET: to be measured in Task 9 on a Linux guest with Docker (the plan's decision rule then applies: keep polling when at most 0.5 % of one core, else implement the per-container streams).

Host, Docker version, cgroup version, A and B numbers: to be filled in at that measurement.

## Method

`go test ./pkg/dockerstats -run TestCost -v -timeout 10m` with `STATS_COST=1`, as root, on Linux with a reachable Docker and the `busybox` image (`pkg/dockerstats/cost_linux_test.go`).

The test starts 30 containers (`docker run -d --label stats-cost=1 --name statscost-N busybox sleep 3600`), waits 10 s, then measures two strategies one after the other for 60 s each. The cost is the CPU of the test process (`getrusage`, user plus system) plus the growth of dockerd's CPU (`/proc/<pid>/stat` fields 14 and 15), as percent of one core.

- A, poll: every 5 s one `ContainerList` and one `ContainerStatsOneShot` per container, 8 in flight at most.
- B, streams: one `ContainerStats(stream=true)` goroutine per container, every frame decoded (one per second).

The containers are removed at the end, even on failure.

## Decision rule

- A at most 0.5 %: keep polling (also when A and B are close: no goroutine lifecycle to manage).
- A over 0.5 % and B under: implement B.
- Both over: stop; the 5 s interval or the per-minute design must change.

## Current implementation

Strategy A, the default: `Source.Stats` polls with at most 8 calls in flight; a container whose call fails or answers garbage is absent from the result. It is re-measured in Task 9 with the finished sampler.
