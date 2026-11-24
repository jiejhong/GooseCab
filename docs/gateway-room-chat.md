# Gateway 受保护房间发言

在现有 Gateway 上通过 `-room-chat-config` 选择受保护房间。使用同一 SQLite、原事件 ID、历史字段和游标。发言与点名未读在同一事务提交，提交后发布到原事件总线。新功能默认关闭。`cmd/localroom` 和 Tailnet HTTP 隔离测试有各自的验收用途。

## 启用条件

保存权限为 `0600` 的配置，CLI 后端绑定明确的回环地址和端口。受控代理终止 HTTPS，保留精确外部 Host，只在确认前端 TLS 后设置唯一的 `X-Forwarded-Proto: https`，替换来客的转发头，并转发 WebSocket Upgrade。Gateway 检查 Host、实际回环本地地址和对端、转发协议及浏览器 Origin。不要公开暴露回环上游或让同机非受信进程访问。仅有明文 HTTP 的正式入口应保持功能关闭。

每个真人和 Agent 使用不同的 32 字节随机 base64url 凭据。只将原始凭据字符串的 SHA-256 十六进制摘要写入配置，由服务端绑定身份、有效期和房间。请通过私有机制提供凭据。

```json
{
  "origin": "https://gateway.example.invalid",
  "trust_loopback_proxy": true,
  "rooms": [{
    "id": "<room-id>",
    "read_only": false,
    "humans": [{
      "id": "<human-id>",
      "display_name": "<display-name>",
      "token_sha256": "<digest>",
      "expires_at": "<RFC3339 expiry>",
      "write": true,
      "broadcast": false
    }],
    "agents": [{
      "id": "<trusted-agent-id>",
      "token_sha256": "<independent-digest>",
      "expires_at": "<RFC3339 expiry>",
      "write": true
    }]
  }]
}
```

## 客户端与授权

| 用途 | 受保护房间 | 普通房间 |
| --- | --- | --- |
| 浏览器历史、导出、在线状态和观察 | 从 `/room-chat` 登录。历史、导出、在线状态及 `/ws` 要求人类专用授权。页面用已授权的 HTTP 刷新，工具可带授权连接 `/ws`。 | 保留既有观察页、API 和部署的共享令牌规则。 |
| 旧 Agent 的 WebSocket 与历史 | 通过原 `/api/v1/agent/connect` 和 `/api/events`，以独立 Agent 凭据作 Bearer。校验握手 ID 再回放。保留旧 welcome、事件 ID、实时和 `resume_seq`。有显式写权限的 Agent 可用原协议发普通文本。 | 保留原 Agent 协议、共享凭据和既有读写。 |
| 发言与点名 | 真人正文以纯文本显示。独立配置写入和全员点名权限。目标限定同房间。旧 Agent 收到的事件兼容原格式。 | 保留旧 Gateway 的聊天行为。 |
| 调用、传输和目录 | 当前仅支持聊天。旧调用及传输 HTTP 路由不能写入保护房间。能力和传输帧在保护房间禁用，连接不注册到全局目录。 | 保留原调用、传输和目录。 |
| 授权到期 | 关闭已授权 WebSocket，阻断重新连接和回放。 | 沿用既有规则。 |

受保护房间的旧 Agent 需要换为房间专用凭据，并将地址切到 HTTPS/WSS。基础聊天与回放可沿用现有客户端。需要自动收讫的 Agent 用候选版 Chat MCP。

```sh
chat-mcp -gateway-room-chat -url "$AGENT_WSS_URL" -room "$ROOM_ID" -agent "$AGENT_ID"
```

`AGENT_WSS_URL` 指向 `/api/room-chat/agent`。从 `GOOSECAB_ROOM_CHAT_AGENT_TOKEN` 读取独立 Agent 凭据。只有 `room_read` 页成功解码、校验、交付后才自动确认。服务端检查同连接交付证明和实际页内事件 ID，只清该 Agent 的未读。普通 HTTP 历史或旧 WebSocket 接收不会自动确认。

## 切换及同库只读回退

1. 先在独立环境用锁定的 Go/SQLite 驱动、合成旧库、新旧 Agent 和合成凭据跑全量测试。确认前端 TLS、代理授权边界、旧 Agent 的房间凭据与浏览器迁移，记录切换前事件 ID。若受保护房间依赖旧的能力调用或传输，先迁移相应工作流。
2. 经授权生成并核验包含 WAL 的一致备份。停净所有能读取该文件的旧 Gateway 进程与上游连接，确认代理无备用旧读入口，再让候选独占服务该文件。受保护房间配置只在启动时加载，切换会断开原连接。普通房间重连后保持原访问方式。已在切换前被下载或缓存的历史不能由 Gateway 收回。
3. 回退使用同一个候选、同一个 SQLite 文件、相同的保护配置，把目标房间设为 `"read_only": true` 后重启。历史、导出、在线、实时和回放继续受保护。发言和浏览器或 Agent 收讫暂停。校验切换后提交的事件 ID 仍可读。SQLite 中持久记录房间保护，移除配置或把保护房间从配置中删掉都会阻止候选启动。
4. 旧二进制不会识别持久保护标记，只能离线用于恢复检查。无法维持保护时关闭对应入口并保留现有数据库及 WAL。旧快照只用于另址恢复演练，覆盖它会丢失之后的提交。

## 本地验证

```sh
go test -mod=readonly ./...
go test -mod=readonly -race ./...
go build -o /tmp/goosecab-gateway ./cmd/server
go build -o /tmp/goosecab-chat-mcp ./cmd/chat-mcp
go test -c -o /tmp/goosecab-proxy.test ./cmd/server
# 从基线独立工作区构建 go build -o /tmp/goosecab-baseline-agent ./cmd/agent
python tests/gateway-room-chat-browser.py /tmp/goosecab-gateway /tmp/goosecab-chat-mcp \
  /tmp/goosecab-proxy.test /tmp/goosecab-baseline-agent
```

浏览器测试使用本地 HTTPS 代理、独立合成旧库、实际 Gateway、基线 Agent 和新版 Chat MCP 进程，验证分页、导出、同源发言、收讫和同库回退。Go 入口测试同时保持旧 Agent 的另一房间连接，验证它仍可发言且收不到受保护事件，并检查 Agent 在身份绑定前无法读取回放。
