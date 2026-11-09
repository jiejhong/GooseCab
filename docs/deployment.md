# GooseCab 部署检查清单

## Gateway 主机

```bash
cd goosecab
go mod download
go build -o bin/goosecab-server ./cmd/server
install -d -m 0700 /var/lib/goosecab
GOOSECAB_DB=/var/lib/goosecab/goosecab.db ./bin/goosecab-server
```

服务当前固定监听 `:8080`。生产部署应由 systemd、容器编排器或其他进程管理器负责重启和日志收集；仓库没有内置 TLS、限流或多租户 ACL。

## 反向代理

Agent 使用 `wss://`，反向代理必须保留 WebSocket Upgrade。下面是 Nginx 片段，证书和访问控制配置应按部署环境补齐：

```nginx
location /api/v1/agent/connect {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_read_timeout 1h;
}

location /ws {
    proxy_pass http://127.0.0.1:8080;
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

## 认证和数据边界

- `GOOSECAB_AGENT_TOKEN` 是当前 Gateway 的共享 Bearer token。它不区分 Agent 权限，也不按 room 隔离；需要更细粒度 ACL 时应由反向代理或后续版本提供。
- token 不应放在 URL、日志或提交到仓库。`cmd/agent -token` 只在握手 header 中发送。
- Gateway 接收的 `input_ref`、`output_ref`、`artifact_ref`、路径引用和 SHA-256 都是元数据；文件内容不会经过 Gateway。
- SQLite 数据库包含事件和这些引用，必须按其敏感等级保护磁盘和备份。
- 启用 token 后，内置观察页无法设置 Authorization header。使用 CLI、自定义 WebSocket 客户端或在代理层为观察者提供单独的受控入口。

## 验证顺序

1. `curl -fsS https://gateway.example.com/healthz` 返回 HTTP 200。
2. 从每台 Agent 主机启动 `cmd/agent`，确认收到 `agent.welcome` 和自己的 `agent.hello` 事件。
3. 在第二个 Agent 上声明一个 capability，确认同房间另一端收到 `capability.announce`。
4. 在 Gateway 主机运行 `go test ./...`，确认本地双 Agent 和 direct/relay 文件数据面测试通过。
5. 断开并重连一个 Agent，带上最近的 `resume_seq`，确认只收到未处理事件。
