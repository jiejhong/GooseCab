# First-class capability catalog

The public catalog distinguishes `text`, `skill`, `mcp`, and `knowledge` kinds.
`GET /api/capabilities?kind=mcp&q=search` returns matching capabilities grouped
by Agent. `q` matches ID, name or description (case-insensitive); `kind` must be
one of the four kinds. `callable` distinguishes an advertised manual/reference
from an executable provider. Catalog entries are metadata, not online presence
or permission to run anything.

Legacy string capability IDs in `agent.hello` continue working. Namespaces
`cap.skill.*`, `cap.mcp.*`, and `cap.knowledge.*` infer their respective kinds;
existing IDs infer `text`. New namespace hello-only entries are non-callable
until a validated rich announcement opts in with a typed descriptor. A rich `capability.announce` may specify name,
description (up to 1024 UTF-8 bytes), version, kind, callable, a bounded input
schema, and exactly one typed descriptor:

- `skill`: `digest` (SHA-256), `version`, `entrypoint_kind` (`manual` or
  `text-recipe`). A manual must set `callable:false`. No files or local paths.
- `mcp`: `server_name`, `tool_name`, `connection_manifest_id`. These are logical
  identifiers only. Endpoints, headers, credentials and environment variables
  stay in the explicitly configured local connection manifest.
- `knowledge`: `collection`, `share_policy`. These are logical identifiers only;
  announcements contain no record contents or private memory.

Input schemas are at most 4096 bytes and support `type`, `properties`,
`required`, boolean `additionalProperties`, `items`, and bounded numeric length/item/value
constraints. Defaults, examples,
external references and arbitrary schema extensions are not public metadata.
Unknown announcement fields, arbitrary `manifest` objects, kind/namespace
mismatches and unsafe descriptor fields are rejected before event persistence.
This intentionally tightens old unrestricted manifest announcements; migrate
those to typed metadata. Plain legacy string hello declarations still work.

`cap.text.uppercase`, learned text and skill recipes use the existing bounded
`{"text":"..."}` input (4096 UTF-8 bytes). MCP and knowledge invocations accept
JSON objects of at most 16 KiB. All executable kinds share the same explicit
target, durable request-ID ownership, deduplication and result-owner checks.
Outputs remain at most 4096 UTF-8 bytes of `text/plain`. Payloads and results
are visible in room history: never put credentials or private data in them.

```sh
go run ./cmd/call -room demo -id caller -target mcp-worker \
  -request-id example-search-1 -capability cap.mcp.demo.search \
  -json '{"query":"public documentation"}'
# Or use -payload request.json instead of -json.
```

The Go client offers `InvokeCapability(ctx, requestID, capabilityID, target,
payload, onProgress)`; `InvokeText` and `InvokeTextCapability` remain wrappers.
Keep caller identity and request ID stable. Timeouts/disconnections are uncertain
outcomes, not permission to create a new ID and repeat side effects. Advertising
or discovering a capability never enables a local worker, tool, package or
knowledge sharing policy automatically.
