# VPS review: verified gaps and implementation order

Baseline: `cb2e750d0fe06d0b9d42471d69c83808d8761675` (draft PR #1).
This is a source review after the Tokyo VPS protocol/AI discussion. A model's
suggestion is not evidence of a reproducible bug.

## P0: delivery correctness before WAN acceptance

1. **Verified bug: silent subscriber overflow.** `eventbus.Publish` dropped an
   event when its 256-entry WebSocket buffer filled, then could deliver later
   sequence numbers. A receiver could advance its resume cursor past a missing
   message. The fix closes/removes the overflowing subscriber, drains only the
   already queued prefix and closes the socket (1013 when writable). Clients
   reconnect with their last successfully processed `resume_seq`. Room-specific
   queues prevent unrelated room traffic from exhausting the buffer. Writes
   have a deadline so a stuck reader cannot retain the handler forever.
2. **Acceptance gap, not a proven code bug: physical-machine collaboration.**
   Existing process smoke tests use loopback. Tokyo VPS health checks and local
   CLI exchange do not satisfy VPS + independent local host + cloud host WAN
   acceptance. Validate actual HTTP/WSS reachability, messages, task lifecycle,
   a >=30 MiB file with matching SHA-256, disconnection and cursor replay.
   Do not weaken firewall/TLS/auth to make a test pass.
3. **Known trust boundary: room binding is not room authorization.** Existing
   tests reject cross-room publishes and actor changes on a connection. A shared
   token holder can still choose the initial room/identity; the global catalog
   is not private per-room. Independent credentials/ACLs require a separate
   design before untrusted multi-tenant exposure, not a claim of isolation today.

## P1: explicit rejection and real model participation

1. **Verified bug: hello rejection masked by timeout.** Agent Dial queued an
   unwrapped `error` control frame and kept waiting for welcome. The fix returns
   a typed `ProtocolError` immediately. Persisted application `error` events
   remain ordinary replayable events.
2. **Model bridge, not simulated participants.** The Tokyo discussion used real
   model outputs but coordinator-delivered messages. A future bridge must carry
   provider/model provenance, source event/reply IDs and explicit participant
   versus observer mode. Bound calls, output size, concurrency and deadlines;
   deduplicate by durable request ID and never automatically rerun an invocation
   with unknown outcome. Tool access stays disabled unless separately approved.
3. **Skill/MCP/knowledge cross-machine acceptance.** Existing providers already
   have local review/approval and invocation coverage. Test approved transfer,
   exact caller/room restrictions, denied calls, revoke and offline reuse on
   separate machines rather than reimplementing these as missing features.

## Already present (do not duplicate)

- Server `resume_seq` replay and event IDs; invocation journals and unknown-
  outcome handling. This patch does **not** add automatic CLI reconnect.
- Read-only observer versus Agent publish endpoint; structured rejection codes.
- HTML history export and correlation fields; not all model provenance/UI flows.
- Reviewed declarative Skill execution, MCP JSON adapters, selected knowledge
  exchange and bounded streaming relay, with their documented limitations.

## Waiting list (discuss before implementation)

- Additional model/provider adapters beyond the minimal bridge.
- Richer review/approval timeline and presentation improvements to existing HTML
  export; export itself is already implemented.
- General script-capable Skill execution and broader mixed-provider DAGs.

## Verification for the two fixes

Run `go test ./internal/eventbus ./internal/agent ./internal/gateway`, then
`go vet ./...`, `go test ./...`, `go test -race ./...`, observer and process smoke
tests from README. Add focused overflow, unrelated-room pressure, concurrent
unsubscribe, hello rejection and historical error replay regressions. Do not
report passed tests until they have run against the final commit.
