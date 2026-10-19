#!/bin/sh
# Runs bake-off jobs one after another (in parallel they'd skew each other's
# timings): queue.sh "model set" "model set" ...
cd "$(dirname "$0")"
mkdir -p results
for job in "$@"; do
	echo "start $job $(date +%H:%M:%S)" >> results/queue.log
	# shellcheck disable=SC2086
	if uv run python bench.py $job > results/last.out 2> results/last.err; then
		echo "done $job $(tail -1 results/last.out)" >> results/queue.log
	else
		echo "FAILED $job $(tail -3 results/last.err | tr '\n' ' ')" >> results/queue.log
	fi
done
echo "queue finished $(date +%H:%M:%S)" >> results/queue.log
