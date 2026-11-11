# GooseCab 控制面协议

本文记录当前 P0-P2 实现实际支持的消息和元数据接口。协议仍会演进，客户端应忽略不认识的字段。

## WebSocket 连接

Agent 主动连接 Gateway，不需要开放入站端口：

```text
ws://gateway.example.com/api/v1/agent/connect?room_id=demo
```

观察页使用同一套事件流：

```text
wss://gateway.example.com/ws?room_id=demo&resume_seq=0
```

`resume_seq` 是房间内最后已经处理的事件序号。Gateway 会先回放 `seq > resume_seq` 的事件，再广播新事件。两条路径以及全部 `/api` HTTP 接口（包括 `/api/status`）都支持 `Authorization: Bearer <token>`；当 `GOOSECAB_AGENT_TOKEN` 非空时必须发送且只能发送一个该 header。`/` 观察页外壳和 `/healthz` 不要求 token。

`room_id` 必须非空、最多 256 字节且只能出现一次；`resume_seq` 若提供，必须是非负整数且只能出现一次。`/ws` 仅用于读取：任何应用消息均返回 `read_only`，不会登记 Agent 或写事件。Agent 写入只能走 `/api/v1/agent/connect`。

## 建连握手

Agent 客户端连上后，必须先发送一次 hello，再发送其他写入消息：

```json
{
  "type": "agent.hello",
  "agent_id": "agent-a",
  "name": "Echo Agent",
  "role": "worker",
  "capabilities": ["cap.echo"]
}
```

Gateway 回复：

```json
{
  "type": "agent.welcome",
  "room_id": "demo",
  "resume_from": 0
}
```

Gateway 在 hello 成功写库、更新共享 Registry 后回复 welcome，并将 hello 和每个 capability 广播到房间。回放或本次 hello 事件可能先于 welcome 到达，客户端应保留它们。`agent_id` 必须非空且不超过 256 字节；同一连接的身份不能通过再次 hello 修改。

事件帧的基本结构是：

```json
{
  "event_id": "550e8400-e29b-41d4-a716-446655440000",
  "room_id": "demo",
  "seq": 2,
  "type": "capability.announce",
  "actor": {"kind": "agent", "id": "agent-a"},
  "capability_id": "cap.echo",
  "correlation_id": "",
  "ts": "2026-11-09T00:00:00Z",
  "payload": {"agent_id": "agent-a", "capability_id": "cap.echo"}
}
```

`event_id` 是不透明 ID；新自动事件使用全局唯一 UUID，已有 ID 不变。`seq` 由 SQLite 写事务在同一个 `room_id` 内原子分配，WebSocket、P1/P2 和传输生产者共用分配路径。客户端应使用 `(room_id, seq)` 去重，并在重新连接时带上最近一次确认的序号。

SQLite 会保存完整事件信封，包括 `actor`、`task_id`、`capability_id`、`session_id`、`correlation_id` 和 `causation_id`，重连回放与实时事件保持相同字段。启动时会自动为旧数据库添加可空的 `event_json` 列，并保留原有行和列。升级前的事件仍可回放，但旧版本从未保存的顶层字段无法补回，保持为空；升级后的新事件会完整保存。数据库继续只保存事件与本地文件引用，不会读取或上传引用指向的文件内容。

## 能力消息

Agent 可以直接发送以下 JSON 帧。Gateway 会把它们写入事件流并广播给同房间连接；当前 Gateway 不替 Agent 执行 capability。

```json
{"type":"capability.announce","agent_id":"agent-a","capability_id":"cap.echo","name":"Echo","version":"1"}
{"type":"capability.invoke","request_id":"req-1","capability_id":"cap.echo","target_agent_id":"agent-a","payload":{"text":"hello"}}
{"type":"capability.progress","request_id":"req-1","percent":50,"message":"working"}
{"type":"capability.result","request_id":"req-1","status":"completed","output_ref":"file:///agent-a/out.json"}
```

`input_ref`、`output_ref`、`artifact_ref` 是 Agent 本地引用。Gateway 只保存引用字符串，不读取引用指向的文件。

### 通用事件与拒绝规则

```json
{"type":"event.publish","event":{"type":"chat.message","payload":{"text":"hello"}}}
```

