# Explicit two-gateway collaboration

`cmd/cascade` is a real outbound WebSocket bridge agent, not an event replication
stub. It connects to two running GooseCab gateways and forwards explicitly
addressed bounded text invocations and the matching worker progress/results.
Files remain agent-local; this bridge neither reads files nor forwards file
payloads. File streaming uses the separate data plane, not this bridge.

## Two modes

- `leader`: one upstream identity, e.g. `edge`, represents the downstream group.
  Each capability must map to exactly one configured downstream worker.
- `flat`: each explicitly configured downstream worker appears through a real
  namespaced proxy connection, e.g. `edge/worker-a`. Multiple capabilities can
  be configured for that worker. Discovery is configuration-driven, not an
  automatic mirror of a possibly stale registry.

The upstream event actor is always the proxy's own registered identity. The
bridge never impersonates the downstream worker on its connection. Downstream
calls come from `edge/bridge`. `cascade.forwarded` carries the two room names,
proxy/worker names, both stable request IDs and visited bridge route. Downstream
`capability.invoke.route` carries the visited bridge IDs too. This trace is
informational provenance, **not** identity authentication or permission.

## Run

Build the binaries with `go build ./cmd/...`, or run each command using `go run`.
Run gateways on different hosts/ports with different database paths, then start
an ordinary text worker on the downstream gateway. Example bridge config:

```json
{
  "id": "edge",
  "mode": "leader",
  "upstream": {"url": "ws://127.0.0.1:8312/api/v1/agent/connect", "room": "up"},
  "downstream": {"url": "ws://127.0.0.1:8313/api/v1/agent/connect", "room": "down"},
  "routes": [{"worker": "worker", "capability": "cap.text.uppercase"}],
  "max_hops": 4
}
```

```sh
go run ./cmd/cascade -config bridge.json -state edge-mapping.db -timeout 30s
go run ./cmd/invoke -url ws://127.0.0.1:8312/api/v1/agent/connect \
  -room up -id caller -target edge -request-id stable-001 -text 'hello across gateways'
```

For flat mode, change `mode` to `flat`, use a **separate journal**, and invoke
`-target edge/worker`. Use `GOOSECAB_UPSTREAM_TOKEN` and
`GOOSECAB_DOWNSTREAM_TOKEN` for credentials; they are never serialized into
configuration fingerprints or mapping records. Use WSS behind a trusted TLS
proxy for remote deployments. Do not expose an unauthenticated gateway publicly.

Runnable separate-process acceptance for **both modes**:

```sh
bash tests/acceptance/cascade.sh
# Optional: GO=/path/to/go CASCADE_UP_PORT=18312 CASCADE_DOWN_PORT=18313
# Focused real-WebSocket integration and race coverage:
go test -race ./internal/cascade
```

The acceptance starts two gateway OS processes, a durable text worker, bridge
processes and invocation CLI processes. It asserts the real uppercase result.
The Go tests additionally check reconnect deduplication, request provenance,
loop/hop rejection, offline unknown, room/type isolation and payload limits.

## Failure and trust contract

- Only the two configured rooms, explicitly addressed routes, and
  `capability.invoke/progress/result` cross the boundary. Chat, arbitrary events,
  file transfers, unaddressed calls and unrelated workers' results do not.
- Only configured `cap.text.uppercase` or `cap.learned.text.*` capabilities accepting a single UTF-8 `text`
  string of at most 4096 bytes are supported. The configured worker must
  implement the capability. No implicit fallback to another worker exists.
- Each bridge ID must be unique across the topology. A repeated ID or the
  configured hop limit (1–8) rejects a call. Gateway JSON fingerprints include
  `route`, so changing a route for an existing strict invocation conflicts.
- Durable mapping is claimed **before** sending downstream. A mapping stores
  IDs, fingerprint and terminal status, not input/output or file bytes. The
  same mapping is never submitted twice, including after restart. The worker
  must also retain its durable execution journal.
- A crash between claim and send can leave a call **unknown without execution**.
  A timeout, missing worker or transport failure can leave it **unknown even if
  it later executes**. Unknown is never reported as success, and never causes
  automatic re-execution, worker replacement or a fresh request ID. Late
  authoritative results can reconcile unknown.
- On connection failure the bridge exits. A supervisor may restart it using
  the exact same config and journal; replay recovers already-published results.
  Changing route configuration for that journal is refused. Do not delete
  journals or change bridge identity to retry uncertain work.
- This is not online-presence detection. An offline worker can receive its
  queued invocation on a later reconnect. Do not interpret timeout as cancel.
- Keep gateway event history and bridge/worker journals together. History
  truncation or losing any journal needs operator reconciliation, not an
  automatic retry. Replay currently reads room history, so large histories
  remain a scalability limit. One bridge process must own each journal.
- Current gateway credentials are shared bearer tokens; registration does not
  provide cryptographic per-agent identity isolation. Only trusted agents may
  share a deployment. The bridge preserves connection-bound actor identity,
  but does not upgrade the underlying gateway authentication model.
- The two gateways may be remote; automated acceptance uses distinct local
  listeners/processes, not a claim of an already-tested public-WAN deployment.
