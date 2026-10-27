#!/usr/bin/env bash
# Measure idle CPU and RSS for a serving Metricule process.
#
# Usage: scripts/bench-idle.sh [seconds]
#
# Starts the real binary against a temporary SQLite database with one poll metric,
# waits for readiness, then samples process CPU time over the window. The value is
# wall-clock CPU, not a peak: it answers "what does an idle exporter cost?".
set -euo pipefail

duration="${1:-30}"
if ! [[ "$duration" =~ ^[0-9]+$ ]] || [ "$duration" -lt 1 ]; then
  echo "usage: $0 [seconds]" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

for tool in sqlite3 curl python3; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "$tool is required" >&2
    exit 1
  fi
done

make build >/dev/null

tmp="$(mktemp -d)"
pid=""
cleanup() {
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

db="$tmp/app.db"
sqlite3 "$db" "CREATE TABLE t (id INTEGER PRIMARY KEY); INSERT INTO t VALUES (1), (2), (3);"

port="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"

cfg="$tmp/metricule.yaml"
cat >"$cfg" <<EOF
apiVersion: metricule.io/v1alpha1
kind: Exporter
server:
  listen: 127.0.0.1:${port}
sources:
  app:
    type: sqlite
    path: ${db}
    busy_timeout: 3s
metrics:
  - name: app_rows
    help: Rows in the idle benchmark table
    type: gauge
    source: app
    query:
      sql: SELECT COUNT(*) AS value FROM t
    mapping:
      value: value
    collect:
      mode: poll
      interval: 1h
      timeout: 5s
EOF

./bin/metricule serve --config "$cfg" >/dev/null 2>&1 &
pid=$!

ready=0
for _ in $(seq 1 200); do
  if curl -sf "http://127.0.0.1:${port}/readyz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.05
done
if [ "$ready" -ne 1 ]; then
  echo "process did not become ready" >&2
  exit 1
fi

# Cumulative CPU time as seconds; handles hh:mm:ss[.cc] and mm:ss[.cc].
cpu_seconds() {
  ps -o time= -p "$1" | awk -F: 'NF==3 { print ($1*3600)+($2*60)+$3 } NF==2 { print ($1*60)+$2 }'
}

before="$(cpu_seconds "$pid")"
sleep "$duration"
after="$(cpu_seconds "$pid")"
rss_kib="$(ps -o rss= -p "$pid" | tr -d ' ')"

awk -v before="$before" -v after="$after" -v window="$duration" -v rss="$rss_kib" '
  BEGIN {
    cpu = after - before
    printf "idle window: %d s\n", window
    printf "cpu seconds: %.3f\n", cpu
    printf "cpu percent: %.3f%%\n", (cpu / window) * 100
    printf "rss: %.1f MiB\n", rss / 1024
  }'
