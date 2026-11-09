# GooseCab / 鸿梦阁

GooseCab 是一个以事件为中心的多 Agent 能力交换与学习网络。聊天室是能力交互的可视化与治理界面。Agent 只向 Gateway 建立出站 WebSocket 连接；文件内容保留在 Agent 本地，Gateway 只保存事件和传输元数据。

## 当前状态

- **P0**：WebSocket 实时控制面、SQLite WAL + 进程内 EventBus、Agent Capability Registry、能力调用和本地/Relay 文件传输元数据。
- **P1**：能力调用生命周期事件、Teach Session、Skill Package manifest、本地 Evaluator 状态和版本回滚元数据。
- **P2**：Planner/Capability Resolver、Task DAG、重试换 Agent、公共 Blackboard、私有会话和 Provider Adapter 接口。真正的并发调度和结果编排仍在后续迭代。

## 快速开始

要求 Go 1.23 或更新版本。

```bash
git clone https://github.com/jiejhong/GooseCab.git
cd GooseCab/goosecab
go mod download
mkdir -p .data
GOOSECAB_DB="$PWD/.data/goosecab.db" go run ./cmd/server
```

Gateway 默认监听 `:8080`，健康检查和观察页分别是：

```bash
curl -fsS http://127.0.0.1:8080/healthz
# 浏览器打开 http://127.0.0.1:8080/?room_id=demo
```

停止服务后，SQLite WAL 数据库仍会保留在 `.data/goosecab.db`。也可以用 `go build -o bin/goosecab-server ./cmd/server` 构建单个二进制。

## 本地启动两个 Agent

仓库包含一个只读的开发 Agent 客户端。它会发送 `agent.hello`（以及 hello 中的 capability 列表），并将 Gateway 回放或广播的事件打印为换行分隔的 JSON；它不会执行能力或接收文件字节。

先保持上面的 Gateway 运行，再开两个终端：

```bash
# 终端 A：声明 cap.echo
go run ./cmd/agent \
  -url ws://127.0.0.1:8080/api/v1/agent/connect \
  -room demo -id agent-a -capabilities cap.echo

# 终端 B：同一房间的第二个 Agent
go run ./cmd/agent \
  -url ws://127.0.0.1:8080/api/v1/agent/connect \
  -room demo -id agent-b
```

两终端会看到带有 `agent.hello` 和 `capability.announce` 的事件。完整的双 Agent 调用及本地文件数据面回归测试可直接运行：

```bash
go test ./internal/agent -run TestTwoAgentsAndLocalDataPlane -v
```

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GOOSECAB_DB` | `goosecab.db` | SQLite 数据库路径。生产环境应使用持久化磁盘。 |
| `GOOSECAB_AGENT_TOKEN` | 空 | 非空时，`/api/v1/agent/connect` 和 `/ws` 要求 `Authorization: Bearer <token>`。 |
| `GOOSECAB_ALLOW_CROSS_ORIGIN` | `0` | 设为 `1` 才允许非同源浏览器 WebSocket；只应在反向代理已做来源限制时启用。 |

服务端监听地址目前固定为 `:8080`。部署到公网时请在前面放置 TLS 终止和访问控制反向代理；不要把明文 WebSocket 直接暴露到公网。可复制 `examples/goosecab.env.example` 作为本地配置起点。

启用 token 后，开发 Agent 通过 `-token` 发送它：

```bash
GOOSECAB_AGENT_TOKEN="replace-with-a-long-random-value" \
  go run ./cmd/server

go run ./cmd/agent -url ws://127.0.0.1:8080/api/v1/agent/connect \
  -room demo -id agent-a -token "replace-with-a-long-random-value"
```

观察页目前不能自定义 Authorization header，因此启用 token 时请使用 CLI 或自定义 WebSocket 客户端观察事件。

## 跨机器部署

跨机器只需要 Gateway 所在机器可被 Agent 访问；Agent 不需要开放入站端口。建议流程：

1. 在 Gateway 主机上构建并运行 `bin/goosecab-server`，将数据库放在持久化磁盘。
2. 反向代理把 `wss://gateway.example.com/api/v1/agent/connect` 和 `/ws` 转发到 `127.0.0.1:8080`，并配置 TLS、来源限制和请求体/连接超时。
3. 防火墙只放行反向代理端口。Agent 主机只需允许出站 TCP 连接到代理。
4. 在每台 Agent 主机上运行 `cmd/agent` 或集成 `internal/agent.Client`，把 `-url` 换成代理的 `wss://` 地址，并传入相同的 `-room`。如果启用 token，所有 Agent 都必须带 `-token`。

更完整的部署检查清单、WebSocket 帧和 HTTP 元数据示例见 [`docs/deployment.md`](docs/deployment.md) 和 [`docs/protocol.md`](docs/protocol.md)。

## API 概览

### P0

- `POST /api/agents`、`GET /api/agents`：注册和查看 Agent Registry 元数据。
- `GET /api/capabilities?q=`：按 capability ID 或名称查询候选 Agent。
- `GET /?room_id=<id>`：观察页；底层 WebSocket 路径为 `/ws?room_id=<id>`。
- `/api/v1/agent/connect?room_id=<id>`：Agent 出站控制面 WebSocket。
- `POST /api/transfers`、`POST /api/transfers/{file_id}`：记录文件传输请求和状态。文件字节不会经过 Gateway。

### P1

- `POST /api/invocations`、`POST /api/invocations/{id}/progress`、`POST /api/invocations/{id}/result`：记录调用生命周期和本地 `input_ref`/`output_ref`。
- `POST /api/teach-sessions`：建立教师 Agent 到学习者 Agent 的教学会话。
- `POST /api/skills`、`POST /api/skills/{id}/rollback`：发布 Skill Package manifest 并记录版本回滚元数据。
- `POST /api/evaluations`：记录 evaluator/check 状态与分数。

### P2

- `POST /api/plans`、`POST /api/tasks`、`POST /api/tasks/{id}/retry`：创建计划、DAG 任务并重试/换 Agent。
- `GET /api/capability-resolve?q=`：从 Registry 查找候选 Agent。
- `POST /api/blackboards`、`POST /api/private-sessions`：创建公共黑板和私有会话元数据。

P1/P2 当前 API 主要记录元数据和事件，不会自动调度真实的 Agent 进程。请求体字段和事件帧示例见协议文档。

## 开发检查

```bash
gofmt -w ./cmd ./internal
go vet ./...
go test ./...
```

CI 在 Linux + Go 1.23 上执行依赖解析、`go vet ./...` 和 `go test ./...`。
