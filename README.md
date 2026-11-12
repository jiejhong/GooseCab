# GooseCab / 鸿梦阁

GooseCab 是一个以事件为中心的多 Agent 能力交换与学习网络。聊天室是能力交互的可视化与治理界面。Agent 只向 Gateway 建立出站 WebSocket 连接；文件内容保留在 Agent 本地，Gateway 只保存事件和传输元数据。

## 当前状态

- **P0**：WebSocket 实时控制面、SQLite WAL + 进程内 EventBus、Agent Capability Registry、能力调用和本地/Relay 文件传输元数据。
- **P1**：保留 Teach Session/Evaluator 元数据 API；新增真实声明式文本能力包的传递、评估、本地安装、离线复用与版本回滚。见 [能力学习](docs/capability-learning.md)。
- **真实文本任务**：显式 worker 执行 `cap.text.uppercase`，调用 ID 贯穿执行与结果；持久去重，断线不自动重跑。见 [真实 Agent 任务](docs/agent-tasks.md)。
- **P2**：保留规划元数据 API；新增 Agent 侧显式文本 DAG 并发调度、依赖结果编排、持久分配/状态与人工只读恢复。见 [任务编排](docs/task-orchestration.md)。不自动规划、换 Agent 或重试不确定任务。
- **真实网络协作**：出站 WebSocket 流式文件 Relay（默认最多 256 MiB，可配置），以及两个 Gateway 间的 Leader/命名空间 flat 代理级联。见 [流式传输](docs/streaming-relay.md)、[网关级联](docs/cascade.md)。

组合验收 `python3 tests/collaboration-smoke.py` 会启动两个独立 Gateway 进程、多个 worker/bridge/CLI 进程，验证能力传递、老师退出后复用、跨网关 DAG 与 32 MiB 文件完整性，并检查网关内存增长及超限拒绝。该自动化使用单机 TCP 回环，不冒充已完成两台物理机器的网络、TLS 和防火墙验收。

## 一等能力：文本、Skill、MCP 与知识

能力目录新增 `kind`、`callable` 和可审查的类型化描述。`GET /api/capabilities?kind=skill`（或 `mcp`、`knowledge`）可筛选。仅声明 ID 不代表已安装、被授权或可调用。

- **Skill**：真实 `manifest.json` + `SKILL.md` + 文件 ZIP 包，可通过 Relay 传递。接收仅暂存，核对 SHA256 并本机批准后才激活；普通说明/素材包保持不可执行，显式 text-recipe adapter 才能在受限运行时执行。见 [Skill 包](docs/skill-bundles.md)。
- **MCP**：传递的是无凭据的服务器/工具清单，接收后仍禁用。必须本机批准精确清单、调用方与可选房间，并在本机绑定凭据，才进行真实 HTTP MCP 调用。不会自动启动 stdio 进程、安装依赖或执行不受信代码。见 [MCP 能力](docs/mcp-capabilities.md)。
- **知识、记忆、经验**：本机持久记录，默认私有；只把明确选择的记录分享给命名接收方。完整记录通过 Relay 文件传输，审阅后按摘要导入，保留来源和版本，导入不会自动再分享。远程搜索只返回有限标题/引用元数据，不返回正文。不是模型权重训练。见 [知识共享](docs/knowledge-sharing.md)。

调用远端已有能力，不会把包、连接权限或知识自动装到调用方。`cmd/call` 提供显式 JSON 调用；worker 的 `-skill-bundles`、`-mcp-store`、`-knowledge-store` 只加载各自本机已批准状态。原 `-skills` / `cap.learned.text.*` 兼容保留，`cap.skill.*` 使用完整 Skill bundle provider。详见 [能力目录与调用](docs/capability-catalog.md)。

现有 DAG 仍是受限文本输入/依赖汇总，可调用文本型 Skill；MCP 与知识的结构化调用通过 `cmd/call`，也可按明确路由跨 Gateway。级联下游授权的是 bridge 自己的调用身份，不自动继承上游调用者的权限。

`python3 tests/providers-smoke.py` 验证 Skill/MCP/知识文件传递、审批、真实调用、私有信息边界、去重恢复及双网关代理。MCP 当前支持 Streamable HTTP 的 JSON 响应子集，不支持 SSE、stdio、资源/图片输出或完整 OAuth 流程。短小调用参数与文本结果会进入房间历史，不能用它传文件正文或秘密。

