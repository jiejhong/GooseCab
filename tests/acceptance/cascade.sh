#!/usr/bin/env bash
# Real separate OS processes: two gateways, one worker, bridge and caller.
set -euo pipefail
cd "$(dirname "$0")/../.."
GO=${GO:-go}
ROOT=$(mktemp -d)
PIDS=()
cleanup() { for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done; wait 2>/dev/null || true; rm -rf "$ROOT"; }
trap cleanup EXIT
UP=${CASCADE_UP_PORT:-18312}
DOWN=${CASCADE_DOWN_PORT:-18313}
for cmd in server agent invoke cascade; do "$GO" build -o "$ROOT/$cmd" "./cmd/$cmd"; done
for spec in "up:$UP" "down:$DOWN"; do
 name=${spec%:*}; port=${spec#*:}
 GOOSECAB_DB="$ROOT/$name.db" "$ROOT/server" -addr "127.0.0.1:$port" >"$ROOT/$name.log" 2>&1 & PIDS+=("$!")
 for i in $(seq 1 100); do if curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then break; fi; sleep .05; done
 curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null
done
"$ROOT/agent" -url "ws://127.0.0.1:$DOWN/api/v1/agent/connect" -room down -id worker -mode worker -state "$ROOT/worker.db" >"$ROOT/worker.log" 2>&1 & PIDS+=("$!")
for mode in leader flat; do
 cat >"$ROOT/$mode.json" <<JSON
{"id":"edge-$mode","mode":"$mode","upstream":{"url":"ws://127.0.0.1:$UP/api/v1/agent/connect","room":"up"},"downstream":{"url":"ws://127.0.0.1:$DOWN/api/v1/agent/connect","room":"down"},"routes":[{"worker":"worker","capability":"cap.text.uppercase"}],"max_hops":4}
JSON
 "$ROOT/cascade" -config "$ROOT/$mode.json" -state "$ROOT/$mode.db" -timeout 5s >"$ROOT/$mode.log" 2>&1 & PIDS+=("$!")
 target="edge-$mode"; if [[ "$mode" == flat ]]; then target="$target/worker"; fi
 # Invocations are durably replayed even if they beat the bridge's hello.
 "$ROOT/invoke" -url "ws://127.0.0.1:$UP/api/v1/agent/connect" -room up -id caller -target "$target" -request-id "real-$mode" -text "two live gateways" -timeout 10s >"$ROOT/result.json"
 grep -q 'TWO LIVE GATEWAYS' "$ROOT/result.json"
 echo "$mode: $(cat "$ROOT/result.json")"
done
