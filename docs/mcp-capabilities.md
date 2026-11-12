# Explicitly approved MCP capabilities

MCP is a real HTTP client integration, not a descriptive label or automatic server installer. A transferable manifest contains the exact endpoint, allowed tool names, capability IDs and object input schemas. It contains **no credentials or local approval fields**. Review endpoint paths and schemas before sharing; never put secrets in these fields. Full schemas remain in the reviewed manifest instead of being broadcast. The registry announces only logical server/tool names and manifest digest, not the connection endpoint.

Import and export do not grant permissions. Approval requires exact allowed caller agent IDs (`-callers`, no wildcard); `-rooms` optionally further restricts rooms. Runtime checks the verified event actor, never a caller field supplied in the invocation payload. Caller IDs remain identities within the trusted Gateway domain, not cryptographic per-agent identities. Review the entire exported file and its SHA256 before explicitly approving that digest on the receiving agent. Approval state and the optional bearer-token environment-variable name live only in the receiver's local SQLite database. Export never exports bindings, token values or approval state. Do not share this database.

```sh
go run ./cmd/mcp -state .data/mcp.db -file examples/mcp-manifest.json import
go run ./cmd/mcp -state .data/mcp.db list
go run ./cmd/mcp -state .data/mcp.db -sha256 REVIEWED_SHA256 -callers planner-agent -rooms demo -token-env MY_MCP_TOKEN approve
go run ./cmd/mcp -state .data/mcp.db -sha256 REVIEWED_SHA256 -file shared-manifest.json export
```

Flags go before the command. Bind `MY_MCP_TOKEN` locally without putting its value in command arguments or manifests. Import a transferred manifest into a different database and it remains inactive until approved there. Workers opt in with `go run ./cmd/agent -mode worker -state .data/worker.db -mcp-store .data/mcp.db` (plus their usual Gateway/room/agent flags). Applications use `mcpclient.Open`, `Store.Provider`, and the provider's `CapabilityIDs`, `Announcements`, `Invoke`; the worker's persistent executor must own deduplication and unknown-outcome recovery.

## Transport and limits

- MCP protocol `2025-03-26`: initialize, initialized notification, tools/list, tools/call. Per-invocation initialization and single bounded discovery page; exact tool name and JSON schema must match approval. Other server-advertised tools are not callable.
- Streamable HTTP **JSON-response subset only**. SSE, stdio, subprocess commands, npm installation, scripts, dynamic config, OAuth discovery and unsolicited server requests are unsupported. No processes are launched.
- HTTPS required by default. Redirects, proxies, URL userinfo/query/fragment and non-HTTP schemes are rejected. DNS results are checked and a checked address is dialed directly, avoiding a second DNS lookup. Private/non-global addresses require local `-allow-private` approval. For isolated tests only, `-allow-http-loopback` permits a literal loopback HTTP endpoint. It cannot permit arbitrary HTTP hosts.
- Requests are bounded JSON objects (16 KiB); response envelopes 64 KiB; only UTF-8 text content up to 4096 bytes is emitted. Images, resources and file bytes never enter Gateway results. Schemas are inspected and matched, not a complete local JSON Schema validator; the MCP server validates arguments against the exact approved schema.
- All error messages are fixed and do not contain response bodies, endpoint URLs, credentials or transport exception details. Exact bearer values echoed in text are redacted. This is not general data-loss prevention: approved tools can still return confidential information, so approve only appropriate tools and inputs.
- Initialization/discovery failures are `failed` because no tool call was attempted. Explicit JSON-RPC rejection and tool `isError` are `failed`. Transport errors, HTTP errors, unsupported/malformed/oversized responses after tools/call are conservatively `unknown`, because the tool may have executed. Unsupported output after a successful call is also `unknown`, not permission to retry. Tool calls are never automatically retried.

The local approval binding, not the transferred manifest, grants permission to invoke selected tools. It is not proof of safety or that a side-effecting tool is idempotent. Restrict Gateway access and capability routing accordingly.

## Revoke or change local approval

```sh
go run ./cmd/mcp -state .data/mcp.db -sha256 REVIEWED_SHA256 revoke
```

Revocation deletes only the local approval; export bytes and imported manifests remain unchanged. Running providers reread local approval before invocation and again before tools/call, failing closed if revoked, changed, or unreadable. Changed bindings and newly approved manifests require a worker restart to load them. Revocation cannot undo or reliably cancel a tool call already dispatched; treat any in-flight outcome according to the normal unknown-outcome rules. Keep the local MCP database open for the worker's entire lifetime.