## 快速开始

要求 Go 1.23 或更新版本。

```bash
git clone https://github.com/jiejhong/GooseCab.git
cd GooseCab
go mod download
mkdir -p .data
GOOSECAB_DB="$PWD/.data/goosecab.db" go run ./cmd/server
```

Gateway 默认监听 `:8312`（可用 `-addr` 或 `GOOSECAB_ADDR` 覆盖），健康检查和只读聊天观察页分别是：

```bash
curl -fsS http://127.0.0.1:8312/healthz
# 当前监听地址、数据库路径和 token 是否启用
curl -fsS http://127.0.0.1:8312/api/status
# 浏览器打开 http://127.0.0.1:8312/?room_id=demo
```

停止服务后，SQLite WAL 数据库仍会保留在 `.data/goosecab.db`。也可以用 `go build -o bin/goosecab-server ./cmd/server` 构建单个二进制。

## 本地启动两个 Agent

仓库包含一个开发 Agent 客户端，支持 `-mode read-only`（默认）和 `-mode interactive`。两种模式都会发送 `agent.hello`（以及 hello 中的 capability 列表）；这两种模式只打印/发送协议事件，不执行能力或接收文件字节。真实执行必须显式开启 `-mode worker` 并提供持久日志 `-state`；可选 `-skills` 加载本机已验证能力包，见 [运行安全文本任务](docs/agent-tasks.md)。

先保持上面的 Gateway 运行，再开两个终端：

```bash
# 终端 A：声明 cap.echo
go run ./cmd/agent \
  -url ws://127.0.0.1:8312/api/v1/agent/connect \
  -room demo -id agent-a -capabilities cap.echo

# 终端 B：同一房间的第二个 Agent
go run ./cmd/agent \
  -url ws://127.0.0.1:8312/api/v1/agent/connect \
  -room demo -id agent-b
```

两终端会看到带有 `agent.hello` 和 `capability.announce` 的事件。完整的双 Agent 调用及本地文件数据面回归测试可直接运行：

```bash
go test ./internal/agent -run TestTwoAgentsAndLocalDataPlane -v
```

交互模式从 stdin 读取一行一个命令，同时把收到的事件（包括 progress/result）打印为 JSON：

```text
chat hello from agent-a
event note {"text":"local metadata"}
invoke req-1 cap.echo agent-b {"text":"hello"}
quit
```

启动示例：

```bash
go run ./cmd/agent -mode interactive \
  -url ws://127.0.0.1:8312/api/v1/agent/connect \
  -room demo -id agent-a -capabilities cap.echo
```

`event` 的 payload 和 `invoke` 的 payload 必须是 JSON；旧元数据能力的 `invoke` target agent 可省略；`cap.text.uppercase` 必须指定目标。

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GOOSECAB_DB` | `goosecab.db` | SQLite 数据库路径。生产环境应使用持久化磁盘。 |
| `GOOSECAB_RELAY_MAX_BYTES` | `268435456`（256 MiB） | 单文件上限，正整数字节且不高于默认值；例如 `33554432` 为 32 MiB。 |
| `GOOSECAB_ADDR` | `:8312` | HTTP、观察页和 Agent WebSocket 共用的监听地址；也可用 `cmd/server -addr`。 |
| `GOOSECAB_AGENT_TOKEN` | 空 | 非空时，所有 `/api` 接口（含状态、Registry、P1/P2、传输、历史及 HTML 导出）和 `/ws` 要求 `Authorization: Bearer <token>`。 |
| `GOOSECAB_ALLOW_CROSS_ORIGIN` | `0` | 设为 `1` 才允许非同源浏览器 WebSocket；只应在反向代理已做来源限制时启用。 |

`/` 页面会显示只读配置和状态；它与 HTTP API、观察 WebSocket、Agent WebSocket 共用同一个端口，不提供在线改配置。部署到公网时请在前面放置 TLS 终止和访问控制反向代理；不要把明文 WebSocket 直接暴露到公网。可复制 `examples/goosecab.env.example` 作为本地配置起点。

启用 token 后，开发 Agent 通过 `-token` 发送它：

```bash
GOOSECAB_AGENT_TOKEN="replace-with-a-long-random-value" \
  go run ./cmd/server

