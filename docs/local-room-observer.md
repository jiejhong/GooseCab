# 本地房间观察页

`cmd/localroom` 是显式启用的独立试验进程，一次服务一个固定房间，仅监听 IPv4 回环。浏览器从进程记录的回环地址打开观察页。只有授予本房间读取权限的人类身份可以查看发言，授予读写权限后可发纯文本，并可附一个本房间的 Agent 或人类 ID。全员点名需要额外的房间权限。

## 本地运行

```sh
go run ./cmd/localroom -config /path/to/synthetic-policy.json -state /path/to/new-private-directory -port 0
```

配置文件只使用本次试验的合成身份。每个房间实例使用独立凭据，每个身份使用由 32 个随机字节编码成无填充 base64url 的独立令牌。文件仅填写该字符串的 64 位十六进制 SHA-256 摘要。将配置放在状态目录外。以下为禁用状态的占位示例。

```json
{
  "enabled": false,
  "room_id": "<本地测试房间 ID>",
  "agent_transport": false,
  "participants": [
    {
      "kind": "user",
      "id": "<稳定人类 ID>",
      "display_name": "<受信配置的房间显示名>",
      "token_sha256": "<SHA-256 摘要>",
      "expires_at": "<RFC3339 到期时间>",
      "read": true,
      "write": true,
      "broadcast": false
    }
  ]
}
```

完整候选使用新建的独占私有目录和支持内核文件锁的本机文件系统。数据库 `room.gcdb` 带 v2 格式标记，在同一文件中原子保存发言和未读收件状态。该进程拒绝首段 v1 试验库、旧数据库或其他无关文件。两个进程无法同时打开同一目录。进程被强制杀死后，内核释放锁，下一次启动读取上次已安装的数据库，并清理中断写入的临时文件。

数据库最多保存 256 条发言，另有 8 MiB 文件上限。达到任一上限后，新发言返回 `409 Conflict`，观察页提示记录已满。历史、未读和标记已读继续可用，已有记录全部保留。试验库不从已有 staging 库迁移，验收应使用新目录。

## 读取和写入

- 浏览器仅在当前页面内存持有令牌，使用 `Authorization: GooseCabLocal …`，退出、重新载入或过期后需重新认证。旧 Bearer、Basic、Cookie 和 URL 凭据均无授权效力。
- `GET /api/events` 为本房间有读权限的人类返回经过认证的稳定 ID、配置显示名和最多 20 条发言，按事件从新到旧。显示名留空时使用稳定 ID。发言的显示名在写入时由服务端绑定，浏览器同时展示角色及 ID。认证的人类发言使用 `actor.kind: "human"`，Agent 使用 `"agent"`。旧 v2 库中的人类作者 `"user"` 会在读取时映射为 `"human"`。启动读取本身不改写库文件。配置中的人类角色、点名目标及未读收件人仍使用 `"user"`。
- 历史的 `next_before` 由持久事件锚定。请求 `GET /api/events?before=<next_before>` 读取该事件以前的下一页。历史采集期间插入新发言不会移动较早页的边界。历史、未读和确认各使用独立的请求。
- 显式授予读权限的合成 Agent 可通过 `/ws` 接收本房间只读回放和实时事件。该入口和观察页都不会自动清除未读，连接在凭据到期时关闭。
- `POST /api/messages` 要求精确同源和人类读写权限。JSON 接受 `text` 及一种可选点名。单人使用 `mention: {"kind":"agent","id":"…"}`，人类目标使用 `kind: "user"`。全员使用 `mention_all: true`，仅允许显式配置 `broadcast: true` 的人类发言者。服务器按提交时本房间有效读权限确定收件人，并排除发送者。服务端绑定房间、发送者、事件 ID 和序号。单个正文限 4096 字节，同一身份两条成功发言之间至少相隔 500 毫秒。
- 持久化成功后才发布回放可见的事件和 HTTP 回执。遇到连接中断而结果未确认时，先查看历史。页面按纯文本渲染正文和点名，正文里的 `@所有人` 只是文字。

## 点名未读

