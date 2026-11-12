# Explicit real text DAG orchestration

`cmd/plan` is an opt-in coordinator running on an Agent machine. It makes real
outbound WebSocket calls to explicit workers through one Gateway. Independent
ready tasks run concurrently (1–8), with up to 64 tasks in a validated acyclic
plan. The P2 HTTP planning APIs remain metadata-only; this command does not turn
natural-language goals into plans or run arbitrary code.

Each task may explicitly select a bounded text `capability`: `cap.text.uppercase`
or a `cap.learned.text.*` ID accepted by the text execution protocol. Omitting
`capability` preserves the uppercase default. Learned capabilities must already
be installed and active on the explicitly selected worker; declaring an ID does
not install or authorize a recipe. This scheduler does not select agents from
stale Registry entries, change assignments, retry, execute shell commands, or
automatically install skills.

## Run a real fan-out/fan-in plan

Start the Gateway and two workers using separate durable worker journals:

```sh
GOOSECAB_DB="$PWD/.data/gateway.db" go run ./cmd/server -addr 127.0.0.1:8312
go run ./cmd/agent -mode worker -room demo -id worker-a -state "$PWD/.data/a.db"
go run ./cmd/agent -mode worker -room demo -id worker-b -state "$PWD/.data/b.db"
```

Create `plan.json` (do not include secrets):

```json
{
  "id": "example-001",
  "url": "ws://127.0.0.1:8312/api/v1/agent/connect",
  "room": "demo",
  "caller": "coordinator",
  "concurrency": 2,
  "separator": " | ",
  "tasks": [
    {"id": "a", "target": "worker-a", "text": "hello"},
    {"id": "b", "target": "worker-b", "text": "world"},
    {"id": "join", "target": "worker-a", "text": "result", "depends_on": ["a", "b"]}
  ]
}
```

```sh
go run ./cmd/plan -plan plan.json -state .data/plan.db -action run -timeout 30s
go run ./cmd/plan -plan plan.json -state .data/plan.db -action status
go run ./cmd/plan -plan plan.json -state .data/plan.db -action events
```

The final result is `RESULT | HELLO | WORLD`. To reuse an installed learned
recipe in a downstream task, add for example `"capability":
"cap.learned.text.greeting"` to that task and set its `target` to the worker where
that recipe is active. The result then follows that recipe rather than uppercase.
Do this before the first run: changing a journal-bound plan is rejected. Each task's input consists of its
literal `text`, followed by completed dependency outputs in `depends_on` order,
joined by `separator`. An empty literal is still an initial component. Inputs
and outputs are each limited to 4096 UTF-8 bytes, including the assembled fan-in.
Oversized or invalid UTF-8 resolved inputs fail locally before sending. The
selected worker validates its actual operation and output, including learned
recipe output limits. Failed dependencies
prevent dependent calls. Unknown dependencies leave their dependents pending.

Status defaults to local inspection. `run` is required to dispatch. JSON status
and results go to stdout; durable state transitions also go to stderr. `events`
returns the local, ordered assignment/status event journal. Gateway history
contains the corresponding capability invocation/progress/result events using
stable request IDs shown by local status. Local task state is authoritative for
the scheduler; it is not automatically exposed through the P2 HTTP APIs or UI.

Use `wss://` for remote deployments. All workers and the coordinator initiate
outbound connections; no Agent inbound port is required. Set the coordinator's
`GOOSECAB_AGENT_TOKEN` when Gateway authentication is enabled. Tokens are not
stored in the plan journal. The URL cannot contain credentials, a query or a
fragment. Use the same dedicated caller ID and room for a plan's lifetime.
Shared-token identity limitations still apply; use mutually trusted participants.

## Persistence and uncertain outcomes

The coordinator journal is a local SQLite WAL database with full synchronous
writes. Initial assignments, resolved inputs, and the running state are durable
**before** any outbound task call. A crash in that interval is deliberately
classified as unknown even if the invocation had not yet reached the Gateway.
The plan, endpoint, room, caller, assignments and task inputs are bound to that
journal; changing the plan is rejected. Request IDs are stable hashes of room,
caller, plan ID and task ID. Different intentional work requires a new plan ID.

Keep both coordinator and worker journals. Never replace or delete journals to
work around an unknown result. Backups must include active SQLite WAL data or
use an appropriate SQLite snapshot. The journal also stores plaintext task
inputs and outputs; use a private directory and normal disk access controls.
Only bounded text is sent as room events, visible to authorized room readers.
Files remain on Agent disks; this coordinator does not transfer file contents.

An exclusive `<journal>.lock` prevents simultaneous journal owners. Normal exit
removes it. After a crash, independently confirm the old coordinator process is
stopped, then remove **only that lock file**, not the database/WAL. The lock is
never automatically stolen. Do not open the same database through aliases,
symlinks, different paths, or a shared network filesystem, and do not run two
coordinators with separate journals for the same plan identity. Inspection using
this CLI requires the coordinator to have stopped.

On open, any interrupted `running` task becomes `unknown`. A timeout,
disconnection, or unverified terminal result also becomes unknown. Neither
`run` nor restart resubmits unknown work. A timeout does not cancel an invocation
already queued at the Gateway; an offline worker can still execute it later.
Completed tasks never rerun; still-pending independent tasks can continue.

Manual recovery is a separate, read-only action:

```sh
go run ./cmd/plan -plan plan.json -state .data/plan.db -action reconcile
go run ./cmd/plan -plan plan.json -state .data/plan.db -action status
# Only when recovered state is understood, explicitly continue pending tasks:
go run ./cmd/plan -plan plan.json -state .data/plan.db -action run
```

`reconcile` connects and reads pre-welcome Gateway history, verifies the exact
original caller/target/capability/input/request, and records an authoritative completed or
failed result. It never sends `capability.invoke`. If no terminal result exists,
the task stays unknown; a later result requires another explicit reconciliation.
Recover the original worker with its original journal when appropriate. This
version deliberately has no force-retry, reassignment, cancellation, automatic
reconnection, distributed exactly-once guarantee, or arbitrary result override.

## Verification

```sh
go test -race ./internal/orchestrator ./internal/agent ./cmd/plan
```

The scheduler tests use a real HTTP/WebSocket Gateway and two real durable
executors. A barrier proves independent tasks are concurrently assigned before
either completes. Tests verify fan-in results, completed restart without new
invocations, offline timeout without retry, read-only recovery after the original
worker later executes, interrupted assignments, journal ownership and identity
conflicts, cycles, missing/duplicate dependencies, invalid UTF-8, and bounds. Additional integration coverage runs a dependency through an
installed learned recipe, recovers its result without another invocation, and
rejects recovery under a changed capability.