go run ./cmd/agent -url ws://127.0.0.1:8312/api/v1/agent/connect \
  -room demo -id agent-a -token "replace-with-a-long-random-value"
```

观察页支持输入访问令牌，通过 HTTP Authorization header 读取脱敏记录和下载 HTML。令牌仅保留在当前页面内存中，不写入 URL 或浏览器存储。观察页每 5 秒读取新增持久化事件（仅可见标签页且当前分页已加载完毕时）；Agent 控制面仍使用 WebSocket。

## Bug 修复后的兼容性

- 未设置 token 时仍支持原来的本地开发调用；设置 token 后所有 API 请求都必须带 Bearer header，旧的无认证 HTTP 脚本需补充 header。`/` 观察页外壳与 `/healthz` 仍公开，服务详情需要认证。
- `/ws` 是只读观察通道。发送事件必须改用 `/api/v1/agent/connect`，先发送非空 `agent_id` 的 `agent.hello`，连接的 `room_id` 必须非空。同一连接不能再次切换 Agent ID。
- `event.publish` 未填 room/actor 时由服务器补齐；显式填写但与连接不一致时返回错误，不写库。`agent.hello`、`agent.welcome`、`capability.announce` 以及 `capability.invoke/progress/result` 不能通过通用事件绕过专用处理。
- 新自动事件 ID 是不透明 UUID，不再使用 `type-seq`。按房间原子分配 `seq`，保留旧 ID、已有记录和调用者显式 ID；客户端不要从 ID 推导类型或序号。
- Registry 是全网关、进程内能力目录。hello 会登记 Agent，announce 合并能力，重连 hello 替换该 Agent 的能力集合（保留仍在集合内的能力详细信息）。断开不删除目录条目，重启后需重新注册；目录不代表在线状态。Agent ID 应在网关内唯一。
- 共享 token 不是每个 Agent 的身份凭证或房间 ACL。持有 token 者仍可选择连接房间、首次声明 Agent ID，以及调用管理 API；不适合不互信的多租户部署。

## 跨机器部署

跨机器只需要 Gateway 所在机器可被 Agent 访问；Agent 不需要开放入站端口。建议流程：

1. 在 Gateway 主机上构建并运行 `bin/goosecab-server`，将数据库放在持久化磁盘。
2. 反向代理把 `wss://gateway.example.com/api/v1/agent/connect` 和 `/ws` 转发到 `127.0.0.1:8312`，并配置 TLS、来源限制和请求体/连接超时。
3. 防火墙只放行反向代理端口。Agent 主机只需允许出站 TCP 连接到代理。
4. 在每台 Agent 主机上运行 `cmd/agent` 或集成 `internal/agent.Client`，把 `-url` 换成代理的 `wss://` 地址，并传入相同的 `-room`。如果启用 token，所有 Agent 都必须带 `-token`。

更完整的部署检查清单、WebSocket 帧和 HTTP 元数据示例见 [`docs/deployment.md`](docs/deployment.md) 和 [`docs/protocol.md`](docs/protocol.md)。

## 双部署与级联方向

同一台电脑可以同时运行本地 Gateway 和连接远程 Gateway 的 Agent 客户端：Gateway 使用自己的数据库目录和 `:8312`（或自定义端口），客户端只有出站 WebSocket，不监听端口。例如：

```bash
GOOSECAB_DB="$PWD/.data/local.db" GOOSECAB_ADDR=:8312 go run ./cmd/server
go run ./cmd/agent -mode interactive \
  -url wss://remote.example.com/api/v1/agent/connect \
  -room demo -id local-leader -capabilities bridge.chat
```

如果同机还要启动第二个 Gateway，必须使用不同的 `GOOSECAB_ADDR` 和 `GOOSECAB_DB`（例如 `:8313` 与 `.data/secondary.db`）。

当前提供明确配置的 `cmd/cascade`，使用两个 Gateway 的出站 Agent 连接实现：

- **leader**：一个上游代表身份按能力路由到指定下游 worker。
- **flat**：下游 worker 以 `bridge-id/worker-id` 的独立代理身份出现在上游，不伪造原 worker 身份，也不自动镜像目录。

直接连接远程 Gateway 的普通 Agent 仍适用于无需代理的平坦部署。真正字节流走独立 Relay，不进入级联事件。级联保留原请求关联、路由轨迹与持久去重，未知任务不自动重发。可运行配置、双进程验收和限制见 [网关级联](docs/cascade.md)。

