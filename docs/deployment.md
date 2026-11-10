# GooseCab 部署检查清单

## Gateway 主机

```bash
cd goosecab
go mod download
go build -o bin/goosecab-server ./cmd/server
install -d -m 0700 /var/lib/goosecab
GOOSECAB_DB=/var/lib/goosecab/goosecab.db GOOSECAB_ADDR=:8312 ./bin/goosecab-server
```

服务默认监听 `:8312`，可通过 `GOOSECAB_ADDR` 或 `./bin/goosecab-server -addr :8313` 覆盖。`/` 是只读配置/状态页，`/api/status` 在启用 token 时需要 Bearer header，返回监听地址、数据库路径和 token 是否启用；页面、HTTP API、观察 WebSocket 和 Agent WebSocket 共用该端口，不支持在线改配置。生产部署应由 systemd、容器编排器或其他进程管理器负责重启和日志收集；仓库没有内置 TLS、限流或多租户 ACL。

## 反向代理

Agent 使用 `wss://`，反向代理必须保留 WebSocket Upgrade。下面是 Nginx 片段，证书和访问控制配置应按部署环境补齐：

```nginx
location /api/v1/agent/connect {
    proxy_pass http://127.0.0.1:8312;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_read_timeout 1h;
}

location /ws {
    proxy_pass http://127.0.0.1:8312;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_read_timeout 1h;
}
```

如果使用 `GOOSECAB_ALLOW_CROSS_ORIGIN=1`，应在反向代理层限制 `Origin`。该开关只影响 Gateway 的 WebSocket Origin 检查，不提供身份认证。

## Agent 主机

Agent 只建立出站 TCP 连接。最小 smoke client：

```bash
go run ./cmd/agent \
  -url wss://gateway.example.com/api/v1/agent/connect \
  -room production -id agent-a \
  -capabilities cap.echo \
  -token "$GOOSECAB_AGENT_TOKEN"
```

启动第二个实例时必须使用不同的 `-id`，可以使用相同的 `-room` 来观察同一条事件流。`cmd/agent` 只打印事件，不执行能力；生产 Agent 应实现自己的能力执行器并使用同样的 WebSocket 帧格式。

### Agent CLI 模式

`cmd/agent` 默认是 `-mode read-only`，持续输出换行分隔的 JSON 事件。`-mode interactive` 在输出事件的同时接受以下命令：

```text
chat <text>
event <event-type> [json-payload]
invoke <request-id> <capability-id> [target-agent-id] [json-payload]
help
quit
```

interactive 模式发送的帧仍是现有协议的 `event.publish`、`capability.invoke` 等消息；Gateway 不会替 Agent 执行 capability。`-url` 可以是本机 `ws://127.0.0.1:8312/...`，也可以是远程 `wss://gateway.example.com/...`。

## 认证和数据边界

- `GOOSECAB_AGENT_TOKEN` 是当前 Gateway 的共享 Bearer token。它不区分 Agent 权限，也不按 room 隔离；需要更细粒度 ACL 时应由反向代理或后续版本提供。
- token 不应放在 URL、日志或提交到仓库。`cmd/agent -token` 只在握手 header 中发送。
- Gateway 接收的 `input_ref`、`output_ref`、`artifact_ref`、路径引用和 SHA-256 都是元数据；文件内容不会经过 Gateway。
- SQLite 数据库包含事件和这些引用，必须按其敏感等级保护磁盘和备份。
- 启用 token 后，内置观察页支持内存中的令牌输入，通过 Authorization header 获取脱敏 HTTP 事件和 HTML 导出。不会把令牌放入 URL、本地存储或导出文档。
- 所有 `/api` 接口以及 `/ws` 受共享 token 保护，包含 Registry、P1/P2、传输、状态和历史读写。只有页面外壳和 `/healthz` 继续公开；token 未设置时保留开发模式，无认证。默认 `:8312` 监听所有接口，不是自动回环隔离；仅本机使用时显式配置 `GOOSECAB_ADDR=127.0.0.1:8312`。
- `/ws` 只读；Agent 使用 `/api/v1/agent/connect`，必须先 hello，后续 room/actor 绑定到此连接。但 token 仍非 room 级 ACL，也不是独立的 Agent 身份凭证：持有者可选择任意房间和初始 Agent ID。
- 历史列表与导出响应设置 `Cache-Control: no-store`；HTML 附件自带 CSP，不运行脚本或加载远程资源。原始 `/ws` 和数据库不做同样的脱敏。
- 页面 5 秒轮询已持久化事件；刷新会重新读取快照。序号乱序补写的记录可能需要手动刷新完整历史才能看到。

## 验证顺序

1. `curl -fsS https://gateway.example.com/healthz` 返回 HTTP 200。
2. 从每台 Agent 主机启动 `cmd/agent`，确认收到 `agent.welcome` 和自己的 `agent.hello` 事件。
3. 在第二个 Agent 上声明一个 capability，确认同房间另一端收到 `capability.announce`。
4. 在 Gateway 主机运行 `go test ./...`，确认本地双 Agent 和 direct/relay 文件数据面测试通过。
5. 断开并重连一个 Agent，带上最近的 `resume_seq`，确认只收到未处理事件。

## 同机双部署和级联

Agent 连接是出站连接，因此同一台电脑可一边运行本地 Gateway，一边运行连接远程 Gateway 的客户端，不会争用 Agent 监听端口：

```bash
GOOSECAB_DB="$PWD/.data/local.db" GOOSECAB_ADDR=:8312 ./bin/goosecab-server
./bin/goosecab-agent -mode interactive \
  -url wss://remote.example.com/api/v1/agent/connect \
  -room demo -id local-leader
```

如果要在同一台电脑启动两个 Gateway，则显式分配不同的地址和数据库目录：

```bash
GOOSECAB_DB=.data/gateway-a.db GOOSECAB_ADDR=:8312 ./bin/goosecab-server
GOOSECAB_DB=.data/gateway-b.db GOOSECAB_ADDR=:8313 ./bin/goosecab-server
```

部署层可选择以下两种不改变现有协议的方向：

1. **cascade / Leader Agent**：本地 Gateway 上的 Leader Agent 代表本地网络连接上游 Gateway，在上游 room 中使用独立 Agent ID，并明确转发允许的 `event.publish`、`capability.invoke`、`capability.progress`、`capability.result` 事件。Leader 负责认证、过滤、去重和失败处理。
2. **flat / transparent**：本地 Agent 直接连接远程 Gateway，并使用与本地视图相同的 room ID。远程 Gateway 把它当普通 Agent，不需要 Gateway 间连接。

本仓库只提供客户端和协议帧，不实现自动 Gateway 桥接或跨 room 重写；这样可避免把本地数据库、token 或文件字节意外暴露到上游。

## 升级回归

```bash
go test ./...
go test -race ./...
node tests/observer.test.cjs
```

新增回归使用临时 SQLite 数据库和本机测试服务器，覆盖跨房间 ID、并行序号分配、跨 Store 混合生产者、重连回放、只读观察、room/actor 伪造、hello/announce 能力发现、全 API 鉴权，以及空房间首次消息出现后的持续轮询。旧数据库无需重建；升级前仍建议做常规备份。
