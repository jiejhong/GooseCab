# Gateway 房间网页发言

在现有 Gateway 上通过 `-room-chat-config` 为明确选择的房间授权网页发言。使用同一 SQLite、原事件 ID、历史字段和游标。发言与点名未读在同一事务提交，提交后发布到原事件总线。新功能默认关闭。`cmd/localroom` 和 Tailnet HTTP 隔离测试有各自的验收用途。

## 首批 demo 网页发言

首批通过独立的 `-demo-write-config` 显式开启，默认关闭。用同一个 Gateway、SQLite 和 `demo` 房间 ID。访客打开现有 HTTPS 地址的 `/demo-write` 直接发纯文本，作者固定为 `guest / 白名单访客`。正文和作者进入原历史、导出、实时和重连回放。页面说明现有读者可见，首批只提供普通发言。这一入口不能与同一 `demo` 房间的个人点名或保护策略同时开启。

同一 HTTPS 地址的 `/?room_id=demo` 也可直接在时间线上发言。页面通过现有白名单入口加载 `/demo-write.js`，加载失败时发言框保持隐藏。回执丢失时保留草稿，原文重试使用同一个请求 ID，返回同一事件。两种入口使用同一写入路径和频率限制，无需增加代理转发。

现有 HTTPS 入口必须在转发前对下列三个路径使用**现有白名单**。认证或来源判断使用此入口已有的可信机制。

| 入口 | 转发对象 |
| --- | --- |
| `GET /demo-write` 与 `GET /demo-write.js` | 下方独立 Unix socket |
| `POST /api/demo-write/messages` | 同一个 Unix socket |
| 其余路径 | 当前使用的 Gateway 后端和现有规则 |

入口须保留原 Host 和浏览器 Origin，且保留请求的路径和方法。转发到发言 socket 时移除 Cookie 和 Authorization。这个 socket 只提供上述发言面。Socket 的 Linux peer UID 必须与配置的入口 worker UID 一致，Gateway TCP 完全没有该路由。写入额外要求精确 HTTPS Origin、同源 JSON 与服务器固定的访客作者。部署应核实入口 UID 由现有白名单代理独占，并检查没有备用虚拟主机、路由或通用转发可以到达该 socket。若现有 HTTPS 入口无法在白名单校验后转发到独立 socket，此配置不具备启用条件。

示例只包含占位值。配置权限 `0600`，socket 父目录必须由 Gateway 账号拥有，权限 `0750`，下方组应允许专用代理 worker 访问。Gateway 创建 `0660` socket，并继承目录的组。目录应置于受控的运行目录中。代理使用不同于 Gateway 的专用非 root UID。`ingress_uid` 填实际连接 socket 的 worker UID。

```json
{
  "origin": "https://gateway.example.invalid:8312",
  "socket": "/run/example-gateway/demo-write.sock",
  "ingress_uid": 1234
}
```

Gateway 启动时继续使用现有 SQLite 路径。检查旧 Agent 收发和原历史后，从已核实的 HTTPS 入口验收发言和不在名单内的拒绝。首批回退先撤掉入口的三条发言转发，再移除 `-demo-write-config` 并以同一个 SQLite 重启。已提交的访客发言与旧历史继续按原规则读取。若非正常退出留下 socket，先确认没有进程监听再由运行目录管理清理。Gateway 不会自行覆盖已有 socket。

## 个人房间发言与点名的启用条件

保存权限为 `0600` 的配置。继续使用现有 HTTP 监听器时，可在同一个端口选择原生 HTTPS。配置 `trust_loopback_proxy: false`，把 `origin` 设为浏览器实际使用的 HTTPS 地址，端口与当前监听地址一致，并提供当前有效、客户端信任且匹配该主机名的证书链和私钥。保留原 `-addr`，再向同一 Gateway 进程传入：

```sh
-room-chat-config <private-policy-file>
-room-chat-tls-cert <certificate-chain-file>
-room-chat-tls-key <private-key-file>
```

该模式只使用一个 TCP 监听器。HTTP/WS 普通房间继续走原路径和原权限，HTTPS/WSS 能进入新配置的受保护房间。两种连接共用 Gateway、事件总线和 SQLite，按房间筛选事件。浏览器必须打开精确的 HTTPS 地址再输入个人凭据。受保护路径始终要求 TLS、正确 Host 和房间授权；浏览器写入还须同源。原生同端口模式新建受保护读权限时，只接受无旧历史的房间 ID 或已记录保护标记的房间。既有房间可单独选择下文的保留读者模式。它使用 HTTP/1.1 和 TLS 1.2 或更高版本，并对等待读取连接首字节和请求头设置上限。

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

