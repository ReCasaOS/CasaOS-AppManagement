#!/bin/sh
# Proves the container stats sampler against a real Docker and measures its cost.
# Needs Linux, a reachable Docker with `docker compose`, Go, network access for the
# busybox image, and root (or the docker group). Installs nothing.
# Usage: sh scripts/stats-proof.sh   (from anywhere; logs go to $STATS_PROOF_LOGS or a temp dir)
set -u

cd "$(dirname "$0")/.." || exit 2
logs=${STATS_PROOF_LOGS:-$(mktemp -d)}
mkdir -p "$logs" || exit 2
failed=0

report() { # step, exit code, log
	if [ "$2" -eq 0 ]; then
		echo "PASS $1"
	else
		echo "FAIL $1 (exit $2, log $3)"
		failed=1
	fi
}

if ! docker version >"$logs/docker-version.log" 2>&1; then
	report "docker version" 1 "$logs/docker-version.log"
	exit 1
fi
report "docker version" 0 ""
if ! docker compose version >"$logs/compose-version.log" 2>&1; then
	report "docker compose version" 1 "$logs/compose-version.log"
	exit 1
fi
report "docker compose version" 0 ""
command -v go >/dev/null 2>&1 || { echo "FAIL go not found"; exit 1; }

STATS_COST=1 go test ./pkg/dockerstats -run TestCost -v -count=1 -timeout 10m >"$logs/cost.log" 2>&1
rc=$?
grep -E '% of one core' "$logs/cost.log"
grep -q -- '--- SKIP' "$logs/cost.log" && rc=1
report "cost (TestCost: sampler at most 0.5 % of one core with 30 containers)" "$rc" "$logs/cost.log"

STATS_PROOF=1 go test ./pkg/dockerstats -run TestProof -v -count=1 -timeout 10m >"$logs/proof.log" 2>&1
rc=$?
grep -E -- '^ *--- (PASS|FAIL)|minutes kept' "$logs/proof.log"
grep -q -- '--- SKIP' "$logs/proof.log" && rc=1
report "proof (TestProof)" "$rc" "$logs/proof.log"

# -race needs cgo, hence a C compiler
if [ "$(go env CGO_ENABLED)" != "1" ] || ! command -v "$(go env CC)" >/dev/null 2>&1; then
	echo "FAIL go test -race needs CGO_ENABLED=1 and a C compiler (install gcc); not run"
	failed=1
else
	go generate ./... >"$logs/generate.log" 2>&1
	rc=$?
	report "go generate ./..." "$rc" "$logs/generate.log"
	go test -race -count=1 ./... >"$logs/race.log" 2>&1
	rc=$?
fi
[ -f "$logs/race.log" ] && grep -E '^(ok|FAIL|---)' "$logs/race.log" | grep -v '^ok' | head -n 40
[ -f "$logs/race.log" ] && report "go test -race ./..." "$rc" "$logs/race.log"

echo "logs: $logs"
if [ "$failed" -ne 0 ]; then
	echo "RESULT FAIL"
	exit 1
fi
echo "RESULT PASS"
