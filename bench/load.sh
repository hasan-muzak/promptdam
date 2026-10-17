#!/usr/bin/env bash
# Load-tests the gateway on this machine as it runs in production: the fake
# provider (bench/cmd/fake-upstream), two gateway replicas built as release
# binaries, Caddy in front of them (deploy/Caddyfile) and an organization on
# the Team plan, then one scenario (docs/performance.md):
#
#   a  one replica, no Caddy, 1k calls/s for 2 minutes
#   b  both replicas behind Caddy, 1k calls/s for 10 minutes
#   c  coding agents' 600 KB calls only, 50/s for 2 minutes, to one replica
#   d  behind Caddy, 1k, 2k, then 4k calls/s, a minute each
#   e  behind Caddy, 1k calls/s for 4 minutes, Valkey then ClickHouse stopped
#      for 30 s each
#   run  the load generator with the flags given (bench/load.sh run -rate 200)
#
# Needs Promptdam's stack up and migrated (task dev:stack, task migrate) and
# caddy on PATH. Usage: bench/load.sh a|b|c|d|e|run [flags]
set -euo pipefail
cd "$(dirname "$0")/.."
scenario=${1:?usage: bench/load.sh a|b|c|d|e|run [flags]}
shift
work=$(mktemp -d -t promptdam-load) # the binaries read .env themselves

pids=()
cleanup() {
	for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
	wait 2>/dev/null || true
	echo "logs: $work"
}
trap cleanup EXIT

go build -o "$work/gateway" ./server/cmd/gateway
go build -o "$work/fake" ./bench/cmd/fake-upstream
go build -o "$work/load" ./bench/cmd/load

"$work/fake" -addr :8299 >"$work/fake.log" 2>&1 &
pids+=($!)
for i in 0 1; do
	PROMPTDAM_GATEWAY_ADDR=":828$((2 + i))" PROMPTDAM_GATEWAY_DEBUG_ADDR="127.0.0.1:606$i" \
		"$work/gateway" >"$work/gateway$i.log" 2>&1 &
	pids+=($!)
done
caddy run --config deploy/Caddyfile --adapter caddyfile >"$work/caddy.log" 2>&1 &
pids+=($!)

ready() {
	for _ in $(seq 200); do
		curl -fs "$1" >/dev/null 2>&1 && return
		sleep 0.1
	done
	echo "$1 never became ready (logs: $work)" >&2
	exit 1
}
ready http://localhost:8282/readyz
ready http://localhost:8283/readyz
ready http://localhost:8280/readyz

key=$(go run ./server/cmd/api dev seed -plan team -name "Load $(date +%s)" | grep -o 'OPENAI_API_KEY=pd_sk_[^ ]*' | cut -d= -f2)
load() { "$work/load" -key "$key" -debug 127.0.0.1:6060,127.0.0.1:6061 "$@"; }

# rows counts the request log's rows since a moment, once the replicas have
# written their last batches.
rows() {
	sleep 5
	docker exec promptdam-clickhouse-1 clickhouse-client --user keel --password keel -d promptdam \
		-q "SELECT count() FROM requests WHERE ts >= fromUnixTimestamp($1)"
}

# Each process's CPU and memory every two seconds: what ran out first.
(while sleep 2; do ps -A -o pcpu=,rss=,comm= | grep -E "$work/|caddy" | grep -v grep; done >"$work/ps.log") &
pids+=($!)

start=$(date +%s)
case $scenario in
a) load -target http://localhost:8282 -rate 1000 -duration 2m ;;
b) load -target http://localhost:8280 -rate 1000 -duration 10m ;;
c) load -target http://localhost:8282 -rate 50 -duration 2m -mix agent-large=1 ;;
d) for rate in 1000 2000 4000; do
	echo "## $rate calls/s"
	load -target http://localhost:8280 -rate "$rate" -duration 1m
done ;;
e) (
	sleep 40 && docker stop promptdam-valkey-1 >/dev/null && echo "$(date +%T) Valkey stopped" &&
		sleep 30 && docker start promptdam-valkey-1 >/dev/null && echo "$(date +%T) Valkey started" &&
		sleep 40 && docker stop promptdam-clickhouse-1 >/dev/null && echo "$(date +%T) ClickHouse stopped" &&
		sleep 30 && docker start promptdam-clickhouse-1 >/dev/null && echo "$(date +%T) ClickHouse started"
) &
	load -target http://localhost:8280 -rate 1000 -duration 4m
	wait ;;
run) load "$@" ;;
*) echo "no scenario $scenario" >&2 && exit 2 ;;
esac
echo "Request log rows written since the start: $(rows "$start")"
echo
echo "| Process | CPU % average / max | Memory max |"
echo "|---|---|---|"
awk '{n=$3; sub(".*/", "", n); c[n]+=$1; k[n]++; if ($1>m[n]) m[n]=$1; if ($2>r[n]) r[n]=$2}
	END {for (n in c) printf "| %s | %.0f / %.0f | %d MiB |\n", n, c[n]/k[n], m[n], r[n]/1024}' "$work/ps.log" | sort