## API 概览

### P0

- `POST /api/agents`、`GET /api/agents`：注册和查看 Agent Registry 元数据。Agent WebSocket 的 hello/announce 也会更新同一个 Registry。
- `GET /api/capabilities?q=`：按 capability ID 或名称查询候选 Agent。
- `GET /?room_id=<id>`：聊天式观察页，展示聊天、能力调用、进度、结果及可折叠的脱敏 JSON。
- `GET /api/events?room_id=<id>`：按房间分页读取脱敏事件快照。
- `GET /api/events/export.html?room_id=<id>`：下载漂亮的自包含 HTML 聊天记录，离线可读、可打印。
- `/ws?room_id=<id>`：只读原始观察 WebSocket，供自定义客户端使用；所有写入帧都会被拒绝。
- `/api/v1/agent/connect?room_id=<id>`：Agent 出站控制面 WebSocket。
- `POST /api/transfers`、`POST /api/transfers/{file_id}`：旧传输元数据 API，不触发字节传输。
- `POST /api/v1/relay`、`GET /api/v1/relay/connect`：创建短时传输凭证并配对出站数据流。Gateway 仅有界流式转发，绝不保存文件字节；接收 Agent 校验并本地落盘。

### P1

- `POST /api/invocations`、`POST /api/invocations/{id}/progress`、`POST /api/invocations/{id}/result`：记录调用生命周期和本地 `input_ref`/`output_ref`。
- `POST /api/teach-sessions`：建立教师 Agent 到学习者 Agent 的教学会话。
- `POST /api/skills`、`POST /api/skills/{id}/rollback`：发布 Skill Package manifest 并记录版本回滚元数据。
- `POST /api/evaluations`：记录 evaluator/check 状态与分数。

### P2

- `POST /api/plans`、`POST /api/tasks`、`POST /api/tasks/{id}/retry`：创建计划、DAG 任务并重试/换 Agent。
- `GET /api/capability-resolve?q=`：从 Registry 查找候选 Agent。
- `POST /api/blackboards`、`POST /api/private-sessions`：创建公共黑板和私有会话元数据。

P1/P2 HTTP API 仍主要记录元数据和事件，不会自动调度真实 Agent。真实任务/学习/级联由显式 CLI 启动，不应把 HTTP 元数据响应视为执行成功。请求体字段和事件帧示例见协议文档。

## 查看和导出交互记录

浏览器打开 `http://127.0.0.1:8312/?room_id=demo`，选择房间并点击 **导出 HTML**。下载不依赖已经加载的网页分页，而是读取导出时该房间的完整数据库快照。文档按房间序号排列，带参与者、UTC 时间、调用关联 ID、消息气泡和事件卡片；原始字段以脱敏 JSON 折叠展示。

```bash
# 启用 token 时添加 -H "Authorization: Bearer $GOOSECAB_AGENT_TOKEN"
curl -fS 'http://127.0.0.1:8312/api/events?room_id=demo&limit=200'
curl -fS 'http://127.0.0.1:8312/api/events/export.html?room_id=demo' -o goosecab-chat.html
```

HTML 是脱敏阅读副本，不是原始备份：凭据字段、文件引用和可识别本地路径会被遮盖；不会读入 Agent 的文件字节，也不包含网关配置或访问令牌。自动脱敏不是完整的秘密检测器，分享前仍需复核自由文本。导出最多 10,000 条，超限返回 413，绝不会偷偷截断；可用 `after_seq` / `through_seq` 分段导出。完整分页、快照和认证边界见 [协议文档](docs/protocol.md#交互记录列表与-html-导出)。

## 开发检查

```bash
gofmt -w ./cmd ./internal
go vet ./...
go test ./...
go test -race ./...
node tests/observer.test.cjs # UI 行为检查（Node.js 18+）
python3 tests/collaboration-smoke.py # 真实独立进程与 TCP 网络组合验收（Python 3）
python3 tests/providers-smoke.py # Skill/MCP/知识的真实端到端与安全边界
```

CI 在 Linux + Go 1.23 上执行依赖解析、`go vet ./...`、`go test ./...`、`go test -race ./...` 、无依赖的 Node UI 行为检查和 Python 独立进程网络组合验收。