- 观察页的「未读点名」独立于时间线。每页至多 20 条，可查看更早的记录。标记本页已读只确认当时展示的事件 ID。重复确认结果相同，标记一页只影响该页已列出的事件。
- `GET /api/mentions` 和 `POST /api/mentions/read` 绑定经过认证的房间及收件身份。具备读权限的人类和 Agent 均可列出自己的未读。HTTP 确认入口只接受人类凭据，Agent 通过下面的独立传输自动确认。分页参数 `before` 使用未读列表返回的 `next_before`，确认请求使用 `{"event_ids":["…"]}`。GET、回放和分页均不改变收件状态。

## 隔离 Agent 传输

在独立进程显式设置 `agent_transport: true` 后才提供 `/api/v1/agent/connect`。为合成 Agent 配置独立摘要、稳定 ID、显示名和读权限，需要发言时另外授予写权限。旧共享 Bearer、浏览器凭据、客户端冒报的 Agent ID、其他房间请求和能力调用均拒绝。传输只接受 IPv4 回环，旧 Gateway 的默认路由照常运行。

现有 Agent 客户端可显式使用该传输。在本地为合成 Agent 单独设置 `GOOSECAB_LOCALROOM_AGENT_TOKEN`。下列命令启动交互客户端。

```sh
go run ./cmd/agent -local-room -url ws://127.0.0.1:<port>/api/v1/agent/connect \
  -room <fixture-room> -id <fixture-agent> -mode interactive
```

`chat <text>` 发言。流式回放、实时推送、心跳和发言本身均不清除未读。

正常分页拉取使用独立的 Chat MCP 模式。它要求同一个房间和 Agent 的专用凭据，并且只公布 `room_read`。

```sh
go run ./cmd/chat-mcp -local-room -url ws://127.0.0.1:<port>/api/v1/agent/connect \
  -room <fixture-room> -agent <fixture-agent>
```

调用 `room_read` 时使用 `after_seq`、`after_cursor` 和 `limit`。继续分页时成对保留响应里的 `next_seq`、`next_cursor`。客户端成功解码、校验并写出工具结果后，自动确认本连接实际交付且属于该 Agent 的点名事件 ID。序号和游标只选择读页，会话外凭证、未包含的同序号事件和未返回的页无法确认。相同交付的重复确认幂等。

结果含待确认事件时会带 `mention_receipt: "pending_confirmation"`。适配器写出结果后在同一连接等待服务端提交。如果确认失败或结果不确定，适配器向 stderr 报错并退出。重新连接后重复原 `room_read` 参数即可确认仍未读的同一页。连接、重连或心跳本身均不清除未读。其余程序用原命令仍可访问原 Gateway。

## 本地验收命令

```sh
go test -mod=readonly -race ./cmd/localroom ./internal/humanauth
go test -mod=readonly -tags localroom_sqlite_integration ./cmd/localroom -run '^TestLegacyStoreRefusesLocalRoomDatabase$'
```

第一条覆盖读写权限、分页、旧标签页、同序号跨房、容量上限和强杀后的进程恢复。有 Python SQLite 时还会运行数据库格式拒绝检查。第二条让旧 Gateway 的存储入口尝试打开真实生成的独立库，需标准锁定的 SQLite Go 模块。

浏览器及隔离 Agent 客户端可用独立二进制复测。

```sh
go build -mod=readonly -o /tmp/goosecab-localroom ./cmd/localroom
GOOSECAB_LOCALROOM_TEST_BINARY=/tmp/goosecab-localroom \
  go test -mod=readonly -race internal/agent/client.go internal/agent/localroom_test.go
GOOSECAB_LOCALROOM_TEST_BINARY=/tmp/goosecab-localroom \
  go test -mod=readonly -race internal/chatmcp/server.go internal/chatmcp/localroom.go \
  internal/chatmcp/server_test.go internal/chatmcp/localroom_test.go
python3 tests/localroom-observer-browser.py /tmp/goosecab-localroom
```

浏览器测试使用本机 Chromium 和 Playwright。这些集成测试均使用临时目录和合成身份。
