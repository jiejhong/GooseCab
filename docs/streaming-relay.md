# Real cross-machine streaming relay

`internal/relay` is the data plane, distinct from the old `internal/transfer`
metadata service and `agent.CopyLocal`. Sender and receiver each open an outbound
WebSocket to the Gateway. No inbound agent port, remote filesystem API, shared
filesystem, or agent-to-agent reachability is required.

Gateway forwards binary frames directly from sender to receiver with backpressure.
It never writes file bytes to SQLite, events, logs, or its filesystem. Default bounds:
32 KiB/message, 256 MiB/transfer, 64 sessions (including consumed tickets until expiry),
136 admitted handlers/connections, 4 KiB creation metadata, five-second metadata body
read deadline, and two-minute absolute session deadline. There is no whole-file
buffer, queue of chunks, resumable partial download, or unbounded tombstone map.
The default file limit is 268435456 bytes (256 MiB), comfortably above 30 MiB
(31457280 bytes). MiB means 1024×1024 bytes; decimal MB means 1000000 bytes.
Set `GOOSECAB_RELAY_MAX_BYTES` at server startup to an explicit decimal byte limit
from 1 through 268435456, for example `33554432` for 32 MiB. The unset variable
uses the default; empty, malformed, zero, negative and over-ceiling values reject
startup rather than falling back. Raising the current client/protocol ceiling
requires a coordinated code change. The setting is read at startup, not hot-reloaded.
This is a file-size ceiling, not a buffer allocation: the relay still forwards
bounded 32 KiB frames with backpressure, never buffering a whole 30/32 MiB file.

Excess sessions return HTTP 429; stalled/disconnected sessions expire. Restarting
Gateway invalidates all tickets. Scale-out requires sticky routing to one process.

## Explicit invocation

Use HTTPS for the gateway base URL outside local development, set
`GOOSECAB_AGENT_TOKEN` on Gateway and both agent hosts, and proxy WebSocket Upgrade
for `/api/v1/relay/connect` with at least a two-minute proxy read timeout. The server
routes are inside the same outer token guard as all other `/api` routes. The
standalone relay handler must likewise be placed behind authentication.

On sender host (paths are local to this host):

```sh
go run ./cmd/transfer -mode create -url https://gateway.example.com \
  -sender agent-a -recipient agent-b -source ./artifact.bin -ticket ./private-ticket.json
go run ./cmd/transfer -mode send -url https://gateway.example.com \
  -source ./artifact.bin -ticket ./private-ticket.json
```

Securely deliver a recipient copy of the ticket JSON with `send_secret` removed to
agent-b; do not put tickets in room events, public chat, URLs, or logs. Keep the
sender's copy private (remove `receive_secret` if sending through an intermediary).
Within two minutes of creation, on receiver host:

```sh
go run ./cmd/transfer -mode receive -url https://gateway.example.com \
  -ticket ./recipient-ticket.json -dir ./trusted-downloads -name artifact.bin
```

The destination directory must already exist and its ancestors must be owned and
protected by the receiving agent. The basename is a receiver-local choice. Path
traversal, destination overwrite, and destination symlink overwrite are rejected.
Data is staged in a 0600 temporary file, verified against the ticket's size and
SHA-256, synced, closed, and published via an atomic no-overwrite hard link; the
directory is synced before successful acknowledgement on Linux/macOS. On Windows,
file data is still synced and the hard link is created atomically without overwrite,
but directory fsync is not supported by this implementation: an acknowledged file's
new directory entry can be lost after a crash or power loss. This is an explicit
Windows durability limit, not an ignored filesystem error. File sync, close and
hard-link errors still fail the transfer on every platform; Linux/macOS directory
open/sync/close errors also fail it. Failure cleans the temporary file. If final
directory sync or final network ACK fails, the published destination
may already exist: inspect it before retrying. This is not an exactly-once delivery
protocol. The sender only reports success after the receiver acknowledges.

## API and security boundaries

`Client.Create(ctx, sender, recipient, size, sha256)` creates a one-time `Ticket`.
`Client.Send(ctx, ticket, io.Reader)` streams a bounded source. `Client.Receive`
verifies bytes into a private staging writer; never give it an active capability
file. Its ACK means verified bytes only. `ReceiveAndFinalize` runs a caller-supplied
bounded local installation callback after verification and before ACK, suitable for
skill installation. `ReceiveFile` supplies the safe local-file publication flow.
Local readers, writers, and finalizers must themselves be bounded/nonblocking; a
network deadline cannot interrupt a custom reader blocked on unrelated resources.

The POST `/api/v1/relay` response includes two cryptographically random role secrets.
WebSocket requests send ID, role, agent binding and secret in `X-Relay-*` headers,
not query parameters. Each role is admitted once; second senders/receivers and wrong
secrets/agent bindings are rejected. Only share the secret for the intended role.
The shared Gateway token is still a single trust domain, not independent agent
identity or room ACL. Ticket possession authorizes its role. Use separate Gateway
instances or an authenticated reverse proxy for stronger tenant isolation. TLS is
required to keep bytes and credentials confidential in transit; this relay is not
end-to-end encrypted against the Gateway process. Disable proxy body/frame logging.

Tests use real TCP HTTP/WebSocket connections, exercise multiple binary chunks,
empty files, integrity failure, no-overwrite, traversal, replay, duplicate roles,
secret/recipient binding, capacity, cancellation, expiry and oversized frames:

```sh
go test -race ./internal/relay ./cmd/transfer
```

The process-level acceptance smoke (`python3 tests/collaboration-smoke.py`) includes
a 32 MiB real agent-to-agent transfer with the Gateway limit set to 33554432 bytes,
checks exact file size and hash, and rejects metadata declaring 33554433 bytes.
Its Gateway peak RSS measurement reports process memory rather than an inferred
file-buffer size. Metadata boundary unit tests independently cover 30 MiB and the
exact configured 32 MiB ceiling.