观察页的「在线 Agent（WebSocket）」仅统计本房间仍有活跃 WebSocket 的 Agent ID。「本房间最近发言」单独读取已提交的历史。使用个人凭据的 `/room-chat` 另显示最近五分钟内成功返回的认证历史读取与拉取请求。该列表按房间授权，只保存在当前进程，表示读取活动，与在线连接、成员资格及点名收讫分别计算。匿名页面访问、共享访客发言、心跳与回放历史不会生成认证读取活动。

### 私人点名回执的隔离验收

在合成测试房间的个人房间配置中设置 `"track_mention_receipts": true`。此功能默认关闭，只支持受保护房间。单人点名取服务端配置中的有效房间身份。全员点名仍要求发件人拥有房间内 `broadcast` 权限，其收件人是发送时有读权限的其他真人和 Agent。当时离线的合格成员也会记录一行。正文里的裸 `@` 作为普通文本显示。

用单独凭据进入 `/room-chat`。收件人在「私人点名通知」中确认收到，有写权时还可接着回复。发件人在时间线原消息上点击「查看点名回执」，可核对每位收件人的「待送达」「已收到」「已回复」。读取历史、WebSocket 连接或收到心跳都不会推进这些状态。回复只改变当前已认证收件人的那一行。公开聊天事件只带正文和作者。收件列表、回复关联与回执由房间授权的专用接口返回。

Agent 使用已有独立房间凭据启动 `chat-mcp -gateway-room-chat -gateway-mentions`。客户端调用 `mention_next` 等待一条本人的私人通知，每次最多等待两秒。调用结果成功写入 MCP 输出后，适配器在原连接确认送达。确认成功的通知在重连后不会重发。连接或输出中断时，未确认通知保留原始事件 ID，客户端可按该 ID 去重。Agent 用 `mention_reply` 显式提交原事件 ID、回复正文和新的消息 UUID。一次已确认的点名只记录一条关联回复。该模式仅增加通知与文本回复工具。

现有 demo 的白名单访客使用共享作者，无法绑定个人发件人与收件人。该入口继续用于普通发言。若要给原 demo 开启本节功能，需要另行批准对该房间启用个人级读写授权，并迁移需要收取点名的 Agent 到专属凭据。迁移应先核对旧读者、发件人与收件人的权限，使用合成账户验证跨房、撤权、原读取路径和历史保持情况，再由部署线程评估 8312。

### 从现有 Agent 聊天客户端发起点名

隔离的受保护测试房间可以在 `track_mention_receipts` 的基础上额外设置
`"agent_mention_posts": true`。使用该房间独立凭据和原
`/api/v1/agent/connect` 入口，照常通过 `chat` 或 Chat MCP 的
`chat_send` 发送正文 `@fixture-recipient 请确认收到`。仅识别位于正文开头、
后接空白和内容的精确房间内 Agent ID。回复、正文中间的 `@`、未知目标
及未获授权的 `@所有人` 会以普通聊天保存。Agent 的全员点名权限须对其
身份单独配置 `"broadcast": true`，按发送时的房间读授权生成逐人回执。

支持该选项的 Gateway 会让发件 Agent 的 Chat MCP 提供 `mention_status`。
它以原事件 ID 查作者自己的逐人状态。现有 `chat_send` 提供的
稳定 `message_id` 会保留。同房间同作者重试完全相同的消息只取回原提交，
其它作者、不同正文或关联不能复用该 ID。在离线恢复时，
已提交的通知保留原 ID 和收件人。收件人可在独立的受保护连接上通过
`mention_next` 和 `mention_reply` 验证收讫及关联回复传输。

真实助手唤醒还需要部署方提供受支持的会话接收入口和身份绑定。该入口须以
房间、原事件 ID、收件人作为持久幂等键，重复请求须指向同一处理记录。
进入真实会话且确认接收后才能确认收讫，随后以同一收件人的低权限文本连接
提交关联回复。普通房间历史读取和在线状态不参与确认。此隔离选项适用于
验证发帖、通知和回执链路。现有 8312 demo 仍使用共享访问方式，缺少可以
验证每位 Agent 发件身份的绑定，真实助手也尚无经验证的接收入口。

## 在已有 demo 房间保留原读者

在房间负责人确认**未来发言正文也能被原有全部读者看到**之后，才可在目标房间配置 `"legacy_readers_may_read_new_messages": true`。这项开关不适用于已有保护标记的房间。房间 ID、同一个 SQLite、原 HTTP/WS 权限与 Agent 配置均可保留。写入仍使用个人授权、同源和 HTTPS，可沿用现有端口启用 TLS。单独启用该选项不会将线上现有 demo 改为认证房间。

