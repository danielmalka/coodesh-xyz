#!/usr/bin/env bash
# Posts concurrent webhook load against a running Lexi server, prints a status histogram, waits for drain.
# Usage: scripts/load.sh [count] [base_url]
set -euo pipefail

COUNT="${1:-200}"
export BASE_URL="${2:-http://localhost:8080}"
export PHONE="+5511987654321"
export TEXT="load"
export CURL_TIMEOUT=5
CONCURRENCY=16
POLL_INTERVAL=0.5
MAX_WAIT_SECS=30

post_one() {
	curl -s -o /dev/null -w '%{http_code}\n' --max-time "${CURL_TIMEOUT}" \
		-X POST "${BASE_URL}/webhook" \
		-H 'Content-Type: application/json' \
		-d "{\"message_id\":\"wamid.load.$1\",\"from\":\"${PHONE}\",\"text\":\"${TEXT}\"}"
}
export -f post_one

metric() {
	printf '%s' "$1" | grep -o "\"$2\":[0-9]*" | head -n1 | sed 's/.*://' || true
}

if ! curl -sf --max-time "${CURL_TIMEOUT}" "${BASE_URL}/health" >/dev/null; then
	echo "server unreachable: ${BASE_URL}" >&2
	exit 1
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
start="${EPOCHREALTIME}"

seq 1 "${COUNT}" | xargs -P "${CONCURRENCY}" -I{} bash -c 'post_one "$1"' _ {} >"${tmp}"
sort "${tmp}" | uniq -c | awk '{printf "%s: %s  ", $2, $1} END {print ""}'

deadline=$((SECONDS + MAX_WAIT_SECS))
drained=0
while true; do
	metrics="$(curl -sf --max-time "${CURL_TIMEOUT}" "${BASE_URL}/metrics")" || {
		echo "server unreachable while waiting for drain: ${BASE_URL}" >&2
		exit 1
	}
	if [[ "$(metric "${metrics}" in_flight)" == "0" && "$(metric "${metrics}" queue_depth)" == "0" ]]; then
		drained=1
		break
	fi
	if ((SECONDS >= deadline)); then
		break
	fi
	sleep "${POLL_INTERVAL}"
done

elapsed_ms="$(awk -v a="${start}" -v b="${EPOCHREALTIME}" 'BEGIN {printf "%d", (b - a) * 1000}')"
echo "elapsed_ms=${elapsed_ms}"
echo "${metrics}"
if ((drained == 0)); then
	echo "drain timeout after ${MAX_WAIT_SECS}s" >&2
	exit 2
fi
