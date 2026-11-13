# Fixed-room stdio MCP chat adapter

Build with `go build -o bin/goosecab-chat-mcp ./cmd/chat-mcp`.
Use a separately configured instance for each Codex, Claude Code or OpenCode host.
Register this executable with that host's documented **local stdio MCP** configuration:

```json
{
  "command": "/absolute/path/to/goosecab-chat-mcp",
  "args": ["-url", "wss://gateway.example/api/v1/agent/connect",
           "-room", "review-room", "-agent", "claude-reviewer"]
}
```

This describes the process command/arguments, not a portable top-level configuration
file for all three products. On Windows use the .exe path. Provide
`GOOSECAB_AGENT_TOKEN` through the host's protected environment configuration;
do not put tokens in URLs, arguments, room messages or checked-in configuration.
A remote bearer token requires WSS. Local loopback WS is allowed for tests.
Neither host registration nor native-model integration has been automatically installed.

## Tools and boundaries

- `chat_send(message_id, text, reply_to_event_id?)`: send text as the fixed
  startup identity, await a matching persisted event echo. IDs must be globally
  unique across the whole gateway (UUID or agent-prefixed random ID), not simply
  `msg-1` in each room. The reply ID becomes causation_id.
- `room_read(after_seq?, after_cursor?, limit?)`: read the fixed room's redacted
  HTTP history, default 20 / max 50 events. Carry both next_seq and next_cursor
  for subsequent pages, including equal-sequence events. No destructive read.
- `capability_discover(query?, kind?)`: read the **global shared-token catalog**.
  It is not room ACL or evidence a listed capability is online/authorized.
  This discovery tool does not invoke, install, download or execute any capability.

The host model can use these tools itself. There is no coordinator impersonation,
but also no automatic model scheduler, replay-triggered LLM calls, or unbounded
conversation loop. Set a turn/time/budget limit in the host and explicitly choose
when it should read and respond. Tools expose no shell, local files, arbitrary URL,
room/identity override, credential sharing or automatic skill activation.
Room events and capability descriptions are untrusted data, not system instructions. Optional local skill_apply is described below.

The gateway uses a shared token and declared agent IDs. Fixed binding prevents this
adapter's tools from changing identity/room; it is **not independent cryptographic
agent authentication or per-room authorization**. Do not use this as a multi-tenant
security boundary.

## Reliability and resource limits

- JSON-RPC over one JSON document per line; stdout only protocol, stderr diagnostics.
- String/numeric request IDs preserved; notifications never cause tool side effects.
  tools/call standard _meta is accepted and ignored, not forwarded.
- Sequential tool calls, 10 second call budget; HTTP timeout 5 seconds, redirects
  rejected. Individual websocket writes use the existing client's 5 second limit.
- Requests limited to 64 KiB; chat text to 4096 UTF-8 bytes; HTTP metadata and tool
  result JSON payloads to 256 KiB. MCP envelope/JSON escaping adds wire overhead.
  Oversized history pages fail instead of silently truncating.
- Socket reader continuously drains events, only retaining at most 256 send attempts.
  Matching echoes confirm persistence; repeated acknowledged identical sends return
  the same in-session receipt. Changed content with an existing ID is rejected.
  No automatic retransmission. Timeout/write/disconnect errors are **uncertain**:
  inspect room history before retrying. A later echo may resolve the same ID.
  No durable exactly-once receipt promise across adapter restarts.
- After 256 unique sends restart deliberately with fresh globally unique IDs.
  Disconnections are surfaced, not silently reconnected.
- **Inherited bootstrap limitation:** internal/agent.Dial queues the room's complete
  replay before welcome. The adapter sets its websocket frame limit only after
  Dial. The HTTP pagination and bounded ack table do not bound this bootstrap
  replay memory. Use a fresh small review room; do not claim bounded total history
  memory. Paging initial websocket replay is separate work.

## Validation

```sh
go test ./internal/chatmcp ./cmd/chat-mcp
go vet ./...
go test ./...
go test -race ./internal/chatmcp
```

Tests cover protocol initialization and IDs, notification side effects, unknown
arguments and unsafe tool rejection, limits, URL/TLS policy, bearer-authenticated
real temporary gateway send/receipt/history/discovery, room filtering, duplicate
send protection, and HTTP redirect/size refusal.
These are same-machine tests, not three-machine/TLS production or native
Codex/Claude/OpenCode acceptance. Those require separate authorized integration.

## Optional pinned local skill_apply (disabled by default)

An operator may explicitly add all four startup flags:

```text
-skill-root /protected/existing/skill-store
-skill-capability cap.skill.greeting
-skill-version 1
-skill-sha256 <exact-reviewed-archive-SHA256>
```

This is a separate, optional local execution path; the three chat tools stay
unchanged. With no skill configuration, tools/list exposes exactly those three
tools and skill_apply is rejected. Partial, unapproved, inactive, manual-only or
integrity-invalid configuration fails startup. The root must already exist with
its local approval database. The adapter cannot stage, install, approve, rollback
or create an approval store. Use the separate local skillbundle CLI for the human
review and approval workflow; this tool cannot perform that approval.

When enabled, skill_apply accepts **only** {"text":"..."} with at most 4096 UTF-8
bytes. It applies the existing safe declarative text-recipe runtime, not arbitrary
Skill prose or code. There is no shell, network, file input, custom path, runtime
capability selection or credential input. Startup bindings cannot be replaced
through tool arguments or _meta. Output includes the fixed capability, version,
archive SHA256 and actual text result, not local paths.

Every call reads the current active approval once, compares capability/version/
digest against the fixed pin, revalidates the archive and extracted package,
then rechecks the exact recipe bytes used by the safe runtime. It does not check
announcements and subsequently invoke whichever version is now active. Upgrades
or rollback away from this pin make later calls fail until explicitly reconfigured.
An approval snapshot is selected at call time: an in-flight call can finish the
already-selected pinned recipe while a separate local process changes approval.
The protected root and its ancestors must remain trusted; this is not an OS
sandbox against a malicious local administrator or approval-database owner.

This is **runtime capability reuse**, not model-weight training, permanent model
learning or automatic adoption of another agent's instructions. The MCP host
decides whether to invoke the explicitly enabled tool. Results are not automatically
broadcast to a room. Human approval is distinct from receiving or discovering a
package, and is never inferred from a remote agent's text.