Gateway 将事件的 room 和 actor 绑定到握手 room 与 hello 的 Agent ID；省略时补齐，显式填写但不一致时拒绝。客户端提供的 `seq` 会被服务器分配的值替换；可保留显式 `event_id` 和时间戳。重复显式 ID、数据库不可写等失败会返回 `persist_failed`，不会假装发布成功。`agent.hello`、`agent.welcome`、`capability.announce` 为保留事件，不能用 `event.publish` 绕过专用处理。

拒绝帧格式为 `{"type":"error","code":"room_mismatch","message":"..."}`，不会写入事件历史。常用 code 包括 `read_only`、`hello_required`、`already_initialized`、`identity_mismatch`、`room_mismatch`、`reserved_event`、`persist_failed`；连接可继续使用并更正输入。非法 JSON 或超出 1 MiB 的消息会断开连接。

hello 与 announce 更新 HTTP `/api/agents`、`/api/capabilities` 和 P2 `/api/capability-resolve` 共享的 Registry。announce 的 `agent_id` 必须省略或等于连接身份；同一能力按 ID 合并，保存 name/version/manifest。重连 hello 替换能力列表，仍存在的能力保留详细信息。Registry 不提供在线状态，断连不清除；重启后需重连或重新注册。

这仍是共享 token、全网关目录，不提供初始 Agent ID 的独立认证或房间级权限。连接绑定防止在同一会话中伪造 envelope，但持有共享 token 者仍可在新连接声明其他 Agent ID。能力执行、结果归属授权和多租户隔离不在本次修复范围。

## 文件传输元数据

文件传输的控制面只传元数据，文件字节由 Agent 之间的 direct 或 relay 数据面实现：

```json
{
  "type": "transfer.request",
  "file_id": "file-1",
  "source_agent": "agent-a",
  "source_path_ref": "/var/lib/agent-a/out.bin",
  "target_agent": "agent-b",
  "target_path_ref": "/var/lib/agent-b/in.bin",
  "size": 42,
  "sha256": "...",
  "mode": "direct",
  "offset": 0,
  "status": "requested"
}
```

可广播的传输状态类型为 `transfer.accepted`、`transfer.progress`、`transfer.completed` 和 `transfer.failed`。状态更新应携带同一个 `file_id` 或 `transfer_id`。

## HTTP 元数据接口示例

以下 HTTP 示例在启用 token 时均需添加 `-H "Authorization: Bearer $GOOSECAB_AGENT_TOKEN"`，包含读、写、状态及发现接口。

注册 Agent 时，Registry 的 `capabilities` 是对象数组：

```bash
curl -X POST http://127.0.0.1:8312/api/agents \
  -H 'Content-Type: application/json' \
  -d '{"id":"agent-a","name":"Echo Agent","capabilities":[{"id":"cap.echo","name":"Echo","version":"1"}]}'
```

P1 调用生命周期示例：

```bash
curl -X POST http://127.0.0.1:8312/api/invocations \
  -H 'Content-Type: application/json' \
  -d '{"id":"req-1","room_id":"demo","capability_id":"cap.echo","caller_agent_id":"agent-b","target_agent_id":"agent-a","input_ref":"file:///agent-b/in.json"}'

curl -X POST http://127.0.0.1:8312/api/invocations/req-1/progress \
  -H 'Content-Type: application/json' -d '{"percent":50,"message":"working"}'

curl -X POST http://127.0.0.1:8312/api/invocations/req-1/result \
  -H 'Content-Type: application/json' -d '{"status":"completed","output_ref":"file:///agent-a/out.json"}'
```

P2 的 plan/task/blackboard/private-session 接口当前记录编排元数据，不会启动调度器或 Provider 进程。


## 交互记录列表与 HTML 导出

### JSON 列表

`GET /api/events?room_id=demo&limit=200`。必须明确指定非空 `room_id`（最多 256 字节）；没有跨房间聚合模式。只接受 GET。`limit` 默认 200，最大 1000。

响应包含 `room_id`、`events`、`total`、`snapshot_seq`、`snapshot_cursor`、`has_more`、`next_seq`、`next_cursor` 和 `redacted: true`。`total` 是这个房间在快照边界内的总记录数，不是当前页大小。事件保留完整信封结构与精确 JSON 数值，敏感字段内容被替换。

当 `has_more` 为 true，下一页携带上一页的 `next_seq` 和 `next_cursor`，并固定首个响应的两个快照边界：

