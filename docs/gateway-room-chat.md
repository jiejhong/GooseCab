# Gateway 受保护房间发言

在现有 Gateway 上通过 `-room-chat-config` 选择受保护房间。使用同一 SQLite、原事件 ID、历史字段和游标。发言与点名未读在同一事务提交，提交后发布到原事件总线。新功能默认关闭。`cmd/localroom` 和 Tailnet HTTP 隔离测试有各自的验收用途。

## 启用条件

保存权限为 `0600` 的配置。继续使用现有 HTTP 监听器时，可在同一个端口选择原生 HTTPS。配置 `trust_loopback_proxy: false`，把 `origin` 设为浏览器实际使用的 HTTPS 地址，端口与当前监听地址一致，并提供当前有效、客户端信任且匹配该主机名的证书链和私钥。保留原 `-addr`，再向同一 Gateway 进程传入：

```sh
-room-chat-config <private-policy-file>
-room-chat-tls-cert <certificate-chain-file>
-room-chat-tls-key <private-key-file>
```

该模式只使用一个 TCP 监听器。HTTP/WS 普通房间继续走原路径和原权限，HTTPS/WSS 能进入新配置的受保护房间。两种连接共用 Gateway、事件总线和 SQLite，按房间筛选事件。浏览器必须打开精确的 HTTPS 地址再输入个人凭据。受保护路径始终要求 TLS、正确 Host 和房间授权；浏览器写入还须同源。原生同端口模式只接受新房间 ID，或已记录保护标记的房间，避免在兼容升级时改变旧房间的访问规则。首次启用请选未分配且无历史的房间 ID。它使用 HTTP/1.1 和 TLS 1.2 或更高版本，并对等待读取连接首字节和请求头设置上限。

已有可信 HTTPS 代理的部署可继续选择回环代理模式。CLI 后端绑定回环地址，代理保留精确 Host，确认前端 TLS 后替换转发头并设置唯一的 `X-Forwarded-Proto: https`，并转发 WebSocket Upgrade。该模式与原生同端口 TLS 分别配置。

每个真人和 Agent 使用不同的 32 字节随机 base64url 凭据。只将原始凭据字符串的 SHA-256 十六进制摘要写入配置，由服务端绑定身份、有效期和房间。请通过私有机制提供凭据。

```json
{
  "origin": "https://gateway.example.invalid",
  "trust_loopback_proxy": false,
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

旧房间的 Agent 可按原 HTTP/WS 地址、凭据和房间继续工作。准备在新房间收取点名的 Agent 须配置同房间的专属凭据，并使用该房间 ID 和 WSS 地址，原客户端仍可用于回放和普通聊天。多人共用的旧令牌或 IP 地址不能提供可核验的个人身份，因此新房间的点名对象来自配置中的身份。先给已配置的真人和 Agent 提供房间内发言与点名。需要自动收讫的 Agent 用候选版 Chat MCP。

```sh
chat-mcp -gateway-room-chat -url "$AGENT_WSS_URL" -room "$ROOM_ID" -agent "$AGENT_ID"
```

`AGENT_WSS_URL` 指向 `/api/room-chat/agent`。从 `GOOSECAB_ROOM_CHAT_AGENT_TOKEN` 读取独立 Agent 凭据。只有 `room_read` 页成功解码、校验、交付后才自动确认。服务端检查同连接交付证明和实际页内事件 ID，只清该 Agent 的未读。普通 HTTP 历史或旧 WebSocket 接收不会自动确认。

## 切换及同库只读回退

1. 先在独立环境用锁定的 Go/SQLite 驱动、合成旧库、新旧 Agent 和合成凭据跑全量测试。确认实际证书信任及入口、目标房间凭据、旧房间的 HTTP/WS 回归，记录切换前事件 ID。若受保护房间依赖旧的能力调用或传输，先迁移相应工作流。
2. 经授权生成并核验包含 WAL 的一致备份。停净所有能读取该文件的旧 Gateway 进程与上游连接，确认代理无备用旧读入口，再让候选独占服务该文件。受保护房间配置只在启动时加载，切换会断开原连接。普通房间重连后保持原访问方式。已在切换前被下载或缓存的历史不能由 Gateway 收回。
3. 回退使用同一个候选、同一个 SQLite 文件、同一监听及证书、相同的保护配置，把目标房间设为 `"read_only": true` 后重启。历史、导出、在线、实时和回放继续受保护。发言和浏览器或 Agent 收讫暂停。校验切换后提交的事件 ID 仍可读。SQLite 中持久记录房间保护，移除配置或把保护房间从配置中删掉都会阻止候选启动。
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

Go 同端口回归使用一个合成监听器并同时连接 HTTP、HTTPS、WS 和 WSS。它检查旧连接发言、旧历史 ID、网页发言和点名、原房间及受保护房间在各自的历史与导出中的可见性，以及同库只读回退。