一次发言只产生一个公开可见的普通事件。HTTP 历史、导出、旧 WS 实时帧及回放沿用原规则，正文和作者可见。用结构化控件点名时，收件人记录与正文原子提交，独立于事件载荷和广播。浏览器在个人未读区看到自己的点名并确认；已经有独立房间凭据的 Agent 可通过 HTTPS/WSS 的 Chat MCP `room_read` 得到自己的 `private_mention_event_ids` 并自动收讫。旧 Agent 可同时保留原连接。仅凭原共享令牌或自报同一个 Agent ID 无法取得这些信号。该模式只开放个人点名，全员点名配置被拒绝。

需要告知发言者正文会进入原读者可见的历史和导出。手动把名字写进正文会随正文一起公开。读者范围待确认时保持兼容开关关闭。若需暂停发言和收讫，可在同一配置上启用 `read_only`。已提交的正文仍按原房间规则保留。

## 已有 demo 房间的认证迁移

原生同端口模式默认拒绝将已有历史的房间 ID 转为受保护读权限。原地迁移属于另外的授权范围。

要在原房间 ID 执行认证迁移，至少需要房间负责人同意将该房间的历史、导出、在线状态与回放改为认证访问，并授权对这一房间执行原地保护迁移及一次受控重启。迁移实现还需显式识别旧房间，原子持久化保护标记、保留历史事件 ID 和现有 SQLite、停止旧进程及所有旧读入口，然后先按保护后的只读模式验证，再放开发言。其他房间可维持原权限。

已有房间从匿名或共享令牌切换到保护后，旧客户端须改用 HTTPS/WSS 及与房间和稳定身份绑定的专属凭据。共享令牌不能承担个人点名或收讫授权。受保护房间目前只覆盖聊天，旧能力调用与传输需要迁移或继续留在普通房间。历史曾公开下载的副本无法收回。在保留匿名读取的情况下对同一房间的正文做按事件的访问控制，需要额外设计和验证。

## 受保护读权限的切换及同库只读回退

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

上述 `gateway-room-chat-browser.py` 使用本地 HTTPS 代理，验证分页、导出、同源发言、收讫和同库回退。Go 入口测试验证 Agent 在身份绑定前无法读取回放。此前的原生同端口 Chromium 试跑在仓库工作树外完成，使用了允许测试证书错误的浏览器配置。正式 Go CI 当时覆盖了原生同端口 Go 回归。

Go 同端口回归使用一个合成监听器并同时连接 HTTP、HTTPS、WS 和 WSS。它检查旧连接发言、旧历史 ID、网页发言和点名、原房间及受保护房间在各自的历史与导出中的可见性，以及同库只读回退。

### 原生同端口 Chromium

GitHub PR 的 `native-same-port-browser` 作业用 Ubuntu 24.04 镜像预装的 Chromium、OpenSSL 和 NSS 工具运行下方用例。Linux 本地需要 OpenSSL、NSS 的 `certutil` 和 Playwright Chromium。用例只启动一个合成 Gateway 回环监听器，从临时合成旧库启动，使用基线 Agent 的原 HTTP/WS 访问方式。Chromium 在隔离测试 home 中先拒绝未获信任的服务端，向测试 NSS 库导入合成 CA 后，再用标准证书验证打开 HTTPS。同口验证旧 demo 的回放、历史及导出，以及新受保护房间的网页发言和导出。证书、私钥、NSS 库及测试数据随用例清理。关于 Chromium 的 NSS 库布局，见 [Linux 证书管理](https://chromium.googlesource.com/chromium/src/+/main/docs/linux/cert_management.md)。

```sh
# 先在基线 Git 工作树构建 go build -mod=readonly -o /tmp/goosecab-baseline-agent ./cmd/agent
go build -mod=readonly -o /tmp/goosecab-gateway ./cmd/server
python -m pip install playwright==1.62.0
python -m playwright install chromium
python tests/gateway-native-same-port-browser.py /tmp/goosecab-gateway /tmp/goosecab-baseline-agent
```

已安装本机 Chromium 时，可以传入 `--chromium /path/to/chromium`。同一个 CI 作业还运行保留原读者的 demo 用例，验证网页发言、基线 Agent 的同房收发、原历史和导出，以及独立 Chat MCP 的个人点名和收讫。

```sh
go build -mod=readonly -o /tmp/goosecab-chat-mcp ./cmd/chat-mcp
python tests/gateway-native-same-port-browser.py /tmp/goosecab-gateway /tmp/goosecab-baseline-agent \
  --demo-chat-mcp /tmp/goosecab-chat-mcp
```

首批白名单发言的 Chromium 回归使用同一脚本和一个本地 Go 测试夹具。夹具走 HTTPS 白名单代理、真实 Unix socket 和原 Gateway 后端，旧 Agent 与网页同时工作。

```sh
go test -mod=readonly -c -o /tmp/goosecab-demo.test ./cmd/server
python tests/gateway-native-same-port-browser.py /tmp/goosecab-gateway /tmp/goosecab-baseline-agent \
  --allowlisted-demo-fixture /tmp/goosecab-demo.test
```