```text
/api/events?room_id=demo&limit=200&after_seq=<next_seq>&after_cursor=<next_cursor>&through_seq=<snapshot_seq>&through_cursor=<snapshot_cursor>
```

序号仅在房间内使用；同一序号的记录由不透明行游标区分，不会因分页漏掉。`snapshot_cursor` 限制底层已存在的行，即使并发写入复用了较早序号，也不会混入已固定的快照。首次边界、计数与事件页在同一 SQLite 读取事务中获取。调用者应将游标当作不透明值，不把它当事件 ID。仅指定 `after_seq` 时，它是严格大于的下界，不回传该序号的记录。

### HTML 下载

`GET /api/events/export.html?room_id=demo` 以附件返回完整、独立的 HTML 文档。它读取数据库中的事件，不是导出浏览器当前已加载的几条。支持可选 `after_seq`（不含）和 `through_seq`（包含），所有被选中的记录均按序号及写入行顺序导出。标题、摘要和页脚明确说明房间、范围、条数、UTC 导出时间和脱敏边界。

文档区分聊天、能力调用、进度、结果和其他事件；每条可展开完整的脱敏事件信封。无 JavaScript、远程字体、图片或其他外部资源。Go `html/template` 对所有事件内容自动转义，离线 CSP 限制主动内容。可用浏览器打印另存为 PDF。

单次最多 10,000 条事件。超限返回 HTTP 413，没有部分 HTML；请缩小序号范围再导出。空房间返回带空状态的有效文档。读取/渲染失败返回 500，不冒充成功导出。旧数据库未保存的 actor/task/session 等字段无法恢复，旧记录照实显示缺失。

### 认证和数据边界

两接口在设置 `GOOSECAB_AGENT_TOKEN` 时要求 `Authorization: Bearer <token>`。拒绝 URL token 参数，并返回 `Cache-Control: no-store`。共享 token 不是分房间授权；不同租户仍需反向代理或后续 ACL。未设置 token 时必须自行限制网络访问。网页通过 HTTP 分页和每 5 秒轮询展示持久化事件，不会用原始 WebSocket 数据绕过脱敏。

这是用于阅读和分享的脱敏副本：凭据字段（token/secret/password/API key/Authorization/cookie 等）、文件路径与 `*_ref` 引用、可识别的本地路径和当前网关 token 被隐藏；常见带引号凭据赋值也被过滤。存储的原始事件不会被改写，导出不会解引用路径、读取或打包文件字节。自由文本中未识别的秘密仍可能存在，分享前必须人工复核。该功能不提供原始数据备份或任意文件下载。


## 可执行文本能力（向后兼容扩展）

`cap.text.uppercase` 是 opt-in worker 唯一的真实执行能力；旧能力仍是原有元数据流程。调用必须带显式目标及最多 128 bytes 的请求 ID。相关事件仍使用 `capability.invoke/progress/result`，`correlation_id=request_id`。

```json
{"type":"capability.invoke","request_id":"text-1","capability_id":"cap.text.uppercase","target_agent_id":"text-worker","payload":{"text":"Hello"}}
{"type":"capability.progress","request_id":"text-1","status":"running","message":"Executing bounded text capability"}
{"type":"capability.progress","request_id":"text-1","status":"completed","percent":100,"message":"Execution complete; publishing result"}
{"type":"capability.result","request_id":"text-1","status":"completed","result_available":true,"output":{"text":"HELLO","media_type":"text/plain"}}
```

新增 progress.status 与 result.result_available/output；旧客户端应忽略不认识的字段。progress.completed 仅表示执行完成；只有 completed result 同时具备 `result_available=true` 和内联 `output.text` 才表示可取结果。失败及不确定分别使用 result.status=`failed`/`unknown`，`result_available=false` 并提供 `error`。旧 `output_ref` 不等于交付。

Gateway 保证同 ID 的新能力调用只能保持原调用者、目标和输入，且只有绑定目标能发进度和结果；禁止通过通用 event.publish 发这三类生命周期。worker 使用持久日志去重，结果未知不自动重跑。小文本可留在事件中，输入输出各限 4096 bytes；任何文件内容仍在 Agent。本能力不是 shell 或通用任务执行器。

完整命令、取结果方式、恢复语义与边界见 [真实 Agent 任务](agent-tasks.md)。
